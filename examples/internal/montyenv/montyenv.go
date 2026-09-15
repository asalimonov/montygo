// Package montyenv selects the pool backend for the example programs.
package montyenv

import (
	"os"
	"strings"

	monty "github.com/asalimonov/montygo"
)

// BackendEnv names the variable that forces a backend: auto (default), native or wasm.
const BackendEnv = "MONTY_EXAMPLES_BACKEND"

// PoolOptions returns pool options honouring BackendEnv.
func PoolOptions() monty.Options {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(BackendEnv))) {
	case "native":
		return monty.Options{Backend: monty.BackendNative}
	case "wasm":
		return monty.Options{Backend: monty.BackendWasm}
	}
	return monty.Options{}
}
