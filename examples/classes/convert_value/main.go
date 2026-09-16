// Command convert_value wraps derived host objects with their own policy as
// they cross into the sandbox.
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

type Wallet struct {
	Balance int
}

func (w *Wallet) Pay(amount int) *Wallet {
	return &Wallet{Balance: w.Balance - amount}
}

func walletWrapper(w *Wallet) (*host.ClassInstance, error) {
	return host.NewClassInstance(w, host.ClassInstanceOptions{
		EagerAttrs:     host.All(),
		AllowedMethods: host.Names("pay"),
		ConvertValue:   convertValue,
	})
}

func convertValue(_ string, value any) (any, error) {
	if w, ok := value.(*Wallet); ok {
		return walletWrapper(w)
	}
	return value, nil
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
		w, err := walletWrapper(&Wallet{Balance: 100})
		if err != nil {
			return nil, err
		}
		return session.FeedRun(ctx, "w.pay(30).pay(20).balance", &montygo.FeedOptions{Inputs: map[string]any{"w": w}})
	}()
	if err != nil {
		return err
	}

	if !sandbox.Equal(result, 50) {
		return fmt.Errorf("assertion failed: result == %s", sandbox.Repr(result))
	}
	fmt.Fprintf(out, "balance after two payments: %s\n", sandbox.Repr(result))
	return nil
}
