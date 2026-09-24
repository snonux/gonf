package file

import (
	"os/user"
	"path/filepath"
	"testing"

	opt "github.com/snonux/gonf/resource/options"
)

// TestRootOwnerResolvesToUIDAndGIDZero pins the apply-side half of Root for
// both File and the Target a ConfigSet member publishes through: the recorded
// owner "root" and group "0" resolve to uid 0 and gid 0 via the same numeric
// group parse any older destination binary already has, with no group-name
// lookup that could differ between Linux (root) and the BSDs/darwin (wheel).
func TestRootOwnerResolvesToUIDAndGIDZero(t *testing.T) {
	if _, err := user.Lookup("root"); err != nil {
		t.Skipf("no root account in this host's user database: %v", err)
	}
	path := filepath.Join(t.TempDir(), "f")
	f, err := build(path, opt.WithContent("x\n"), opt.Perm(0o640, opt.Root))
	if err != nil {
		t.Fatal(err)
	}
	uid, gid, err := f.ownerIDs()
	if err != nil || uid != 0 || gid != 0 {
		t.Fatalf("File ownerIDs() = %d, %d, %v; want 0, 0, nil", uid, gid, err)
	}
	target, err := NewTarget(path, opt.Perm(0o640, opt.Root))
	if err != nil {
		t.Fatal(err)
	}
	if err := target.ResolveOwnership(); err != nil {
		t.Fatalf("Target.ResolveOwnership() = %v, want nil", err)
	}
}
