// Command lazy_attrs exposes attributes fetched from the host on demand; names
// outside the policy raise AttributeError inside the sandbox.
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

type Config struct {
	Retries int
	APIKey  string
}

func newConfig() *Config {
	return &Config{Retries: 3, APIKey: "hunter2"}
}

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
		wrapper, err := montygo.NewClassInstance(newConfig(), montygo.ClassInstanceOptions{LazyAttrs: montygo.Names("retries")})
		if err != nil {
			return err
		}
		result, err := session.FeedRun(ctx, "cfg.retries", &montygo.FeedOptions{Inputs: map[string]any{"cfg": wrapper}})
		if err != nil {
			return err
		}
		if !montygo.Equal(result, 3) {
			return fmt.Errorf("assertion failed: cfg.retries == %s", montygo.Repr(result))
		}
		return nil
	})
	if err != nil {
		return err
	}

	return withSession(ctx, pool, func(session *montygo.Session) error {
		wrapper, err := montygo.NewClassInstance(newConfig(), montygo.ClassInstanceOptions{LazyAttrs: montygo.Names("retries")})
		if err != nil {
			return err
		}
		_, err = session.FeedRun(ctx, "cfg.api_key", &montygo.FeedOptions{Inputs: map[string]any{"cfg": wrapper}})
		var exc *montygo.RuntimeError
		switch {
		case errors.As(err, &exc):
			fmt.Fprintf(out, "denied as expected: %v\n", exc)
			return nil
		case err != nil:
			return err
		}
		return errors.New("expected api_key to be denied")
	})
}
