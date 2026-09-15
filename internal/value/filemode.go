package value

import (
	"errors"
	"fmt"
)

// CanonicalFileMode validates a Python open() mode string and returns its
// canonical form (r, rb, w, wb, a, ab).
func CanonicalFileMode(mode string) (string, error) {
	var action rune
	binary, text := false, false
	for _, ch := range mode {
		switch ch {
		case 'r', 'w', 'a':
			if action != 0 {
				return "", errors.New("must have exactly one of create/read/write/append mode")
			}
			action = ch
		case 'x':
			return "", errors.New("exclusive creation mode is not supported")
		case 'b':
			if binary {
				return "", errors.New("invalid mode: binary mode specified twice")
			}
			binary = true
		case 't':
			if text {
				return "", errors.New("invalid mode: text mode specified twice")
			}
			text = true
		case '+':
			return "", errors.New("update modes ('+') are not yet supported")
		default:
			return "", fmt.Errorf("invalid mode: %s", RustDebugChar(ch))
		}
	}
	if binary && text {
		return "", errors.New("can't have text and binary mode at once")
	}
	if action == 0 {
		return "", errors.New("Must have exactly one of create/read/write/append mode and at most one plus")
	}
	if binary {
		return string(action) + "b", nil
	}
	return string(action), nil
}

// ModeCreates reports whether the canonical mode creates the file (w, a).
func ModeCreates(mode string) bool {
	return len(mode) > 0 && (mode[0] == 'w' || mode[0] == 'a')
}
