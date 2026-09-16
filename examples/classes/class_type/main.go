// Command class_type lets sandbox code instantiate a host class when Init is
// granted; without it construction raises TypeError.
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

type Person struct {
	Name string
	Age  int
}

func (p *Person) Greeting() string { return "hi " + p.Name }

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func withSession(ctx context.Context, pool *montygo.Pool, fn func(*montygo.Session) error) error {
	rt, err := montygo.NewRuntime(montygo.RuntimeOptions{})
	if err != nil {
		return err
	}
	session, err := pool.Checkout(ctx, rt, montygo.CheckoutOptions{})
	if err != nil {
		return err
	}
	defer session.Close(ctx)
	return fn(session)
}

func run(ctx context.Context, out io.Writer) error {
	pool, err := montygo.NewPool(ctx, montyenv.PoolOptions())
	if err != nil {
		return err
	}
	defer pool.Close(ctx)

	err = withSession(ctx, pool, func(session *montygo.Session) error {
		wrapper, err := host.NewClassType[Person](host.ClassTypeOptions{Init: true, InstanceEagerAttrs: host.All(), InstanceAllowedMethods: host.All()})
		if err != nil {
			return err
		}
		result, err := session.FeedRun(ctx, "p = Person(\"Samuel\", 4)\np.greeting()", &montygo.FeedOptions{Inputs: map[string]any{"Person": wrapper}})
		if err != nil {
			return err
		}
		if result != "hi Samuel" {
			return fmt.Errorf("assertion failed: result == %s", sandbox.Repr(result))
		}
		fmt.Fprintf(out, "constructed in the sandbox: %s\n", sandbox.Repr(result))
		return nil
	})
	if err != nil {
		return err
	}

	return withSession(ctx, pool, func(session *montygo.Session) error {
		wrapper, err := host.NewClassType[Person](host.ClassTypeOptions{})
		if err != nil {
			return err
		}
		_, err = session.FeedRun(ctx, "Person(\"Samuel\", 4)", &montygo.FeedOptions{Inputs: map[string]any{"Person": wrapper}})
		var exc *monterr.RuntimeError
		switch {
		case errors.As(err, &exc):
			fmt.Fprintf(out, "construction denied: %v\n", exc)
			return nil
		case err != nil:
			return err
		}
		return errors.New("expected construction to be denied")
	})
}
