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
// group is the caller's user-private group (gid == egid == euid, not 0),
// because such a group has nobody else in it.
//
// TestCheckDirAttrsRule runs the rule against SYNTHETIC process identities
// (procIDs) and directory attributes, so every branch is exercised on every
// account, root and non-private-group users included, with no skips. The other
// tests here are integration tests on real directories; they never change the
// process's gid, so each environment-dependent case is its own subtest that
// skips, with a reason, when the runner is not a user-private-group user or has
// no supplementary group to chgrp to.

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
		t.Run(mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "shared-read")
			sharedGroupDir(t, dir, mode) // skips this subtest when no foreign group is usable
			if err := SecureDir(dir); err != nil {
				t.Fatalf("SecureDir(%s, mode %v, foreign group) = %v, want success", dir, mode, err)
			}
			if got := modeOf(t, dir); got != mode {
				t.Fatalf("mode after SecureDir = %v, want %v unchanged", got, mode)
			}
		})
	}
}

// idCase is one row of TestCheckDirAttrsRule: the process identity, the
// directory attributes and the verdict; want "" means accepted, else a
// substring of the refusal.
type idCase struct {
	name string
	me   procIDs
	a    dirAttrs
	want string
}

// dirOf is a directory owned by uid, group gid, with mode.
func dirOf(uid, gid, mode uint32) dirAttrs {
	return dirAttrs{isDir: true, uid: uid, gid: gid, mode: mode}
}

// Process identities of the rule's rows: a Fedora/Ubuntu-style user with a
// private group, a user whose primary group is a shared "users" group, and root.
var (
	upg           = procIDs{euid: 1000, egid: 1000}
	sharedPrimary = procIDs{euid: 1000, egid: 100}
	rootIDs       = procIDs{euid: 0, egid: 0}
)

// caseAdder collects the rows of TestCheckDirAttrsRule.
type caseAdder struct{ cases []idCase }

func (c *caseAdder) add(name string, me procIDs, a dirAttrs, want string) {
	c.cases = append(c.cases, idCase{name, me, a, want})
}

// groupWriteRows: the private group is accepted only when gid == egid == euid,
// so a different gid or a shared primary group is refused (each row is chosen
// so that dropping one condition of the rule flips it).
func groupWriteRows(c *caseAdder) {
	const notPrivate = "not your private group"
	c.add("UPG user, 0775 in the private group", upg, dirOf(1000, 1000, 0o775), "")
	c.add("UPG user, 2775 (setgid) in the private group", upg, dirOf(1000, 1000, 0o2775), "")
	c.add("UPG user, 0720 in the private group", upg, dirOf(1000, 1000, 0o720), "")
	c.add("UPG user, 0755 accepted", upg, dirOf(1000, 1000, 0o755), "")
	// A different gid (someone else's group, or a supplementary one) drops "gid == egid".
	c.add("UPG user, 0775 in another group", upg, dirOf(1000, 1001, 0o775), notPrivate)
	c.add("UPG user, 2775 in another group", upg, dirOf(1000, 1001, 0o2775), notPrivate)
	c.add("UPG user, 0755 in another group accepted (no group write)", upg, dirOf(1000, 1001, 0o755), "")
	// egid != euid: the primary group is shared, so a group-writable dir of it
	// is refused even though gid == egid (drops "egid == euid") ...
	c.add("shared primary group, 0775 in the primary group", sharedPrimary, dirOf(1000, 100, 0o775), notPrivate)
	c.add("shared primary group, 0770 in the primary group", sharedPrimary, dirOf(1000, 100, 0o770), notPrivate)
	// ... and the group numbered like the user (gid == euid, not the egid) is not this process's private group either.
	c.add("shared primary group, 0775 in the group numbered like the user", sharedPrimary, dirOf(1000, 1000, 0o775), notPrivate)
	c.add("shared primary group, 0755 accepted", sharedPrimary, dirOf(1000, 100, 0o755), "")
}

// worldWritableRows: world-writable is refused for everybody, sticky or not,
// private group or not.
func worldWritableRows(c *caseAdder) {
	c.add("UPG user, 0777", upg, dirOf(1000, 1000, 0o777), "world-writable")
	c.add("UPG user, 0757", upg, dirOf(1000, 1000, 0o757), "world-writable")
	c.add("UPG user, 1777 (sticky, like /tmp)", upg, dirOf(1000, 1000, 0o1777), "world-writable")
	c.add("UPG user, 0702", upg, dirOf(1000, 1000, 0o702), "world-writable")
	c.add("shared primary group, 0777", sharedPrimary, dirOf(1000, 100, 0o777), "world-writable")
	c.add("root, 0777 root-owned", rootIDs, dirOf(0, 0, 0o777), "world-writable")
}

// ownerRows: root is not exempt from the owner rule, which is judged before the
// mode; another user's directory lets that user swap plan.jsonl.
func ownerRows(c *caseAdder) {
	c.add("root, directory owned by another uid", rootIDs, dirOf(1000, 0, 0o755), "is owned by uid 1000")
	c.add("root, directory owned by another uid, 0700", rootIDs, dirOf(1000, 1000, 0o700), "is owned by uid 1000")
	c.add("UPG user, directory owned by another uid", upg, dirOf(1001, 1000, 0o755), "is owned by uid 1001")
	c.add("UPG user, directory owned by root", upg, dirOf(0, 0, 0o755), "is owned by uid 0")
	c.add("owner rule comes before the mode", upg, dirOf(1001, 1000, 0o777), "is owned by uid 1001")
	c.add("root, own directory 0755 accepted", rootIDs, dirOf(0, 0, 0o755), "")
	c.add("root, own directory 0700 accepted", rootIDs, dirOf(0, 0, 0o700), "")
	c.add("root, own directory in another group 0750 accepted", rootIDs, dirOf(0, 5, 0o750), "")
}

// rootGroupAndKindRows: gid 0 is never a private group (on the BSDs it is
// "wheel"), so a root run refuses group write even where gid == egid == euid
// == 0 (drops "gid != 0"); and a non-directory (a file, a symlink as Lstat
// reports it) is refused.
func rootGroupAndKindRows(c *caseAdder) {
	const notPrivate = "not your private group"
	c.add("root, 0775 root:root", rootIDs, dirOf(0, 0, 0o775), notPrivate)
	c.add("root, 0770 root:root", rootIDs, dirOf(0, 0, 0o770), notPrivate)
	c.add("root, 2775 root:root", rootIDs, dirOf(0, 0, 0o2775), notPrivate)
	c.add("root, 0775 in another group", rootIDs, dirOf(0, 5, 0o775), notPrivate)
	c.add("non-root user whose egid is 0, 0775 root group", procIDs{euid: 1000, egid: 0}, dirOf(1000, 0, 0o775), notPrivate)
	c.add("not a directory", upg, dirAttrs{uid: 1000, gid: 1000, mode: 0o700}, "is not a directory")
	c.add("not a directory, root", rootIDs, dirAttrs{uid: 0, gid: 0, mode: 0o700}, "is not a directory")
}

// TestCheckDirAttrsRule pins the acceptance rule on synthetic identities and
// attributes, independent of who runs the test: the owner rule (root is not
// exempt), o+w always refused, and g+w accepted only for the caller's private
// group, which needs BOTH halves of the convention, gid == egid and
// egid == euid, and is never gid 0. The rows live in the *Rows builders above.
func TestCheckDirAttrsRule(t *testing.T) {
	var rows caseAdder
	groupWriteRows(&rows)
	worldWritableRows(&rows)
	ownerRows(&rows)
	rootGroupAndKindRows(&rows)
	for _, tc := range rows.cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkDirAttrs("/some/dir", tc.a, tc.me)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("checkDirAttrs(%+v, %+v) = %v, want accepted", tc.a, tc.me, err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("checkDirAttrs(%+v, %+v) = %v, want a refusal containing %q", tc.a, tc.me, err, tc.want)
			}
		})
	}
}

// TestIsPrivateGroup pins the convention itself, row by row, so a change of it
// shows up here and not only through the wording of a refusal.
func TestIsPrivateGroup(t *testing.T) {
	for _, tc := range []struct {
		name string
		me   procIDs
		gid  uint32
		want bool
	}{
		{"gid == egid == euid", procIDs{1000, 1000}, 1000, true},
		{"gid != egid", procIDs{1000, 1000}, 1001, false},
		{"egid != euid, gid == egid", procIDs{1000, 100}, 100, false},
		{"egid != euid, gid == euid", procIDs{1000, 100}, 1000, false},
		{"root: gid 0 is wheel on the BSDs, never private", procIDs{0, 0}, 0, false},
		{"root, another gid", procIDs{0, 0}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.me.isPrivateGroup(tc.gid); got != tc.want {
				t.Fatalf("%+v.isPrivateGroup(%d) = %v, want %v", tc.me, tc.gid, got, tc.want)
			}
		})
	}
}

// TestCurrentIDsIsTheProcess: production callers judge directories for the
// running process, so the synthetic tests above are about the same rule.
func TestCurrentIDsIsTheProcess(t *testing.T) {
	if got, want := currentIDs(), (procIDs{uint32(os.Geteuid()), uint32(os.Getegid())}); got != want {
		t.Fatalf("currentIDs() = %+v, want %+v", got, want)
	}
}
