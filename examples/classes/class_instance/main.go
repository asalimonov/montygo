// Command class_instance exposes a host object to the sandbox with an explicit
// policy and gets the original object back.
package main

import (
	"context"
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

func (p *Person) String() string {
	return fmt.Sprintf("Person(name=%s, age=%d)", montygo.Repr(p.Name), p.Age)
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	person := &Person{Name: "Samuel", Age: 4}

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
		user, err := montygo.NewClassInstance(person, montygo.ClassInstanceOptions{EagerAttrs: montygo.All(), AllowedMethods: montygo.Names("greeting")})
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
