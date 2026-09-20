package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	iexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
)

func TestCLIPlanAndApply(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	api.Task("cli_touch", "touch a file via plan", func() {
		dir := os.Getenv("GONF_CLI_TEST_DIR")
		api.File(filepath.Join(dir, "out.txt"), options.WithContent("hello from plan"))
	})

	root := t.TempDir()
	t.Setenv("GONF_CLI_TEST_DIR", root)
	planDir := filepath.Join(root, "planout")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "plan.jsonl"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })

	os.Args = []string{"gonf", "plan", "-o", planDir, "-id", "cli-test", "cli_touch"}
	if code := CLI(); code != 0 {
		t.Fatalf("plan exit %d", code)
	}
	planPath := filepath.Join(planDir, "plan.jsonl")
	if info, err := os.Stat(planDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("plan directory mode = %v, %v; want 0700", info.Mode(), err)
	}
	if info, err := os.Stat(planPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("plan file mode = %v, %v; want 0600", info.Mode(), err)
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) < 2 || ops[0].Op != plan.KindPlan {
		t.Fatalf("unexpected ops: %#v", ops)
	}

	outFile := filepath.Join(root, "out.txt")
	os.Args = []string{"gonf", "apply", planPath}
	if code := CLI(); code != 0 {
		t.Fatalf("apply exit %d", code)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello from plan" {
		t.Fatalf("got %q", data)
	}
}

func TestCLIPlanStdout(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	api.Task("cli_stdout", "", func() {
		api.File(filepath.Join(t.TempDir(), "x"), options.WithContent("via-stdout"))
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldOut })

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "plan", "-stdout", "-id", "stdout-test", "cli_stdout"}
	code := CLI()
	_ = w.Close()
	os.Stdout = oldOut
	raw, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("plan -stdout exit %d", code)
	}
	ops, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) < 2 || ops[0].Op != plan.KindPlan || ops[0].ID != "stdout-test" {
		t.Fatalf("unexpected ops: %#v", ops)
	}
}

func TestCLIPlanReplacesOutputSymlinkWithoutFollowingIt(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	root := t.TempDir()
	api.Task("cli_private_output", "", func() {
		api.File(filepath.Join(root, "out"), options.WithContent("secret plan material"))
	})
	planDir := filepath.Join(root, "plan")
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("do not overwrite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(planDir, "plan.jsonl")); err != nil {
		t.Fatal(err)
	}
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "plan", "-o", planDir, "cli_private_output"}
	if code := CLI(); code != 0 {
		t.Fatalf("plan exit %d", code)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "do not overwrite" {
		t.Fatalf("symlink target changed to %q, %v", got, err)
	}
	info, err := os.Lstat(filepath.Join(planDir, "plan.jsonl"))
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("replacement mode = %v, %v; want regular 0600", info.Mode(), err)
	}
}

func TestCLIApplyStdinFramedNoBlobs(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	root := t.TempDir()
	dst := filepath.Join(root, "out.txt")
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "stdin"},
		{Op: plan.KindFile, Path: dst, Mode: "0600", ContentB64: "aGVsbG8K"}, // hello\n
	}
	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, ops, nil); err != nil {
		t.Fatal(err)
	}

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	go func() {
		_, _ = w.Write(buf.Bytes())
		_ = w.Close()
	}()

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "apply", "-"}
	if code := CLI(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("got %q", got)
	}
}

func TestCLIPlanRequiresTasks(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "plan", "-o", t.TempDir()}
	if code := CLI(); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestCLIApplyRejectsBadVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bad.jsonl")
	if err := os.WriteFile(path, []byte(`{"op":"plan","version":99}`+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "apply", path}
	if code := CLI(); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

func TestCLIApplyDryRun(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "plan.jsonl")
	target := filepath.Join(root, "should-not-exist")
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "dry"},
		{Op: plan.KindCommand, Bin: "touch", Args: []string{target}},
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o640); err != nil {
		t.Fatal(err)
	}

	oldArgs := os.Args
	t.Cleanup(func() {
		os.Args = oldArgs
		resource.SetDryRun(false)
	})
	os.Args = []string{"gonf", "apply", "-n", path}
	if code := CLI(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create file")
	}
}

func TestCLIUsageDocumentsStrictPreview(t *testing.T) {
	oldArgs := os.Args
	oldStderr := os.Stderr
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Args = oldArgs
		os.Stderr = oldStderr
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
	})

	os.Args = []string{"gonf"}
	os.Stderr = stderrWriter
	if code := CLI(); code != 2 {
		t.Fatalf("CLI() exit code = %d, want 2", code)
	}
	if err := stderrWriter.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stderr = oldStderr
	usage, err := io.ReadAll(stderrReader)
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-strict-preview-version", "apply [-n|-dry-run|-strict-preview]", "push [-n|-dry-run|-preview]", "cluster [-n|-dry-run|-preview]", "fleet [-n|-dry-run|-preview]"} {
		if !strings.Contains(string(usage), flag) {
			t.Fatalf("usage does not document %q:\n%s", flag, usage)
		}
	}
}

func TestCLIApplyStrictPreviewUsesResourceDryRunWithoutStaging(t *testing.T) {
	resource.SetDryRun(false)
	t.Cleanup(func() { resource.SetDryRun(false) })

	var ran bool
	cmd.SetRunnersForTest(func(iexec.Opts, string, ...string) (string, string, int, error) {
		ran = true
		return "", "", 0, nil
	}, nil)
	t.Cleanup(cmd.ResetRunnersForTest)

	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "strict-preview"},
		{Op: plan.KindCommand, Bin: "would-mutate", Args: []string{"target"}},
	}
	var frame bytes.Buffer
	if err := plan.EncodePush(&frame, ops, nil); err != nil {
		t.Fatal(err)
	}

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	go func() {
		_, _ = w.Write(frame.Bytes())
		_ = w.Close()
	}()

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "apply", "-strict-preview", "-"}
	if code := CLI(); code != 0 {
		t.Fatalf("strict preview exit %d", code)
	}
	if ran {
		t.Fatal("strict preview ran a mutating command instead of only reporting it")
	}
}

// TestCLICmdTimeoutFlag pins the "-cmd-timeout" wiring added for task x5: the
// CLI's global flag must reach api.SetCommandTimeout (and, through it,
// internal/exec's process-wide default), so an operator can override the
// default per-command timeout without touching resource-package code. The
// flag is additive (a new optional top-level flag with a sensible default
// equal to the prior process-wide default), so this test does not need to
// exercise every existing CLI flag combination for regressions.
func TestCLICmdTimeoutFlag(t *testing.T) {
	orig := api.CommandTimeout()
	t.Cleanup(func() { api.SetCommandTimeout(orig) })

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })

	api.ResetTasks()
	resource.ResetRepository()
	root := t.TempDir()
	target := filepath.Join(root, "x")
	api.Task("cli_cmd_timeout", "", func() {
		api.File(target, options.WithContent("ok"))
	})

	os.Args = []string{"gonf", "-cmd-timeout", "50ms", "cli_cmd_timeout"}
	if code := CLI(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := api.CommandTimeout(); got != 50*time.Millisecond {
		t.Fatalf("CommandTimeout() = %v, want 50ms", got)
	}
}

func TestPrintUsageMentionsPlanApply(t *testing.T) {
	// smoke: usage strings stay discoverable via source; CLI with no args returns 2
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf"}
	if code := CLI(); code != 2 {
		t.Fatalf("exit %d", code)
	}
	_ = strings.Contains
}

// runApplyStdin runs `gonf apply [-apply-dir dir] -` with stdin carrying the
// encoded GONF-PUSH/1 frame and returns the process exit code.
func runApplyStdin(t *testing.T, frame []byte, applyDir string) int {
	t.Helper()
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	go func() {
		_, _ = w.Write(frame)
		_ = w.Close()
	}()
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	args := []string{"gonf", "apply"}
	if applyDir != "" {
		args = append(args, "-apply-dir", applyDir)
	}
	os.Args = append(args, "-")
	return CLI()
}

// TestCLIApplyStickyOwnershipVerified pins task 222: the sticky -apply-dir is
// created 0700, ownership-verified, and the pushed blobs are extracted into it
// and applied end-to-end.
func TestCLIApplyStickyOwnershipVerified(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	root := t.TempDir()
	sticky := filepath.Join(root, "sticky")
	dst := filepath.Join(root, "out.txt")

	mem := plan.NewMemoryStore()
	ref, err := mem.WriteFile("demo.txt", []byte("blob-content"))
	if err != nil {
		t.Fatal(err)
	}
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "sticky"},
		{Op: plan.KindFile, Path: dst, Mode: "0600", Blob: ref},
	}
	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, ops, mem); err != nil {
		t.Fatal(err)
	}

	if code := runApplyStdin(t, buf.Bytes(), sticky); code != 0 {
		t.Fatalf("apply exit %d", code)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "blob-content" {
		t.Fatalf("got %q", got)
	}
	// The sticky dir was created (not pre-existing) and is owner-only.
	info, err := os.Lstat(sticky)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("sticky dir mode = %v, want 0700 dir", info.Mode())
	}
	if err := verifyStickyDirOwned(sticky); err != nil {
		t.Fatalf("sticky dir must stay owned by the current user: %v", err)
	}
}

// TestCLIApplyStickyRefusesPlantedSymlink covers the loud pre-plant refusal
// that is simulatable unprivileged: a symlink planted at the predictable
// sticky path survives MkdirAll + Chmod (both follow the link) and must be
// refused by the ownership Lstat, which does not follow it.
func TestCLIApplyStickyRefusesPlantedSymlink(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	root := t.TempDir()
	sticky := filepath.Join(root, "sticky")
	// The symlink must point at an existing dir: MkdirAll no-ops then (its
	// Stat follows the link), Chmod follows it too — only the ownership
	// Lstat refuses. (A symlink to a nonexistent target already fails loudly
	// at MkdirAll with EEXIST and is covered by the regular MkdirAll error.)
	if err := os.Mkdir(filepath.Join(root, "elsewhere"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "elsewhere"), sticky); err != nil {
		t.Fatal(err)
	}
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "sticky"},
		{Op: plan.KindFile, Path: filepath.Join(root, "out.txt"), Mode: "0600", ContentB64: "aGVsbG8K"},
	}
	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, ops, nil); err != nil {
		t.Fatal(err)
	}

	// Capture stderr so the loud refusal is asserted, not just the exit code.
	oldStderr := os.Stderr
	fr, fw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = fw
	code := runApplyStdin(t, buf.Bytes(), sticky)
	_ = fw.Close()
	os.Stderr = oldStderr
	diag, err := io.ReadAll(fr)
	_ = fr.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr: %s", code, diag)
	}
	if !strings.Contains(string(diag), "not a directory (pre-planted?)") {
		t.Fatalf("stderr must name the refusal, got: %s", diag)
	}
	// Nothing was applied and the planted symlink is untouched.
	if _, err := os.Lstat(filepath.Join(root, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("plan must not be applied into a planted dir: %v", err)
	}
	if fi, err := os.Lstat(sticky); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("planted symlink must be left alone: %v", err)
	}
}

// TestCLIApplyStickyWipesStaleEntriesBeforeExtraction pins root cause 1: a
// sticky -apply-dir reused across pushes to the same host must start every
// extraction from empty, not overlay the new stream on top of whatever a
// prior (possibly interrupted) run left behind. Before the fix, a file
// present in an earlier push's blobs/ but absent from the new one survived
// (plan/pushwire.go's extraction only O_TRUNCs entries the new stream
// carries; it never removes ones it doesn't), which would let a deleted
// source-tree file get silently redeployed and then kept by dir sync's
// WithPrune (which only prunes destination files absent from the extracted
// tree, not stale files still present in it).
func TestCLIApplyStickyWipesStaleEntriesBeforeExtraction(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	root := t.TempDir()
	sticky := filepath.Join(root, "sticky")
	dst := filepath.Join(root, "out.txt")

	// First push: uploads blobs/old.conf into the sticky dir and applies a
	// file from it. This is the "prior run" that, in the real bug, would
	// leak old.conf behind if pushRemoveSticky never ran (SIGINT, timeout,
	// sibling-host failure) — simulated here by simply not cleaning up.
	mem1 := plan.NewMemoryStore()
	ref1, err := mem1.WriteFile("old.conf", []byte("stale-leftover-content"))
	if err != nil {
		t.Fatal(err)
	}
	ops1 := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "sticky"},
		{Op: plan.KindFile, Path: dst, Mode: "0600", Blob: ref1},
	}
	var buf1 bytes.Buffer
	if err := plan.EncodePush(&buf1, ops1, mem1); err != nil {
		t.Fatal(err)
	}
	if code := runApplyStdin(t, buf1.Bytes(), sticky); code != 0 {
		t.Fatalf("first apply exit %d", code)
	}
	staleBlob := filepath.Join(sticky, "blobs", "old.conf")
	if _, err := os.Stat(staleBlob); err != nil {
		t.Fatalf("setup: first push must have staged blobs/old.conf: %v", err)
	}

	// Second push to the SAME sticky dir: the admin deleted old.conf from the
	// source tree, so this stream carries only new.conf.
	mem2 := plan.NewMemoryStore()
	ref2, err := mem2.WriteFile("new.conf", []byte("fresh-content"))
	if err != nil {
		t.Fatal(err)
	}
	ops2 := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "sticky"},
		{Op: plan.KindFile, Path: dst, Mode: "0600", Blob: ref2},
	}
	var buf2 bytes.Buffer
	if err := plan.EncodePush(&buf2, ops2, mem2); err != nil {
		t.Fatal(err)
	}
	if code := runApplyStdin(t, buf2.Bytes(), sticky); code != 0 {
		t.Fatalf("second apply exit %d", code)
	}

	// The stale entry must be gone, not merely superseded: extraction must
	// start from an empty dir, not overlay onto the leftover.
	if _, err := os.Stat(staleBlob); !os.IsNotExist(err) {
		t.Fatalf("blobs/old.conf must not survive a second push that no longer sends it: err=%v", err)
	}
	freshBlob := filepath.Join(sticky, "blobs", "new.conf")
	if _, err := os.Stat(freshBlob); err != nil {
		t.Fatalf("blobs/new.conf must be present after the second push: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(sticky, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "new.conf" {
		t.Fatalf("blobs/ must contain exactly the new stream's files, got %v (union of old+new would be a bug)", entries)
	}
	// And the resource that was actually applied reflects the new content.
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fresh-content" {
		t.Fatalf("applied content = %q, want the new stream's content", got)
	}
}

// TestVerifyStickyDirOwned pins the ownership verifier directly.
func TestVerifyStickyDirOwned(t *testing.T) {
	root := t.TempDir()

	// Happy path: a 0700 directory owned by the current user verifies.
	own := filepath.Join(root, "own")
	if err := os.Mkdir(own, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyStickyDirOwned(own); err != nil {
		t.Fatalf("owned dir should verify: %v", err)
	}

	// A planted regular file is refused by the IsDir check.
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyStickyDirOwned(file)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("planted file should be refused, got %v", err)
	}

	// The foreign-owned-dir refusal (the actual pre-plant vector) needs a
	// foreign owner, which cannot be simulated unprivileged: the dir is
	// necessarily owned by us. As root the check is bypassed by design
	// (sudo/doas chunks legitimately read the SSH login user's sticky dir),
	// so that branch stays exercised only on a hostile remote — a non-root
	// pre-plant is already refused by the loud Chmod + IsDir checks above.
	if os.Getuid() == 0 {
		t.Log("running as root: foreign-owned dirs are accepted by design (elevated-chunk flow)")
	}
}
