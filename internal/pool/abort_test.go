package pool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/internal/worker"
	"github.com/asalimonov/montygo/montypb"
)

type scriptedWorker struct {
	mu       sync.Mutex
	requests []string
	frames   [][]byte
	notify   chan struct{}
	killed   bool
	reply    func(tag string, requests []string) *montypb.ChildEvent
}

func newScriptedWorker(reply func(tag string, requests []string) *montypb.ChildEvent) *scriptedWorker {
	return &scriptedWorker{notify: make(chan struct{}), reply: reply}
}

func scriptedRequestTag(req *montypb.ParentRequest) string {
	switch req.GetKind().(type) {
	case *montypb.ParentRequest_Configure:
		return "configure"
	case *montypb.ParentRequest_Feed:
		return "feed"
	case *montypb.ParentRequest_ResumeCall:
		return "resume-call"
	case *montypb.ParentRequest_ResumeNameLookup:
		return "resume-name-lookup"
	case *montypb.ParentRequest_ResumeFutures:
		return "resume-futures"
	case *montypb.ParentRequest_Dump:
		return "dump"
	case *montypb.ParentRequest_Load:
		return "load"
	case *montypb.ParentRequest_Reset_:
		return "reset"
	case *montypb.ParentRequest_Shutdown:
		return "shutdown"
	case *montypb.ParentRequest_AbortFeed:
		return "abort-feed"
	}
	return fmt.Sprintf("%T", req.GetKind())
}

func (w *scriptedWorker) Send(_ context.Context, payload []byte) error {
	req := &montypb.ParentRequest{}
	if err := proto.Unmarshal(payload, req); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.killed {
		return worker.ErrWorkerGone
	}
	tag := scriptedRequestTag(req)
	w.requests = append(w.requests, tag)
	frame, err := proto.Marshal(w.reply(tag, w.requests))
	if err != nil {
		return err
	}
	w.frames = append(w.frames, frame)
	close(w.notify)
	w.notify = make(chan struct{})
	return nil
}

func (w *scriptedWorker) Recv(ctx context.Context) ([]byte, error) {
	for {
		w.mu.Lock()
		if len(w.frames) > 0 {
			frame := w.frames[0]
			w.frames = w.frames[1:]
			w.mu.Unlock()
			return frame, nil
		}
		if w.killed {
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

func (w *scriptedWorker) Kill() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.killed {
		w.killed = true
		close(w.notify)
		w.notify = make(chan struct{})
	}
}

func (w *scriptedWorker) Close() { w.Kill() }

func (w *scriptedWorker) Wait(context.Context) (worker.Status, bool) {
	return worker.Status{Known: true, Killed: true}, true
}

func (w *scriptedWorker) PID() (int, bool) { return 0, false }

func (w *scriptedWorker) Kind() worker.Kind { return worker.KindWasm }

func (w *scriptedWorker) Alive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.killed
}

func (w *scriptedWorker) snapshot() ([]string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.requests...), w.killed
}

type scriptedSpawner struct{ w worker.Worker }

func (s scriptedSpawner) Spawn(context.Context) (worker.Worker, error) { return s.w, nil }
func (s scriptedSpawner) Kind() worker.Kind                            { return worker.KindWasm }
func (s scriptedSpawner) Close(context.Context) error                  { return nil }

type terminationMetrics struct {
	noopMetrics
	mu      sync.Mutex
	reasons []string
}

func (m *terminationMetrics) WorkerTerminated(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reasons = append(m.reasons, reason)
}

func TestSuspensionAnsweringAbortFeedDiscardsWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	call := func(callID int) *montypb.ChildEvent {
		return &montypb.ChildEvent{Kind: &montypb.ChildEvent_FunctionCall{
			FunctionCall: &montypb.FunctionCall{FunctionName: "fetch", CallId: uint32(callID)},
		}}
	}
	w := newScriptedWorker(func(tag string, requests []string) *montypb.ChildEvent {
		if tag == "configure" {
			return &montypb.ChildEvent{Kind: &montypb.ChildEvent_Ok{Ok: &montypb.Ok{}}, MaxSuspensions: proto.Uint64(1)}
		}
		return call(len(requests))
	})
	metrics := &terminationMetrics{}
	p, err := New(ctx, Config{Spawner: scriptedSpawner{w: w}, MaxProcesses: 1, Metrics: metrics})
	require.NoError(t, err)
	defer p.Close(ctx)

	c, err := p.Checkout(ctx, wire.Configure{ScriptName: "main.py"}, CheckoutOptions{})
	require.NoError(t, err)

	first, err := c.Feed(ctx, "fetch()", nil, nil, "", nil, true, nil)
	require.NoError(t, err)
	require.Equal(t, wire.EventFunctionCall, first.Kind)

	turn, err := c.Resume(ctx, wire.ExtResult{Kind: wire.ExtReturn, Value: nil}, nil)
	require.Nil(t, turn)
	var perr *Error
	require.True(t, errors.As(err, &perr), "expected *pool.Error, got %T: %v", err, err)
	require.Equal(t, KindProtocol, perr.Kind)
	require.Equal(t, "worker answered AbortFeed with something other than an Error", perr.Message)

	requests, killed := w.snapshot()
	require.Equal(t, []string{"configure", "feed", "resume-call", "abort-feed"}, requests)
	require.True(t, killed)
	require.True(t, c.Finished())

	require.NoError(t, c.Finish(ctx))
	requests, _ = w.snapshot()
	require.Equal(t, []string{"configure", "feed", "resume-call", "abort-feed"}, requests)
	live, idle := p.Size()
	require.Zero(t, live)
	require.Zero(t, idle)
	metrics.mu.Lock()
	require.Equal(t, []string{"discarded"}, metrics.reasons)
	metrics.mu.Unlock()
}
