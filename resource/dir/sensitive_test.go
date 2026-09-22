package dir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// fakeTreeSecret is synthetic secret material inside a synced template,
// spelled so a text/template parse error quotes it.
const fakeTreeSecret = "fakeTreeSecret4c1a"

// A sensitive synced tree writes its entries as sensitive files, so a
// template failure of one entry reports the step only, on the direct path
// (WithSensitive) and on the plan path (a sensitive sync_dir op); a plain
// tree keeps text/template's details.
func TestSensitiveSyncedTreeWithholdsEntryTemplateDetails(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "conf.tmpl"), []byte("key {{"+fakeTreeSecret+"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		apply     func(dst string, sensitive bool) error
		sensitive bool
	}{
		{"direct sensitive", ensureTree(src), true},
		{"direct plain", ensureTree(src), false},
		{"plan sensitive", syncTreeOp(src), true},
		{"plan plain", syncTreeOp(src), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetForTest()
			t.Cleanup(resource.ResetForTest)
			err := tt.apply(filepath.Join(t.TempDir(), "dst"), tt.sensitive)
			if err == nil {
				t.Fatal("broken template entry applied")
			}
			if leaked := strings.Contains(err.Error(), fakeTreeSecret); leaked == tt.sensitive {
				t.Fatalf("sensitive=%v: error quotes the template text = %v: %v", tt.sensitive, leaked, err)
			}
		})
	}
}

// ensureTree applies a direct Dir mirroring src, marked sensitive on demand.
func ensureTree(src string) func(string, bool) error {
	return func(dst string, sensitive bool) error {
		opts := []opt.DirOption{opt.WithSource(src)}
		if sensitive {
			opts = append(opts, opt.WithSensitive)
		}
		return Ensure(dst, opts...)
	}
}

// syncTreeOp applies what the sync_dir handler builds for an op whose blob
// tree resolved to src (syncDirOptions, then ensureWithPlanFacts),
// sensitive on demand.
func syncTreeOp(src string) func(string, bool) error {
	return func(dst string, sensitive bool) error {
		opts, err := syncDirOptions(plan.Op{Op: plan.KindSyncDir, Path: dst, Sensitive: sensitive}, src)
		if err != nil {
			return err
		}
		return ensureWithPlanFacts(dst, plan.Facts{}, opts...)
	}
}
