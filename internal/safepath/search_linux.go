package safepath

import "golang.org/x/sys/unix"

// searchFlag opens a directory for traversal only. O_PATH needs no read
// permission on the directory itself (only search permission on the directory
// it is opened in, exactly as lstat(2) needs), and its descriptor supports
// everything the walk and its callers do with one: fstat, and openat,
// mkdirat, fstatat, renameat and unlinkat relative to it. It does not support
// fchmod, so a directory the walk creates is reopened with readDirFlags for
// that (see OpenOrCreateDirAt). Together with O_DIRECTORY, O_PATH|O_NOFOLLOW
// still fails on a symlink (ENOTDIR) instead of opening the link itself.
const searchFlag = unix.O_PATH

// SearchOnly reports whether walked directories need only search permission
// on this platform (true), or read permission as well (false).
const SearchOnly = true
