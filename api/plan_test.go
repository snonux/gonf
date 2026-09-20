package api

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestRecordPlanEmitsOrderedOpsWithGuards(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	planDir := filepath.Join(dir, "plan-out")
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "unit.service"), []byte("[Unit]\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(dir, "bashrc")
	filePath := filepath.Join(dir, "taskrc")
	installSrc := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(installSrc, []byte("user.name=test\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	installDst := filepath.Join(dir, "out-gitconfig")
	syncPath := filepath.Join(dir, "systemd")

	Task("demo_home", "record demo", func() {
		Link(linkPath, options.WithSymlink("/dotfiles/bashrc"))
		File(filePath, options.WithContent("set x=1\n"), options.WithMode(0o640))
		InstallFile(installDst, installSrc)
		Dir(filepath.Join(dir, "empty"), options.WithMode(0o700))
		SyncDir(syncPath, filepath.Join(srcDir, "*"), options.WithPrune)
		Package("fish")
		Command("systemctl", []string{"--user", "enable", "x.timer"},
			options.WithName("enable.x"),
			options.Unless("systemctl", []string{"--user", "is-enabled", "x.timer"}),
			options.Creates(filepath.Join(dir, "created")),
		)
	})

	ops, err := RecordPlan("demo-plan", planDir, "demo_home")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	if len(ops) < 2 || ops[0].Op != plan.KindPlan || ops[0].ID != "demo-plan" {
		t.Fatalf("header = %#v", ops[0])
	}

	kinds := make([]plan.Kind, 0, len(ops)-1)
	for _, op := range ops[1:] {
		kinds = append(kinds, op.Op)
	}
	wantKinds := []plan.Kind{
		plan.KindLink,
		plan.KindFile,
		plan.KindFile,
		plan.KindDir,
		plan.KindSyncDir,
		plan.KindPackage,
		plan.KindCommand,
	}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("kinds=%v want %v", kinds, wantKinds)
	}
	for i := range wantKinds {
		if kinds[i] != wantKinds[i] {
			t.Fatalf("kinds[%d]=%s want %s (all %v)", i, kinds[i], wantKinds[i], kinds)
		}
	}

	fileOp := ops[2]
	wantB64 := base64.StdEncoding.EncodeToString([]byte("set x=1\n"))
	if fileOp.ContentB64 != wantB64 || fileOp.Mode != "0640" {
		t.Fatalf("file op = %#v", fileOp)
	}

	installOp := ops[3]
	wantInstall := base64.StdEncoding.EncodeToString([]byte("user.name=test\n"))
	if installOp.ContentB64 != wantInstall || installOp.Blob != "" {
		t.Fatalf("InstallFile op = %#v", installOp)
	}

	syncOp := ops[5]
	// The blob ref carries a human-readable "systemd" basename plus a hash
	// suffix (see api/plan.go blobName) so two SyncDirs whose destinations
	// share a basename never collide on the same ref.
	if !strings.HasPrefix(syncOp.Blob, "blobs/systemd-") || !syncOp.Prune {
		t.Fatalf("sync_dir op = %#v", syncOp)
	}
	blobFile := filepath.Join(planDir, filepath.FromSlash(syncOp.Blob), "unit.service")
	if _, err := os.Stat(blobFile); err != nil {
		t.Fatalf("expected packaged blob file: %v", err)
	}

	cmdOp := ops[7]
	if cmdOp.Name != "enable.x" || cmdOp.Bin != "systemctl" {
		t.Fatalf("command op = %#v", cmdOp)
	}
	if cmdOp.Unless == nil || cmdOp.Unless.Bin != "systemctl" {
		t.Fatalf("unless guard = %#v", cmdOp.Unless)
	}
	if cmdOp.Creates == "" {
		t.Fatalf("creates missing: %#v", cmdOp)
	}

	// Plan-record must not apply (no host mutations from the task body appliers).
	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Fatalf("link should not exist after RecordPlan: %v", err)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("file should not exist after RecordPlan: %v", err)
	}

	encoded, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.DecodePlanBytes(encoded); err != nil {
		t.Fatalf("encode/decode: %v", err)
	}
}

// TestRecordPlanSameBasenameSyncDirsGetDistinctBlobs is the e5 regression:
// two SyncDir resources whose destinations merely share a last path segment
// ("conf.d") must not collide on the same blob ref. Before the fix,
// blobName derived the ref from filepath.Base(d.Path) alone, so both
// packaged to "blobs/conf.d"; the second store.WriteGlob call silently
// overwrote the first one's content, so the first destination would apply
// with the second's files (and WithPrune could delete the first's
// legitimate files as "extra").
func TestRecordPlanSameBasenameSyncDirsGetDistinctBlobs(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	srcA := filepath.Join(dir, "a", "src")
	srcB := filepath.Join(dir, "b", "src")
	if err := os.MkdirAll(srcA, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(srcB, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcA, "app.conf"), []byte("from-a\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcB, "app.conf"), []byte("from-b\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Same basename ("conf.d") under different parents — this is exactly
	// the shape that used to collide.
	dst1 := filepath.Join(dir, "x", "conf.d")
	dst2 := filepath.Join(dir, "y", "conf.d")

	Task("e5_same_basename_syncdirs", "", func() {
		SyncDir(dst1, filepath.Join(srcA, "*"), options.WithPrune)
		SyncDir(dst2, filepath.Join(srcB, "*"), options.WithPrune)
	})

	planDir := filepath.Join(dir, "plan")
	ops, err := RecordPlan("e5-plan", planDir, "e5_same_basename_syncdirs")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	var syncOps []plan.Op
	for _, op := range ops {
		if op.Op == plan.KindSyncDir {
			syncOps = append(syncOps, op)
		}
	}
	if len(syncOps) != 2 {
		t.Fatalf("got %d sync_dir ops, want 2: %#v", len(syncOps), syncOps)
	}
	if syncOps[0].Blob == "" || syncOps[1].Blob == "" {
		t.Fatalf("expected non-empty blob refs: %#v", syncOps)
	}
	if syncOps[0].Blob == syncOps[1].Blob {
		t.Fatalf("both SyncDirs packaged to the same blob ref %q: content of one destination would silently clobber the other",
			syncOps[0].Blob)
	}

	// Packaging must not have let the second write clobber the first blob
	// on disk either.
	dataA, err := os.ReadFile(filepath.Join(planDir, filepath.FromSlash(syncOps[0].Blob), "app.conf"))
	if err != nil {
		t.Fatalf("read packaged blob for dst1: %v", err)
	}
	dataB, err := os.ReadFile(filepath.Join(planDir, filepath.FromSlash(syncOps[1].Blob), "app.conf"))
	if err != nil {
		t.Fatalf("read packaged blob for dst2: %v", err)
	}
	if string(dataA) != "from-a\n" || string(dataB) != "from-b\n" {
		t.Fatalf("packaged blob content = %q / %q, want %q / %q", dataA, dataB, "from-a\n", "from-b\n")
	}

	if err := ApplyPlan(ops, planDir); err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	got1, err := os.ReadFile(filepath.Join(dst1, "app.conf"))
	if err != nil {
		t.Fatalf("read %s: %v", dst1, err)
	}
	got2, err := os.ReadFile(filepath.Join(dst2, "app.conf"))
	if err != nil {
		t.Fatalf("read %s: %v", dst2, err)
	}
	if string(got1) != "from-a\n" {
		t.Fatalf("dst1 app.conf = %q, want %q", got1, "from-a\n")
	}
	if string(got2) != "from-b\n" {
		t.Fatalf("dst2 app.conf = %q, want %q", got2, "from-b\n")
	}
}

// TestRecordPlanSameBasenameLargeFilesGetDistinctBlobs is the e5 regression
// for the other blobName caller: two >512KiB Files (packaged via
// store.WriteFile instead of WriteGlob/WriteTree) whose destinations share a
// basename must not collide on the same blob ref either.
func TestRecordPlanSameBasenameLargeFilesGetDistinctBlobs(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	srcA := filepath.Join(dir, "a-app.conf")
	srcB := filepath.Join(dir, "b-app.conf")
	dataA := append([]byte("A-marker\n"), make([]byte, 600<<10)...)
	dataB := append([]byte("B-marker\n"), make([]byte, 600<<10)...)
	if err := os.WriteFile(srcA, dataA, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcB, dataB, 0o640); err != nil {
		t.Fatal(err)
	}

	// Same basename ("app.conf") under different parents.
	dst1 := filepath.Join(dir, "x", "app.conf")
	dst2 := filepath.Join(dir, "y", "app.conf")
	if err := os.MkdirAll(filepath.Dir(dst1), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst2), 0o750); err != nil {
		t.Fatal(err)
	}

	Task("e5_same_basename_files", "", func() {
		InstallFile(dst1, srcA)
		InstallFile(dst2, srcB)
	})

	planDir := filepath.Join(dir, "plan")
	ops, err := RecordPlan("e5-file-plan", planDir, "e5_same_basename_files")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	var fileOps []plan.Op
	for _, op := range ops {
		if op.Op == plan.KindFile && op.Blob != "" {
			fileOps = append(fileOps, op)
		}
	}
	if len(fileOps) != 2 {
		t.Fatalf("got %d blob-backed file ops, want 2: %#v", len(fileOps), ops)
	}
	if fileOps[0].Blob == fileOps[1].Blob {
		t.Fatalf("both large Files packaged to the same blob ref %q", fileOps[0].Blob)
	}

	if err := ApplyPlan(ops, planDir); err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	got1, err := os.ReadFile(dst1)
	if err != nil {
		t.Fatalf("read %s: %v", dst1, err)
	}
	got2, err := os.ReadFile(dst2)
	if err != nil {
		t.Fatalf("read %s: %v", dst2, err)
	}
	if !strings.HasPrefix(string(got1), "A-marker\n") {
		t.Fatalf("dst1 content does not start with A-marker")
	}
	if !strings.HasPrefix(string(got2), "B-marker\n") {
		t.Fatalf("dst2 content does not start with B-marker")
	}
}

func TestRecordPlanNegative(t *testing.T) {
	ResetTasks()
	Task("x", "", func() {})

	if _, err := RecordPlan("", "", "x"); err == nil {
		t.Fatal("expected error for empty plan id")
	}
	if _, err := RecordPlan("id", ""); err == nil {
		t.Fatal("expected error for no tasks")
	}
	if _, err := RecordPlan("id", "", "missing"); err == nil {
		t.Fatal("expected error for unknown task")
	}
}

func TestRecordPlanToMemoryStoreNoDiskArtifacts(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	probe := t.TempDir()
	srcDir := filepath.Join(probe, "src")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "unit.service"), []byte("[Unit]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	Task("mem_sync", "", func() {
		SyncDir(filepath.Join(probe, "dst"), filepath.Join(srcDir, "*"), options.WithPrune)
	})

	store := plan.NewMemoryStore()
	ops, err := RecordPlanTo("mem", store, "mem_sync")
	if err != nil {
		t.Fatal(err)
	}
	if !store.HasBlobs() {
		t.Fatal("expected in-memory blobs")
	}
	var found bool
	for _, op := range ops {
		if op.Op == plan.KindSyncDir && op.Blob != "" {
			found = true
			if _, ok := store.TreeBlob(op.Blob); !ok {
				t.Fatalf("missing tree for %s", op.Blob)
			}
		}
	}
	if !found {
		t.Fatal("expected sync_dir with blob ref")
	}

	// No plan.jsonl or blobs/ under the probe dir (or cwd).
	if _, err := os.Stat(filepath.Join(probe, "plan.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("plan.jsonl must not exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(probe, "blobs")); !os.IsNotExist(err) {
		t.Fatalf("blobs/ must not exist on disk: %v", err)
	}
}

func TestRecordPlanInlineVsBlobThreshold(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	smallSrc := filepath.Join(root, "small")
	largeSrc := filepath.Join(root, "large")
	if err := os.WriteFile(smallSrc, make([]byte, 100<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(largeSrc, make([]byte, 600<<10), 0o600); err != nil {
		t.Fatal(err)
	}

	Task("thresh", "", func() {
		InstallFile(filepath.Join(root, "dst-small"), smallSrc)
		InstallFile(filepath.Join(root, "dst-large"), largeSrc)
	})
	ops, err := RecordPlan("thresh", planDir, "thresh")
	if err != nil {
		t.Fatal(err)
	}

	var smallOp, largeOp *plan.Op
	for i := range ops {
		op := &ops[i]
		if op.Op != plan.KindFile {
			continue
		}
		switch {
		case op.ContentB64 != "" && op.Blob == "":
			smallOp = op
		case op.Blob != "" && op.ContentB64 == "":
			largeOp = op
		}
	}
	if smallOp == nil {
		t.Fatal("expected 100KiB file as content_b64")
	}
	if largeOp == nil {
		t.Fatal("expected 600KiB file as blob")
	}
	raw, err := base64.StdEncoding.DecodeString(smallOp.ContentB64)
	if err != nil || len(raw) != 100<<10 {
		t.Fatalf("small content len=%d err=%v", len(raw), err)
	}
}

func TestRecordPlanDoesNotLeaveRecorderEnabled(t *testing.T) {
	ResetTasks()
	Task("noop", "", func() {
		Package("helix")
	})
	if _, err := RecordPlan("p", "", "noop"); err != nil {
		t.Fatal(err)
	}
	if resource.PlanDraftRecording() {
		t.Fatal("recorder should be cleared after RecordPlan")
	}
}

func TestRecordPlanSyncDirRequiresPlanDir(t *testing.T) {
	ResetTasks()
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	Task("sync", "", func() {
		SyncDir(filepath.Join(dir, "dst"), filepath.Join(dir, "*"))
	})
	_, err := RecordPlan("p", "", "sync")
	if err == nil || !strings.Contains(err.Error(), "plan dir required") {
		t.Fatalf("want plan dir required error, got %v", err)
	}
}

// TestRecordPlanEmptyFileContentSetsHasContent is the k5 regression: a File
// with WithContent("") or a WithSource pointing at a zero-byte file must
// still record content_b64:"" plus has_content:true (round-tripping through
// draftToOp/packageDraft), not the empty-op shape that would make apply
// mistake it for a record-time bug ("missing content_b64 and blob").
func TestRecordPlanEmptyFileContentSetsHasContent(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	contentPath := filepath.Join(dir, "empty-content.conf")
	sourcePath := filepath.Join(dir, "empty-source.conf")
	emptySrc := filepath.Join(dir, "empty.src")
	if err := os.WriteFile(emptySrc, nil, 0o640); err != nil {
		t.Fatal(err)
	}

	Task("empty_files", "", func() {
		File(contentPath, options.WithContent(""))
		File(sourcePath, options.WithSource(emptySrc))
	})
	ops, err := RecordPlan("empty-plan", dir, "empty_files")
	if err != nil {
		t.Fatal(err)
	}

	var contentOp, sourceOp *plan.Op
	for i := range ops {
		op := &ops[i]
		if op.Op != plan.KindFile {
			continue
		}
		switch op.Path {
		case contentPath:
			contentOp = op
		case sourcePath:
			sourceOp = op
		}
	}
	if contentOp == nil {
		t.Fatal("missing op for WithContent(\"\") file")
	}
	if contentOp.ContentB64 != "" || !contentOp.HasContent {
		t.Fatalf("WithContent(\"\") op = %+v, want content_b64=\"\" has_content=true", contentOp)
	}
	if sourceOp == nil {
		t.Fatal("missing op for empty WithSource file")
	}
	if sourceOp.ContentB64 != "" || !sourceOp.HasContent {
		t.Fatalf("empty WithSource op = %+v, want content_b64=\"\" has_content=true", sourceOp)
	}
}

// TestApplyPlanEmptyFileResourceSucceeds is the k5 regression at the
// apply boundary: recording and then applying a File with WithContent("") or
// an empty WithSource file must produce an empty destination file instead of
// failing with "missing content_b64 and blob".
func TestApplyPlanEmptyFileResourceSucceeds(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	contentPath := filepath.Join(dir, "empty-content.conf")
	sourcePath := filepath.Join(dir, "empty-source.conf")
	emptySrc := filepath.Join(dir, "empty.src")
	if err := os.WriteFile(emptySrc, nil, 0o640); err != nil {
		t.Fatal(err)
	}

	Task("empty_files_apply", "", func() {
		File(contentPath, options.WithContent(""), options.WithMode(0o640))
		File(sourcePath, options.WithSource(emptySrc), options.WithMode(0o640))
	})
	ops, err := RecordPlan("empty-apply-plan", dir, "empty_files_apply")
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyPlan(ops, dir); err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}

	for _, path := range []string{contentPath, sourcePath} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		if len(got) != 0 {
			t.Fatalf("content of %s = %q, want empty", path, got)
		}
	}
}

// TestRecordPlanValidationCodecApply exercises the complete public plan
// path for WithValidation: RecordPlan, JSON codec, and destination apply. It
// also proves the typed CandidatePath token survives the codec rather than
// accidentally turning into a controller-side pathname.
func TestRecordPlanValidationCodecApply(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	target := filepath.Join(dir, "service.conf")
	marker := filepath.Join(dir, "validator-record")
	t.Setenv("GONF_API_VALIDATION_HELPER", "1")
	validatorArgs := List("-test.run=^TestPlanValidationHelperProcess$", "validated-content", marker, options.CandidatePath)
	Task("validated_file", "", func() {
		File(target, options.WithContent("from recorded plan\n"), options.WithValidation(os.Args[0], validatorArgs))
	})

	ops, err := RecordPlan("validated-plan", dir, "validated_file")
	if err != nil {
		t.Fatal(err)
	}
	var fileOp *plan.Op
	for i := range ops {
		if ops[i].Op == plan.KindFile {
			fileOp = &ops[i]
			break
		}
	}
	if fileOp == nil {
		t.Fatal("recorded plan has no file op")
	}
	if fileOp.ValidationBin != os.Args[0] || len(fileOp.ValidationArgs) != len(validatorArgs) || fileOp.ValidationArgs[len(fileOp.ValidationArgs)-1] != options.CandidatePath || !fileOp.HasContent {
		t.Fatalf("recorded validation op = %+v", fileOp)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyPlan(decoded, dir); err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from recorded plan\n" {
		t.Fatalf("target content = %q", got)
	}
	candidatePath, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("validator did not record its candidate: %v", err)
	}
	candidate := strings.TrimSpace(string(candidatePath))
	if candidate == "" || candidate == target || filepath.Dir(candidate) != dir {
		t.Fatalf("validator candidate = %q, target = %q", candidate, target)
	}
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatalf("candidate was not cleaned up: %v", err)
	}
}

// TestPlanValidationHelperProcess is invoked as a direct argv validator by
// TestRecordPlanValidationCodecApply. Keeping the assertion in Go makes the
// test portable across Linux and BSD destinations.
func TestPlanValidationHelperProcess(t *testing.T) {
	if os.Getenv("GONF_API_VALIDATION_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg != "validated-content" {
			continue
		}
		if i+2 >= len(os.Args) {
			t.Fatal("validation helper missing marker or candidate")
		}
		candidate := os.Args[i+2]
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat candidate: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("candidate mode = %v, want 0600", info.Mode().Perm())
		}
		content, err := os.ReadFile(candidate)
		if err != nil {
			t.Fatalf("read candidate: %v", err)
		}
		if string(content) != "from recorded plan\n" {
			t.Fatalf("candidate content = %q", content)
		}
		if err := os.WriteFile(os.Args[i+1], []byte(candidate+"\n"), 0o600); err != nil {
			t.Fatalf("record candidate: %v", err)
		}
		return
	}
	t.Fatal("validation helper invocation missing marker")
}
