package monty_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/baggage"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	monty "github.com/asalimonov/montygo"
)

type telStorageKey struct{}

func telStorage(ctx context.Context) any { return ctx.Value(telStorageKey{}) }

type telPrintContext func(ctx context.Context, stream monty.Stream, text string) error

func (f telPrintContext) Print(stream monty.Stream, text string) error {
	return f(context.Background(), stream, text)
}

func (f telPrintContext) PrintContext(ctx context.Context, stream monty.Stream, text string) error {
	return f(ctx, stream, text)
}

type telTracerFunc struct {
	tracenoop.Tracer
	start func(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span)
}

func (f telTracerFunc) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return f.start(ctx, name, opts...)
}

type telAutoResumer interface {
	ResumeAuto(ctx context.Context) (monty.Snapshot, error)
}

func TestCallbackContext(t *testing.T) {
	cases := []telCase{
		{"concurrent host callbacks inherit their Monty span and caller async storage", telConcurrentCallbacks},
	}
	for _, mode := range []string{"disabled", "broken-tracer", "broken-context", "sampled-out"} {
		cases = append(cases, telCase{"callback context fallback: " + mode, func(t *testing.T, b monty.Backend) {
			telCallbackFallback(t, b, mode)
		}})
	}
	cases = append(cases,
		telCase{"callback failures are not retried and do not leak context", telCallbackFailures},
		telCase{"snapshot resumes capture the resuming caller storage and retain the Monty parent", telSnapshotResumes},
	)
	telRun(t, "TestCallbackContext", cases)
}

func telConcurrentCallbacks(t *testing.T, b monty.Backend) {
	ctx := testCtx(t)
	tp, recorder := telTracing()
	tracer := tp.Tracer("callbacks")
	require.NoError(t, monty.Instrument(monty.TelemetryComponents{Tracer: tracer}))
	p := telPool(t, b, monty.Options{MinProcesses: 2, MaxProcesses: 2})
	var wg sync.WaitGroup
	for _, index := range []int{1, 2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			telCallbacksFor(t, ctx, p, tracer, index)
		}()
	}
	wg.Wait()
	require.NoError(t, p.Close(ctx))
	require.NoError(t, monty.Flush(ctx))

	spans := recorder.Ended()
	byID := map[trace.SpanID]sdktrace.ReadOnlySpan{}
	for _, s := range spans {
		byID[s.SpanContext().SpanID()] = s
	}
	root := func(s sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
		for s.Parent().IsValid() {
			parent, ok := byID[s.Parent().SpanID()]
			require.True(t, ok, "parent of %s was not recorded", s.Name())
			s = parent
		}
		return s
	}
	for _, index := range []int{1, 2} {
		host := fmt.Sprintf("host %d", index)
		for _, kind := range [][2]string{
			{"print", "run code"},
			{"sync", "call {function_name}"},
			{"async", "call {function_name}"},
			{"os", "os call {function}"},
		} {
			span := telFind(spans, telNamed(fmt.Sprintf("%s %d", kind[0], index)))
			require.NotNil(t, span, kind[0])
			require.Equal(t, kind[1], byID[span.Parent().SpanID()].Name(), kind[0])
			require.Equal(t, host, root(span).Name(), kind[0])
		}
		lookup := telFind(spans, func(s sdktrace.ReadOnlySpan) bool {
			name, _ := telSpanAttr(s, "name")
			return s.Name() == "name lookup {name}" && name.AsString() == "lazy_value" && root(s).Name() == host
		})
		require.NotNil(t, lookup, "lookup")
		require.Equal(t, "run code", byID[lookup.Parent().SpanID()].Name())
	}
	require.NoError(t, tp.Shutdown(ctx))
}

func telCallbacksFor(t *testing.T, base context.Context, p *monty.Pool, tracer trace.Tracer, index int) {
	member, err := baggage.NewMember("request", strconv.Itoa(index))
	if !assert.NoError(t, err) {
		return
	}
	bag, err := baggage.New(member)
	if !assert.NoError(t, err) {
		return
	}
	ctx, host := tracer.Start(baggage.ContextWithBaggage(context.WithValue(base, telStorageKey{}, index), bag), fmt.Sprintf("host %d", index))
	defer host.End()
	check := func(ctx context.Context) {
		assert.Equal(t, index, telStorage(ctx))
		assert.Equal(t, strconv.Itoa(index), baggage.FromContext(ctx).Member("request").Value())
	}
	child := func(ctx context.Context, name string) {
		check(ctx)
		_, span := tracer.Start(ctx, fmt.Sprintf("%s %d", name, index))
		span.End()
	}

	s, err := p.Checkout(ctx, monty.CheckoutOptions{ScriptName: strconv.Itoa(index)})
	if !assert.NoError(t, err) {
		return
	}
	v, err := s.FeedRun(ctx, "print('hello'); sync_callback() + await async_callback()", &monty.FeedOptions{
		Print: telPrintContext(func(ctx context.Context, _ monty.Stream, _ string) error {
			child(ctx, "print")
			return nil
		}),
		ExternalLookup: map[string]any{
			"sync_callback": func(ctx context.Context) int {
				child(ctx, "sync")
				return 10
			},
			"async_callback": func(ctx context.Context) *monty.Future {
				check(ctx)
				return monty.Async(func() (any, error) {
					spanCtx, span := tracer.Start(ctx, fmt.Sprintf("async %d", index))
					defer span.End()
					time.Sleep(10 * time.Millisecond)
					check(spanCtx)
					assert.True(t, telSameSpan(span, trace.SpanFromContext(spanCtx)))
					return 20, nil
				})
			},
		},
	})
	assert.NoError(t, err)
	assert.Equal(t, int64(30), v)

	v, err = s.FeedRun(ctx, "from pathlib import Path; Path('/x').exists()", &monty.FeedOptions{
		OS: func(ctx context.Context, _ string, _ []any, _ monty.Kwargs) (any, error) {
			child(ctx, "os")
			return true, nil
		},
	})
	assert.NoError(t, err)
	assert.Equal(t, true, v)

	v, err = s.FeedRun(ctx, "lazy_value", &monty.FeedOptions{ExternalLookup: map[string]any{"lazy_value": 7}})
	assert.NoError(t, err)
	assert.Equal(t, int64(7), v)

	assert.True(t, telSameSpan(host, trace.SpanFromContext(ctx)))
	check(ctx)
	assert.NoError(t, s.Close(ctx))
}

func telCallbackFallback(t *testing.T, b monty.Backend, mode string) {
	ctx := testCtx(t)
	tp, _ := telTracing()
	tracer := tp.Tracer("callbacks")
	offProvider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	offTracer := offProvider.Tracer("not-recording")
	var mu sync.Mutex
	var runSpan trace.Span
	if mode != "disabled" {
		require.NoError(t, monty.Instrument(monty.TelemetryComponents{Tracer: telTracerFunc{
			start: func(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
				switch mode {
				case "broken-tracer":
					panic("tracer failed")
				case "broken-context":
					return ctx, nil
				}
				use := tracer
				if mode == "sampled-out" {
					use = offTracer
				}
				spanCtx, span := use.Start(ctx, name, opts...)
				if name == "run code" {
					mu.Lock()
					runSpan = span
					mu.Unlock()
				}
				return spanCtx, span
			},
		}}))
	}

	hostCtx, host := tracer.Start(context.WithValue(ctx, telStorageKey{}, "caller"), "host")
	p := telPool(t, b, monty.Options{})
	s, err := p.Checkout(hostCtx, monty.CheckoutOptions{})
	require.NoError(t, err)
	var count atomic.Int32
	v, err := s.FeedRun(hostCtx, "print('hello'); 42", &monty.FeedOptions{
		Print: telPrintContext(func(cbCtx context.Context, _ monty.Stream, _ string) error {
			count.Add(1)
			assert.Equal(t, "caller", telStorage(cbCtx))
			want := host
			if mode == "sampled-out" {
				mu.Lock()
				want = runSpan
				mu.Unlock()
				if assert.NotNil(t, want) {
					assert.False(t, want.IsRecording())
				}
			}
			assert.True(t, telSameSpan(want, trace.SpanFromContext(cbCtx)))
			return nil
		}),
	})
	require.NoError(t, err)
	require.Equal(t, int64(42), v)
	require.Equal(t, int32(1), count.Load())
	require.True(t, telSameSpan(host, trace.SpanFromContext(hostCtx)))
	require.NoError(t, s.Close(ctx))
	require.NoError(t, p.Close(ctx))
	host.End()
	require.NoError(t, offProvider.Shutdown(ctx))
	require.NoError(t, monty.Flush(ctx))
	require.NoError(t, tp.Shutdown(ctx))
}

func telCallbackFailures(t *testing.T, b monty.Backend) {
	ctx := testCtx(t)
	tp, _ := telTracing()
	tracer := tp.Tracer("callbacks")
	require.NoError(t, monty.Instrument(monty.TelemetryComponents{Tracer: tracer}))
	hostCtx, host := tracer.Start(context.WithValue(ctx, telStorageKey{}, "caller"), "host")
	p := telPool(t, b, monty.Options{})
	var count atomic.Int32

	s, err := p.Checkout(hostCtx, monty.CheckoutOptions{})
	require.NoError(t, err)
	_, err = s.FeedRun(hostCtx, "print('hello')", &monty.FeedOptions{
		Print: telPrintContext(func(cbCtx context.Context, _ monty.Stream, _ string) error {
			count.Add(1)
			assert.Equal(t, "caller", telStorage(cbCtx))
			return errors.New("print failed")
		}),
	})
	require.ErrorContains(t, err, "print failed")
	require.True(t, telSameSpan(host, trace.SpanFromContext(hostCtx)))
	require.NoError(t, s.Close(ctx))

	second, err := p.Checkout(hostCtx, monty.CheckoutOptions{})
	require.NoError(t, err)
	_, err = second.FeedRun(hostCtx, "fail()", &monty.FeedOptions{ExternalLookup: map[string]any{
		"fail": func(context.Context) error {
			count.Add(1)
			return errors.New("function failed")
		},
	}})
	require.ErrorContains(t, err, "function failed")
	require.True(t, telSameSpan(host, trace.SpanFromContext(hostCtx)))
	_, err = second.FeedRun(hostCtx, "await fail()", &monty.FeedOptions{ExternalLookup: map[string]any{
		"fail": func(context.Context) *monty.Future {
			count.Add(1)
			return monty.Async(func() (any, error) { return nil, errors.New("async failed") })
		},
	}})
	require.ErrorContains(t, err, "async failed")
	require.True(t, telSameSpan(host, trace.SpanFromContext(hostCtx)))
	require.Equal(t, int32(3), count.Load())
	require.NoError(t, second.Close(ctx))
	require.NoError(t, p.Close(ctx))
	host.End()
	require.NoError(t, monty.Flush(ctx))
	require.NoError(t, tp.Shutdown(ctx))
}

func telSnapshotResumes(t *testing.T, b monty.Backend) {
	ctx := testCtx(t)
	tp, recorder := telTracing()
	tracer := tp.Tracer("callbacks")
	require.NoError(t, monty.Instrument(monty.TelemetryComponents{Tracer: tracer}))
	p := telPool(t, b, monty.Options{})
	s, err := p.Checkout(ctx, monty.CheckoutOptions{})
	require.NoError(t, err)

	snap, err := s.FeedStart(context.WithValue(ctx, telStorageKey{}, "feed"), "value = await callback(); print(value); value", &monty.FeedOptions{
		ExternalLookup: map[string]any{
			"callback": func(ctx context.Context) *monty.Future {
				assert.Equal(t, "resume", telStorage(ctx))
				return monty.Async(func() (any, error) {
					spanCtx, span := tracer.Start(ctx, "snapshot callback")
					defer span.End()
					assert.Equal(t, "resume", telStorage(spanCtx))
					return 42, nil
				})
			},
		},
		Print: telPrintContext(func(ctx context.Context, _ monty.Stream, _ string) error {
			assert.Equal(t, "resume", telStorage(ctx))
			_, span := tracer.Start(ctx, "snapshot print")
			span.End()
			return nil
		}),
	})
	require.NoError(t, err)
	resumeCtx := context.WithValue(ctx, telStorageKey{}, "resume")
	for {
		if _, done := snap.(*monty.Complete); done {
			break
		}
		resumer, ok := snap.(telAutoResumer)
		require.True(t, ok, "%T", snap)
		snap, err = resumer.ResumeAuto(resumeCtx)
		require.NoError(t, err)
	}
	require.Equal(t, int64(42), snap.(*monty.Complete).Output)
	require.NoError(t, s.Close(ctx))
	require.NoError(t, p.Close(ctx))
	require.NoError(t, monty.Flush(ctx))

	spans := recorder.Ended()
	for _, pair := range [][2]string{{"snapshot callback", "call {function_name}"}, {"snapshot print", "run code"}} {
		span := telFind(spans, telNamed(pair[0]))
		require.NotNil(t, span, pair[0])
		parent := telFind(spans, func(s sdktrace.ReadOnlySpan) bool { return s.SpanContext().SpanID() == span.Parent().SpanID() })
		require.NotNil(t, parent, pair[0])
		require.Equal(t, pair[1], parent.Name(), pair[0])
	}
	require.NoError(t, tp.Shutdown(ctx))
}
