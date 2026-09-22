package api

import (
	"os"
	"path/filepath"
	"testing"
)

// countFatalHelperTempDirs returns how many directories directly under the
// real temp root (the ambient $GOTMPDIR, or os.TempDir() when that is unset)
// match "Test*FatalHelperProcess*" - the shape t.TempDir() gives a directory
// created by one of the *FatalHelperProcess helper tests (login_class_test.go,
// systemd_units_test.go, systemd_units_merge_refusal_test.go). Those helpers
// run in a re-exec'd child that ends via logger.Fatal (os.Exit), which skips
// the deferred cleanup t.TempDir() would otherwise register, so each caller
// below points the child's $GOTMPDIR at a directory this (surviving) test
// owns instead: the child's t.TempDir() calls then nest one level deeper,
// under that owned directory, invisible to this single-level glob, and get
// swept up by this test's own t.Cleanup(RemoveAll) instead of by the child.
//
// Regression check for the leak the 8b2 fix closed: every run of the
// misuse-case loops below used to leave ~34 such directories behind (see the
// 8b2 task). Callers snapshot the count before their loop and assert it is
// unchanged after, so a future misuse case that forgets the $GOTMPDIR
// override starts leaking directly into the real temp root again and this
// catches it without depending on internal containment details.
func countFatalHelperTempDirs(t *testing.T) int {
	t.Helper()
	root := os.Getenv("GOTMPDIR")
	if root == "" {
		root = os.TempDir()
	}
	matches, err := filepath.Glob(filepath.Join(root, "Test*FatalHelperProcess*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

// assertNoNewFatalHelperTempDirs fails the test if the real temp root (see
// countFatalHelperTempDirs) gained any Test*FatalHelperProcess* entries since
// before, naming the leak explicitly so a regression here is easy to place.
func assertNoNewFatalHelperTempDirs(t *testing.T, before int) {
	t.Helper()
	if after := countFatalHelperTempDirs(t); after != before {
		t.Fatalf("helper processes leaked %d Test*FatalHelperProcess* temp dir(s) into the real temp root (before %d, after %d)", after-before, before, after)
	}
}
