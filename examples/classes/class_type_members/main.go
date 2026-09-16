// Command class_type_members exposes class constants and class-level functions
// without letting the sandbox construct the class.
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

type Shape struct{}

const (
	shapeSides = 4
	shapeKind  = "polygon"
)

func shapeUnit() int { return shapeSides }

func shapeDouble(n int) int { return n * 2 }

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
		wrapper, err := host.NewClassType[Shape](host.ClassTypeOptions{
			EagerAttrs:     host.All(),
			AllowedMethods: host.Names("unit", "double"),
			Statics: map[string]any{
				"SIDES":  shapeSides,
				"KIND":   shapeKind,
				"unit":   shapeUnit,
				"double": shapeDouble,
			},
		})
		if err != nil {
			return nil, err
		}
		return session.FeedRun(ctx,
			"assert Shape.KIND == \"polygon\"\nShape.unit() + Shape.double(10)",
			&montygo.FeedOptions{Inputs: map[string]any{"Shape": wrapper}},
		)
	}()
	if err != nil {
		return err
	}

	if !sandbox.Equal(result, 24) {
		return fmt.Errorf("assertion failed: result == %s", sandbox.Repr(result))
	}
	fmt.Fprintf(out, "class constants and classmethods work: %s\n", sandbox.Repr(result))
	return nil
}
