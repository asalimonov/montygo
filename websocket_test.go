package monty_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	monty "github.com/asalimonov/montygo"
)

type wsTraceKey struct{}

const wsUnreachableURL = "ws://127.0.0.1:9"

// wsNewPool creates a WebSocket pool closed at test end.
func wsNewPool(t *testing.T, opts monty.WebSocketOptions) *monty.Pool {
	t.Helper()
	p, err := monty.NewWebSocket(testCtx(t), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

// wsCheckout checks out a session closed at test end.
func wsCheckout(t *testing.T, ctx context.Context, p *monty.Pool) *monty.Session {
	t.Helper()
	s, err := p.Checkout(ctx, monty.CheckoutOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func TestWebSocket(t *testing.T) {
	t.Run("feed_run_over_websocket", func(t *testing.T) {
		relay := wsStartRelay(t)
		ctx := testCtx(t)
		p := wsNewPool(t, monty.WebSocketOptions{URL: relay.URL, RequestTimeout: 30 * time.Second})
		require.Equal(t, monty.BackendWebSocket, p.Backend())
		s := wsCheckout(t, ctx, p)
		v, err := s.FeedRun(ctx, "1 + 1", nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), v)
		_, err = s.FeedRun(ctx, "x = 21", nil)
		require.NoError(t, err)
		v, err = s.FeedRun(ctx, "x * 2", nil)
		require.NoError(t, err)
		require.Equal(t, int64(42), v)
	})

	t.Run("inputs_and_async_external_function_over_websocket", func(t *testing.T) {
		relay := wsStartRelay(t)
		ctx := testCtx(t)
		double := func(x int) *monty.Future {
			return monty.Async(func() (any, error) { return x * 2, nil })
		}
		p := wsNewPool(t, monty.WebSocketOptions{URL: relay.URL, RequestTimeout: 30 * time.Second})
		s := wsCheckout(t, ctx, p)
		v, err := s.FeedRun(ctx, "await double(n) + 1", &monty.FeedOptions{
			Inputs:         map[string]any{"n": 20},
			ExternalLookup: map[string]any{"double": double},
		})
		require.NoError(t, err)
		require.Equal(t, int64(41), v)
	})

	t.Run("separate_checkouts_are_isolated", func(t *testing.T) {
		relay := wsStartRelay(t)
		ctx := testCtx(t)
		p := wsNewPool(t, monty.WebSocketOptions{URL: relay.URL, RequestTimeout: 30 * time.Second})
		first, err := p.Checkout(ctx, monty.CheckoutOptions{})
		require.NoError(t, err)
		_, err = first.FeedRun(ctx, "leaked = 123", nil)
		require.NoError(t, err)
		require.NoError(t, first.Close(ctx))
		second := wsCheckout(t, ctx, p)
		_, err = second.FeedRun(ctx, "leaked", nil)
		var re *monty.RuntimeError
		require.ErrorAs(t, err, &re)
		require.Equal(t, "name 'leaked' is not defined", re.Display(monty.DisplayMsg))
	})

	t.Run("connect_headers_sent_per_checkout", func(t *testing.T) {
		relay := wsStartRelay(t)
		ctx := testCtx(t)
		var calls atomic.Int32
		p := wsNewPool(t, monty.WebSocketOptions{
			URL:            relay.URL,
			RequestTimeout: 30 * time.Second,
			ConnectHeaders: func(ctx context.Context) (map[string]string, error) {
				calls.Add(1)
				return map[string]string{"traceparent": ctx.Value(wsTraceKey{}).(string)}, nil
			},
		})
		traces := []string{"00-aaa-111-01", "00-bbb-222-01"}
		results := make([]any, len(traces))
		errs := make([]error, len(traces))
		var wg sync.WaitGroup
		for i, trace := range traces {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cctx := context.WithValue(ctx, wsTraceKey{}, trace)
				s, err := p.Checkout(cctx, monty.CheckoutOptions{})
				if err != nil {
					errs[i] = err
					return
				}
				defer s.Close(context.Background())
				results[i], errs[i] = s.FeedRun(cctx, "1 + 1", nil)
			}()
		}
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}
		require.Equal(t, []any{int64(2), int64(2)}, results)
		require.Equal(t, int32(2), calls.Load())
		var sent []string
		for _, h := range relay.captured() {
			sent = append(sent, h.Get("traceparent"))
		}
		sort.Strings(sent)
		require.Equal(t, traces, sent)
	})

	t.Run("connect_headers_accepts_any_mapping", func(t *testing.T) {
		relay := wsStartRelay(t)
		ctx := testCtx(t)
		type headerMap map[string]string
		source := headerMap{"x-token": "t"}
		p := wsNewPool(t, monty.WebSocketOptions{
			URL:            relay.URL,
			RequestTimeout: 30 * time.Second,
			ConnectHeaders: func(context.Context) (map[string]string, error) { return source, nil },
		})
		s := wsCheckout(t, ctx, p)
		v, err := s.FeedRun(ctx, "1 + 1", nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), v)
		captured := relay.captured()
		require.Len(t, captured, 1)
		require.Equal(t, "t", captured[0].Get("x-token"))
	})

	t.Run("connect_headers_not_callable", func(t *testing.T) {
		t.Skip("ConnectHeaders is a func field; a non-callable value does not compile in Go")
	})

	t.Run("connect_headers_failure_leaves_the_pool_usable", func(t *testing.T) {
		relay := wsStartRelay(t)
		ctx := testCtx(t)
		errNoToken := errors.New("no token yet")
		var attempts atomic.Int32
		p := wsNewPool(t, monty.WebSocketOptions{
			URL:            relay.URL,
			RequestTimeout: 30 * time.Second,
			ConnectHeaders: func(context.Context) (map[string]string, error) {
				if attempts.Add(1) == 1 {
					return nil, errNoToken
				}
				return map[string]string{"x-token": "t"}, nil
			},
		})
		_, err := p.Checkout(ctx, monty.CheckoutOptions{})
		require.ErrorIs(t, err, errNoToken)
		require.Equal(t, "no token yet", err.Error())
		s := wsCheckout(t, ctx, p)
		v, err := s.FeedRun(ctx, "1 + 1", nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), v)
		var tokens []string
		for _, h := range relay.captured() {
			tokens = append(tokens, h.Get("x-token"))
		}
		require.Equal(t, []string{"t"}, tokens)
	})

	t.Run("connect_headers_not_called_on_an_inactive_pool", func(t *testing.T) {
		ctx := testCtx(t)
		var calls atomic.Int32
		p, err := monty.NewWebSocket(ctx, monty.WebSocketOptions{
			URL: wsUnreachableURL,
			ConnectHeaders: func(context.Context) (map[string]string, error) {
				calls.Add(1)
				return map[string]string{}, nil
			},
		})
		require.NoError(t, err)
		require.NoError(t, p.Close(ctx))
		_, err = p.Checkout(ctx, monty.CheckoutOptions{})
		require.ErrorIs(t, err, monty.ErrPoolClosed)
		require.Equal(t, int32(0), calls.Load())
	})

	t.Run("connect_headers_errors_raise_on_entry", func(t *testing.T) {
		errNoToken := errors.New("no token available")
		spawnError := func(message string) func(t *testing.T, err error) {
			return func(t *testing.T, err error) {
				var se *monty.SpawnError
				require.ErrorAs(t, err, &se)
				require.Equal(t, message, err.Error())
			}
		}
		cases := []struct {
			name    string
			skip    string
			headers func(context.Context) (map[string]string, error)
			check   func(t *testing.T, err error)
		}{
			{name: "non_mapping_result", skip: "the callback returns map[string]string; a non-mapping result does not compile in Go"},
			{name: "non_str_header_name", skip: "map[string]string keys are always strings"},
			{name: "non_str_header_value", skip: "map[string]string values are always strings"},
			{
				name:    "invalid_header_name",
				headers: func(context.Context) (map[string]string, error) { return map[string]string{"bad header": "t"}, nil },
				check:   spawnError(`failed to spawn monty worker: ws://127.0.0.1:9: connect header "bad header": invalid HTTP header name`),
			},
			{
				name:    "invalid_header_value",
				headers: func(context.Context) (map[string]string, error) { return map[string]string{"x-token": "a\nb"}, nil },
				check:   spawnError(`failed to spawn monty worker: ws://127.0.0.1:9: connect header "x-token" value: failed to parse header value`),
			},
			{
				name:    "callback_error",
				headers: func(context.Context) (map[string]string, error) { return nil, errNoToken },
				check: func(t *testing.T, err error) {
					require.ErrorIs(t, err, errNoToken)
					require.Equal(t, "no token available", err.Error())
				},
			},
			{name: "unencodable_header_value", skip: "Go strings are byte strings; a lone surrogate cannot raise UnicodeEncodeError"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if tc.skip != "" {
					t.Skip(tc.skip)
				}
				ctx := testCtx(t)
				p := wsNewPool(t, monty.WebSocketOptions{URL: wsUnreachableURL, ConnectHeaders: tc.headers})
				s, err := p.Checkout(ctx, monty.CheckoutOptions{})
				require.Nil(t, s)
				tc.check(t, err)
			})
		}
	})

	t.Run("trace_context_headers_precede_connect_headers", func(t *testing.T) {
		relay := wsStartRelay(t)
		ctx := testCtx(t)
		state, err := trace.ParseTraceState("vendor=a")
		require.NoError(t, err)
		span := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    trace.TraceID{0x0a, 0x0b},
			SpanID:     trace.SpanID{0x0c},
			TraceFlags: trace.FlagsSampled,
			TraceState: state,
		})
		traced := trace.ContextWithSpanContext(ctx, span)
		inst, err := monty.NewInstrumentation(monty.InstrumentationConfig{Logs: telBool(false), Metrics: telBool(false)})
		require.NoError(t, err)
		t.Cleanup(inst.Disable)
		inst.SetTracerProvider(sdktrace.NewTracerProvider())
		plain := wsNewPool(t, monty.WebSocketOptions{URL: relay.URL, RequestTimeout: 30 * time.Second})
		override := wsNewPool(t, monty.WebSocketOptions{
			URL:            relay.URL,
			RequestTimeout: 30 * time.Second,
			ConnectHeaders: func(context.Context) (map[string]string, error) {
				return map[string]string{"tracestate": "caller=1"}, nil
			},
		})
		for _, p := range []*monty.Pool{plain, override} {
			s, err := p.Checkout(traced, monty.CheckoutOptions{})
			require.NoError(t, err)
			v, err := s.FeedRun(traced, "1 + 1", nil)
			require.NoError(t, err)
			require.Equal(t, int64(2), v)
			require.NoError(t, s.Close(ctx))
		}
		untraced := wsCheckout(t, ctx, plain)
		_, err = untraced.FeedRun(ctx, "1", nil)
		require.NoError(t, err)
		inst.Disable()
		uninstrumented := wsCheckout(t, traced, plain)
		_, err = uninstrumented.FeedRun(traced, "1", nil)
		require.NoError(t, err)

		captured := relay.captured()
		require.Len(t, captured, 4)
		traceparent := "00-" + span.TraceID().String() + "-" + span.SpanID().String() + "-01"
		require.Equal(t, traceparent, captured[0].Get("traceparent"))
		require.Equal(t, "vendor=a", captured[0].Get("tracestate"))
		require.Equal(t, traceparent, captured[1].Get("traceparent"))
		require.Equal(t, "caller=1", captured[1].Get("tracestate"))
		require.Empty(t, captured[2].Values("traceparent"))
		require.Empty(t, captured[3].Values("traceparent"))
	})

	t.Run("checkout_rejects_unknown_limits", func(t *testing.T) {
		t.Skip("ResourceLimits is a struct; an unknown limits key does not compile in Go")
	})
}
