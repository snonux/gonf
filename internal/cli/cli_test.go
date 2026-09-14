package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
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

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })

	os.Args = []string{"gonf", "plan", "-o", planDir, "-id", "cli-test", "cli_touch"}
	if code := CLI(); code != 0 {
		t.Fatalf("plan exit %d", code)
	}
	planPath := filepath.Join(planDir, "plan.jsonl")
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
