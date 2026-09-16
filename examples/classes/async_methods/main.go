// Command async_methods awaits an asynchronous host method from sandbox code.
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

type Fetcher struct{}

func (f *Fetcher) Fetch(ctx context.Context, url string) *host.Future {
	return host.Async(func() (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return "contents of " + url, nil
	})
}

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
		client, err := host.NewClassInstance(&Fetcher{}, host.ClassInstanceOptions{AllowedMethods: host.Names("fetch")})
		if err != nil {
			return nil, err
		}
		return session.FeedRun(ctx, `await client.fetch("https://example.com")`, &montygo.FeedOptions{Inputs: map[string]any{"client": client}})
	}()
	if err != nil {
		return err
	}

	if result != "contents of https://example.com" {
		return fmt.Errorf("assertion failed: result == %s", sandbox.Repr(result))
	}
	fmt.Fprintln(out, result)
	return nil
}
