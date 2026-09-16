// Package montyenv selects the pool workers for the example programs.
package montyenv

import (
	"os"
	"strings"

	"github.com/asalimonov/montygo"
)

// BackendEnv names the variable that forces a backend: auto (default), native or wasm.
const BackendEnv = "MONTY_EXAMPLES_BACKEND"

// PoolOptions returns pool options honouring BackendEnv.
func PoolOptions() montygo.PoolOptions {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(BackendEnv))) {
	case "native":
		return montygo.PoolOptions{Workers: montygo.Native(montygo.NativeOptions{})}
	case "wasm":
		return montygo.PoolOptions{Workers: montygo.Wasm(montygo.WasmOptions{})}
	}
	return montygo.PoolOptions{Workers: montygo.Auto()}
}
