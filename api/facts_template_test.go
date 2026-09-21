package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/resource/options"
)

const factsTmpl = "goos={{.Gonf.GOOS}} profile={{.Gonf.Profile}} host={{.Gonf.Hostname}}\n"

// recordAndApplyFactsTemplates records one task holding a WithSource Dir
// (a sync_dir op whose tree contains x.conf.tmpl) and a single File sourced
// from the same .tmpl, applies the plan through ApplyPlan (DetectFacts, so
// SetProfileOverride applies), and returns both rendered files' contents.
func recordAndApplyFactsTemplates(t *testing.T) (synced, single string) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	tmpl := filepath.Join(src, "x.conf.tmpl")
	if err := os.WriteFile(tmpl, []byte(factsTmpl), 0o600); err != nil {
		t.Fatal(err)
	}
	syncDst := filepath.Join(root, "out", "synced")
	fileDst := filepath.Join(root, "out", "single.conf")
	Task("j62_facts", "", func() {
		Dir(syncDst, options.WithSource(src))
		File(fileDst, options.WithSource(tmpl))
	})
	planDir := filepath.Join(root, "plan")
	ops, err := RecordPlan("j62-facts", planDir, "j62_facts")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if err := ApplyPlan(ops, planDir); err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	return readString(t, filepath.Join(syncDst, "x.conf")), readString(t, fileDst)
}

func readString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestApplyPlanSyncDirTemplateHonorsProfileOverride pins j62 end to end: the
// CLI -profile override (SetProfileOverride) must reach a .tmpl inside a
// synced tree exactly as it reaches a single file op in the same apply.
// Before the fix the synced entry re-detected the host profile instead.
func TestApplyPlanSyncDirTemplateHonorsProfileOverride(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	SetProfileOverride("j62-override")

	synced, single := recordAndApplyFactsTemplates(t)
	f := DetectFacts()
	want := "goos=" + f.GOOS + " profile=j62-override host=" + f.Hostname + "\n"
	if single != want {
		t.Fatalf("single file rendered %q, want %q", single, want)
	}
	if synced != single {
		t.Errorf("sync_dir entry rendered %q, single file rendered %q", synced, single)
	}
}

// TestApplyPlanSyncDirTemplateWithoutOverride is the negative control:
// without an override both paths render the detected host profile, so the
// fix does not change output for recipes that never use -profile.
func TestApplyPlanSyncDirTemplateWithoutOverride(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	synced, single := recordAndApplyFactsTemplates(t)
	f := DetectFacts()
	want := "goos=" + f.GOOS + " profile=" + f.Profile + " host=" + f.Hostname + "\n"
	if single != want || synced != want {
		t.Errorf("rendered sync_dir %q / single %q, want both %q", synced, single, want)
	}
}
