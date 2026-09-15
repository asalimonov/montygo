//go:build unix && !linux

package mountfs

import "syscall"

// unlinkat(2) on a directory fails with EPERM on Darwin and the BSDs.
var unlinkDirErrno = syscall.EPERM
