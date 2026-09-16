package montygo_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
)

func Example() {
	ctx := context.Background()
	pool, err := montygo.NewPool(ctx, montygo.PoolOptions{})
	if err != nil {
		panic(err)
	}
	defer pool.Close(ctx)
	session, err := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	if err != nil {
		panic(err)
	}
	defer session.Close(ctx)

	result, err := session.FeedRun(ctx, "1 + 2", nil)
	fmt.Println(result, err)
	// Output: 3 <nil>
}

func Example_oneShot() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Shutdown(ctx)

	// Run checks out a session, feeds once and closes it.
	result, err := pool.Run(ctx, defaultRuntime, "sum(range(n))", &montygo.RunOptions{
		FeedOptions: montygo.FeedOptions{Inputs: map[string]any{"n": 10}},
	})
	fmt.Println(result, err)
	// Output: 45 <nil>
}

func Example_sessionState() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	_, _ = session.FeedRun(ctx, "x = 21", nil)
	result, _ := session.FeedRun(ctx, "x * 2", nil)
	fmt.Println(result)
	// Output: 42
}

func Example_inputs() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	result, _ := session.FeedRun(ctx, "x + y", &montygo.FeedOptions{Inputs: map[string]any{"x": 10, "y": 20}})
	fmt.Println(result)
	// Output: 30
}

func Example_externalLookup() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	lookup := map[string]any{
		"add": func(a, b int) int { return a + b },
		"fetch_data": func(url string) *host.Future {
			return host.Async(func() (any, error) { return "contents of " + url, nil })
		},
		"greeting": "hello ",
	}
	sum, _ := session.FeedRun(ctx, "add(2, 3)", &montygo.FeedOptions{ExternalLookup: lookup})
	fetched, _ := session.FeedRun(ctx, "await fetch_data('https://example.com')", &montygo.FeedOptions{ExternalLookup: lookup})
	text, _ := session.FeedRun(ctx, "greeting + name", &montygo.FeedOptions{Inputs: map[string]any{"name": "Ada"}, ExternalLookup: lookup})
	fmt.Println(sum, fetched, text)
	// Output: 5 contents of https://example.com hello Ada
}

func Example_keywordArguments() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	scale := func(x int, kwargs host.Kwargs) int64 { return int64(x) * kwargs["factor"].(int64) }
	result, _ := session.FeedRun(ctx, "scale(4, factor=10)", &montygo.FeedOptions{ExternalLookup: map[string]any{"scale": scale}})
	fmt.Println(result)
	// Output: 40
}

type exampleWallet struct {
	Balance int
}

func (w *exampleWallet) Pay(amount int) *exampleWallet {
	return &exampleWallet{Balance: w.Balance - amount}
}

func wrapWallet(w *exampleWallet) *host.ClassInstance {
	return host.MustClassInstance(w, host.ClassInstanceOptions{
		EagerAttrs:     host.All(),
		AllowedMethods: host.All(),
		ConvertValue: func(_ string, v any) (any, error) {
			if next, ok := v.(*exampleWallet); ok {
				return wrapWallet(next), nil
			}
			return v, nil
		},
	})
}

func Example_classInstance() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	wallet := &exampleWallet{Balance: 100}
	balance, _ := session.FeedRun(ctx, "w.pay(30).balance", &montygo.FeedOptions{Inputs: map[string]any{"w": wrapWallet(wallet)}})
	same, _ := session.FeedRun(ctx, "w", &montygo.FeedOptions{Inputs: map[string]any{"w": wrapWallet(wallet)}})
	fmt.Println(balance, same == wallet)
	// Output: 70 true
}

func Example_classType() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	walletClass := host.MustClassType[exampleWallet](host.ClassTypeOptions{
		Init:                   true,
		InstanceEagerAttrs:     host.All(),
		InstanceAllowedMethods: host.All(),
		ConvertValue: func(_ string, v any) (any, error) {
			if next, ok := v.(*exampleWallet); ok {
				return wrapWallet(next), nil
			}
			return v, nil
		},
	})
	result, _ := session.FeedRun(ctx, "w = Wallet(100)\nw.pay(30).balance", &montygo.FeedOptions{Inputs: map[string]any{"Wallet": walletClass}})
	_, err := session.FeedRun(ctx, "Wallet(1)", &montygo.FeedOptions{Inputs: map[string]any{"Wallet": host.MustClassType[exampleWallet](host.ClassTypeOptions{})}})
	fmt.Println(result)
	fmt.Println(err)
	// Output:
	// 70
	// TypeError: cannot instantiate host class 'exampleWallet'
}

func Example_stop() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Shutdown(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	lookup := map[string]any{"wait": func(ctx context.Context) error {
		<-ctx.Done() // the host call's context ends on Stop
		return ctx.Err()
	}}
	run := session.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: lookup})
	stopped, err := run.Stop(ctx) // KeyboardInterrupt now, kill after 3 s, join the callback
	var runtimeErr *monterr.RuntimeError
	fmt.Println(err, stopped.How, stopped.SessionKept(), errors.As(stopped.Err, &runtimeErr), runtimeErr.TypeName)

	result, _ := session.FeedRun(ctx, "1 + 1", nil) // the session is still usable
	fmt.Println(result)
	// Output:
	// <nil> aborted true true KeyboardInterrupt
	// 2
}

func Example_catchableStop() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Shutdown(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	started := make(chan struct{})
	lookup := map[string]any{
		"wait": func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
		"cleanup": func(ctx context.Context) (string, error) { // runs after the stop was delivered
			if err := ctx.Err(); err != nil {
				return "", err // host calls made during cleanup get a live context
			}
			return "cleaned up", nil
		},
	}
	script := `try:
    wait()
except KeyboardInterrupt:
    result = cleanup()
result`
	run := session.Go(ctx, script, &montygo.FeedOptions{ExternalLookup: lookup})
	<-started
	stopped, err := run.Stop(ctx, montygo.StopPolicy{Catchable: true}) // raised in the sandbox, not AbortFeed
	result, _ := run.Wait()
	fmt.Println(err, stopped.How, stopped.SessionKept(), result)
	// Output: <nil> finished true cleaned up
}

func Example_slot() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Shutdown(ctx)
	slot := pool.Slot(defaultRuntime, montygo.CheckoutOptions{})
	defer slot.Close(ctx)

	fmt.Println(slot.State())               // no session yet
	_, _ = slot.FeedRun(ctx, "x = 21", nil) // checks out lazily
	result, _ := slot.FeedRun(ctx, "x * 2", nil)
	fmt.Println(result, slot.State())

	started := make(chan struct{})
	lookup := map[string]any{"wait": func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	run, _ := slot.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: lookup})
	<-started
	_, busy := slot.Go(ctx, "1", nil)            // one execution at a time
	stopped, _ := run.Stop(ctx, montygo.KillNow) // the worker is killed; the session is lost
	fmt.Println(errors.Is(busy, monterr.ErrSessionBusy), stopped.How, stopped.SessionKept(), slot.State())

	_, err := slot.FeedRun(ctx, "x", nil) // a fresh session: sandbox state is not restored
	var runtimeErr *monterr.RuntimeError
	fmt.Println(errors.As(err, &runtimeErr), runtimeErr.TypeName)
	// Output:
	// idle
	// 42 idle
	// true killed false idle
	// true NameError
}

type payer interface {
	Pay(amount int) *exampleWallet
}

func Example_host() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)

	h := host.NewHost()
	_ = h.Func("add", func(a, b int) int { return a + b })
	_ = h.Object("wallet", &exampleWallet{Balance: 100}, host.ClassInstanceOptions{
		AllowedMethods: host.Expose[payer](),
		ConvertValue: func(_ string, v any) (any, error) {
			if next, ok := v.(*exampleWallet); ok {
				return wrapWallet(next), nil
			}
			return v, nil
		},
	})
	fmt.Print(h.Stubs())

	session, _ := pool.Checkout(ctx, mustRuntime(montygo.RuntimeOptions{Host: h, TypeCheck: true, TypeCheckStubs: h.Stubs()}), montygo.CheckoutOptions{})
	defer session.Close(ctx)
	result, _ := session.FeedRun(ctx, "add(1, wallet.pay(30).balance)", nil)
	fmt.Println(result)
	// Output:
	// from typing import Any, Awaitable
	//
	// def add(arg0: int, arg1: int, /) -> int: ...
	//
	// class exampleWallet:
	//     def pay(self, arg0: int, /) -> Any: ...
	//
	// wallet: exampleWallet
	// 71
}

func Example_lines() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	lines := sandbox.Lines(func(stream sandbox.Stream, line string) error {
		fmt.Printf("%s: %q\n", stream, line)
		return nil
	})
	_, _ = session.FeedRun(ctx, "print('a\\nb')\nprint('c', end='')", &montygo.FeedOptions{Print: lines})
	// Output:
	// stdout: "a"
	// stdout: "b"
	// stdout: "c"
}

func Example_poolShutdown() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{MaxWorkers: 2})
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	fmt.Printf("%+v\n", pool.Stats())

	run := session.Go(ctx, "sum(range(10))", nil)
	// Drain lets the run end on its own; without it Shutdown stops it at once.
	// Every open session is closed and every worker has exited when Shutdown returns.
	fmt.Println(pool.Shutdown(ctx, montygo.StopPolicy{Drain: 5 * time.Second}))
	result, err := run.Wait()
	fmt.Println(result, err, session.State())
	fmt.Printf("%+v\n", pool.Stats())
	// Output:
	// {Starting:0 Active:1 Idle:0 Retiring:0}
	// <nil>
	// 45 <nil> closed
	// {Starting:0 Active:0 Idle:0 Retiring:0}
}

func Example_feedStart() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	snap, _ := session.FeedStart(ctx, `greet(name) + "!"`, &montygo.FeedOptions{Inputs: map[string]any{"name": "Ada"}})
	if call, ok := snap.(*montygo.FunctionSnapshot); ok {
		fmt.Println(call.FunctionName, call.Args)
		done, _ := call.Resume(ctx, "hello Ada")
		fmt.Println(done.(*montygo.Complete).Output)
	}
	// Output:
	// greet [Ada]
	// hello Ada!
}

func Example_resumeAuto() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	snap, _ := session.FeedStart(ctx, `greet(name) + "!"`, &montygo.FeedOptions{
		Inputs:         map[string]any{"name": "Ada"},
		ExternalLookup: map[string]any{"greet": func(n string) string { return "hello " + n }},
	})
	for {
		complete, done := snap.(*montygo.Complete)
		if done {
			fmt.Println(complete.Output)
			break
		}
		switch s := snap.(type) {
		case *montygo.FunctionSnapshot:
			snap, _ = s.ResumeAuto(ctx)
		case *montygo.NameLookupSnapshot:
			snap, _ = s.ResumeAuto(ctx)
		case *montygo.FutureSnapshot:
			snap, _ = s.ResumeAuto(ctx)
		}
	}
	// Output: hello Ada!
}

func Example_dump() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	snap, _ := session.FeedStart(ctx, "fetch('key') * 2", nil)
	state, _ := snap.(*montygo.FunctionSnapshot).Dump(ctx)

	fresh, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer fresh.Close(ctx)
	restored, _ := fresh.LoadSnapshot(ctx, state, nil)
	done, _ := restored.(*montygo.FunctionSnapshot).Resume(ctx, 21)
	fmt.Println(done.(*montygo.Complete).Output)
	// Output: 42
}

func Example_print() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	text, _ := sandbox.NewCollectString(sandbox.DefaultMaxPrintCollectBytes)
	_, _ = session.FeedRun(ctx, `print("hello")`, &montygo.FeedOptions{Print: text})
	fmt.Printf("%q\n", text.Output())
	// Output: "hello\n"
}

func Example_mount() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	dir, _ := os.MkdirTemp("", "monty-example")
	defer os.RemoveAll(dir)
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("mounted text"), 0o644)
	mount, err := sandbox.NewMountDir(sandbox.MountDirOptions{HostPath: dir, VirtualPath: "/mnt/data", Mode: sandbox.MountReadOnly})
	if err != nil {
		panic(err)
	}
	defer mount.Close()

	result, _ := session.FeedRun(ctx, "open('file.txt').read()", &montygo.FeedOptions{Mount: []*sandbox.MountDir{mount}, Cwd: "/mnt/data"})
	fmt.Println(result)
	// Output: mounted text
}

func Example_osHandler() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)

	files := map[string]string{"/data/message.txt": "hello from the host"}
	handler := func(_ context.Context, name string, args []any, _ host.Kwargs) (any, error) {
		path := string(args[0].(sandbox.Path))
		switch name {
		case "open":
			return sandbox.NewFileHandle(path, args[1].(string), 0)
		case "Path.read_text":
			if text, ok := files[path]; ok {
				return text, nil
			}
		}
		return host.NotHandled, nil
	}
	rt, _ := montygo.NewRuntime(montygo.RuntimeOptions{OS: handler})
	session, _ := pool.Checkout(ctx, rt, montygo.CheckoutOptions{})
	defer session.Close(ctx)
	result, _ := session.FeedRun(ctx, "open('/data/message.txt').read()", nil)
	fmt.Println(result)
	// Output: hello from the host
}

func Example_limits() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxRecursionDepth: 10}})
	defer session.Close(ctx)

	_, err := session.FeedRun(ctx, "def f(n):\n    return f(n + 1)\nf(0)", nil)
	var runtimeErr *monterr.RuntimeError
	fmt.Println(errors.As(err, &runtimeErr), runtimeErr.TypeName)
	// Output: true RecursionError
}

func Example_errors() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	_, err := session.FeedRun(ctx, "1 / 0", nil)
	var runtimeErr *monterr.RuntimeError
	if errors.As(err, &runtimeErr) {
		fmt.Println(runtimeErr.Exception().TypeName)
		fmt.Println(runtimeErr.Display(monterr.DisplayTraceback))
	}
	// Output:
	// ZeroDivisionError
	// Traceback (most recent call last):
	//   File "<python-input-0>", line 1, in <module>
	//     1 / 0
	//     ~~~~~
	// ZeroDivisionError: division by zero
}

func Example_typeCheck() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, mustRuntime(montygo.RuntimeOptions{TypeCheck: true, TypeCheckStubs: "def fetch(url: str) -> str: ...", TypeCheckFormat: montygo.FormatConcise}), montygo.CheckoutOptions{})
	defer session.Close(ctx)

	_, err := session.FeedRun(ctx, "fetch(123)", nil)
	var typingErr *monterr.TypingError
	fmt.Println(errors.As(err, &typingErr))
	// Output: true
}

func Example_wasmBackend() {
	ctx := context.Background()
	pool, err := montygo.NewPool(ctx, montygo.PoolOptions{Workers: montygo.Wasm(montygo.WasmOptions{})})
	if err != nil {
		panic(err)
	}
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, defaultRuntime, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	result, _ := session.FeedRun(ctx, "sum(range(10))", nil)
	fmt.Println(pool.Workers(), result)
	// Output: wasm 45
}

func ExampleCheckServerHealth() {
	ctx := context.Background()
	server := montygo.StaticServer(
		"wss://monty.example.com/",
		&tls.Config{MinVersion: tls.VersionTLS12},
		func(ctx context.Context) (map[string]string, error) {
			return map[string]string{"authorization": "Bearer ..."}, nil
		},
	)
	remote := montygo.RemoteOptions{DialTimeout: 5 * time.Second}
	if err := montygo.CheckServerHealth(ctx, server, remote); err != nil {
		fmt.Println("server unavailable:", err)
		return
	}
	pool, err := montygo.NewPool(ctx, montygo.PoolOptions{Workers: montygo.Remote(server, remote)})
	if err != nil {
		panic(err)
	}
	defer pool.Close(ctx)
	rt, _ := montygo.NewRuntime(montygo.RuntimeOptions{})
	session, err := pool.Checkout(ctx, rt, montygo.CheckoutOptions{})
	if err != nil {
		panic(err)
	}
	defer session.Close(ctx)
	result, err := session.FeedRun(ctx, "1 + 2", nil)
	fmt.Println(result, err)
}

func ExampleFetchServerInfo() {
	ctx := context.Background()
	server := montygo.StaticServer("ws://127.0.0.1:8000/", nil, nil)
	info, err := montygo.FetchServerInfo(ctx, server, montygo.RemoteOptions{})
	if errors.Is(err, monterr.ErrNoServerInfo) {
		fmt.Println("server predates /info")
		return
	}
	if err != nil {
		fmt.Println("server unavailable:", err)
		return
	}
	fmt.Println(info.Version, info.MontyRev, info.ProtocolVersion, info.Limits.MaxSessions, info.Limits.SessionTimeout)
	pool, err := montygo.NewPool(ctx, montygo.PoolOptions{
		Workers:    montygo.Remote(server, montygo.RemoteOptions{RotateSessions: true}),
		MaxWorkers: info.Limits.MaxSessions,
	})
	if err != nil {
		panic(err)
	}
	defer pool.Close(ctx)
}
