package mountfs

import "syscall"

// unlinkat(2) on a directory fails with EISDIR on Linux.
var unlinkDirErrno = syscall.EISDIR
