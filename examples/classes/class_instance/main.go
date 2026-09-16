// Command class_instance exposes a host object to the sandbox with an explicit
// policy and gets the original object back.
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

type Person struct {
	Name string
	Age  int
}

func (p *Person) Greeting() string { return "hi " + p.Name }

func (p *Person) String() string {
	return fmt.Sprintf("Person(name=%s, age=%d)", sandbox.Repr(p.Name), p.Age)
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	person := &Person{Name: "Samuel", Age: 4}

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
		user, err := host.NewClassInstance(person, host.ClassInstanceOptions{EagerAttrs: host.All(), AllowedMethods: host.Names("greeting")})
		if err != nil {
			return nil, err
		}
		return session.FeedRun(ctx,
			"assert user.name == \"Samuel\"\nassert user.greeting() == \"hi Samuel\"\nuser",
			&montygo.FeedOptions{Inputs: map[string]any{"user": user}},
		)
	}()
	if err != nil {
		return err
	}

	if got, ok := result.(*Person); !ok || got != person {
		return fmt.Errorf("assertion failed: expected the original object, got %#v", result)
	}
	fmt.Fprintf(out, "got back the original object: %v\n", result)
	return nil
}
