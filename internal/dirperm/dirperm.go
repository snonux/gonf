// Package dirperm holds the permission conventions gonf applies when it
// decides whether a directory is safe to keep its own files in, so such
// checks accept and refuse the same things. Every such check uses it: plan
// output directories (plan/private.go), the file validator's candidate parent
// and its ancestors (resource/file/validation.go), the crontab lock parent
// (resource/cron/lock_setup.go) and the remote cross-build dir
// (internal/remote/crossbuild.go). A new check of this kind should call
// IDs.IsPrivateGroup rather than restate the rule.
package dirperm

import "os"

// IDs are a process's effective user and group ids.
type IDs struct {
	EUID, EGID uint32
}

// Current returns the calling process's effective ids.
func Current() IDs {
	return IDs{EUID: uint32(os.Geteuid()), EGID: uint32(os.Getegid())}
}

// IsPrivateGroup reports whether gid is the caller's user-private group by
// the standard convention: it is the caller's effective gid, that gid equals
// the effective uid, and it is not 0.
//
// Fedora, Ubuntu, RHEL, Rocky and most other Linux distributions give every
// user a private group (gid == uid, that user its only member) and set umask
// 002, so a fresh mkdir or `git clone` is 0775. Group write on such a
// directory lets nobody but the caller write, so it is safe to accept. Any
// other group (a supplementary group, a primary group shared by many users
// such as a classic "users" group, or a process whose egid differs from its
// euid) may have other members. Gid 0 is never private: on FreeBSD, macOS
// and the other BSDs gid 0 is "wheel", whose administrator members could
// write, so root never gets the exception. The convention is not verified
// against the group database: an administrator who added members to a
// private group defeats it, which is their choice to make.
func (ids IDs) IsPrivateGroup(gid uint32) bool {
	return gid == ids.EGID && ids.EGID == ids.EUID && ids.EGID != 0
}
