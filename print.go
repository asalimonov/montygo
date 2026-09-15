package monty

import (
	"context"
	"fmt"
	"sync"
)

// Stream names an output stream.
type Stream string

const (
	Stdout Stream = "stdout"
	Stderr Stream = "stderr"
)

// PrintTarget receives sandbox output in batched chunks. A returned error fails the feed.
type PrintTarget interface {
	Print(stream Stream, text string) error
}

// ContextPrintTarget is a PrintTarget that also receives the callback context,
// which carries the caller's values and the Monty telemetry span.
type ContextPrintTarget interface {
	PrintTarget
	PrintContext(ctx context.Context, stream Stream, text string) error
}

// PrintFunc adapts a function to PrintTarget.
type PrintFunc func(stream Stream, text string) error

// Print implements PrintTarget.
func (f PrintFunc) Print(stream Stream, text string) error { return f(stream, text) }

const (
	// DefaultMaxPrintCollectBytes caps the collectors by default (10 MiB).
	DefaultMaxPrintCollectBytes int64 = 10 << 20
	// UnlimitedPrintCollect disables a collector's cap.
	UnlimitedPrintCollect       int64 = -1
	collectStreamsEntryOverhead int64 = 64
)

func collectLimit(maxBytes int64) (int64, error) {
	if maxBytes < UnlimitedPrintCollect {
		return 0, &OptionError{Message: "maxBytes must be a finite non-negative number or null"}
	}
	return maxBytes, nil
}

func collectOverflow(used, limit int64) error {
	return &RuntimeError{TypeName: "MemoryError", Message: fmt.Sprintf("memory limit exceeded: %d bytes > %d bytes", used, limit)}
}

// CollectString accumulates all output into one string.
type CollectString struct {
	mu       sync.Mutex
	buf      []byte
	maxBytes int64
	used     int64
	custom   bool
}

// NewCollectString builds a collector capped at maxBytes (UnlimitedPrintCollect disables the cap).
func NewCollectString(maxBytes int64) (*CollectString, error) {
	limit, err := collectLimit(maxBytes)
	if err != nil {
		return nil, err
	}
	return &CollectString{maxBytes: limit, custom: true}, nil
}

func (c *CollectString) limit() int64 {
	if !c.custom {
		return DefaultMaxPrintCollectBytes
	}
	return c.maxBytes
}

// Print implements PrintTarget.
func (c *CollectString) Print(_ Stream, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	add := int64(len(text))
	if limit := c.limit(); limit >= 0 && c.used+add > limit {
		return collectOverflow(c.used+add, limit)
	}
	c.used += add
	c.buf = append(c.buf, text...)
	return nil
}

// Output returns everything collected.
func (c *CollectString) Output() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

// CollectedStreamEntry is one chunk of output with its stream.
type CollectedStreamEntry struct {
	Stream Stream
	Text   string
}

// CollectStreams accumulates chunks with their stream labels.
type CollectStreams struct {
	mu       sync.Mutex
	entries  []CollectedStreamEntry
	maxBytes int64
	used     int64
	custom   bool
}

// NewCollectStreams builds a collector capped at maxBytes (UnlimitedPrintCollect disables the cap).
func NewCollectStreams(maxBytes int64) (*CollectStreams, error) {
	limit, err := collectLimit(maxBytes)
	if err != nil {
		return nil, err
	}
	return &CollectStreams{maxBytes: limit, custom: true}, nil
}

// Print implements PrintTarget.
func (c *CollectStreams) Print(stream Stream, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	limit := c.maxBytes
	if !c.custom {
		limit = DefaultMaxPrintCollectBytes
	}
	add := int64(len(text)) + collectStreamsEntryOverhead
	if limit >= 0 && c.used+add > limit {
		return collectOverflow(c.used+add, limit)
	}
	c.used += add
	c.entries = append(c.entries, CollectedStreamEntry{Stream: stream, Text: text})
	return nil
}

// Output returns a copy of the collected entries.
func (c *CollectStreams) Output() []CollectedStreamEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]CollectedStreamEntry(nil), c.entries...)
}
