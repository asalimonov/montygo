// Command sandbox_copy shows that sandbox mutations stay on the sandbox copy
// and never touch the host object.
package main

import (
	"context"
	"fmt"
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

	pool, err := montygo.New(ctx, montyenv.PoolOptions())
	if err != nil {
		return err
	}
	defer pool.Close(ctx)

	result, err := func() (any, error) {
		session, err := pool.Checkout(ctx, montygo.CheckoutOptions{})
		if err != nil {
			return nil, err
		}
		defer session.Close(ctx)
		p, err := montygo.NewClassInstance(point, montygo.ClassInstanceOptions{EagerAttrs: montygo.All()})
		if err != nil {
			return nil, err
		}
		return session.FeedRun(ctx, "p.x = 99\np.x", &montygo.FeedOptions{Inputs: map[string]any{"p": p}})
	}()
	if err != nil {
		return err
	}

	if !montygo.Equal(result, 99) {
		return fmt.Errorf("assertion failed: result == %s", montygo.Repr(result))
	}
	if point.X != 1 {
		return fmt.Errorf("assertion failed: point.x == %d", point.X)
	}
	fmt.Fprintf(out, "sandbox copy saw x=%s, host object still %v\n", montygo.Repr(result), point)
	return nil
}
