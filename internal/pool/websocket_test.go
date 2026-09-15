package pool_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/internal/worker"
	"github.com/asalimonov/montygo/montypb"
)

// wsPeer is the mock child's side of one accepted connection.
type wsPeer struct {
	t      *testing.T
	conn   *websocket.Conn
	header http.Header
}

func (p *wsPeer) fail(format string, args ...any) {
	p.t.Errorf("mock child: "+format, args...)
	runtime.Goexit()
}

// tryRead returns the next request, or nil once the client ended the connection.
func (p *wsPeer) tryRead() *montypb.ParentRequest {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	typ, data, err := p.conn.Read(ctx)
	if err != nil {
		return nil
	}
	if typ != websocket.MessageBinary {
		p.fail("expected a binary request frame, got %v", typ)
	}
	req := &montypb.ParentRequest{}
	if err := proto.Unmarshal(data, req); err != nil {
		p.fail("decode request: %v", err)
	}
	return req
}

func (p *wsPeer) read() *montypb.ParentRequest {
	req := p.tryRead()
	if req == nil {
		p.fail("expected a request, the connection ended")
	}
	return req
}

func (p *wsPeer) send(ev *montypb.ChildEvent) {
	data, err := proto.Marshal(ev)
	if err != nil {
		p.fail("encode event: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.conn.Write(ctx, websocket.MessageBinary, data); err != nil {
		p.fail("send event: %v", err)
	}
}

func (p *wsPeer) expectConfigure() {
	if req := p.read(); req.GetConfigure() == nil {
		p.fail("expected Configure, got %v", req)
	}
	p.send(wsOk())
}

func (p *wsPeer) expectFeed(code string) {
	req := p.read()
	if req.GetFeed() == nil || req.GetFeed().GetCode() != code {
		p.fail("expected Feed of %q, got %v", code, req)
	}
}

func (p *wsPeer) expectLoad(state []byte) {
	req := p.read()
	if req.GetLoad() == nil || string(req.GetLoad().GetState()) != string(state) {
		p.fail("expected Load of %q, got %v", state, req)
	}
}

func (p *wsPeer) expectResumeCall() *montypb.ResumeCall {
	req := p.read()
	if req.GetResumeCall() == nil {
		p.fail("expected ResumeCall, got %v", req)
	}
	return req.GetResumeCall()
}

func (p *wsPeer) expectAbortFeed(message string) *montypb.RaisedException {
	req := p.read()
	exc := req.GetAbortFeed().GetException()
	if exc == nil {
		p.fail("expected AbortFeed carrying an exception, got %v", req)
	}
	if exc.GetExcType() != "RuntimeError" || exc.GetMessage() != message {
		p.fail("expected AbortFeed RuntimeError(%q), got %v", message, exc)
	}
	return exc
}

func (p *wsPeer) expectClose() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, err := p.conn.Read(ctx)
	if err == nil {
		p.fail("expected a close frame, got a data message")
	}
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		p.fail("expected a normal close frame, got %v", err)
	}
}

func (p *wsPeer) expectHangUp() {
	if req := p.tryRead(); req != nil {
		p.fail("expected the client to hang up, got %v", req)
	}
}

func (p *wsPeer) answerRequests() {
	for {
		req := p.tryRead()
		if req == nil {
			return
		}
		if req.GetFeed() != nil {
			p.send(wsComplete(wsInt(42)))
		} else {
			p.send(wsOk())
		}
	}
}

func (p *wsPeer) serveEndlessSuspensions(expectedCalls uint32) {
	if req := p.read(); req.GetFeed() == nil {
		p.fail("expected Feed, got %v", req)
	}
	p.send(wsFunctionCall("fetch", 1))
	for id := uint32(2); id <= expectedCalls; id++ {
		p.expectResumeCall()
		p.send(wsFunctionCall("fetch", id))
	}
	exc := p.expectAbortFeed(fmt.Sprintf("suspension limit %d exceeded", expectedCalls-1))
	p.send(wsErrorEvent(exc))
}

// wsServer accepts connections in order, running the next script on each.
type wsServer struct {
	URL     string
	srv     *httptest.Server
	mu      sync.Mutex
	scripts []func(p *wsPeer)
	next    int
	wg      sync.WaitGroup
}

func wsServe(t *testing.T, scripts ...func(p *wsPeer)) *wsServer {
	t.Helper()
	s := &wsServer{scripts: scripts}
	s.wg.Add(len(scripts))
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		if s.next >= len(s.scripts) {
			s.mu.Unlock()
			http.Error(w, "no script for this connection", http.StatusServiceUnavailable)
			return
		}
		script := s.scripts[s.next]
		s.next++
		s.mu.Unlock()
		defer s.wg.Done()
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			t.Errorf("mock child: accept: %v", err)
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(wire.MaxFrameLen)
		done := make(chan struct{})
		go func() {
			defer close(done)
			script(&wsPeer{t: t, conn: conn, header: r.Header.Clone()})
		}()
		<-done
	}))
	s.URL = "ws" + strings.TrimPrefix(s.srv.URL, "http")
	t.Cleanup(s.srv.Close)
	return s
}

func (s *wsServer) join(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("mock child did not finish")
	}
}

func wsOk() *montypb.ChildEvent {
	return &montypb.ChildEvent{Kind: &montypb.ChildEvent_Ok{Ok: &montypb.Ok{}}}
}

func wsInt(v int64) *montypb.MontyObject {
	return &montypb.MontyObject{Kind: &montypb.MontyObject_Int{Int: v}}
}

func wsStr(v string) *montypb.MontyObject {
	return &montypb.MontyObject{Kind: &montypb.MontyObject_Str{Str: v}}
}

func wsComplete(v *montypb.MontyObject) *montypb.ChildEvent {
	return &montypb.ChildEvent{Kind: &montypb.ChildEvent_Complete{Complete: &montypb.Complete{Value: v}}}
}

func wsShutdown(dump []byte) *montypb.ChildEvent {
	return &montypb.ChildEvent{Kind: &montypb.ChildEvent_Shutdown{Shutdown: &montypb.ShutdownDump{Dump: dump}}}
}

func wsFunctionCall(name string, callID uint32) *montypb.ChildEvent {
	return &montypb.ChildEvent{Kind: &montypb.ChildEvent_FunctionCall{FunctionCall: &montypb.FunctionCall{FunctionName: name, CallId: callID}}}
}

func wsErrorEvent(exc *montypb.RaisedException) *montypb.ChildEvent {
	return &montypb.ChildEvent{Kind: &montypb.ChildEvent_Error{Error: &montypb.Error{Exception: exc}}}
}

func wsCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func wsConfig(url string) pool.Config {
	return pool.Config{
		Spawner:            &worker.WebSocketDialer{URL: url},
		MaxProcesses:       1,
		DurationLimitGrace: time.Second,
		SingleUse:          true,
		MontyVersion:       worker.DefaultUserAgent[len("monty-pool/"):],
		ProtocolVersion:    1,
	}
}

func wsNewPool(t *testing.T, cfg pool.Config) *pool.Pool {
	t.Helper()
	p, err := pool.New(wsCtx(t), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

// wsWebSocketPool is a single-worker pool with a 10s turn timeout.
func wsWebSocketPool(t *testing.T, url string) *pool.Pool {
	cfg := wsConfig(url)
	cfg.RequestTimeout = 10 * time.Second
	return wsNewPool(t, cfg)
}

func wsCheckout(t *testing.T, p *pool.Pool, cfg wire.Configure) *pool.Checkout {
	t.Helper()
	co, err := p.Checkout(wsCtx(t), cfg, pool.CheckoutOptions{})
	require.NoError(t, err)
	return co
}

func wsFeed(ctx context.Context, co *pool.Checkout, code string) (*wire.Event, error) {
	return co.Feed(ctx, code, nil, nil, "", nil, false, nil)
}

func wsReturn(ctx context.Context, co *pool.Checkout, v any) (*wire.Event, error) {
	return co.Resume(ctx, wire.ExtResult{Kind: wire.ExtReturn, Value: v}, nil)
}

func wsPoolError(t *testing.T, err error, kind pool.ErrorKind) *pool.Error {
	t.Helper()
	var perr *pool.Error
	require.ErrorAs(t, err, &perr)
	require.Equal(t, kind, perr.Kind, "got %v", err)
	return perr
}

func wsUint64(v uint64) *uint64 { return &v }

func wsMaxDuration(d time.Duration) *wire.Limits {
	return &wire.Limits{MaxDurationMicros: wsUint64(uint64(d / time.Microsecond))}
}

func wsMaxSuspensions(n uint64) *wire.Limits {
	return &wire.Limits{MaxSuspensions: wsUint64(n)}
}

func wsRequireComplete(t *testing.T, ev *wire.Event, err error, want any) {
	t.Helper()
	require.NoError(t, err)
	require.Equal(t, wire.EventComplete, ev.Kind)
	require.Equal(t, want, ev.Value)
}

// wsMounts serves read_text under /mnt from a host directory.
type wsMounts struct{ dir string }

func (m wsMounts) HandleOsCall(_ context.Context, call *wire.OsCall) (bool, any, *wire.Exception) {
	rel, ok := strings.CutPrefix(call.Path, "/mnt/")
	if call.Op != wire.OpReadText || !ok {
		return false, nil, nil
	}
	data, err := os.ReadFile(filepath.Join(m.dir, rel))
	if err != nil {
		return true, nil, wire.NewException("FileNotFoundError", err.Error())
	}
	return true, string(data), nil
}

func TestWebSocket(t *testing.T) {
	t.Run("drives_a_session_over_websocket", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) { p.answerRequests() })
		p := wsWebSocketPool(t, srv.URL)
		co := wsCheckout(t, p, wire.Configure{ScriptName: "test.py"})
		_, hasPID := co.PID()
		require.False(t, hasPID)
		ev, err := wsFeed(wsCtx(t), co, "1 + 1")
		wsRequireComplete(t, ev, err, int64(42))
		require.NoError(t, co.Finish(wsCtx(t)))
		srv.join(t)
	})

	t.Run("connect_headers_are_per_checkout", func(t *testing.T) {
		seen := make(chan string, 2)
		script := func(p *wsPeer) {
			seen <- p.header.Get("traceparent")
			p.answerRequests()
		}
		srv := wsServe(t, script, script)
		p := wsWebSocketPool(t, srv.URL)
		for _, trace := range []string{"00-checkout-1", "00-checkout-2"} {
			ctx := pool.WithConnectHeaders(wsCtx(t), [][2]string{{"traceparent", trace}})
			co, err := p.Checkout(ctx, wire.Configure{ScriptName: "test.py"}, pool.CheckoutOptions{})
			require.NoError(t, err)
			require.Equal(t, trace, <-seen)
			ev, err := wsFeed(ctx, co, "1 + 1")
			wsRequireComplete(t, ev, err, int64(42))
			require.NoError(t, co.Finish(ctx))
		}
		srv.join(t)
	})

	t.Run("duplicate_connect_headers_are_last_wins", func(t *testing.T) {
		seen := make(chan []string, 1)
		srv := wsServe(t, func(p *wsPeer) {
			seen <- p.header.Values("traceparent")
			p.answerRequests()
		})
		p := wsWebSocketPool(t, srv.URL)
		ctx := pool.WithConnectHeaders(wsCtx(t), [][2]string{{"traceparent", "first"}, {"traceparent", "second"}})
		co, err := p.Checkout(ctx, wire.Configure{}, pool.CheckoutOptions{})
		require.NoError(t, err)
		require.Equal(t, []string{"second"}, <-seen)
		require.NoError(t, co.Finish(ctx))
		srv.join(t)
	})

	t.Run("user_agent_is_set_and_overridable", func(t *testing.T) {
		seen := make(chan string, 2)
		script := func(p *wsPeer) {
			seen <- p.header.Get("User-Agent")
			p.answerRequests()
		}
		srv := wsServe(t, script, script)
		p := wsWebSocketPool(t, srv.URL)
		co := wsCheckout(t, p, wire.Configure{})
		require.Equal(t, "monty-pool/0.0.23", <-seen)
		require.NoError(t, co.Finish(wsCtx(t)))

		ctx := pool.WithConnectHeaders(wsCtx(t), [][2]string{{"User-Agent", "my-app/1.0"}})
		co, err := p.Checkout(ctx, wire.Configure{}, pool.CheckoutOptions{})
		require.NoError(t, err)
		require.Equal(t, "my-app/1.0", <-seen)
		require.NoError(t, co.Finish(ctx))
		srv.join(t)
	})

	t.Run("telemetry_context_is_propagated_on_the_dial", func(t *testing.T) {
		t.Skip("the root pool injects trace headers; covered by TestWebSocket/trace_context_headers_precede_connect_headers")
	})

	t.Run("malformed_connect_headers_fail_the_dial", func(t *testing.T) {
		p := wsNewPool(t, wsConfig("ws://127.0.0.1:9"))
		cases := []struct {
			name, value, want string
		}{
			{"bad name", "v", `ws://127.0.0.1:9: connect header "bad name": invalid HTTP header name`},
			{"traceparent", "line\nbreak", `ws://127.0.0.1:9: connect header "traceparent" value: failed to parse header value`},
		}
		for _, tc := range cases {
			ctx := pool.WithConnectHeaders(wsCtx(t), [][2]string{{tc.name, tc.value}})
			_, err := p.Checkout(ctx, wire.Configure{}, pool.CheckoutOptions{})
			perr := wsPoolError(t, err, pool.KindSpawn)
			require.Equal(t, tc.want, perr.Message)
		}
		live, _ := p.Size()
		require.Zero(t, live)
	})

	t.Run("an_unparsable_url_fails_the_dial", func(t *testing.T) {
		p := wsNewPool(t, wsConfig("not a url"))
		_, err := p.Checkout(wsCtx(t), wire.Configure{}, pool.CheckoutOptions{})
		perr := wsPoolError(t, err, pool.KindSpawn)
		require.True(t, strings.HasPrefix(perr.Message, "not a url: "), perr.Message)
	})

	t.Run("connect_headers_debug_is_redacted", func(t *testing.T) {
		t.Skip("Go connect headers travel in the context and are never formatted by the pool")
	})

	t.Run("mounted_reads_are_serviced_from_the_parent_filesystem", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "data.txt"), []byte("parent-side bytes"), 0o644))
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			if req := p.read(); req.GetFeed() == nil {
				p.fail("expected Feed, got %v", req)
			}
			p.send(&montypb.ChildEvent{Kind: &montypb.ChildEvent_OsCall{OsCall: &montypb.OsCall{
				CallId: 7,
				Call:   &montypb.OsCall_ReadText{ReadText: "/mnt/data.txt"},
			}}})
			resume := p.expectResumeCall()
			if resume.GetCallId() != 7 {
				p.fail("expected ResumeCall(7), got %v", resume)
			}
			if got := resume.GetResult().GetReturnValue().GetStr(); got != "parent-side bytes" {
				p.fail("expected the mounted file's contents, got %v", resume.GetResult())
			}
			p.send(wsComplete(wsStr("done")))
			p.expectClose()
		})
		p := wsNewPool(t, wsConfig(srv.URL))
		co := wsCheckout(t, p, wire.Configure{})
		ctx := wsCtx(t)
		ev, err := co.Feed(ctx, "unused", nil, wsMounts{dir: dir}, "/mnt", nil, false, nil)
		require.NoError(t, err)
		require.Equal(t, wire.EventOsCall, ev.Kind)
		ev, handled, err := co.ResumeFromMounts(ctx, nil)
		require.True(t, handled)
		wsRequireComplete(t, ev, err, "done")
		require.NoError(t, co.Finish(ctx))
		srv.join(t)
	})

	t.Run("malformed_os_call_is_a_protocol_error", func(t *testing.T) {
		dir := t.TempDir()
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			if req := p.read(); req.GetFeed() == nil {
				p.fail("expected Feed, got %v", req)
			}
			p.send(&montypb.ChildEvent{Kind: &montypb.ChildEvent_OsCall{OsCall: &montypb.OsCall{
				CallId: 3,
				Call:   &montypb.OsCall_Open_{Open: &montypb.OsCall_Open{Path: "/mnt/data.txt", Mode: "q"}},
			}}})
			p.expectHangUp()
		})
		p := wsNewPool(t, wsConfig(srv.URL))
		co := wsCheckout(t, p, wire.Configure{})
		_, err := co.Feed(wsCtx(t), "unused", nil, wsMounts{dir: dir}, "/mnt", nil, false, nil)
		perr := wsPoolError(t, err, pool.KindProtocol)
		require.Equal(t, `invalid OS call payload: invalid open mode "q"`, perr.Message)
		srv.join(t)
	})

	t.Run("duration_backstop_kills_an_unresponsive_worker", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			if req := p.read(); req.GetFeed() == nil {
				p.fail("expected Feed, got %v", req)
			}
			p.expectHangUp()
		})
		cfg := wsConfig(srv.URL)
		cfg.DurationLimitGrace = 300 * time.Millisecond
		p := wsNewPool(t, cfg)
		co := wsCheckout(t, p, wire.Configure{Limits: wsMaxDuration(100 * time.Millisecond)})
		_, err := wsFeed(wsCtx(t), co, "while True:\n    pass")
		perr := wsPoolError(t, err, pool.KindTimeout)
		require.LessOrEqual(t, perr.Timeout, 400*time.Millisecond)
		srv.join(t)
	})

	t.Run("duration_backstop_arms_on_the_raw_path", func(t *testing.T) {
		t.Skip("the Go pool has no raw relay path (turn_raw)")
	})

	t.Run("a_raw_load_adopts_the_dumps_duration_budget", func(t *testing.T) {
		t.Skip("the Go pool has no raw relay path (turn_raw); restored_session_rearms_the_duration_backstop covers Restore")
	})

	t.Run("lifecycle_requests_are_refused_on_the_raw_path", func(t *testing.T) {
		t.Skip("the Go pool has no raw relay path (turn_raw)")
	})

	t.Run("an_oversize_raw_load_keeps_the_duration_budget", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			if req := p.read(); req.GetFeed() == nil {
				p.fail("expected Feed (the oversize Load must never arrive), got %v", req)
			}
			p.expectHangUp()
		})
		cfg := wsConfig(srv.URL)
		cfg.DurationLimitGrace = 300 * time.Millisecond
		p := wsNewPool(t, cfg)
		co := wsCheckout(t, p, wire.Configure{Limits: wsMaxDuration(100 * time.Millisecond)})
		ctx := wsCtx(t)
		_, _, err := co.Restore(ctx, make([]byte, wire.MaxFrameLen+1), nil, nil)
		wsPoolError(t, err, pool.KindRuntime)
		_, err = wsFeed(ctx, co, "while True:\n    pass")
		perr := wsPoolError(t, err, pool.KindTimeout)
		require.Equal(t, 400*time.Millisecond, perr.Timeout)
		srv.join(t)
	})

	t.Run("a_shutdown_dump_on_the_raw_path_discards_the_worker", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.expectFeed("1 + 1")
			p.send(wsShutdown([]byte("relay-signed state")))
			p.expectHangUp()
		})
		p := wsNewPool(t, wsConfig(srv.URL))
		co := wsCheckout(t, p, wire.Configure{})
		ctx := wsCtx(t)
		_, err := wsFeed(ctx, co, "1 + 1")
		perr := wsPoolError(t, err, pool.KindShutdown)
		require.Equal(t, []byte("relay-signed state"), perr.Dump)
		_, err = wsFeed(ctx, co, "1 + 1")
		wsPoolError(t, err, pool.KindFinished)
		srv.join(t)
	})

	t.Run("a_mounted_feed_turn_is_still_bounded_by_the_request_timeout", func(t *testing.T) {
		dir := t.TempDir()
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			if req := p.read(); req.GetFeed() == nil {
				p.fail("expected Feed, got %v", req)
			}
			p.expectHangUp()
		})
		cfg := wsConfig(srv.URL)
		cfg.RequestTimeout = 300 * time.Millisecond
		p := wsNewPool(t, cfg)
		co := wsCheckout(t, p, wire.Configure{})
		_, err := co.Feed(wsCtx(t), "unused", nil, wsMounts{dir: dir}, "/mnt", nil, false, nil)
		perr := wsPoolError(t, err, pool.KindTimeout)
		require.Equal(t, 300*time.Millisecond, perr.Timeout)
		co.Abandon()
		srv.join(t)
	})

	t.Run("restored_session_rearms_the_duration_backstop", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.expectLoad([]byte{1, 2, 3})
			name := "restored.py"
			p.send(&montypb.ChildEvent{
				Kind:               &montypb.ChildEvent_Ok{Ok: &montypb.Ok{}},
				RestoredScriptName: &name,
				MaxDurationMicros:  wsUint64(100_000),
			})
			if req := p.read(); req.GetFeed() == nil {
				p.fail("expected Feed, got %v", req)
			}
			p.expectHangUp()
		})
		cfg := wsConfig(srv.URL)
		cfg.DurationLimitGrace = 300 * time.Millisecond
		p := wsNewPool(t, cfg)
		co := wsCheckout(t, p, wire.Configure{})
		ctx := wsCtx(t)
		ev, name, err := co.Restore(ctx, []byte{1, 2, 3}, nil, nil)
		require.NoError(t, err)
		require.Nil(t, ev)
		require.NotNil(t, name)
		require.Equal(t, "restored.py", *name)
		_, err = wsFeed(ctx, co, "while True:\n    pass")
		perr := wsPoolError(t, err, pool.KindTimeout)
		require.LessOrEqual(t, perr.Timeout, 400*time.Millisecond)
		srv.join(t)
	})

	t.Run("suspension_limit_is_enforced_by_the_parent", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.serveEndlessSuspensions(3)
			p.expectHangUp()
		})
		p := wsNewPool(t, wsConfig(srv.URL))
		co := wsCheckout(t, p, wire.Configure{Limits: wsMaxSuspensions(2)})
		ctx := wsCtx(t)
		ev, err := wsFeed(ctx, co, "fetch()")
		require.NoError(t, err)
		require.Equal(t, wire.EventFunctionCall, ev.Kind)
		ev, err = wsReturn(ctx, co, nil)
		require.NoError(t, err)
		require.Equal(t, wire.EventFunctionCall, ev.Kind)
		_, err = wsReturn(ctx, co, nil)
		perr := wsPoolError(t, err, pool.KindRuntime)
		require.Equal(t, "suspension limit 2 exceeded", perr.Exception.MessageText())
		co.Abandon()
		srv.join(t)
	})

	t.Run("a_suspension_answering_an_abort_is_a_protocol_violation", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.expectFeed("fetch()")
			p.send(wsFunctionCall("ext", 1))
			p.expectResumeCall()
			p.send(wsFunctionCall("ext", 2))
			p.expectAbortFeed("suspension limit 1 exceeded")
			p.send(wsFunctionCall("ext", 3))
			p.expectHangUp()
		})
		p := wsNewPool(t, wsConfig(srv.URL))
		co := wsCheckout(t, p, wire.Configure{Limits: wsMaxSuspensions(1)})
		ctx := wsCtx(t)
		ev, err := wsFeed(ctx, co, "fetch()")
		require.NoError(t, err)
		require.Equal(t, wire.EventFunctionCall, ev.Kind)
		_, err = wsReturn(ctx, co, nil)
		perr := wsPoolError(t, err, pool.KindProtocol)
		require.Equal(t, "worker answered AbortFeed with something other than an Error", perr.Message)
		_, err = wsFeed(ctx, co, "1")
		wsPoolError(t, err, pool.KindFinished)
		srv.join(t)
	})

	t.Run("a_malformed_over_budget_os_call_is_a_protocol_violation", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.expectFeed("open('x')")
			p.send(&montypb.ChildEvent{
				Kind:           &montypb.ChildEvent_OsCall{OsCall: &montypb.OsCall{CallId: 1}},
				MaxSuspensions: wsUint64(0),
			})
			p.expectHangUp()
		})
		p := wsNewPool(t, wsConfig(srv.URL))
		co := wsCheckout(t, p, wire.Configure{})
		_, err := wsFeed(wsCtx(t), co, "open('x')")
		perr := wsPoolError(t, err, pool.KindProtocol)
		require.Equal(t, "OsCall event with no call", perr.Message)
		srv.join(t)
	})

	t.Run("suspension_limit_is_enforced_on_the_raw_path", func(t *testing.T) {
		t.Skip("the Go pool has no raw relay path (turn_raw); suspension_limit_is_enforced_by_the_parent covers the typed path")
	})

	t.Run("rejected_raw_load_keeps_the_suspension_count", func(t *testing.T) {
		t.Skip("the Go pool has no raw relay path (turn_raw); Restore clears the pending suspension, so the scenario has no typed equivalent")
	})

	t.Run("configured_suspension_limit_caps_a_restored_one", func(t *testing.T) {
		for _, reported := range []*uint64{wsUint64(5), nil} {
			srv := wsServe(t, func(p *wsPeer) {
				p.expectConfigure()
				p.expectLoad([]byte{1, 2, 3})
				p.send(&montypb.ChildEvent{Kind: &montypb.ChildEvent_Ok{Ok: &montypb.Ok{}}, MaxSuspensions: reported})
				p.serveEndlessSuspensions(2)
				p.expectHangUp()
			})
			p := wsNewPool(t, wsConfig(srv.URL))
			co := wsCheckout(t, p, wire.Configure{Limits: wsMaxSuspensions(1)})
			ctx := wsCtx(t)
			ev, _, err := co.Restore(ctx, []byte{1, 2, 3}, nil, nil)
			require.NoError(t, err)
			require.Nil(t, ev)
			ev, err = wsFeed(ctx, co, "fetch()")
			require.NoError(t, err)
			require.Equal(t, wire.EventFunctionCall, ev.Kind)
			_, err = wsReturn(ctx, co, nil)
			perr := wsPoolError(t, err, pool.KindRuntime)
			require.Equal(t, "suspension limit 1 exceeded", perr.Exception.MessageText(), "reported %v", reported)
			co.Abandon()
			srv.join(t)
		}
	})

	t.Run("suspension_limit_defaults_to_one_thousand", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.serveEndlessSuspensions(1001)
			p.expectHangUp()
		})
		p := wsNewPool(t, wsConfig(srv.URL))
		co := wsCheckout(t, p, wire.Configure{})
		ctx := wsCtx(t)
		ev, err := wsFeed(ctx, co, "fetch()")
		for range 999 {
			require.NoError(t, err)
			require.Equal(t, wire.EventFunctionCall, ev.Kind)
			ev, err = wsReturn(ctx, co, nil)
		}
		require.NoError(t, err)
		_, err = wsReturn(ctx, co, nil)
		perr := wsPoolError(t, err, pool.KindRuntime)
		require.Equal(t, "suspension limit 1000 exceeded", perr.Exception.MessageText())
		co.Abandon()
		srv.join(t)
	})

	t.Run("aborted_restored_suspension_keeps_the_dump_limit", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.expectLoad([]byte{1, 2, 3})
			restored := wsFunctionCall("fetch", 1)
			restored.MaxSuspensions = wsUint64(0)
			p.send(restored)
			p.send(wsErrorEvent(p.expectAbortFeed("suspension limit 0 exceeded")))
			p.expectFeed("fetch()")
			p.send(wsFunctionCall("fetch", 2))
			p.send(wsErrorEvent(p.expectAbortFeed("suspension limit 0 exceeded")))
			p.expectHangUp()
		})
		p := wsNewPool(t, wsConfig(srv.URL))
		co := wsCheckout(t, p, wire.Configure{Limits: wsMaxSuspensions(1)})
		ctx := wsCtx(t)
		_, _, err := co.Restore(ctx, []byte{1, 2, 3}, nil, nil)
		perr := wsPoolError(t, err, pool.KindRuntime)
		require.Equal(t, "suspension limit 0 exceeded", perr.Exception.MessageText())
		_, err = wsFeed(ctx, co, "fetch()")
		perr = wsPoolError(t, err, pool.KindRuntime)
		require.Equal(t, "suspension limit 0 exceeded", perr.Exception.MessageText())
		co.Abandon()
		srv.join(t)
	})

	t.Run("restored_session_readopts_the_suspension_limit", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.expectLoad([]byte{1, 2, 3})
			p.send(&montypb.ChildEvent{Kind: &montypb.ChildEvent_Ok{Ok: &montypb.Ok{}}, MaxSuspensions: wsUint64(1)})
			p.serveEndlessSuspensions(2)
			p.expectHangUp()
		})
		p := wsNewPool(t, wsConfig(srv.URL))
		co := wsCheckout(t, p, wire.Configure{})
		ctx := wsCtx(t)
		ev, _, err := co.Restore(ctx, []byte{1, 2, 3}, nil, nil)
		require.NoError(t, err)
		require.Nil(t, ev)
		ev, err = wsFeed(ctx, co, "fetch()")
		require.NoError(t, err)
		require.Equal(t, wire.EventFunctionCall, ev.Kind)
		_, err = wsReturn(ctx, co, nil)
		perr := wsPoolError(t, err, pool.KindRuntime)
		require.Equal(t, "suspension limit 1 exceeded", perr.Exception.MessageText())
		co.Abandon()
		srv.join(t)
	})

	t.Run("shutdown_hands_back_a_restorable_dump", func(t *testing.T) {
		srv := wsServe(t,
			func(p *wsPeer) {
				p.expectConfigure()
				p.expectFeed("1 + 1")
				p.send(wsShutdown([]byte("fake-dump")))
			},
			func(p *wsPeer) {
				p.expectConfigure()
				p.expectLoad([]byte("fake-dump"))
				p.send(wsOk())
				p.expectFeed("1 + 1")
				p.send(wsComplete(wsInt(42)))
				for p.tryRead() != nil {
				}
			},
		)
		p := wsWebSocketPool(t, srv.URL)
		ctx := wsCtx(t)
		co := wsCheckout(t, p, wire.Configure{})
		_, err := wsFeed(ctx, co, "1 + 1")
		perr := wsPoolError(t, err, pool.KindShutdown)
		require.True(t, perr.HasDump)
		require.Equal(t, []byte("fake-dump"), perr.Dump)

		co = wsCheckout(t, p, wire.Configure{})
		ev, _, err := co.Restore(ctx, perr.Dump, nil, nil)
		require.NoError(t, err)
		require.Nil(t, ev)
		ev, err = wsFeed(ctx, co, "1 + 1")
		wsRequireComplete(t, ev, err, int64(42))
		require.NoError(t, co.Finish(ctx))
		srv.join(t)
	})

	t.Run("shutdown_during_a_suspension_carries_the_suspended_dump", func(t *testing.T) {
		srv := wsServe(t,
			func(p *wsPeer) {
				p.expectConfigure()
				p.expectFeed("ext()")
				p.send(wsFunctionCall("ext", 7))
				if resume := p.expectResumeCall(); resume.GetCallId() != 7 {
					p.fail("expected ResumeCall(7), got %v", resume)
				}
				p.send(wsShutdown([]byte("susp-dump")))
			},
			func(p *wsPeer) {
				p.expectConfigure()
				p.expectLoad([]byte("susp-dump"))
				p.send(wsFunctionCall("ext", 7))
				for p.tryRead() != nil {
				}
			},
		)
		p := wsWebSocketPool(t, srv.URL)
		ctx := wsCtx(t)
		co := wsCheckout(t, p, wire.Configure{})
		ev, err := wsFeed(ctx, co, "ext()")
		require.NoError(t, err)
		require.Equal(t, wire.EventFunctionCall, ev.Kind)
		require.Equal(t, uint32(7), ev.FunctionCall.CallID)
		_, err = wsReturn(ctx, co, int64(5))
		perr := wsPoolError(t, err, pool.KindShutdown)
		require.Equal(t, []byte("susp-dump"), perr.Dump)

		co = wsCheckout(t, p, wire.Configure{})
		ev, _, err = co.Restore(ctx, perr.Dump, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, ev)
		require.Equal(t, wire.EventFunctionCall, ev.Kind)
		require.Equal(t, uint32(7), ev.FunctionCall.CallID)
		require.NoError(t, co.Finish(ctx))
		srv.join(t)
	})

	t.Run("shutdown_without_a_session_carries_no_dump", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			if req := p.read(); req.GetConfigure() == nil {
				p.fail("expected Configure, got %v", req)
			}
			p.send(wsShutdown(nil))
		})
		p := wsWebSocketPool(t, srv.URL)
		_, err := p.Checkout(wsCtx(t), wire.Configure{}, pool.CheckoutOptions{})
		perr := wsPoolError(t, err, pool.KindShutdown)
		require.False(t, perr.HasDump)
		require.Nil(t, perr.Dump)
		srv.join(t)
	})

	t.Run("pings_are_answered_while_idle", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			go func() { _, _, _ = p.conn.Read(context.Background()) }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.conn.Ping(ctx); err != nil {
				p.fail("expected a pong while the client is idle: %v", err)
			}
		})
		p := wsNewPool(t, wsConfig(srv.URL))
		co := wsCheckout(t, p, wire.Configure{})
		srv.join(t)
		co.Abandon()
	})

	t.Run("finishing_a_checkout_sends_a_close_frame", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.expectFeed("1 + 1")
			p.send(wsComplete(wsInt(42)))
			p.expectClose()
		})
		p := wsWebSocketPool(t, srv.URL)
		ctx := wsCtx(t)
		co := wsCheckout(t, p, wire.Configure{})
		ev, err := wsFeed(ctx, co, "1 + 1")
		wsRequireComplete(t, ev, err, int64(42))
		require.NoError(t, co.Finish(ctx))
		srv.join(t)
	})

	t.Run("an_abandoned_checkout_sends_a_close_frame", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.expectClose()
		})
		p := wsWebSocketPool(t, srv.URL)
		co := wsCheckout(t, p, wire.Configure{})
		co.Abandon()
		srv.join(t)
	})

	t.Run("a_timed_out_turn_sends_a_close_frame", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.expectFeed("while True:\n    pass")
			p.expectClose()
		})
		cfg := wsConfig(srv.URL)
		cfg.RequestTimeout = 200 * time.Millisecond
		p := wsNewPool(t, cfg)
		co := wsCheckout(t, p, wire.Configure{})
		_, err := wsFeed(wsCtx(t), co, "while True:\n    pass")
		wsPoolError(t, err, pool.KindTimeout)
		srv.join(t)
	})

	t.Run("cancelling_finish_does_not_leak_capacity", func(t *testing.T) {
		release := make(chan struct{})
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			<-release
		})
		cfg := wsConfig(srv.URL)
		cfg.CheckoutTimeout = 200 * time.Millisecond
		p := wsNewPool(t, cfg)
		co := wsCheckout(t, p, wire.Configure{})

		feedCtx, cancelFeed := context.WithTimeout(wsCtx(t), time.Second)
		defer cancelFeed()
		_, err := wsFeed(feedCtx, co, strings.Repeat("#", 32<<20))
		require.Error(t, err, "the unread feed must block, wedging the socket")
		require.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)

		finishCtx, cancelFinish := context.WithTimeout(wsCtx(t), 200*time.Millisecond)
		defer cancelFinish()
		require.NoError(t, co.Finish(finishCtx))

		close(release)
		srv.join(t)
		srv.srv.Close()

		_, err = p.Checkout(wsCtx(t), wire.Configure{}, pool.CheckoutOptions{})
		wsPoolError(t, err, pool.KindSpawn)
	})

	t.Run("a_dropped_connection_is_a_disconnect", func(t *testing.T) {
		srv := wsServe(t, func(p *wsPeer) {
			p.expectConfigure()
			p.expectFeed("1 + 1")
			p.send(wsComplete(wsInt(42)))
			_ = p.conn.Close(websocket.StatusNormalClosure, "")
		})
		p := wsWebSocketPool(t, srv.URL)
		ctx := wsCtx(t)
		co := wsCheckout(t, p, wire.Configure{})
		ev, err := wsFeed(ctx, co, "1 + 1")
		wsRequireComplete(t, ev, err, int64(42))
		srv.join(t)
		_, err = wsFeed(ctx, co, "x")
		wsPoolError(t, err, pool.KindDisconnected)
	})
}
