package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
)

// The tests in this file pin the group-write rule of checkDirAttrs: a
// directory the caller owns that is group-writable is accepted only when that
// group is the caller's user-private group (gid == egid == euid), because such
// a group has nobody else in it. They never change the process's gid: the
// accepting cases skip unless the runner is a user-private-group user, and the
// refusing ones chgrp a directory to a supplementary group of the runner (and
// skip when it has none).

// sharedGroupDir creates dir with mode and a group other than the caller's
// effective gid (the mode is applied after the chgrp so nothing can clear a
// setgid bit), and returns that gid.
func sharedGroupDir(t *testing.T, dir string, mode os.FileMode) int {
	t.Helper()
	mkdirMode(t, dir, 0o700)
	gid := testutil.ChgrpForeign(t, dir)
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	return gid
}

// TestSecureDirAcceptsPrivateGroupWritableDirs is the UPG regression: on
// Fedora, Ubuntu, RHEL or Rocky a fresh checkout is 0775 with the user's own
// group, and the default `gonf plan -o .` must keep working there. The
// directory is accepted and left exactly as it was, and CheckExistingDir (the
// FileInfo-based pre-check) agrees.
func TestSecureDirAcceptsPrivateGroupWritableDirs(t *testing.T) {
	testutil.RequirePrivateGroupUser(t)
	for _, mode := range []os.FileMode{0o770, 0o775, 0o720, os.ModeSetgid | 0o775} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "checkout")
			mkdirMode(t, dir, mode)
			want := modeOf(t, dir)
			info, err := os.Lstat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckExistingDir(dir, info); err != nil {
				t.Fatalf("CheckExistingDir(%s, mode %v) = %v, want success", dir, mode, err)
			}
			if err := SecureDir(dir); err != nil {
				t.Fatalf("SecureDir(%s, mode %v) = %v, want success", dir, mode, err)
			}
			if got := modeOf(t, dir); got != want {
				t.Fatalf("mode after SecureDir = %v, want it unchanged (%v)", got, want)
			}
			requireEntries(t, dir)
		})
	}
}

// TestSecureDirAcceptsUmask002Checkout builds the directory the way a UPG
// system does, with mkdir under umask 002 (no chmod), and pins that the plan
// files can then be written into it while its 0775 stays. It changes the
// process umask, so it must not run in parallel.
func TestSecureDirAcceptsUmask002Checkout(t *testing.T) {
	testutil.RequirePrivateGroupUser(t)
	dir := filepath.Join(t.TempDir(), "checkout")
	testutil.WithUmask(0o002, func() {
		if err := os.Mkdir(dir, 0o777); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.Chown(dir, -1, os.Getegid()); err != nil {
		t.Fatal(err) // a setgid parent must not hand the directory another group
	}
	if got := modeOf(t, dir); got != 0o775 {
		t.Fatalf("mkdir under umask 002 made mode %v, want 0775", got)
	}
	if err := WritePrivateFile(dir, "plan.jsonl", []byte("plan")); err != nil {
		t.Fatalf("WritePrivateFile into a umask-002 checkout = %v, want success", err)
	}
	assertOwnerOnlyFile(t, filepath.Join(dir, "plan.jsonl"))
	if got := modeOf(t, dir); got != 0o775 {
		t.Fatalf("checkout mode after the write = %v, want 0775 unchanged", got)
	}
}

// TestSecureDirRefusesSharedGroupWritableDirs: group write for any group other
// than the caller's private group (a shared 2775 project directory, a directory
// whose group merely is one of the caller's supplementary groups) lets other
// users replace plan.jsonl, so it stays refused, with a message that names the
// group and says what to do. Nothing is written or changed.
func TestSecureDirRefusesSharedGroupWritableDirs(t *testing.T) {
	for _, mode := range []os.FileMode{0o770, 0o775, 0o720, os.ModeSetgid | 0o775} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "project")
			gid := sharedGroupDir(t, dir, mode)
			want := modeOf(t, dir)
			info, err := os.Lstat(dir)
			if err != nil {
				t.Fatal(err)
			}
			for name, err := range map[string]error{
				"SecureDir":        SecureDir(dir),
				"CheckExistingDir": CheckExistingDir(dir, info),
				"WritePrivateFile": WritePrivateFile(dir, "plan.jsonl", []byte("secret")),
			} {
				for _, part := range []string{dir, fmt.Sprintf("group-writable by group %d", gid),
					"not your private group", "chmod go-w", "-o <private dir>"} {
					if err == nil || !strings.Contains(err.Error(), part) {
						t.Fatalf("%s = %v, want a refusal containing %q", name, err, part)
					}
				}
			}
			if got := modeOf(t, dir); got != want {
				t.Fatalf("mode after refusal = %v, want it untouched (%v)", got, want)
			}
			requireEntries(t, dir)
		})
	}
}

// TestSecureDirAcceptsForeignGroupWithoutGroupWrite is the negative twin: a
// foreign group alone is harmless while it cannot write, so a directory of ours
// in a shared group but 0755 or 0750 is accepted and left as it is.
func TestSecureDirAcceptsForeignGroupWithoutGroupWrite(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o750, 0o700} {
		dir := filepath.Join(t.TempDir(), "shared-read")
		sharedGroupDir(t, dir, mode)
		if err := SecureDir(dir); err != nil {
			t.Fatalf("SecureDir(%s, mode %v, foreign group) = %v, want success", dir, mode, err)
		}
		if got := modeOf(t, dir); got != mode {
			t.Fatalf("mode after SecureDir = %v, want %v unchanged", got, mode)
		}
	}
}

// attrCase is one row of TestCheckDirAttrsGroupRule; want "" means accepted,
// else a substring of the refusal.
type attrCase struct {
	name string
	a    dirAttrs
	want string
}

// TestCheckDirAttrsGroupRule pins the rule on synthetic attributes, so the
// cases a test runner cannot create (a gid that is not the caller's, whoever
// the caller is) are covered too: o+w is always refused, g+w only passes for
// the caller's private group, and the owner rule comes first.
func TestCheckDirAttrsGroupRule(t *testing.T) {
	euid, egid := uint32(os.Geteuid()), uint32(os.Getegid())
	otherGid := egid + 1
	base := dirAttrs{isDir: true, uid: euid, gid: egid}
	// The caller's own primary group is private only when egid == euid.
	primary := attrCase{"0775 in the private group accepted", withMode(base, 0o775), ""}
	if egid != euid {
		primary = attrCase{"0775 in the primary group of a non-private-group process refused", withMode(base, 0o775), "not your private group"}
	}
	cases := []attrCase{
		primary,
		{"0755 accepted", withMode(base, 0o755), ""},
		{"0777 refused", withMode(base, 0o777), "world-writable"},
		{"1777 refused", withMode(base, 0o1777), "world-writable"},
		{"0757 in a foreign group refused", withGID(withMode(base, 0o757), otherGid), "world-writable"},
		{"0775 in a foreign group refused", withGID(withMode(base, 0o775), otherGid), "not your private group"},
		{"2775 in a foreign group refused", withGID(withMode(base, 0o2775), otherGid), "not your private group"},
		{"0755 in a foreign group accepted", withGID(withMode(base, 0o755), otherGid), ""},
		{"foreign owner refused before the mode", withUID(withMode(base, 0o775), euid+1), "is owned by uid"},
		{"not a directory refused", dirAttrs{uid: euid, gid: egid, mode: 0o700}, "is not a directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkDirAttrs("/some/dir", tc.a)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("checkDirAttrs = %v, want accepted", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("checkDirAttrs = %v, want a refusal containing %q", err, tc.want)
			}
		})
	}
}

func withMode(a dirAttrs, mode uint32) dirAttrs { a.mode = mode; return a }
func withGID(a dirAttrs, gid uint32) dirAttrs   { a.gid = gid; return a }
func withUID(a dirAttrs, uid uint32) dirAttrs   { a.uid = uid; return a }
