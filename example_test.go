package monty_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	monty "github.com/asalimonov/montygo"
)

func Example() {
	ctx := context.Background()
	pool, err := monty.New(ctx, monty.Options{})
	if err != nil {
		panic(err)
	}
	defer pool.Close(ctx)
	session, err := pool.Checkout(ctx, monty.CheckoutOptions{})
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
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	_, _ = session.FeedRun(ctx, "x = 21", nil)
	result, _ := session.FeedRun(ctx, "x * 2", nil)
	fmt.Println(result)
	// Output: 42
}

func Example_inputs() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	result, _ := session.FeedRun(ctx, "x + y", &monty.FeedOptions{Inputs: map[string]any{"x": 10, "y": 20}})
	fmt.Println(result)
	// Output: 30
}

func Example_externalLookup() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	lookup := map[string]any{
		"add": func(a, b int) int { return a + b },
		"fetch_data": func(url string) *monty.Future {
			return monty.Async(func() (any, error) { return "contents of " + url, nil })
		},
		"greeting": "hello ",
	}
	sum, _ := session.FeedRun(ctx, "add(2, 3)", &monty.FeedOptions{ExternalLookup: lookup})
	fetched, _ := session.FeedRun(ctx, "await fetch_data('https://example.com')", &monty.FeedOptions{ExternalLookup: lookup})
	text, _ := session.FeedRun(ctx, "greeting + name", &monty.FeedOptions{Inputs: map[string]any{"name": "Ada"}, ExternalLookup: lookup})
	fmt.Println(sum, fetched, text)
	// Output: 5 contents of https://example.com hello Ada
}

func Example_keywordArguments() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	scale := func(x int, kwargs monty.Kwargs) int64 { return int64(x) * kwargs["factor"].(int64) }
	result, _ := session.FeedRun(ctx, "scale(4, factor=10)", &monty.FeedOptions{ExternalLookup: map[string]any{"scale": scale}})
	fmt.Println(result)
	// Output: 40
}

type exampleWallet struct {
	Balance int
}

func (w *exampleWallet) Pay(amount int) *exampleWallet {
	return &exampleWallet{Balance: w.Balance - amount}
}

func wrapWallet(w *exampleWallet) *monty.ClassInstance {
	return monty.MustClassInstance(w, monty.ClassInstanceOptions{
		EagerAttrs:     monty.All,
		AllowedMethods: monty.All,
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
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	wallet := &exampleWallet{Balance: 100}
	balance, _ := session.FeedRun(ctx, "w.pay(30).balance", &monty.FeedOptions{Inputs: map[string]any{"w": wrapWallet(wallet)}})
	same, _ := session.FeedRun(ctx, "w", &monty.FeedOptions{Inputs: map[string]any{"w": wrapWallet(wallet)}})
	fmt.Println(balance, same == wallet)
	// Output: 70 true
}

func Example_classType() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	walletClass := monty.MustClassType[exampleWallet](monty.ClassTypeOptions{
		Init:                   true,
		InstanceEagerAttrs:     monty.All,
		InstanceAllowedMethods: monty.All,
		ConvertValue: func(_ string, v any) (any, error) {
			if next, ok := v.(*exampleWallet); ok {
				return wrapWallet(next), nil
			}
			return v, nil
		},
	})
	result, _ := session.FeedRun(ctx, "w = Wallet(100)\nw.pay(30).balance", &monty.FeedOptions{Inputs: map[string]any{"Wallet": walletClass}})
	_, err := session.FeedRun(ctx, "Wallet(1)", &monty.FeedOptions{Inputs: map[string]any{"Wallet": monty.MustClassType[exampleWallet](monty.ClassTypeOptions{})}})
	fmt.Println(result)
	fmt.Println(err)
	// Output:
	// 70
	// TypeError: cannot instantiate host class 'exampleWallet'
}

func Example_feedStart() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	snap, _ := session.FeedStart(ctx, `greet(name) + "!"`, &monty.FeedOptions{Inputs: map[string]any{"name": "Ada"}})
	if call, ok := snap.(*monty.FunctionSnapshot); ok {
		fmt.Println(call.FunctionName, call.Args)
		done, _ := call.Resume(ctx, "hello Ada")
		fmt.Println(done.(*monty.Complete).Output)
	}
	// Output:
	// greet [Ada]
	// hello Ada!
}

func Example_resumeAuto() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	snap, _ := session.FeedStart(ctx, `greet(name) + "!"`, &monty.FeedOptions{
		Inputs:         map[string]any{"name": "Ada"},
		ExternalLookup: map[string]any{"greet": func(n string) string { return "hello " + n }},
	})
	for {
		complete, done := snap.(*monty.Complete)
		if done {
			fmt.Println(complete.Output)
			break
		}
		switch s := snap.(type) {
		case *monty.FunctionSnapshot:
			snap, _ = s.ResumeAuto(ctx)
		case *monty.NameLookupSnapshot:
			snap, _ = s.ResumeAuto(ctx)
		case *monty.FutureSnapshot:
			snap, _ = s.ResumeAuto(ctx)
		}
	}
	// Output: hello Ada!
}

func Example_dump() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	snap, _ := session.FeedStart(ctx, "fetch('key') * 2", nil)
	state, _ := snap.(*monty.FunctionSnapshot).Dump(ctx)

	fresh, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer fresh.Close(ctx)
	restored, _ := fresh.LoadSnapshot(ctx, state, nil)
	done, _ := restored.(*monty.FunctionSnapshot).Resume(ctx, 21)
	fmt.Println(done.(*monty.Complete).Output)
	// Output: 42
}

func Example_print() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	text, _ := monty.NewCollectString(monty.DefaultMaxPrintCollectBytes)
	_, _ = session.FeedRun(ctx, `print("hello")`, &monty.FeedOptions{Print: text})
	fmt.Printf("%q\n", text.Output())
	// Output: "hello\n"
}

func Example_mount() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	dir, _ := os.MkdirTemp("", "monty-example")
	defer os.RemoveAll(dir)
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("mounted text"), 0o644)
	mount, err := monty.NewMountDir(monty.MountDirOptions{HostPath: dir, VirtualPath: "/mnt/data", Mode: monty.MountReadOnly})
	if err != nil {
		panic(err)
	}
	defer mount.Close()

	result, _ := session.FeedRun(ctx, "open('file.txt').read()", &monty.FeedOptions{Mount: []*monty.MountDir{mount}, Cwd: "/mnt/data"})
	fmt.Println(result)
	// Output: mounted text
}

func Example_osHandler() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	files := map[string]string{"/data/message.txt": "hello from the host"}
	handler := func(_ context.Context, name string, args []any, _ monty.Kwargs) (any, error) {
		path := string(args[0].(monty.Path))
		switch name {
		case "open":
			return monty.NewFileHandle(path, args[1].(string), 0)
		case "Path.read_text":
			if text, ok := files[path]; ok {
				return text, nil
			}
		}
		return monty.NotHandled, nil
	}
	result, _ := session.FeedRun(ctx, "open('/data/message.txt').read()", &monty.FeedOptions{OS: handler})
	fmt.Println(result)
	// Output: hello from the host
}

func Example_limits() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{Limits: &monty.ResourceLimits{MaxRecursionDepth: 10}})
	defer session.Close(ctx)

	_, err := session.FeedRun(ctx, "def f(n):\n    return f(n + 1)\nf(0)", nil)
	var runtimeErr *monty.RuntimeError
	fmt.Println(errors.As(err, &runtimeErr), runtimeErr.TypeName)
	// Output: true RecursionError
}

func Example_errors() {
	ctx := context.Background()
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	_, err := session.FeedRun(ctx, "1 / 0", nil)
	var runtimeErr *monty.RuntimeError
	if errors.As(err, &runtimeErr) {
		fmt.Println(runtimeErr.Exception().TypeName)
		fmt.Println(runtimeErr.Display(monty.DisplayTraceback))
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
	pool, _ := monty.New(ctx, monty.Options{})
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{
		TypeCheck:       true,
		TypeCheckStubs:  "def fetch(url: str) -> str: ...",
		TypeCheckFormat: monty.FormatConcise,
	})
	defer session.Close(ctx)

	_, err := session.FeedRun(ctx, "fetch(123)", nil)
	var typingErr *monty.TypingError
	fmt.Println(errors.As(err, &typingErr))
	// Output: true
}

func Example_wasmBackend() {
	ctx := context.Background()
	pool, err := monty.New(ctx, monty.Options{Backend: monty.BackendWasm})
	if err != nil {
		panic(err)
	}
	defer pool.Close(ctx)
	session, _ := pool.Checkout(ctx, monty.CheckoutOptions{})
	defer session.Close(ctx)

	result, _ := session.FeedRun(ctx, "sum(range(10))", nil)
	fmt.Println(pool.Backend(), result)
	// Output: wasm 45
}
