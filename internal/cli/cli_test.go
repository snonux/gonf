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
	"github.com/snonux/gonf/internal/testutil"
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
	// m62: an existing output directory is verified, never rewritten, so the
	// 0755 the operator chose survives; only plan.jsonl is owner-only.
	if info, err := os.Stat(planDir); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("plan directory mode = %v, %v; want the existing 0755 unchanged", info.Mode(), err)
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

// unregisteredDep is a resource.Dependency naming an ID no resource carries:
// what a typo'd DependsOn target looks like once options.DependsOn has
// flattened it into a dep ID.
type unregisteredDep string

func (d unregisteredDep) Dependencies() []string { return []string{string(d)} }

// captureStderr runs fn with os.Stderr redirected into a pipe and returns
// what fn wrote to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = old })
	fn()
	_ = w.Close()
	os.Stderr = old
	out, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// runGonf runs `gonf <args...>` in-process and returns the exit code and
// everything written to stderr.
func runGonf(t *testing.T, args ...string) (int, string) {
	t.Helper()
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = append([]string{"gonf"}, args...)
	var code int
	stderr := captureStderr(t, func() { code = CLI() })
	return code, stderr
}

// requireCLIRecordRefusal checks the exact wording shape a refused record has
// at `gonf plan`: ONE record-time prefix behind the command's own "plan: "
// ("plan: RecordPlan: <reason>"), never the engine prefix doubled ("plan: plan:
// ...").
func requireCLIRecordRefusal(t *testing.T, stderr, wantSubstr string) {
	t.Helper()
	line := strings.TrimRight(stderr, "\n")
	if !strings.HasPrefix(line, "plan: RecordPlan: ") || strings.Contains(line, "plan: plan:") ||
		strings.Contains(line[len("plan: RecordPlan: "):], "plan: ") || strings.Contains(line, "\n") {
		t.Fatalf("stderr %q, want a single line shaped %q", stderr, "plan: RecordPlan: <reason>")
	}
	if !strings.Contains(line, wantSubstr) {
		t.Fatalf("stderr %q must contain %q", stderr, wantSubstr)
	}
}

// TestCLIPlanRefusesDanglingDependency pins the o62 record-time guard on the
// documented `gonf plan` -> `gonf apply plan.jsonl` workflow: `gonf apply`
// cannot tell a whole plan from a single privilege chunk, so a typo'd
// DependsOn must fail when the plan is written. Nothing may be written (no
// plan.jsonl, on stdout neither), nothing applied, and the output directory
// must not even be created.
func TestCLIPlanRefusesDanglingDependency(t *testing.T) {
	for _, stdout := range []bool{false, true} {
		name := "file"
		if stdout {
			name = "stdout"
		}
		t.Run(name, func(t *testing.T) {
			api.ResetTasks()
			resource.ResetRepository()
			root := t.TempDir()
			marker := filepath.Join(root, "marker")
			api.Task("cli_dangling", "", func() {
				api.File(filepath.Join(root, "independent"), options.WithContent("x"))
				api.Command("touch", []string{marker}, options.DependsOn(unregisteredDep("File[/typo/never-registered]")))
			})
			planDir := filepath.Join(root, "planout")

			args := []string{"plan", "-o", planDir, "cli_dangling"}
			if stdout {
				args = []string{"plan", "-stdout", "cli_dangling"}
			}
			code, stderr := runGonf(t, args...)
			if code != 1 {
				t.Fatalf("plan exit %d, want 1; stderr: %s", code, stderr)
			}
			requireCLIRecordRefusal(t, stderr, "depends on File[/typo/never-registered], which is not a registered resource (dangling dependency)")
			for _, p := range []string{planDir, marker, filepath.Join(root, "independent")} {
				if _, err := os.Lstat(p); !os.IsNotExist(err) {
					t.Fatalf("%s exists (stat err %v); a refused plan writes and applies nothing", p, err)
				}
			}
		})
	}
}

// TestCLIPlanRefusalLeavesOutputDirUntouched is the review regression for
// blobs: a refused `gonf plan -o dir` must leave dir exactly as it was. The
// reproduction records a good SyncDir plan into dir, edits the source file, and
// re-runs with a dangling dependency: blob names are deterministic, so the
// refused run used to overwrite the blob and the earlier plan.jsonl then
// applied content it was never recorded with. Every byte and mtime of dir must
// be unchanged, and the earlier plan must still apply its original content.
func TestCLIPlanRefusalLeavesOutputDirUntouched(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	root := t.TempDir()
	src, dst := registerCLISyncTasks(t, root)

	// (1) The absent directory of a refused run is not created (the reviewer's
	// stray "o2/blobs/sync-<hash>/f1" and empty "o1/").
	absent := filepath.Join(root, "o2")
	requireCLIPlanRefused(t, absent, "cli_refused")
	testutil.RequireUnchanged(t, testutil.DirSnapshot{Absent: true}, absent)

	// (2) A directory holding an earlier good plan survives a refused re-run.
	o3 := filepath.Join(root, "o3")
	if code, stderr := runGonf(t, "plan", "-o", o3, "cli_good"); code != 0 {
		t.Fatalf("good plan exit %d; stderr: %s", code, stderr)
	}
	before := testutil.Snapshot(t, o3)
	if err := os.WriteFile(src, []byte("version TWO\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	requireCLIPlanRefused(t, o3, "cli_refused")
	testutil.RequireUnchanged(t, before, o3)

	if code, stderr := runGonf(t, "apply", filepath.Join(o3, "plan.jsonl")); code != 0 {
		t.Fatalf("applying the earlier plan exit %d; stderr: %s", code, stderr)
	}
	got, err := os.ReadFile(filepath.Join(dst, "f1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "version one\n" {
		t.Fatalf("the earlier plan applied %q, want the content it was recorded with", got)
	}
}

// registerCLISyncTasks creates a SyncDir source below root (one file, "version
// one") and registers two tasks that sync it into the returned dst: cli_good
// records it as is, cli_refused adds a dangling dependency so the record is
// refused after the blob was packaged. It returns the source file (edit it to
// change what a re-recorded blob would contain) and dst.
func registerCLISyncTasks(t *testing.T, root string) (srcFile, dst string) {
	t.Helper()
	srcDir := filepath.Join(root, "src")
	dst = filepath.Join(root, "dst")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile = filepath.Join(srcDir, "f1")
	if err := os.WriteFile(srcFile, []byte("version one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncSrc := func() { api.SyncDir(dst, filepath.Join(srcDir, "*")) }
	api.Task("cli_good", "", syncSrc)
	api.Task("cli_refused", "", func() {
		syncSrc()
		api.Command("true", nil, options.DependsOn(unregisteredDep("File[/typo/never-registered]")))
	})
	return srcFile, dst
}

// requireCLIPlanRefused runs `gonf plan -o outDir task` and requires exit 1
// with the dangling-dependency refusal.
func requireCLIPlanRefused(t *testing.T, outDir, task string) {
	t.Helper()
	code, stderr := runGonf(t, "plan", "-o", outDir, task)
	if code != 1 {
		t.Fatalf("refused plan exit %d, want 1; stderr: %s", code, stderr)
	}
	requireCLIRecordRefusal(t, stderr, "dangling dependency")
}

// TestCLIPlanRefusesDependencyOnLaterPrivilegeChunk documents the small
// tightening o62 brought to `gonf plan`: a dependency on a LATER privilege
// chunk used to be recorded (Run and push already refused it at apply time)
// and is now refused at record time. The wording keeps the one-prefix shape and
// nothing is written.
func TestCLIPlanRefusesDependencyOnLaterPrivilegeChunk(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	planDir := filepath.Join(t.TempDir(), "planout")
	api.Task("cli_forward_chunk", "", func() {
		api.Command("true", nil, options.WithName("first"), options.DependsOn(unregisteredDep("Command[later]")))
		api.Command("true", nil, options.WithName("later"), options.WithElevate)
	})
	code, stderr := runGonf(t, "plan", "-o", planDir, "cli_forward_chunk")
	if code != 1 {
		t.Fatalf("plan exit %d, want 1; stderr: %s", code, stderr)
	}
	requireCLIRecordRefusal(t, stderr, "depends on Command[later] which is recorded in later chunk 1")
	testutil.RequireUnchanged(t, testutil.DirSnapshot{Absent: true}, planDir)
}

// TestCLIPlanWithValidDependenciesStillAppliesInOrder is the positive
// counterpart: a dependent recorded BEFORE its dependency (a forward dep inside
// one privilege chunk, so ordering really matters: recorded order would run the
// dependent first and fail its test -f) records, is written and applies in
// dependency order via `gonf apply`.
func TestCLIPlanWithValidDependenciesStillAppliesInOrder(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	root := t.TempDir()
	dep := filepath.Join(root, "dep")
	out := filepath.Join(root, "out")
	api.Task("cli_valid_deps", "", func() {
		// Recorded first, depending on the File registered right after it. The
		// dep is spelled as an ID because the File value does not exist yet.
		api.Command("sh", []string{"-c", "test -f " + dep + " && touch " + out},
			options.WithName("dependent"), options.DependsOn(unregisteredDep("File["+dep+"]")))
		api.File(dep, options.WithContent("d"))
	})
	planDir := filepath.Join(root, "planout")

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "plan", "-o", planDir, "cli_valid_deps"}
	if code := CLI(); code != 0 {
		t.Fatalf("plan exit %d", code)
	}
	os.Args = []string{"gonf", "apply", filepath.Join(planDir, "plan.jsonl")}
	if code := CLI(); code != 0 {
		t.Fatalf("apply exit %d", code)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("dependent was not applied after its dependency: %v", err)
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

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })
	fn()
	_ = w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestCLIPlanBlobLessNeedsNoTempDir is the o62 review regression for the extra
// failure modes staging added to `gonf plan`: a plan without blobs must not
// depend on $TMPDIR, with -o (lazy staging) and with -stdout (no temp dir at
// all), exactly as on the core before staging existed.
func TestCLIPlanBlobLessNeedsNoTempDir(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	root := t.TempDir()
	api.Task("cli_blob_less", "", func() {
		api.File(filepath.Join(root, "x"), options.WithContent("inline"))
	})

	planDir := filepath.Join(root, "out")
	if code, stderr := runGonf(t, "plan", "-o", planDir, "cli_blob_less"); code != 0 {
		t.Fatalf("plan -o exit %d with an unusable TMPDIR; stderr: %s", code, stderr)
	}
	raw, err := os.ReadFile(filepath.Join(planDir, "plan.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if ops, err := plan.DecodePlanBytes(raw); err != nil || len(ops) < 2 {
		t.Fatalf("plan.jsonl decode: %v, %d ops", err, len(ops))
	}

	var code int
	out := captureStdout(t, func() { code, _ = runGonf(t, "plan", "-stdout", "cli_blob_less") })
	if code != 0 {
		t.Fatalf("plan -stdout exit %d with an unusable TMPDIR", code)
	}
	if !bytes.Equal([]byte(out), raw) {
		t.Fatalf("-stdout output differs from the plan.jsonl of -o:\n%s\nvs\n%s", out, raw)
	}
}

// TestCLIPlanStdoutWithBlobsStagesNothing: -stdout cannot print a plan that
// needs blobs; it must say so, and it records in memory: with $TMPDIR pointing
// at nothing it still reaches that message (a temp dir, or a second staging
// pass, would fail on the missing $TMPDIR first).
func TestCLIPlanStdoutWithBlobsStagesNothing(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	tmp := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv("TMPDIR", tmp)
	root := t.TempDir()
	big := filepath.Join(root, "big")
	if err := os.WriteFile(big, bytes.Repeat([]byte("B"), plan.MaxInlineContent+1), 0o600); err != nil {
		t.Fatal(err)
	}
	api.Task("cli_big", "", func() { api.InstallFile(filepath.Join(root, "dst"), big) })

	var code int
	var stderr string
	out := captureStdout(t, func() { code, stderr = runGonf(t, "plan", "-stdout", "cli_big") })
	if code != 1 || !strings.Contains(stderr, "-stdout cannot emit plans that need blobs/") {
		t.Fatalf("exit %d, stderr %q; want exit 1 and the blobs/ refusal", code, stderr)
	}
	if out != "" {
		t.Fatalf("stdout = %q, a refused plan prints nothing", out)
	}
	if _, err := os.Lstat(tmp); !os.IsNotExist(err) {
		t.Fatalf("$TMPDIR path exists (%v), -stdout must not write to disk", err)
	}
}

// TestCLIPlanRefusesUnusableOutputUpFront: the common unusable -o paths (a
// file, a symlink or a symlinked ancestor, a file as a parent) fail BEFORE any
// task body runs, as they did on the core before staging when SecureDir came
// first; nothing is created, and a usable absent path still works. link points
// at root, so anything created through it would change root's snapshot.
func TestCLIPlanRefusesUnusableOutputUpFront(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a-file")
	if err := os.WriteFile(file, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	ran := false
	register := func() {
		api.ResetTasks()
		resource.ResetRepository()
		ran = false
		api.Task("cli_probe", "", func() { ran = true })
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{
		"output is a file":                   file,
		"parent of output is a file":         filepath.Join(file, "sub"),
		"output is a symlink":                link,
		"output is a symlink with a slash":   link + "/",
		"ancestor of output is a symlink":    filepath.Join(link, "newsub"),
		"grandparent of output is a symlink": filepath.Join(link, "newsub", "deeper"),
	} {
		t.Run(name, func(t *testing.T) {
			register()
			before := testutil.Snapshot(t, root)
			code, stderr := runGonf(t, "plan", "-o", out, "cli_probe")
			if code != 1 || !strings.HasPrefix(stderr, "plan: RecordPlan: plan dir: ") {
				t.Fatalf("exit %d, stderr %q; want exit 1 and a \"plan: RecordPlan: plan dir: \" error", code, stderr)
			}
			if ran {
				t.Fatal("the task body ran; an unusable -o must fail before any body")
			}
			testutil.RequireUnchanged(t, before, root)
		})
	}

	register()
	good := filepath.Join(root, "new", "nested")
	if code, stderr := runGonf(t, "plan", "-o", good, "cli_probe"); code != 0 || !ran {
		t.Fatalf("usable absent -o: exit %d (body ran: %v), stderr: %s", code, ran, stderr)
	}
	if _, err := os.Stat(filepath.Join(good, "plan.jsonl")); err != nil {
		t.Fatal(err)
	}
}

// TestCLIPlanErrorPrefixes documents which prefixes a failed `gonf plan` shows
// (see planToDir): a pre-flight refusal carries "RecordPlan: ", an unknown task
// does not - the command only adds its own "plan: ".
func TestCLIPlanErrorPrefixes(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	code, stderr := runGonf(t, "plan", "-o", filepath.Join(t.TempDir(), "o"), "pkg_no_such_task")
	if code != 1 || !strings.HasPrefix(stderr, `plan: unknown task "pkg_no_such_task"`) || strings.Contains(stderr, "RecordPlan:") {
		t.Fatalf("exit %d, stderr %q; want exit 1 and `plan: unknown task ...` without a RecordPlan prefix", code, stderr)
	}
}
