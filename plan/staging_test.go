package plan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestApplyStagingRootSharedRootSticky is the regression test for task ee2:
// os.Chmod(shared, 0o1777) looked plausible but silently dropped the sticky
// bit, because os.Chmod reads the ModeSticky FileMode flag (a high bit), not
// the low-order 0o1000 octal literal. It stats the real, on-disk mode of the
// shared parent after ApplyStagingRoot runs, rather than merely asserting
// which chmod argument was used — that argument-only check is exactly what
// let the original bug slip through.
func TestApplyStagingRootSharedRootSticky(t *testing.T) {
	isolateStagingRoot(t)

	root, err := ApplyStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Dir(root)

	info, err := os.Stat(shared)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSticky == 0 {
		t.Fatalf("shared staging root %s: sticky bit not set, mode %v", shared, info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o777 {
		t.Fatalf("shared staging root %s: permission bits %#o, want 0o777", shared, perm)
	}
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

// deadPID starts and waits out a trivial child process, then returns its
// former PID: not alive by the time this returns (barring the kernel
// reusing the exact PID before the calling test's own assertions run, which
// on a modern PID space within a single test's lifetime is not a realistic
// race to defend against).
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot run a trivial child process to mint a dead pid: %v", err)
	}
	return cmd.Process.Pid
}

// mkSealedRunDir creates root/sealed-run-<pid>-<suffix> and returns its
// path, for tests that need to control the encoded PID directly rather than
// going through NewSealedApplyRunDir (which always encodes its own PID).
func mkSealedRunDir(t *testing.T, root string, pid int, suffix string) string {
	t.Helper()
	dir := filepath.Join(root, fmt.Sprintf("%s%d-%s", sealedRunDirPrefix, pid, suffix))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSweepSealedApplyRunsRemovesDeadOwnerRegardlessOfAge is the core
// docs/design/plan-encryption.md "Plaintext after decryption" guarantee: a
// sealed-run-* leftover of a process that is no longer alive is removed
// even when it was created moments ago, unlike an ordinary run-* directory
// (which only ever gets removed after applyRunTTL).
func TestSweepSealedApplyRunsRemovesDeadOwnerRegardlessOfAge(t *testing.T) {
	isolateStagingRoot(t)
	root, err := ApplyStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	dead := deadPID(t)
	dir := mkSealedRunDir(t, root, dead, "x")
	writeBlob(t, dir)

	if err := sweepSealedApplyRuns(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dead-owner sealed-run dir should be swept regardless of age: %v", err)
	}
}

// TestSweepSealedApplyRunsKeepsAliveOwnerUntilStale is the residual-risk
// half: a sealed-run-* entry whose PID is alive (this test process itself)
// survives while fresh, and is only removed once it also passes the
// ordinary applyRunTTL age rule — the same fallback an alive-but-reused PID
// gets (documented, not silently dropped).
func TestSweepSealedApplyRunsKeepsAliveOwnerUntilStale(t *testing.T) {
	isolateStagingRoot(t)
	root, err := ApplyStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := mkSealedRunDir(t, root, os.Getpid(), "x")
	writeBlob(t, dir)

	if err := sweepSealedApplyRuns(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fresh alive-owner sealed-run dir should survive: %v", err)
	}

	old := time.Now().Add(-2 * applyRunTTL)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	if err := sweepSealedApplyRuns(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("stale alive-owner sealed-run dir should fall back to the ordinary TTL sweep: %v", err)
	}
}

// TestSweepSealedApplyRunsLeavesForeignAndPlainRunDirsAlone: an unrelated
// name and an ordinary "run-*" directory (which has its own sweep,
// SweepApplyStaging) are never touched by sweepSealedApplyRuns.
func TestSweepSealedApplyRunsLeavesForeignAndPlainRunDirsAlone(t *testing.T) {
	isolateStagingRoot(t)
	root, err := ApplyStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * applyRunTTL)

	foreign := filepath.Join(root, "sealed-run-not-a-pid-x")
	plainRun := filepath.Join(root, runDirPrefix+"stale")
	malformed := filepath.Join(root, sealedRunDirPrefix+"noseparator")
	for _, dir := range []string{foreign, plainRun, malformed} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}

	if err := sweepSealedApplyRuns(root); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{foreign, plainRun, malformed} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s should be left alone by sweepSealedApplyRuns: %v", dir, err)
		}
	}
}

// TestSealedRunDirPID pins the name parsing sweepSealedApplyRuns relies on.
func TestSealedRunDirPID(t *testing.T) {
	tests := []struct {
		name    string
		wantPID int
		wantOK  bool
	}{
		{sealedRunDirPrefix + "123-abcxyz", 123, true},
		{sealedRunDirPrefix + "1-a", 1, true},
		{sealedRunDirPrefix + "0-abc", 0, false},
		{sealedRunDirPrefix + "-abc", 0, false},
		{sealedRunDirPrefix + "abc-def", 0, false},
		{sealedRunDirPrefix + "123", 0, false}, // no separator: no random suffix
		{"run-123-abc", 0, false},
		{"sealed-run", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pid, ok := sealedRunDirPID(tt.name)
			if pid != tt.wantPID || ok != tt.wantOK {
				t.Fatalf("sealedRunDirPID(%q) = (%d, %v), want (%d, %v)", tt.name, pid, ok, tt.wantPID, tt.wantOK)
			}
		})
	}
}

// TestProcessAlive checks the syscall.Kill(pid, 0) classification directly:
// this test process's own PID is alive, and a waited-out child's former PID
// is not.
func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("this test process should read as alive")
	}
	if processAlive(deadPID(t)) {
		t.Fatal("a waited-out child's former pid should read as not alive")
	}
}

// TestNewSealedApplyRunDirDistinctPrefixAndDeadOwnerSweep is the
// integration test for NewSealedApplyRunDir itself: it names its directory
// sealed-run-<its own pid>-*, and creating a second one sweeps a first
// dead-owner leftover away regardless of age, while never touching an
// ordinary run-* directory (NewApplyRunDir's own namespace).
func TestNewSealedApplyRunDirDistinctPrefixAndDeadOwnerSweep(t *testing.T) {
	isolateStagingRoot(t)
	root, err := ApplyStagingRoot()
	if err != nil {
		t.Fatal(err)
	}

	plainDir, plainCleanup, err := NewApplyRunDir()
	if err != nil {
		t.Fatal(err)
	}
	defer plainCleanup()

	dead := deadPID(t)
	leftover := mkSealedRunDir(t, root, dead, "leftover")
	writeBlob(t, leftover)

	dir, cleanup, err := NewSealedApplyRunDir()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if !strings.HasPrefix(filepath.Base(dir), fmt.Sprintf("%s%d-", sealedRunDirPrefix, os.Getpid())) {
		t.Fatalf("NewSealedApplyRunDir dir %q does not carry this process's own pid in its name", dir)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("dead-owner leftover should have been swept before the new dir was created: %v", err)
	}
	if _, err := os.Stat(plainDir); err != nil {
		t.Fatalf("an ordinary run-* dir must not be touched by the sealed sweep: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("sealed run dir mode = %v, want a private directory", info.Mode())
	}

	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove the sealed run dir: %v", err)
	}
}
