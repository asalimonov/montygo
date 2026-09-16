// Command service runs an endless sandbox loop in a Slot, stops it with the
// library's default stop policy on SIGINT, and shuts the pool down.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/montyenv"
)

const script = `n = 0
while True:
    n += 1
    print(f"tick {n}: total {counter.add(n)}")
    sleep(200)
`

// Counter is the host object the loop updates.
type Counter struct{ total int }

func (c *Counter) Add(n int) int { c.total += n; return c.total }
func (c *Counter) Total() int    { return c.total }
func (c *Counter) Reset()        { c.total = 0 }

// counterAPI is what the sandbox may call: Reset stays hidden.
type counterAPI interface {
	Add(n int) int
	Total() int
}

// sleep pauses for ms milliseconds, or until the callback context ends.
func sleep(ctx context.Context, ms int) error {
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func main() {
	if err := service(context.Background(), os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func service(ctx context.Context, out io.Writer, args []string) error {
	flags := flag.NewFlagSet("service", flag.ContinueOnError)
	wsURL := flags.String("ws", "", "run the session on a remote Monty server at this ws:// or wss:// URL")
	runFor := flags.Duration("run-for", 0, "stop after this duration instead of on SIGINT")
	if err := flags.Parse(args); err != nil {
		return err
	}

	pool, err := openPool(ctx, *wsURL)
	if err != nil {
		return err
	}
	h := host.NewHost()
	if err := h.Func("sleep", sleep); err != nil {
		return err
	}
	if err := h.Object("counter", &Counter{}, host.ClassInstanceOptions{AllowedMethods: host.Expose[counterAPI]()}); err != nil {
		return err
	}
	rt, err := montygo.NewRuntime(montygo.RuntimeOptions{Host: h})
	if err != nil {
		return err
	}

	slot := pool.Slot(rt, montygo.CheckoutOptions{})
	lines := sandbox.Lines(func(_ sandbox.Stream, line string) error {
		_, err := fmt.Fprintln(out, line)
		return err
	})
	run, err := slot.Go(ctx, script, &montygo.FeedOptions{Print: lines})
	if err != nil {
		return err
	}

	until, cancel := signal.NotifyContext(ctx, os.Interrupt)
	if *runFor > 0 {
		cancel()
		until, cancel = context.WithTimeout(ctx, *runFor)
	}
	defer cancel()
	select {
	case <-until.Done():
	case <-run.Done():
	}

	stopped, err := run.Stop(ctx) // KeyboardInterrupt at the next host call; kill after 3 s
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "stopped:", stopped.How)
	if stopped.How == montygo.StopFinished && stopped.Err != nil {
		return stopped.Err
	}
	if err := slot.Close(ctx); err != nil {
		return err
	}
	return pool.Shutdown(ctx)
}

// openPool dials a remote Monty server when wsURL is set, else starts local workers.
func openPool(ctx context.Context, wsURL string) (*montygo.Pool, error) {
	if wsURL == "" {
		return montygo.NewPool(ctx, montyenv.PoolOptions())
	}
	return montygo.NewPool(ctx, montygo.PoolOptions{
		Workers:        montygo.Remote(montygo.StaticServer(wsURL, nil, nil), montygo.RemoteOptions{}),
		RequestTimeout: montygo.NoRequestTimeout,
	})
}
