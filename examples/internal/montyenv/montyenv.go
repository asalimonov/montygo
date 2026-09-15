// Package montyenv selects the pool backend for the example programs.
package montyenv

import (
	"os"
	"strings"

	"github.com/asalimonov/montygo"
)

// BackendEnv names the variable that forces a backend: auto (default), native or wasm.
const BackendEnv = "MONTY_EXAMPLES_BACKEND"

// PoolOptions returns pool options honouring BackendEnv.
func PoolOptions() montygo.Options {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(BackendEnv))) {
	case "native":
		return montygo.Options{Backend: montygo.BackendNative}
	case "wasm":
		return montygo.Options{Backend: montygo.BackendWasm}
	}
	return montygo.Options{}
}
