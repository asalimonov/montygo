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
)

func Example() {
	ctx := context.Background()
	pool, err := montygo.New(ctx, montygo.Options{})
	if err != nil {
		panic(err)
	}
	defer pool.Close(ctx)
	session, err := pool.Checkout(ctx, montygo.CheckoutOptions{})
	if err != nil {
		panic(err)
	}
	defer session.Close(ctx)

	result, err := session.FeedRun(ctx, "1 + 2", nil)
	fmt.Println(result, err)
	// Output: 3 <nil>
}

func Example_sessionState() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	_, _ = session.FeedRun(ctx, "x = 21", nil)
	result, _ := session.FeedRun(ctx, "x * 2", nil)
	fmt.Println(result)
	// Output: 42
}

func Example_inputs() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	result, _ := session.FeedRun(ctx, "x + y", &montygo.FeedOptions{Inputs: map[string]any{"x": 10, "y": 20}})
	fmt.Println(result)
	// Output: 30
}

func Example_externalLookup() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	lookup := map[string]any{
		"add": func(a, b int) int { return a + b },
		"fetch_data": func(url string) *montygo.Future {
			return montygo.Async(func() (any, error) { return "contents of " + url, nil })
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
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	scale := func(x int, kwargs montygo.Kwargs) int64 { return int64(x) * kwargs["factor"].(int64) }
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

func wrapWallet(w *exampleWallet) *montygo.ClassInstance {
	return montygo.MustClassInstance(w, montygo.ClassInstanceOptions{
		EagerAttrs:     montygo.All(),
		AllowedMethods: montygo.All(),
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
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	wallet := &exampleWallet{Balance: 100}
	balance, _ := session.FeedRun(ctx, "w.pay(30).balance", &montygo.FeedOptions{Inputs: map[string]any{"w": wrapWallet(wallet)}})
	same, _ := session.FeedRun(ctx, "w", &montygo.FeedOptions{Inputs: map[string]any{"w": wrapWallet(wallet)}})
	fmt.Println(balance, same == wallet)
	// Output: 70 true
}

func Example_classType() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	walletClass := montygo.MustClassType[exampleWallet](montygo.ClassTypeOptions{
		Init:                   true,
		InstanceEagerAttrs:     montygo.All(),
		InstanceAllowedMethods: montygo.All(),
		ConvertValue: func(_ string, v any) (any, error) {
			if next, ok := v.(*exampleWallet); ok {
				return wrapWallet(next), nil
			}
			return v, nil
		},
	})
	result, _ := session.FeedRun(ctx, "w = Wallet(100)\nw.pay(30).balance", &montygo.FeedOptions{Inputs: map[string]any{"Wallet": walletClass}})
	_, err := session.FeedRun(ctx, "Wallet(1)", &montygo.FeedOptions{Inputs: map[string]any{"Wallet": montygo.MustClassType[exampleWallet](montygo.ClassTypeOptions{})}})
	fmt.Println(result)
	fmt.Println(err)
	// Output:
	// 70
	// TypeError: cannot instantiate host class 'exampleWallet'
}

func Example_interrupt() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	lookup := map[string]any{"wait": func(ctx context.Context) error {
		<-ctx.Done() // the host call's context ends on Interrupt
		return ctx.Err()
	}}
	run := session.Go(ctx, "wait()", &montygo.FeedOptions{ExternalLookup: lookup})
	stop, stopErr := run.Interrupt(ctx, montygo.InterruptOptions{Grace: montygo.DurationPtr(250 * time.Millisecond)})
	fmt.Println(stopErr == nil, stop.Outcome == montygo.InterruptBeforeStart || stop.Outcome == montygo.InterruptAborted, stop.RunDone)
	_, err := run.Wait()
	var runtimeErr *montygo.RuntimeError
	fmt.Println(errors.As(err, &runtimeErr), runtimeErr.TypeName, errors.Is(err, montygo.ErrSessionLost))

	result, _ := session.FeedRun(ctx, "1 + 1", nil) // the session is still usable
	fmt.Println(result)
	// Output:
	// true true true
	// true KeyboardInterrupt false
	// 2
}

type payer interface {
	Pay(amount int) *exampleWallet
}

func Example_host() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)

	host := montygo.NewHost()
	_ = host.Func("add", func(a, b int) int { return a + b })
	_ = host.Object("wallet", &exampleWallet{Balance: 100}, montygo.ClassInstanceOptions{
		AllowedMethods: montygo.Expose[payer](),
		ConvertValue: func(_ string, v any) (any, error) {
			if next, ok := v.(*exampleWallet); ok {
				return wrapWallet(next), nil
			}
			return v, nil
		},
	})
	fmt.Print(host.Stubs())

	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{Host: host, TypeCheck: true, TypeCheckStubs: host.Stubs()})
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
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	lines := montygo.Lines(func(stream montygo.Stream, line string) error {
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
	pool, _ := montygo.New(ctx, montygo.Options{MaxProcesses: 2})
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	fmt.Printf("%+v\n", pool.Stats())

	go func() {
		_, _ = session.FeedRun(ctx, "1 + 1", nil)
		_ = session.Close(ctx)
	}()
	fmt.Println(pool.Shutdown(ctx)) // waits for the session and every worker
	fmt.Printf("%+v\n", pool.Stats())
	// Output:
	// {Starting:0 Active:1 Idle:0 Retiring:0}
	// <nil>
	// {Starting:0 Active:0 Idle:0 Retiring:0}
}

func Example_feedStart() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
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
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
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
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	snap, _ := session.FeedStart(ctx, "fetch('key') * 2", nil)
	state, _ := snap.(*montygo.FunctionSnapshot).Dump(ctx)

	fresh, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer fresh.Close(ctx)
	restored, _ := fresh.LoadSnapshot(ctx, state, nil)
	done, _ := restored.(*montygo.FunctionSnapshot).Resume(ctx, 21)
	fmt.Println(done.(*montygo.Complete).Output)
	// Output: 42
}

func Example_print() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	text, _ := montygo.NewCollectString(montygo.DefaultMaxPrintCollectBytes)
	_, _ = session.FeedRun(ctx, `print("hello")`, &montygo.FeedOptions{Print: text})
	fmt.Printf("%q\n", text.Output())
	// Output: "hello\n"
}

func Example_mount() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	dir, _ := os.MkdirTemp("", "monty-example")
	defer os.RemoveAll(dir)
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("mounted text"), 0o644)
	mount, err := montygo.NewMountDir(montygo.MountDirOptions{HostPath: dir, VirtualPath: "/mnt/data", Mode: montygo.MountReadOnly})
	if err != nil {
		panic(err)
	}
	defer mount.Close()

	result, _ := session.FeedRun(ctx, "open('file.txt').read()", &montygo.FeedOptions{Mount: []*montygo.MountDir{mount}, Cwd: "/mnt/data"})
	fmt.Println(result)
	// Output: mounted text
}

func Example_osHandler() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	files := map[string]string{"/data/message.txt": "hello from the host"}
	handler := func(_ context.Context, name string, args []any, _ montygo.Kwargs) (any, error) {
		path := string(args[0].(montygo.Path))
		switch name {
		case "open":
			return montygo.NewFileHandle(path, args[1].(string), 0)
		case "Path.read_text":
			if text, ok := files[path]; ok {
				return text, nil
			}
		}
		return montygo.NotHandled, nil
	}
	result, _ := session.FeedRun(ctx, "open('/data/message.txt').read()", &montygo.FeedOptions{OS: handler})
	fmt.Println(result)
	// Output: hello from the host
}

func Example_limits() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{Limits: &montygo.ResourceLimits{MaxRecursionDepth: 10}})
	defer session.Close(ctx)

	_, err := session.FeedRun(ctx, "def f(n):\n    return f(n + 1)\nf(0)", nil)
	var runtimeErr *montygo.RuntimeError
	fmt.Println(errors.As(err, &runtimeErr), runtimeErr.TypeName)
	// Output: true RecursionError
}

func Example_errors() {
	ctx := context.Background()
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	_, err := session.FeedRun(ctx, "1 / 0", nil)
	var runtimeErr *montygo.RuntimeError
	if errors.As(err, &runtimeErr) {
		fmt.Println(runtimeErr.Exception().TypeName)
		fmt.Println(runtimeErr.Display(montygo.DisplayTraceback))
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
	pool, _ := montygo.New(ctx, montygo.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{
		TypeCheck:       true,
		TypeCheckStubs:  "def fetch(url: str) -> str: ...",
		TypeCheckFormat: montygo.FormatConcise,
	})
	defer session.Close(ctx)

	_, err := session.FeedRun(ctx, "fetch(123)", nil)
	var typingErr *montygo.TypingError
	fmt.Println(errors.As(err, &typingErr))
	// Output: true
}

func Example_wasmBackend() {
	ctx := context.Background()
	pool, err := montygo.New(ctx, montygo.Options{Backend: montygo.BackendWasm})
	if err != nil {
		panic(err)
	}
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, montygo.CheckoutOptions{})
	defer session.Close(ctx)

	result, _ := session.FeedRun(ctx, "sum(range(10))", nil)
	fmt.Println(pool.Backend(), result)
	// Output: wasm 45
}

func ExampleCheckWebSocketHealth() {
	ctx := context.Background()
	opts := montygo.WebSocketOptions{
		URL:       "wss://monty.example.com/",
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		ConnectHeaders: func(ctx context.Context) (map[string]string, error) {
			return map[string]string{"authorization": "Bearer ..."}, nil
		},
	}
	if err := montygo.CheckWebSocketHealth(ctx, opts); err != nil {
		fmt.Println("server unavailable:", err)
		return
	}
	pool, err := montygo.NewWebSocket(ctx, opts)
	if err != nil {
		panic(err)
	}
	defer pool.Close(ctx)
	session, err := pool.Checkout(ctx, montygo.CheckoutOptions{})
	if err != nil {
		panic(err)
	}
	defer session.Close(ctx)
	result, err := session.FeedRun(ctx, "1 + 2", nil)
	fmt.Println(result, err)
}

func ExampleFetchServerInfo() {
	ctx := context.Background()
	opts := montygo.WebSocketOptions{URL: "ws://127.0.0.1:8000/"}
	info, err := montygo.FetchServerInfo(ctx, opts)
	if errors.Is(err, montygo.ErrNoServerInfo) {
		fmt.Println("server predates /info")
		return
	}
	if err != nil {
		fmt.Println("server unavailable:", err)
		return
	}
	fmt.Println(info.Version, info.MontyRev, info.ProtocolVersion, info.Limits.MaxSessions, info.Limits.SessionTimeout)
	pool, err := montygo.NewWebSocket(ctx, montygo.WebSocketOptions{URL: opts.URL, MaxProcesses: info.Limits.MaxSessions})
	if err != nil {
		panic(err)
	}
	defer pool.Close(ctx)
}
