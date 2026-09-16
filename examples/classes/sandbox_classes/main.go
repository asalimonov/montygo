// Command sandbox_classes returns an instance of a class defined inside Monty;
// the host receives a read-only ClassProxy snapshot.
package main

import (
	"context"
	"fmt"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
	"io"
	"os"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/montyenv"
)

const code = `from dataclasses import dataclass

@dataclass
class Point:
    x: int
    y: int

    def norm2(self) -> int:
        return self.x ** 2 + self.y ** 2

p = Point(3, 4)
assert p.norm2() == 25  # methods work inside the sandbox
p
`

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	pool, err := montygo.NewPool(ctx, montyenv.PoolOptions())
	if err != nil {
		return err
	}
	defer pool.Close(ctx)

	result, err := func() (any, error) {
		rt, err := montygo.NewRuntime(montygo.RuntimeOptions{})
		if err != nil {
			return nil, err
		}
		session, err := pool.Checkout(ctx, rt, montygo.CheckoutOptions{})
		if err != nil {
			return nil, err
		}
		defer session.Close(ctx)
		return session.FeedRun(ctx, code, nil)
	}()
	if err != nil {
		return err
	}

	proxy, ok := result.(*host.ClassProxy)
	if !ok {
		return fmt.Errorf("assertion failed: expected a ClassProxy, got %T", result)
	}
	if proxy.Name != "Point" {
		return fmt.Errorf("assertion failed: name == %q", proxy.Name)
	}
	if !proxy.IsDataclass {
		return fmt.Errorf("assertion failed: is_dataclass is false")
	}
	want := sandbox.NewDict(sandbox.Pair{Key: "x", Value: 3}, sandbox.Pair{Key: "y", Value: 4})
	if !sandbox.Equal(proxy.Attributes, want) {
		return fmt.Errorf("assertion failed: attributes == %s", sandbox.Repr(proxy.Attributes))
	}
	fmt.Fprintf(out, "host received: %v\n", proxy)
	return nil
}
