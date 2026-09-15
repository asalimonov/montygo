// Command class_type lets sandbox code instantiate a host class when Init is
// granted; without it construction raises TypeError.
package main

import (
	"context"
	"errors"
	"fmt"
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
	session, err := pool.Checkout(ctx, montygo.CheckoutOptions{})
	if err != nil {
		return err
	}
	defer session.Close(ctx)
	return fn(session)
}

func run(ctx context.Context, out io.Writer) error {
	pool, err := montygo.New(ctx, montyenv.PoolOptions())
	if err != nil {
		return err
	}
	defer pool.Close(ctx)

	err = withSession(ctx, pool, func(session *montygo.Session) error {
		wrapper, err := montygo.NewClassType[Person](montygo.ClassTypeOptions{Init: true, InstanceEagerAttrs: montygo.All(), InstanceAllowedMethods: montygo.All()})
		if err != nil {
			return err
		}
		result, err := session.FeedRun(ctx, "p = Person(\"Samuel\", 4)\np.greeting()", &montygo.FeedOptions{Inputs: map[string]any{"Person": wrapper}})
		if err != nil {
			return err
		}
		if result != "hi Samuel" {
			return fmt.Errorf("assertion failed: result == %s", montygo.Repr(result))
		}
		fmt.Fprintf(out, "constructed in the sandbox: %s\n", montygo.Repr(result))
		return nil
	})
	if err != nil {
		return err
	}

	return withSession(ctx, pool, func(session *montygo.Session) error {
		wrapper, err := montygo.NewClassType[Person](montygo.ClassTypeOptions{})
		if err != nil {
			return err
		}
		_, err = session.FeedRun(ctx, "Person(\"Samuel\", 4)", &montygo.FeedOptions{Inputs: map[string]any{"Person": wrapper}})
		var exc *montygo.RuntimeError
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
