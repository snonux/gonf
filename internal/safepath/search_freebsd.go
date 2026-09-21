package safepath

import "golang.org/x/sys/unix"

// searchFlag opens a directory for traversal only: O_SEARCH needs search
// (execute) permission on the directory, not read permission, just as
// lstat(2) needs on the directories of a path. Its descriptor supports fstat
// and the *at calls relative to it.
const searchFlag = unix.O_SEARCH

// SearchOnly reports whether walked directories need only search permission
// on this platform (true), or read permission as well (false).
const SearchOnly = true
