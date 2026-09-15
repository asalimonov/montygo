package mountfs

import "fmt"

// Mode is the access policy of a mount.
type Mode uint8

const (
	ReadOnly Mode = iota
	ReadWrite
	Overlay
)

// ParseMode parses "read-only", "read-write" or "overlay".
func ParseMode(s string) (Mode, error) {
	switch s {
	case "read-only":
		return ReadOnly, nil
	case "read-write":
		return ReadWrite, nil
	case "overlay":
		return Overlay, nil
	}
	return 0, fmt.Errorf("Invalid mode '%s', expected 'read-only', 'read-write', or 'overlay'", s)
}

func (m Mode) String() string {
	switch m {
	case ReadOnly:
		return "read-only"
	case ReadWrite:
		return "read-write"
	case Overlay:
		return "overlay"
	}
	return fmt.Sprintf("Mode(%d)", uint8(m))
}
