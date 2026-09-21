//go:build !linux && !freebsd

package safepath

import "golang.org/x/sys/unix"

// searchFlag is O_RDONLY where golang.org/x/sys exports no search-only open
// (macOS, NetBSD, OpenBSD; OpenBSD has none at all, and the values macOS and
// NetBSD define for O_SEARCH are not exported, so they are not guessed here).
// On these platforms every directory the walk opens must be READABLE by the
// caller, not merely searchable: an ancestor such as another user's 0711
// directory fails with EACCES, where an lstat(2) walk would have passed. Root
// is not affected (it may read every directory).
const searchFlag = unix.O_RDONLY

// SearchOnly reports whether walked directories need only search permission
// on this platform (true), or read permission as well (false).
const SearchOnly = false
