package network

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv(EnvNetworkTests) != "1" {
		fmt.Println("skipping network tests (set " + EnvNetworkTests + "=1 to run)")
		os.Exit(0)
	}
	pool := GetPool()
	fmt.Printf("starting container pool (size=%d, image=%s)\n", pool.Size(), pool.Image())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	err := pool.Start(ctx)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start pool: %v\nbuild the image with `make docker-build` or set %s\n", err, EnvTestImage)
		os.Exit(1)
	}
	code := m.Run()
	_ = pool.Stop(context.Background())
	replCleanup()
	os.Exit(code)
}
