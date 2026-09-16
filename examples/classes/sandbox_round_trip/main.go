// Command sandbox_round_trip passes a ClassProxy back into the sandbox, where it
// resolves to the original object until the sandbox frees it.
package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
	"io"
	"os"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/montyenv"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func echo(value any) any { return value }

func run(ctx context.Context, out io.Writer) error {
	pool, err := montygo.NewPool(ctx, montyenv.PoolOptions())
	if err != nil {
		return err
	}
	defer pool.Close(ctx)

	proxy, err := roundTrip(ctx, pool, out)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "proxy id %s resolved to the original sandbox object\n", proxy.ID)
	return nil
}

func roundTrip(ctx context.Context, pool *montygo.Pool, out io.Writer) (*host.ClassProxy, error) {
	rt, err := montygo.NewRuntime(montygo.RuntimeOptions{})
	if err != nil {
		return nil, err
	}
	session, err := pool.Checkout(ctx, rt, montygo.CheckoutOptions{})
	if err != nil {
		return nil, err
	}
	defer session.Close(ctx)

	if _, err := session.FeedRun(ctx, "class Counter:\n    def __init__(self):\n        self.n = 1\ncounter = Counter()", nil); err != nil {
		return nil, err
	}
	result, err := session.FeedRun(ctx, "counter", nil)
	if err != nil {
		return nil, err
	}
	proxy, ok := result.(*host.ClassProxy)
	if !ok {
		return nil, fmt.Errorf("assertion failed: expected a ClassProxy, got %T", result)
	}

	proxy.Attributes.Set("n", 99)
	result, err = session.FeedRun(ctx, "back is counter and back.n == 1", &montygo.FeedOptions{Inputs: map[string]any{"back": proxy}})
	if err != nil {
		return nil, err
	}
	if result != true {
		return nil, fmt.Errorf("assertion failed: input round-trip returned %s", sandbox.Repr(result))
	}

	result, err = session.FeedRun(ctx, "echo(counter) is counter", &montygo.FeedOptions{ExternalLookup: map[string]any{"echo": echo}})
	if err != nil {
		return nil, err
	}
	if result != true {
		return nil, fmt.Errorf("assertion failed: external-function round-trip returned %s", sandbox.Repr(result))
	}

	if _, err := session.FeedRun(ctx, "counter = back = None", nil); err != nil {
		return nil, err
	}
	_, err = session.FeedRun(ctx, "back", &montygo.FeedOptions{Inputs: map[string]any{"back": proxy}})
	var exc *monterr.RuntimeError
	switch {
	case errors.As(err, &exc):
		fmt.Fprintf(out, "freed object rejected: %v\n", exc)
	case err != nil:
		return nil, err
	default:
		return nil, errors.New("expected the freed proxy to be rejected")
	}
	return proxy, nil
}
