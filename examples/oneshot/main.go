// Command oneshot evaluates one snippet with Pool.Run: checkout, feed and
// close in a single call.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/montyenv"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	pool, err := montygo.New(ctx, montyenv.PoolOptions())
	if err != nil {
		return err
	}
	defer pool.Shutdown(ctx)

	v, err := pool.Run(ctx, "sum(range(n))", &montygo.RunOptions{
		FeedOptions: montygo.FeedOptions{Inputs: map[string]any{"n": 10}},
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, v)
	return err
}
