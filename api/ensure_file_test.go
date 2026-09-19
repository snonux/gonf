package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
)

func TestEnsureFileRecordsPlanAndMatchesDirectApply(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.ResetForTest()
	})

	direct := filepath.Join(t.TempDir(), "daily.local")
	remote := filepath.Join(t.TempDir(), "daily.local")
	for _, path := range []string{direct, remote} {
		if err := os.WriteFile(path, []byte("existing content\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.EnsurePresent(direct, options.WithMode(0o644)); err != nil {
		t.Fatalf("direct EnsurePresent: %v", err)
	}

	Task("ensure_file", "", func() {
		EnsureFile(remote, options.WithMode(0o644))
	})
	ops, err := RecordPlan("ensure-file", "", "ensure_file")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if len(ops) != 2 || ops[1].Op != plan.KindEnsureFile {
		t.Fatalf("ops = %#v, want ensure_file", ops)
	}
	if err := plan.Apply(ops, plan.Facts{}, ""); err != nil {
		t.Fatalf("plan.Apply: %v", err)
	}

	for _, path := range []string{direct, remote} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "existing content\n" {
			t.Fatalf("%s content = %q, want preserved", path, got)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("%s mode = %v, want 0644", path, info.Mode().Perm())
		}
	}
}
