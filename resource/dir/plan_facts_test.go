package dir

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// factsTemplate exercises every {{.Gonf.*}} fact so a divergence in any one
// of them between the sync_dir and single-file paths fails the comparison.
const factsTemplate = "goos={{.Gonf.GOOS}} profile={{.Gonf.Profile}} host={{.Gonf.Hostname}}\n"

// planOnlyFacts deliberately match nothing the local host could detect
// (file.Ensure's fallback), so a sync_dir entry that ignored the plan's
// facts cannot pass by coincidence.
var planOnlyFacts = plan.Facts{GOOS: "plan-goos", Profile: "PLANFACT", Hostname: "plan-host"}

// factsPlan returns a plan with one sync_dir op (from blobRef, into
// syncDst) and one single-file op rendering factsTemplate into fileDst.
func factsPlan(blobRef, syncDst, fileDst string) []plan.Op {
	return []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "j62"},
		{Op: plan.KindSyncDir, Path: syncDst, Blob: blobRef, Mode: "0700", Payload: plan.SyncDirPayload{FileMode: "0600"}},
		{
			Op:         plan.KindFile,
			Path:       fileDst,
			ContentB64: base64.StdEncoding.EncodeToString([]byte(factsTemplate)),
			HasContent: true,
			Template:   true,
			Mode:       "0600",
		},
	}
}

// writeFactsSource creates a source tree holding factsTemplate as
// x.conf.tmpl (plus a nested copy), and returns the tree root.
func writeFactsSource(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"x.conf.tmpl", "sub/y.conf.tmpl"} {
		if err := os.WriteFile(filepath.Join(src, rel), []byte(factsTemplate), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// applyFactsPlan packages blob (via pack) into a fresh plan dir, applies
// factsPlan with facts, and returns the single-file op's rendered content
// plus the sync_dir destination root.
func applyFactsPlan(t *testing.T, facts plan.Facts, pack func(*plan.Store) (string, error)) (fileGot, syncDst string) {
	t.Helper()
	resource.ResetRepository()
	planDir := t.TempDir()
	ref, err := pack(plan.NewStore(planDir))
	if err != nil {
		t.Fatalf("package blob: %v", err)
	}
	out := t.TempDir()
	syncDst = filepath.Join(out, "synced")
	fileDst := filepath.Join(out, "single.conf")
	if err := plan.Apply(factsPlan(ref, syncDst, fileDst), facts, planDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return readFile(t, fileDst), syncDst
}

// TestSyncDirTemplateUsesPlanFacts is the j62 regression: a .tmpl inside a
// synced tree must render {{.Gonf.*}} from the apply's plan facts (which
// carry the -profile override), byte-identical to a single-file op with the
// same template text in the same apply. Before the fix the sync_dir entry
// re-detected facts locally (e.g. "profile=fedora") while the file op
// rendered "profile=PLANFACT".
func TestSyncDirTemplateUsesPlanFacts(t *testing.T) {
	src := writeFactsSource(t)
	fileGot, syncDst := applyFactsPlan(t, planOnlyFacts, func(s *plan.Store) (string, error) {
		return s.WriteTree("tree", src)
	})

	want := "goos=plan-goos profile=PLANFACT host=plan-host\n"
	if fileGot != want {
		t.Fatalf("single-file op rendered %q, want %q", fileGot, want)
	}
	for _, rel := range []string{"x.conf", "sub/y.conf"} {
		if got := readFile(t, filepath.Join(syncDst, rel)); got != fileGot {
			t.Errorf("sync_dir %s rendered %q, single-file op rendered %q", rel, got, fileGot)
		}
	}
}

// TestSyncDirGlobTemplateUsesPlanFacts covers the glob flavor: its blob is a
// flattened directory of the matches, applied through the same sync_dir
// handler, so its .tmpl entries must honor the plan facts too.
func TestSyncDirGlobTemplateUsesPlanFacts(t *testing.T) {
	src := writeFactsSource(t)
	fileGot, syncDst := applyFactsPlan(t, planOnlyFacts, func(s *plan.Store) (string, error) {
		return s.WriteGlob("glob", filepath.Join(src, "*.tmpl"))
	})
	if got := readFile(t, filepath.Join(syncDst, "x.conf")); got != fileGot {
		t.Errorf("glob sync_dir rendered %q, single-file op rendered %q", got, fileGot)
	}
}

// TestSyncDirTemplateZeroPlanFacts pins that sync_dir entries take the plan
// facts verbatim even when they are empty, never falling back to local
// detection: consistency with the single-file op is the contract, not
// "non-empty facts".
func TestSyncDirTemplateZeroPlanFacts(t *testing.T) {
	src := writeFactsSource(t)
	fileGot, syncDst := applyFactsPlan(t, plan.Facts{}, func(s *plan.Store) (string, error) {
		return s.WriteTree("tree", src)
	})
	if want := "goos= profile= host=\n"; fileGot != want {
		t.Fatalf("single-file op rendered %q, want %q", fileGot, want)
	}
	if got := readFile(t, filepath.Join(syncDst, "x.conf")); got != fileGot {
		t.Errorf("sync_dir rendered %q, single-file op rendered %q", got, fileGot)
	}
}

// TestSyncDirPlanFactsDoNotLeakIntoDirectEnsure is the negative side: plan
// facts are per-apply state carried on the Dir, not a global, so a later
// direct (non-plan) Ensure of the same tree still renders the locally
// detected GOOS rather than the previous apply's plan facts.
func TestSyncDirPlanFactsDoNotLeakIntoDirectEnsure(t *testing.T) {
	src := writeFactsSource(t)
	applyFactsPlan(t, planOnlyFacts, func(s *plan.Store) (string, error) {
		return s.WriteTree("tree", src)
	})

	resource.ResetRepository()
	dst := filepath.Join(t.TempDir(), "direct")
	if err := Ensure(dst, opt.WithSource(src)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got := readFile(t, filepath.Join(dst, "x.conf"))
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "goos="+runtime.GOOS+" ") {
		t.Errorf("direct Ensure rendered %q, want local GOOS %q", got, runtime.GOOS)
	}
	if !strings.HasSuffix(got, " host="+host+"\n") {
		t.Errorf("direct Ensure rendered %q, want local hostname %q", got, host)
	}
	if strings.Contains(got, planOnlyFacts.Profile) {
		t.Errorf("direct Ensure rendered %q, leaked plan profile %q", got, planOnlyFacts.Profile)
	}
}
