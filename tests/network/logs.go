package network

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/testcontainers/testcontainers-go"
)

const logDir = "output/containers"

// fileLogConsumer streams one container's log to a file that is renamed per test.
type fileLogConsumer struct {
	mu   sync.Mutex
	file *os.File
	path string
}

func newFileLogConsumer(unitID int) *fileLogConsumer {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil
	}
	path := filepath.Join(logDir, fmt.Sprintf("unit-%d.log", unitID))
	f, err := os.Create(path)
	if err != nil {
		return nil
	}
	return &fileLogConsumer{file: f, path: path}
}

func (c *fileLogConsumer) Accept(l testcontainers.Log) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file != nil {
		_, _ = c.file.Write(l.Content)
	}
}

// Rename moves the log file to "<test>-u<unit>.log"; the open descriptor keeps streaming.
func (c *fileLogConsumer) Rename(testName string, unitID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file == nil {
		return
	}
	safe := strings.NewReplacer("/", "_", " ", "_").Replace(testName)
	target := filepath.Join(logDir, fmt.Sprintf("%s-u%d.log", safe, unitID))
	if err := os.Rename(c.path, target); err == nil {
		c.path = target
	}
}

func (c *fileLogConsumer) Path() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path
}

func (c *fileLogConsumer) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file != nil {
		_ = c.file.Close()
		c.file = nil
	}
}
