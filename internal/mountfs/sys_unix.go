//go:build unix

package mountfs

import "syscall"

const (
	oNonblock  = syscall.O_NONBLOCK
	oDirectory = syscall.O_DIRECTORY
)
