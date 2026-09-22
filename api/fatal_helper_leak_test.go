package api

import (
	"os"
	"path/filepath"
	"testing"
)

// cleanFatalHelperTempDir is the regression check for the leak the 8b2 fix
// closed: TestLoginClassFatalHelperProcess, TestSystemdUnitsFatalHelperProcess
// and TestSystemdUnitsMergeFatalHelperProcess (login_class_test.go,
// systemd_units_test.go, systemd_units_merge_refusal_test.go) each re-exec
// into a child that calls t.TempDir() and then ends via logger.Fatal
// (os.Exit), which skips the deferred cleanup t.TempDir() would otherwise
// register. t.TempDir() creates its directory under $GOTMPDIR (see
// testing.common.makeTempDir), so each misuse-case loop points every child's
// $GOTMPDIR at one directory (dir) the loop's own top-level test owns via its
// own t.TempDir(); the child's temp dirs then nest one level under dir
// instead of leaking into the real temp root.
//
// Unlike TestRecordPlanFatalRemovesStaging's assertNoStagingLeft (stage_test.go),
// nothing in the production code removes a t.TempDir() directory before the
// child exits, so this test does that removal itself, once per exited child,
// keeping dir from accumulating entries across cases in the same loop - and
// then reuses assertNoStagingLeft to confirm dir is empty again, the same way
// the staging tests confirm their own fatal hook's cleanup. Scoped to dir
// (never the real, shared temp root), this cannot flake under unrelated
// concurrent `go test` processes the way a glob over the real temp root did.
func cleanFatalHelperTempDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	assertNoStagingLeft(t, dir)
}
