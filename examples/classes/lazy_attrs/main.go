// Command lazy_attrs exposes attributes fetched from the host on demand; names
// outside the policy raise AttributeError inside the sandbox.
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
		wrapper, err := host.NewClassInstance(newConfig(), host.ClassInstanceOptions{LazyAttrs: host.Names("retries")})
		if err != nil {
			return err
		}
		result, err := session.FeedRun(ctx, "cfg.retries", &montygo.FeedOptions{Inputs: map[string]any{"cfg": wrapper}})
		if err != nil {
			return err
		}
		if !sandbox.Equal(result, 3) {
			return fmt.Errorf("assertion failed: cfg.retries == %s", sandbox.Repr(result))
		}
		return nil
	})
	if err != nil {
		return err
	}

	return withSession(ctx, pool, func(session *montygo.Session) error {
		wrapper, err := host.NewClassInstance(newConfig(), host.ClassInstanceOptions{LazyAttrs: host.Names("retries")})
		if err != nil {
			return err
		}
		_, err = session.FeedRun(ctx, "cfg.api_key", &montygo.FeedOptions{Inputs: map[string]any{"cfg": wrapper}})
		var exc *monterr.RuntimeError
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
