package dir

import (
	"os/user"
	"path/filepath"
	"testing"

	opt "github.com/snonux/gonf/resource/options"
)

// TestRootOwnerResolvesToUIDAndGIDZero pins the apply-side half of Root for
// Dir (and SyncDir, whose directories and files resolve through the same dir
// and file helpers): the recorded owner "root" and group "0" resolve to uid 0
// and gid 0 through the numeric group parse, not a name lookup that would
// differ between Linux (root) and the BSDs/darwin (wheel).
func TestRootOwnerResolvesToUIDAndGIDZero(t *testing.T) {
	if _, err := user.Lookup("root"); err != nil {
		t.Skipf("no root account in this host's user database: %v", err)
	}
	d, err := build(filepath.Join(t.TempDir(), "d"), opt.Perm(0o750, opt.Root))
	if err != nil {
		t.Fatal(err)
	}
	if d.user != "root" || d.group != "0" {
		t.Fatalf("Dir owner = %q:%q, want root:0", d.user, d.group)
	}
	uid, gid, err := ownerIDs(d.user, d.group)
	if err != nil || uid != 0 || gid != 0 {
		t.Fatalf("ownerIDs(root, 0) = %d, %d, %v; want 0, 0, nil", uid, gid, err)
	}
}
