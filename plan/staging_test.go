package plan

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// isolateStagingRoot points $TMPDIR at a scratch directory so the tests never
// touch the real staging root. The calling test must not use t.Parallel.
func isolateStagingRoot(t *testing.T) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
}

// writeBlob puts a blob file into a run directory, simulating an in-flight
// apply that already extracted its blobs.
func writeBlob(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "blob")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestNewApplyRunDirIsolatedFromConcurrentRuns is the regression test for the
// old eager sweep: creating a second run directory used to RemoveAll every
// entry under the staging root, destroying any in-flight apply's staging dir
// (same user, same host).
func TestNewApplyRunDirIsolatedFromConcurrentRuns(t *testing.T) {
	isolateStagingRoot(t)

	dirA, cleanupA, err := NewApplyRunDir()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupA()
	blob := writeBlob(t, dirA)

	dirB, cleanupB, err := NewApplyRunDir()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupB()

	if dirB == dirA {
		t.Fatalf("run dirs must be unique, both %s", dirA)
	}
	if _, err := os.Stat(dirA); err != nil {
		t.Fatalf("second NewApplyRunDir destroyed the in-flight run dir: %v", err)
	}
	data, err := os.ReadFile(blob)
	if err != nil {
		t.Fatalf("in-flight blob vanished mid-apply: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("blob content changed: %q", data)
	}

	cleanupB()
	if _, err := os.Stat(dirB); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove its own run dir: %v", err)
	}
	if _, err := os.Stat(dirA); err != nil {
		t.Fatalf("cleanup must not touch the other run dir: %v", err)
	}

	info, err := os.Stat(dirA)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("run dir mode %#o", info.Mode().Perm())
	}
}

// TestNewApplyRunDirSweepsStaleRuns checks the lazy wiring: creating a new run
// directory sweeps run directories older than the TTL.
func TestNewApplyRunDirSweepsStaleRuns(t *testing.T) {
	isolateStagingRoot(t)

	stale, cleanup, err := NewApplyRunDir()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	writeBlob(t, stale)
	old := time.Now().Add(-2 * applyRunTTL)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	fresh, cleanupFresh, err := NewApplyRunDir()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupFresh()

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale run dir should have been swept: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("new run dir should exist: %v", err)
	}
}

// TestSweepApplyStaging checks the sweep's TTL semantics directly: only run
// directories older than applyRunTTL are removed; fresh runs, foreign names,
// and non-directories are left alone.
func TestSweepApplyStaging(t *testing.T) {
	isolateStagingRoot(t)
	root, err := ApplyStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * applyRunTTL)
	freshDir := filepath.Join(root, "run-fresh")
	staleDir := filepath.Join(root, "run-stale")
	foreignDir := filepath.Join(root, "stale-run")
	asFile := filepath.Join(root, "run-asfile")

	for _, dir := range []string{freshDir, staleDir, foreignDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeBlob(t, dir)
	}
	if err := os.WriteFile(asFile, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Age everything except run-fresh to prove that name and type checks
	// protect entries too, not just the TTL.
	for _, path := range []string{staleDir, foreignDir, asFile} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	if err := SweepApplyStaging(root); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale run dir should be swept: %v", err)
	}
	for _, kept := range []string{freshDir, foreignDir, asFile} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("%s should be kept: %v", kept, err)
		}
	}
}

// TestSweepApplyStagingTTLOverride pins the TTL variable plumbing: a negative
// TTL makes even freshly created run dirs stale.
func TestSweepApplyStagingTTLOverride(t *testing.T) {
	isolateStagingRoot(t)
	orig := applyRunTTL
	t.Cleanup(func() { applyRunTTL = orig })
	applyRunTTL = -time.Second

	root, err := ApplyStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(root, "run-now")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := SweepApplyStaging(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(run); !os.IsNotExist(err) {
		t.Fatalf("run dir should be swept with a negative TTL: %v", err)
	}
}

// TestSweepApplyStagingBestEffort checks that a sweep failure is reported but
// never fails a new apply, and that one undeletable entry does not stop the
// sweep from removing the remaining stale ones.
func TestSweepApplyStagingBestEffort(t *testing.T) {
	// The undeletable-entry trick (stripped write bits) only fails for
	// non-root: as root, RemoveAll succeeds and the sweep returns nil.
	if os.Getuid() == 0 {
		t.Skip("running as root: nothing is undeletable")
	}
	isolateStagingRoot(t)
	root, err := ApplyStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * applyRunTTL)

	// A run dir the sweep cannot remove: the owner cannot unlink entries
	// inside a directory without the write bit.
	stuck := filepath.Join(root, "run-stuck")
	if err := os.MkdirAll(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	writeBlob(t, stuck)
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(stuck, 0o700)
		_ = os.RemoveAll(stuck)
	})

	removable := filepath.Join(root, "run-removable")
	if err := os.MkdirAll(removable, 0o700); err != nil {
		t.Fatal(err)
	}
	writeBlob(t, removable)

	for _, path := range []string{stuck, removable} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	if err := SweepApplyStaging(root); err == nil {
		t.Fatal("expected the sweep to report the undeletable entry")
	}
	if _, err := os.Stat(stuck); err != nil {
		t.Fatalf("undeletable entry should survive the sweep: %v", err)
	}
	if _, err := os.Stat(removable); !os.IsNotExist(err) {
		t.Fatalf("removable stale dir should still be swept despite the failure: %v", err)
	}

	dir, cleanup, err := NewApplyRunDir()
	if err != nil {
		t.Fatalf("sweep failure must not fail NewApplyRunDir: %v", err)
	}
	defer cleanup()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("new run dir should exist despite the sweep failure: %v", err)
	}
}
