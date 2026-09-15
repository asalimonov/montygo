package pool

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/asalimonov/montygo/internal/wire"
	pb "github.com/asalimonov/montygo/montypb"
)

type observedKey struct{}

type recordingObserver struct {
	mu     sync.Mutex
	events []string
	closed int
	done   chan struct{}
}

func newRecordingObserver() *recordingObserver {
	return &recordingObserver{done: make(chan struct{})}
}

func (o *recordingObserver) Sent(req wire.Request, frameLen int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if frameLen <= 0 {
		o.events = append(o.events, "empty frame")
	}
	o.events = append(o.events, "sent "+req.Name())
}

func (o *recordingObserver) Received(ev *wire.Event, frameLen int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if frameLen <= 0 {
		o.events = append(o.events, "empty frame")
	}
	o.events = append(o.events, "received "+ev.Kind.String())
}

func (o *recordingObserver) Closed() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed++
	if o.closed == 1 {
		close(o.done)
	}
}

func (o *recordingObserver) CallbackContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, observedKey{}, "observed")
}

func (o *recordingObserver) snapshot() ([]string, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...), o.closed
}

func (o *recordingObserver) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-o.done:
	case <-time.After(5 * time.Second):
		t.Fatal("observer was not closed")
	}
}

func observe(o *recordingObserver) CheckoutOptions {
	return CheckoutOptions{Observe: func(int, bool) Observer { return o }}
}

func turnPrint(text string) *pb.ChildEvent {
	return &pb.ChildEvent{Kind: &pb.ChildEvent_Print{Print: &pb.Print{Segments: []*pb.PrintSegment{{Stream: pb.PrintStream_PRINT_STREAM_STDOUT, Text: text}}}}}
}

func TestObserverSeesEveryTurnAndClosesOnce(t *testing.T) {
	ctx := context.Background()
	p := turnPool(t, func() *turnWorker {
		return newTurnWorker(okThen(func(*pb.ParentRequest) turnReply {
			return turnReply{events: []*pb.ChildEvent{turnPrint("hi"), turnComplete(2, 10)}}
		}))
	}, nil)
	obs := newRecordingObserver()
	hasPID := true
	co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{Observe: func(_ int, ok bool) Observer {
		hasPID = ok
		return obs
	}})
	require.NoError(t, err)
	require.False(t, hasPID)
	var printed []string
	_, err = co.Feed(ctx, "1 + 1", nil, nil, "", nil, false, func(_ uint8, text string) { printed = append(printed, text) })
	require.NoError(t, err)
	require.Equal(t, []string{"hi"}, printed)
	require.Equal(t, "observed", co.CallbackContext(ctx).Value(observedKey{}))
	require.NoError(t, co.Finish(ctx))
	obs.waitClosed(t)
	require.NoError(t, co.Finish(ctx))

	events, closed := obs.snapshot()
	require.Equal(t, []string{"sent Configure", "received Ok", "sent Feed", "received Print", "received Complete", "sent Reset", "received Ok"}, events)
	require.Equal(t, 1, closed)
}

func TestObserverSeesTheShutdownOfARecycledWorker(t *testing.T) {
	ctx := context.Background()
	p := turnPool(t, func() *turnWorker { return newTurnWorker(okThen(silent)) }, func(c *Config) { c.MaxCheckoutsPerWorker = 1 })
	obs := newRecordingObserver()
	co, err := p.Checkout(ctx, wire.Configure{}, observe(obs))
	require.NoError(t, err)
	require.NoError(t, co.Finish(ctx))
	obs.waitClosed(t)

	events, closed := obs.snapshot()
	require.Equal(t, []string{"sent Configure", "received Ok", "sent Reset", "received Ok", "sent Shutdown"}, events)
	require.Equal(t, 1, closed)
}

func TestObserverSeesTheInternalAbortFeed(t *testing.T) {
	ctx := context.Background()
	p := turnPool(t, func() *turnWorker {
		return newTurnWorker(func(req *pb.ParentRequest) turnReply {
			switch {
			case req.GetConfigure() != nil:
				return turnReply{events: []*pb.ChildEvent{{Kind: &pb.ChildEvent_Ok{Ok: &pb.Ok{}}, MaxSuspensions: proto.Uint64(1)}}}
			case req.GetAbortFeed() != nil:
				return turnReply{events: []*pb.ChildEvent{turnError("RuntimeError", "suspension limit 1 exceeded")}}
			}
			return turnReply{events: []*pb.ChildEvent{turnCall(1, false)}}
		})
	}, nil)
	obs := newRecordingObserver()
	co, err := p.Checkout(ctx, wire.Configure{}, observe(obs))
	require.NoError(t, err)
	ev, err := co.Feed(ctx, "ext()", nil, nil, "", nil, false, nil)
	require.NoError(t, err)
	require.Equal(t, wire.EventFunctionCall, ev.Kind)
	_, err = co.Resume(ctx, wire.ExtResult{Kind: wire.ExtReturn}, nil)
	require.Equal(t, KindRuntime, poolErr(t, err).Kind)
	co.Abandon()
	obs.waitClosed(t)

	events, closed := obs.snapshot()
	require.Equal(t, []string{
		"sent Configure", "received Ok",
		"sent Feed", "received FunctionCall",
		"sent ResumeCall", "received FunctionCall",
		"sent AbortFeed", "received Error",
	}, events)
	require.Equal(t, 1, closed)
}

func TestObserverClosesWhenTheWorkerIsDiscarded(t *testing.T) {
	ctx := context.Background()
	p := turnPool(t, func() *turnWorker { return newTurnWorker(okThen(silent)) }, nil)
	obs := newRecordingObserver()
	co, err := p.Checkout(ctx, wire.Configure{}, observe(obs))
	require.NoError(t, err)
	co.Abandon()
	obs.waitClosed(t)
	require.NoError(t, co.Finish(ctx))

	events, closed := obs.snapshot()
	require.Equal(t, []string{"sent Configure", "received Ok"}, events)
	require.Equal(t, 1, closed)
}

type waitMetrics struct {
	noopMetrics
	mu       sync.Mutex
	outcomes []string
}

func (m *waitMetrics) CheckoutWait(_ time.Duration, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outcomes = append(m.outcomes, outcome)
}

func (m *waitMetrics) taken() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.outcomes...)
}

func TestCheckoutWaitOutcomes(t *testing.T) {
	ctx := context.Background()
	metrics := &waitMetrics{}
	worker := func() *turnWorker { return newTurnWorker(okThen(silent)) }
	p := turnPool(t, worker, func(c *Config) { c.Metrics = metrics })

	first, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	require.NoError(t, first.Finish(ctx))
	held, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	waiting := make(chan error, 1)
	go func() {
		co, err := p.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
		if err == nil {
			err = co.Finish(ctx)
		}
		waiting <- err
	}()
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, held.Finish(ctx))
	require.NoError(t, <-waiting)

	short := turnPool(t, worker, func(c *Config) {
		c.Metrics = metrics
		c.CheckoutTimeout = 20 * time.Millisecond
	})
	blocker, err := short.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.NoError(t, err)
	_, err = short.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.Equal(t, KindExhausted, poolErr(t, err).Kind)
	require.NoError(t, blocker.Finish(ctx))
	require.NoError(t, short.Close(ctx))
	_, err = short.Checkout(ctx, wire.Configure{}, CheckoutOptions{})
	require.Equal(t, KindClosed, poolErr(t, err).Kind)

	require.Equal(t, []string{"spawned", "idle", "waited", "spawned", "exhausted", "error"}, metrics.taken())
}
