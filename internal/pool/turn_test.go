package pool

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/internal/worker"
	pb "github.com/asalimonov/montygo/montypb"
)

type turnReply struct {
	events []*pb.ChildEvent
	exit   *worker.Status
}

type turnWorker struct {
	mu       sync.Mutex
	requests []*pb.ParentRequest
	frames   [][]byte
	notify   chan struct{}
	exited   chan struct{}
	dead     bool
	status   worker.Status
	script   func(req *pb.ParentRequest) turnReply
}

func newTurnWorker(script func(req *pb.ParentRequest) turnReply) *turnWorker {
	return &turnWorker{notify: make(chan struct{}), exited: make(chan struct{}), script: script}
}

func (w *turnWorker) wake() {
	close(w.notify)
	w.notify = make(chan struct{})
}

func (w *turnWorker) die(status worker.Status) {
	if !w.dead {
		w.dead = true
		w.status = status
		close(w.exited)
	}
	w.wake()
}

func (w *turnWorker) Send(_ context.Context, payload []byte) error {
	req := &pb.ParentRequest{}
	if err := proto.Unmarshal(payload, req); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead {
		return worker.ErrWorkerGone
	}
	w.requests = append(w.requests, req)
	reply := w.script(req)
	for _, ev := range reply.events {
		frame, err := proto.Marshal(ev)
		if err != nil {
			return err
		}
		w.frames = append(w.frames, frame)
	}
	if reply.exit != nil {
		w.die(*reply.exit)
	}
	w.wake()
	return nil
}

func (w *turnWorker) Recv(ctx context.Context) ([]byte, error) {
	for {
		w.mu.Lock()
		if len(w.frames) > 0 {
			f := w.frames[0]
			w.frames = w.frames[1:]
			w.mu.Unlock()
			return f, nil
		}
		if w.dead {
			w.mu.Unlock()
			return nil, io.EOF
		}
		ch := w.notify
		w.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (w *turnWorker) Kill() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.die(worker.Status{Known: true, Signal: 9})
}

func (w *turnWorker) Close() { w.Kill() }

func (w *turnWorker) Done() <-chan struct{} { return w.exited }
func (w *turnWorker) Err() error            { return nil }

func (w *turnWorker) Wait(ctx context.Context) (worker.Status, bool) {
	select {
	case <-w.exited:
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.status, true
	case <-ctx.Done():
		return worker.Status{}, false
	}
}

func (w *turnWorker) PID() (int, bool)  { return 0, false }
func (w *turnWorker) Kind() worker.Kind { return worker.KindSubprocess }

func (w *turnWorker) Alive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.dead
}

func (w *turnWorker) sent() []*pb.ParentRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*pb.ParentRequest(nil), w.requests...)
}

type turnSpawner struct {
	make func() *turnWorker
}

func (s turnSpawner) Spawn(context.Context) (worker.Worker, error) { return s.make(), nil }
func (s turnSpawner) Kind() worker.Kind                            { return worker.KindSubprocess }
func (s turnSpawner) Close(context.Context) error                  { return nil }

func turnOk() *pb.ChildEvent { return &pb.ChildEvent{Kind: &pb.ChildEvent_Ok{Ok: &pb.Ok{}}} }

func turnComplete(v int64, execMicros uint64) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_Complete{Complete: &pb.Complete{Value: &pb.MontyObject{Kind: &pb.MontyObject_Int{Int: v}}}}, TotalExecutionMicros: execMicros}
}

func turnCall(id uint32, eager bool) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_FunctionCall{FunctionCall: &pb.FunctionCall{FunctionName: "ext", CallId: id, AllowEagerAwait: eager}}}
}

func turnError(excType, msg string) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_Error{Error: &pb.Error{Exception: &pb.RaisedException{ExcType: excType, Message: &msg}}}}
}

func turnPool(t *testing.T, w func() *turnWorker, mutate func(*Config)) *Pool {
	t.Helper()
	cfg := Config{Spawner: turnSpawner{make: w}, MinProcesses: 0, MaxProcesses: 1, DurationLimitGrace: time.Second, ProtocolVersion: 3}
	if mutate != nil {
		mutate(&cfg)
	}
	p, err := New(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

func okThen(feed func(req *pb.ParentRequest) turnReply) func(req *pb.ParentRequest) turnReply {
	return func(req *pb.ParentRequest) turnReply {
		switch req.GetKind().(type) {
		case *pb.ParentRequest_Configure, *pb.ParentRequest_Reset_, *pb.ParentRequest_Shutdown:
			return turnReply{events: []*pb.ChildEvent{turnOk()}}
		}
		return feed(req)
	}
}

func silent(*pb.ParentRequest) turnReply { return turnReply{} }

func poolErr(t *testing.T, err error) *Error {
	t.Helper()
	var perr *Error
	require.ErrorAs(t, err, &perr)
	return perr
}

func u64(v uint64) *uint64 { return &v }

func TestTurnDeadlines(t *testing.T) {
	ctx := context.Background()
	t.Run("request timeout kills a silent worker", func(t *testing.T) {
		var w *turnWorker
		p := turnPool(t, func() *turnWorker { w = newTurnWorker(okThen(silent)); return w }, func(c *Config) { c.RequestTimeout = 100 * time.Millisecond })
		co, err := p.Checkout(ctx, wire.Configure{ScriptName: "main.py"}, CheckoutOptions{})
		require.NoError(t, err)
		_, err = co.Feed(ctx, "x", nil, nil, "", nil, false, nil)
		perr := poolErr(t, err)
		require.Equal(t, KindTimeout, perr.Kind)
		require.Equal(t, 100*time.Millisecond, perr.Timeout)
		require.Equal(t, "monty worker killed after exceeding request timeout of 100ms", perr.Error())
		require.False(t, w.Alive())
		require.True(t, co.Finished())
		require.Eventually(t, func() bool { live, _ := p.Size(); return live == 0 }, 5*time.Second, 10*time.Millisecond)
	})
	t.Run("duration backstop fires without a request timeout", func(t *testing.T) {
		p := turnPool(t, func() *turnWorker { return newTurnWorker(okThen(silent)) }, func(c *Config) { c.DurationLimitGrace = 50 * time.Millisecond })
		co, err := p.Checkout(ctx, wire.Configure{Limits: &wire.Limits{MaxDurationMicros: u64(100_000)}}, CheckoutOptions{})
		require.NoError(t, err)
		start := time.Now()
		_, err = co.Feed(ctx, "x", nil, nil, "", nil, false, nil)
		require.Equal(t, 150*time.Millisecond, poolErr(t, err).Timeout)
		require.GreaterOrEqual(t, time.Since(start), 140*time.Millisecond)
	})
	t.Run("backstop subtracts reported execution time", func(t *testing.T) {
		feeds := 0
		p := turnPool(t, func() *turnWorker {
			return newTurnWorker(okThen(func(*pb.ParentRequest) turnReply {
				feeds++
				if feeds == 1 {
					return turnReply{events: []*pb.ChildEvent{turnComplete(1, 80_000)}}
				}
				return turnReply{}
			}))
		}, func(c *Config) { c.DurationLimitGrace = 20 * time.Millisecond })
		co, err := p.Checkout(ctx, wire.Configure{Limits: &wire.Limits{MaxDurationMicros: u64(100_000)}}, CheckoutOptions{})
		require.NoError(t, err)
		_, err = co.Feed(ctx, "a", nil, nil, "", nil, false, nil)
		require.NoError(t, err)
		_, err = co.Feed(ctx, "b", nil, nil, "", nil, false, nil)
		require.Equal(t, 40*time.Millisecond, poolErr(t, err).Timeout)
	})
	t.Run("control turns ignore the backstop", func(t *testing.T) {
		p := turnPool(t, func() *turnWorker { return newTurnWorker(okThen(silent)) }, func(c *Config) {
			c.DurationLimitGrace = 10 * time.Millisecond
			c.RequestTimeout = 150 * time.Millisecond
		})
		co, err := p.Checkout(ctx, wire.Configure{Limits: &wire.Limits{MaxDurationMicros: u64(10_000)}}, CheckoutOptions{})
		require.NoError(t, err)
		_, err = co.Dump(ctx)
		require.Equal(t, 150*time.Millisecond, poolErr(t, err).Timeout)
	})
	t.Run("disabled grace leaves no deadline", func(t *testing.T) {
		p := turnPool(t, func() *turnWorker { return newTurnWorker(okThen(silent)) }, func(c *Config) { c.GraceDisabled = true })
		co, err := p.Checkout(ctx, wire.Configure{Limits: &wire.Limits{MaxDurationMicros: u64(1_000)}}, CheckoutOptions{})
		require.NoError(t, err)
		cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err = co.Feed(cctx, "x", nil, nil, "", nil, false, nil)
		perr := poolErr(t, err)
		require.Equal(t, KindCancelled, perr.Kind)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		_, err = co.Feed(ctx, "y", nil, nil, "", nil, false, nil)
		require.Equal(t, "a previous protocol turn was cancelled mid-flight; the worker was discarded", poolErr(t, err).Error())
	})
}

func TestTurnSuspensionLimit(t *testing.T) {
	ctx := context.Background()
	var w *turnWorker
	p := turnPool(t, func() *turnWorker {
		w = newTurnWorker(func(req *pb.ParentRequest) turnReply {
			switch req.GetKind().(type) {
			case *pb.ParentRequest_Configure:
				ev := turnOk()
				ev.MaxSuspensions = u64(1)
				return turnReply{events: []*pb.ChildEvent{ev}}
			case *pb.ParentRequest_AbortFeed:
				return turnReply{events: []*pb.ChildEvent{turnError("RuntimeError", req.GetAbortFeed().GetException().GetMessage())}}
			}
			return turnReply{events: []*pb.ChildEvent{turnCall(1, false)}}
		})
		return w
	}, nil)
	co, err := p.Checkout(ctx, wire.Configure{Limits: &wire.Limits{MaxSuspensions: u64(5)}}, CheckoutOptions{})
	require.NoError(t, err)
	ev, err := co.Feed(ctx, "x", nil, nil, "", nil, false, nil)
	require.NoError(t, err)
	require.Equal(t, wire.EventFunctionCall, ev.Kind)
	_, err = co.Resume(ctx, wire.ExtResult{Kind: wire.ExtReturn, Value: int64(1)}, nil)
	perr := poolErr(t, err)
	require.Equal(t, KindRuntime, perr.Kind)
	require.Equal(t, "suspension limit 1 exceeded", perr.Exception.MessageText())
	kinds := []string{}
	for _, r := range w.sent() {
		kinds = append(kinds, scriptedRequestTag(r))
	}
	require.Equal(t, []string{"configure", "feed", "resume-call", "abort-feed"}, kinds)
}

func TestTurnCwd(t *testing.T) {
	ctx := context.Background()
	var w *turnWorker
	typingFirst := false
	p := turnPool(t, func() *turnWorker {
		w = newTurnWorker(okThen(func(*pb.ParentRequest) turnReply {
			if typingFirst {
				typingFirst = false
				return turnReply{events: []*pb.ChildEvent{{Kind: &pb.ChildEvent_TypingError{TypingError: &pb.TypingError{Diagnostics: "bad"}}}}}
			}
			return turnReply{events: []*pb.ChildEvent{turnComplete(1, 0)}}
		}))
		return w
	}, nil)
	feedCwd := func() string {
		reqs := w.sent()
		return reqs[len(reqs)-1].GetFeed().GetCwd()
	}
	co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	_, err = co.Feed(ctx, "a", nil, nil, "/mnt/data", nil, false, nil)
	require.NoError(t, err)
	require.Equal(t, "/mnt/data", feedCwd())
	_, err = co.Feed(ctx, "b", nil, nil, "/mnt/data", nil, false, nil)
	require.NoError(t, err)
	require.Equal(t, "", feedCwd())
	dir := "/work//"
	_, err = co.Feed(ctx, "c", nil, nil, "", &dir, false, nil)
	require.NoError(t, err)
	require.Equal(t, "/work", feedCwd())
	before := len(w.sent())
	bad := "data"
	_, err = co.Feed(ctx, "d", nil, nil, "", &bad, false, nil)
	perr := poolErr(t, err)
	require.Equal(t, "ValueError", perr.Exception.ExcType)
	require.Equal(t, `cwd must be an absolute POSIX path: "data"`, perr.Exception.MessageText())
	require.Len(t, w.sent(), before)
	require.NoError(t, co.Finish(ctx))

	typingFirst = true
	co, err = p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	_, err = co.Feed(ctx, "e", nil, nil, "", nil, false, nil)
	require.Equal(t, KindTyping, poolErr(t, err).Kind)
	_, err = co.Feed(ctx, "f", nil, nil, "", nil, false, nil)
	require.NoError(t, err)
	require.Equal(t, "/", feedCwd())
}

func TestTurnWorkerDeath(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		reply turnReply
		check func(t *testing.T, perr *Error)
	}{
		{"fatal error is announced", turnReply{
			events: []*pb.ChildEvent{{Kind: &pb.ChildEvent_FatalError{FatalError: &pb.FatalError{Message: "unsupported protocol version"}}}},
			exit:   &worker.Status{Known: true, Exited: true, Code: 4},
		}, func(t *testing.T, perr *Error) {
			require.Equal(t, KindCrashed, perr.Kind)
			require.Equal(t, "monty worker crashed: unsupported protocol version (exit status: 4)", perr.Error())
		}},
		{"exit code 65 is a memory error", turnReply{exit: &worker.Status{Known: true, Exited: true, Code: 65}}, func(t *testing.T, perr *Error) {
			require.Equal(t, KindRuntime, perr.Kind)
			require.True(t, perr.WorkerLost)
			require.Equal(t, "MemoryError", perr.Exception.ExcType)
			require.Equal(t, "the worker exceeded its memory limit and was terminated", perr.Exception.MessageText())
		}},
		{"a vanished worker reports its status", turnReply{exit: &worker.Status{Known: true, Signal: 9}}, func(t *testing.T, perr *Error) {
			require.Equal(t, "monty worker crashed while waiting for a reply (signal: 9 (SIGKILL))", perr.Error())
		}},
		{"a local ShutdownDump is a protocol violation", turnReply{events: []*pb.ChildEvent{{Kind: &pb.ChildEvent_Shutdown{Shutdown: &pb.ShutdownDump{}}}}}, func(t *testing.T, perr *Error) {
			require.Equal(t, "monty worker protocol error: subprocess worker sent a ShutdownDump", perr.Error())
		}},
		{"an event without a kind is a protocol violation", turnReply{events: []*pb.ChildEvent{{}}}, func(t *testing.T, perr *Error) {
			require.Equal(t, "monty worker protocol error: unexpected event", perr.Error())
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := turnPool(t, func() *turnWorker { return newTurnWorker(okThen(func(*pb.ParentRequest) turnReply { return c.reply })) }, nil)
			co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
			require.NoError(t, err)
			_, err = co.Feed(ctx, "x", nil, nil, "", nil, false, nil)
			c.check(t, poolErr(t, err))
			_, err = co.Feed(ctx, "y", nil, nil, "", nil, false, nil)
			require.Equal(t, KindFinished, poolErr(t, err).Kind)
		})
	}
}

func TestTurnPrintOrderAndMisuse(t *testing.T) {
	ctx := context.Background()
	p := turnPool(t, func() *turnWorker {
		return newTurnWorker(okThen(func(req *pb.ParentRequest) turnReply {
			if req.GetFeed().GetCode() == "print" {
				return turnReply{events: []*pb.ChildEvent{
					{Kind: &pb.ChildEvent_Print{Print: &pb.Print{Segments: []*pb.PrintSegment{{Stream: pb.PrintStream_PRINT_STREAM_STDOUT, Text: "a"}, {Stream: pb.PrintStream_PRINT_STREAM_STDERR, Text: "b"}}}}},
					{Kind: &pb.ChildEvent_Print{Print: &pb.Print{Segments: []*pb.PrintSegment{{Stream: pb.PrintStream_PRINT_STREAM_STDOUT, Text: "c"}}}}},
					turnComplete(7, 0),
				}}
			}
			return turnReply{events: []*pb.ChildEvent{turnCall(3, true)}}
		}))
	}, nil)
	co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	var got []string
	ev, err := co.Feed(ctx, "print", nil, nil, "", nil, false, func(stream uint8, text string) {
		got = append(got, string('0'+stream)+text)
	})
	require.NoError(t, err)
	require.Equal(t, int64(7), ev.Value)
	require.Equal(t, []string{"1a", "2b", "1c"}, got)

	_, err = co.Feed(ctx, "call", nil, nil, "", nil, false, nil)
	require.NoError(t, err)
	_, err = co.Feed(ctx, "again", nil, nil, "", nil, false, nil)
	require.Equal(t, "monty worker protocol error: feed called while a suspension is awaiting an answer", poolErr(t, err).Error())
	_, err = co.Resume(ctx, wire.ExtResult{Kind: wire.ExtNotHandled}, nil)
	require.Equal(t, "monty worker protocol error: NotHandled is only valid answering an OS call", poolErr(t, err).Error())
	_, err = co.ResumeNameLookup(ctx, wire.ResumeNameLookup{Kind: wire.LookupUndefined}, nil)
	require.Equal(t, "monty worker protocol error: no suspended name lookup to resume", poolErr(t, err).Error())
	_, err = co.ResumeFutures(ctx, []wire.FutureResult{{CallID: 9, Result: wire.ExtResult{Kind: wire.ExtReturn, Value: int64(1)}}}, nil)
	require.Equal(t, "monty worker protocol error: eager result must match the suspended call id", poolErr(t, err).Error())
	require.False(t, co.Finished())
}

func TestTurnInstallDependenciesValidation(t *testing.T) {
	ctx := context.Background()
	var w *turnWorker
	p := turnPool(t, func() *turnWorker { w = newTurnWorker(okThen(silent)); return w }, nil)
	co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	require.NoError(t, co.InstallDependencies(ctx, nil))
	require.Len(t, w.sent(), 1)
	err = co.InstallDependencies(ctx, []string{" "})
	require.Equal(t, `invalid requirement " ": must not be empty`, poolErr(t, err).Exception.MessageText())
	err = co.InstallDependencies(ctx, []string{"-e x"})
	require.Equal(t, `invalid requirement "-e x": must not start with '-' (it would be parsed as a uv option)`, poolErr(t, err).Exception.MessageText())
	require.Len(t, w.sent(), 1)
}

func TestTurnRestore(t *testing.T) {
	ctx := context.Background()
	suspended := false
	p := turnPool(t, func() *turnWorker {
		return newTurnWorker(okThen(func(req *pb.ParentRequest) turnReply {
			if _, ok := req.GetKind().(*pb.ParentRequest_Load); ok {
				if suspended {
					return turnReply{events: []*pb.ChildEvent{turnCall(4, false)}}
				}
				ev := turnOk()
				name := "restored.py"
				ev.RestoredScriptName = &name
				return turnReply{events: []*pb.ChildEvent{ev}}
			}
			return turnReply{events: []*pb.ChildEvent{turnComplete(1, 0)}}
		}))
	}, nil)
	co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	ev, name, err := co.Restore(ctx, []byte("state"), nil, nil)
	require.NoError(t, err)
	require.Nil(t, ev)
	require.Equal(t, "restored.py", *name)
	require.NoError(t, co.Finish(ctx))

	suspended = true
	co, err = p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	ev, _, err = co.Restore(ctx, []byte("state"), nil, nil)
	require.NoError(t, err)
	require.Equal(t, wire.EventFunctionCall, ev.Kind)
	require.Equal(t, uint32(4), ev.FunctionCall.CallID)
}

func TestTurnPoolCapacity(t *testing.T) {
	ctx := context.Background()
	t.Run("finished workers return to the idle list", func(t *testing.T) {
		p := turnPool(t, func() *turnWorker { return newTurnWorker(okThen(silent)) }, nil)
		co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
		require.NoError(t, err)
		require.NoError(t, co.Finish(ctx))
		live, idle := p.Size()
		require.Equal(t, 1, live)
		require.Equal(t, 1, idle)
	})
	t.Run("max checkouts per worker recycles", func(t *testing.T) {
		spawned := 0
		p := turnPool(t, func() *turnWorker { spawned++; return newTurnWorker(okThen(silent)) }, func(c *Config) { c.MaxCheckoutsPerWorker = 1 })
		for i := 0; i < 2; i++ {
			co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
			require.NoError(t, err)
			require.NoError(t, co.Finish(ctx))
		}
		require.Equal(t, 2, spawned)
	})
	t.Run("exhausted checkout times out", func(t *testing.T) {
		p := turnPool(t, func() *turnWorker { return newTurnWorker(okThen(silent)) }, func(c *Config) { c.CheckoutTimeout = 50 * time.Millisecond })
		held, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
		require.NoError(t, err)
		_, err = p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
		require.Equal(t, "no monty worker became available within the checkout timeout", poolErr(t, err).Error())
		require.NoError(t, held.Finish(ctx))
		next, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
		require.NoError(t, err)
		require.NoError(t, next.Finish(ctx))
	})
	t.Run("invalid sizes are rejected", func(t *testing.T) {
		_, err := New(ctx, Config{Spawner: turnSpawner{}, MinProcesses: 2, MaxProcesses: 1})
		require.Equal(t, "failed to spawn monty worker: invalid pool size: min_processes=2 max_processes=1", err.Error())
	})
}

func TestFormatRustDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		500 * time.Millisecond:  "500ms",
		time.Second:             "1s",
		1500 * time.Millisecond: "1.5s",
		2500 * time.Microsecond: "2.5ms",
		750 * time.Microsecond:  "750µs",
		100 * time.Nanosecond:   "100ns",
		time.Minute:             "60s",
		0:                       "0ns",
	} {
		require.Equal(t, want, FormatRustDuration(d))
	}
}
