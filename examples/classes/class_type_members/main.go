// Command class_type_members exposes class constants and class-level functions
// without letting the sandbox construct the class.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	monty "github.com/asalimonov/montygo"
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
	pool, err := monty.New(ctx, montyenv.PoolOptions())
	if err != nil {
		return err
	}
	defer pool.Close(ctx)

	result, err := func() (any, error) {
		session, err := pool.Checkout(ctx, monty.CheckoutOptions{})
		if err != nil {
			return nil, err
		}
		defer session.Close(ctx)
		wrapper, err := monty.NewClassType[Shape](monty.ClassTypeOptions{
			EagerAttrs:     monty.All,
			AllowedMethods: monty.Names("unit", "double"),
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
			&monty.FeedOptions{Inputs: map[string]any{"Shape": wrapper}},
		)
	}()
	if err != nil {
		return err
	}

	if !monty.Equal(result, 24) {
		return fmt.Errorf("assertion failed: result == %s", monty.Repr(result))
	}
	fmt.Fprintf(out, "class constants and classmethods work: %s\n", monty.Repr(result))
	return nil
}
