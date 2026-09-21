package cli

import (
	"os"
	"reflect"
	"testing"

	"github.com/snonux/gonf/internal/remote"
)

// TestCLICleansUpRemoteBuilds: CLI must remove the private gonf cross-build
// dir when it returns, on success and on a flag error alike. The seam must
// default to the real remote.CleanupBuilds (whose own behaviour is covered
// by internal/remote's TestCleanupBuildsRemovesDefaultPusherDir), and CLI
// must call it exactly once per run.
func TestCLICleansUpRemoteBuilds(t *testing.T) {
	if reflect.ValueOf(cleanupRemoteBuilds).Pointer() != reflect.ValueOf(remote.CleanupBuilds).Pointer() {
		t.Fatal("cleanupRemoteBuilds is not remote.CleanupBuilds")
	}
	old, oldArgs := cleanupRemoteBuilds, os.Args
	t.Cleanup(func() { cleanupRemoteBuilds, os.Args = old, oldArgs })
	var calls int
	cleanupRemoteBuilds = func() error { calls++; return nil }

	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"gonf", "-version"}, 0},
		{[]string{"gonf", "-no-such-flag"}, 2},
	} {
		calls = 0
		os.Args = tc.args
		if code := CLI(); code != tc.code {
			t.Fatalf("%v: exit %d, want %d", tc.args, code, tc.code)
		}
		if calls != 1 {
			t.Fatalf("%v: cleanup called %d times, want 1", tc.args, calls)
		}
	}
}
