package cron

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Tests for root's system lock parent (/var/run, or /var/db on macOS). The
// darwin layout is pinned through injected stat results, so it is checked on
// every build host, not only on a Mac.

// Owner, group and mode of the macOS directories involved (root:daemon 0775
// /private/var/run, root:wheel 0755 /private/var/db); gid 1 is daemon.
const (
	darwinRootUID   = 0
	darwinWheelGID  = 0
	darwinDaemonGID = 1
)

// TestPrivilegedCrontabLockDirPerPlatform: macOS keeps root's lock in
// /var/db, every other supported target in /var/run.
func TestPrivilegedCrontabLockDirPerPlatform(t *testing.T) {
	want := map[string]string{
		"darwin":  "/var/db/gonf-crontab",
		"linux":   "/var/run/gonf-crontab",
		"freebsd": "/var/run/gonf-crontab",
		"openbsd": "/var/run/gonf-crontab",
		"netbsd":  "/var/run/gonf-crontab",
	}
	for goos, dir := range want {
		if got := privilegedCrontabLockDir(goos); got != dir {
			t.Errorf("privilegedCrontabLockDir(%q) = %q, want %q", goos, got, dir)
		}
	}
}

// TestCheckSystemLockParentDarwin pins why root's lock moved on macOS and
// that the move relaxed nothing: /var/db as shipped is accepted, macOS's
// group-writable /var/run is still refused, and so is a /var/db that is
// world-writable, not root's, or writable by any group (wheel included).
func TestCheckSystemLockParentDarwin(t *testing.T) {
	const dir = unix.S_IFDIR
	root := lockIDs{euid: 0, egid: 0}
	testCases := []struct {
		name  string
		path  string
		attrs lockParentAttrs
		want  string // "" = accepted
	}{
		{"/var/db as shipped", "/var/db", lockParentAttrs{darwinRootUID, darwinWheelGID, dir | 0o755}, ""},
		{"/var/run group daemon 0775", "/var/run", lockParentAttrs{darwinRootUID, darwinDaemonGID, dir | 0o775}, "writable by group 1"},
		{"world-writable", "/var/db", lockParentAttrs{darwinRootUID, darwinWheelGID, dir | 0o777}, "world-writable"},
		{"sticky world-writable", "/var/db", lockParentAttrs{darwinRootUID, darwinWheelGID, dir | 0o1777}, "world-writable"},
		{"non-root owner", "/var/db", lockParentAttrs{501, darwinWheelGID, dir | 0o755}, "owned by uid 501"},
		{"group wheel writable", "/var/db", lockParentAttrs{darwinRootUID, darwinWheelGID, dir | 0o775}, "writable by group 0"},
		{"unexpected group writable", "/var/db", lockParentAttrs{darwinRootUID, 80, dir | 0o775}, "writable by group 80"},
		{"not a directory", "/var/db", lockParentAttrs{darwinRootUID, darwinWheelGID, unix.S_IFREG | 0o755}, "not a directory"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := checkSystemLockParent(testCase.path, testCase.attrs, root)
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("err=%v, want accepted", err)
				}
				return
			}
			assertSystemParentRefusal(t, err, testCase.path, testCase.want)
		})
	}
}

// assertSystemParentRefusal checks a system parent refusal: it names the
// directory and the violated rule, and never advises changing the directory.
func assertSystemParentRefusal(t *testing.T, err error, path, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted, want refusal containing %q", want)
	}
	msg := err.Error()
	if !strings.Contains(msg, path) || !strings.Contains(msg, want) {
		t.Fatalf("err=%v, want %s and %q", err, path, want)
	}
	for _, advice := range []string{"chmod", "chown", "remove"} {
		if strings.Contains(msg, advice) {
			t.Fatalf("system parent refusal advises %q: %v", advice, err)
		}
	}
	if !strings.Contains(msg, "do not change it") {
		t.Fatalf("system parent refusal lacks the do-not-change note: %v", err)
	}
}

// TestCheckLockParentKeepsOwnDirRemedy: the account's own parent (~/.cache)
// still gets the actionable remedy that a system parent never gets.
func TestCheckLockParentKeepsOwnDirRemedy(t *testing.T) {
	user := lockIDs{euid: 1001, egid: 1001}
	err := checkLockParent("/home/u/.cache", lockParentAttrs{1001, 100, unix.S_IFDIR | 0o775}, user)
	if err == nil || !strings.Contains(err.Error(), "chmod g-w it") {
		t.Fatalf("err=%v, want the chmod g-w remedy", err)
	}
}

// TestCrontabLockSystemParentRefusalOnDisk exercises the wiring: a location
// without createParent is verified as a system parent, so a world-writable
// one is refused without chmod advice and nothing is left behind in it.
func TestCrontabLockSystemParentRefusalOnDisk(t *testing.T) {
	parent, dir := newLockParent(t)
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	_, err := lockCrontabIn(lockLocation{dir: dir}, euid(), "target", time.Second)
	assertSystemParentRefusal(t, err, filepath.Clean(parent), "world-writable")
	assertAbsent(t, dir)
}
