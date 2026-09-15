package monty

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func montyBinaryName() string {
	if runtime.GOOS == "windows" {
		return "monty.exe"
	}
	return "monty"
}

// FindMontyBinary resolves the native worker: explicit path, MONTY_BIN, PATH,
// then a cargo target directory in an ancestor directory or a sibling monty checkout.
func FindMontyBinary(explicit string) (string, error) {
	if explicit != "" {
		if isExecutable(explicit) {
			return explicit, nil
		}
		return "", &OptionError{Message: "monty binary not found at binaryPath: " + explicit}
	}
	var tried []string
	if env := os.Getenv("MONTY_BIN"); env != "" {
		if isExecutable(env) {
			return env, nil
		}
		tried = append(tried, "MONTY_BIN="+env)
	} else {
		tried = append(tried, "MONTY_BIN")
	}
	if p, err := exec.LookPath(montyBinaryName()); err == nil {
		return p, nil
	}
	tried = append(tried, "PATH")
	if p := findCargoBuild(); p != "" {
		return p, nil
	}
	tried = append(tried, "cargo target directory")
	return "", &OptionError{Message: fmt.Sprintf("could not locate the monty binary (tried: %s). Install pydantic-monty-runtime, set MONTY_BIN, or pass BinaryPath.", strings.Join(tried, ", "))}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func findCargoBuild() string {
	starts := []string{}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		starts = append(starts, filepath.Dir(file))
	}
	var best string
	var bestTime int64
	for _, start := range starts {
		dir := start
		for i := 0; i < 7; i++ {
			for _, root := range []string{dir, filepath.Join(dir, "monty")} {
				for _, profile := range []string{"debug", "release"} {
					candidate := filepath.Join(root, "target", profile, montyBinaryName())
					if info, err := os.Stat(candidate); err == nil && isExecutable(candidate) && info.ModTime().UnixNano() > bestTime {
						best, bestTime = candidate, info.ModTime().UnixNano()
					}
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		if best != "" {
			return best
		}
	}
	return best
}
