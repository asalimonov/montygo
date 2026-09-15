//go:build !unix

package mountfs

import "syscall"

const (
	oNonblock  = 0
	oDirectory = 0
)

var unlinkDirErrno = syscall.EISDIR
