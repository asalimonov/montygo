// Command sandbox_copy shows that sandbox mutations stay on the sandbox copy
// and never touch the host object.
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

type Point struct {
	X int
	Y int
}

func (p Point) String() string { return fmt.Sprintf("Point(x=%d, y=%d)", p.X, p.Y) }

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	point := &Point{X: 1, Y: 2}

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
		p, err := host.NewClassInstance(point, host.ClassInstanceOptions{EagerAttrs: host.All()})
		if err != nil {
			return nil, err
		}
		return session.FeedRun(ctx, "p.x = 99\np.x", &montygo.FeedOptions{Inputs: map[string]any{"p": p}})
	}()
	if err != nil {
		return err
	}

	if !sandbox.Equal(result, 99) {
		return fmt.Errorf("assertion failed: result == %s", sandbox.Repr(result))
	}
	if point.X != 1 {
		return fmt.Errorf("assertion failed: point.x == %d", point.X)
	}
	fmt.Fprintf(out, "sandbox copy saw x=%s, host object still %v\n", sandbox.Repr(result), point)
	return nil
}
