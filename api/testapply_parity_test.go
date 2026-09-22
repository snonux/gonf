package api

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// TestTestapplyOpsMatchApply pins that internal/testapply, which the
// resource/<kind> packages' own tests apply through (they cannot import api),
// lowers registered drafts to the same plan ops as Apply does: one op per
// draft, identical apart from the blob refs (each packager names its blobs
// itself; both must stage the same sources as blobs). A drift here means the
// kind tests no longer exercise the production path.
func TestTestapplyOpsMatchApply(t *testing.T) {
	resource.ResetRepository()
	t.Cleanup(resource.ResetRepository)
	registerParityRecipe(t)
	drafts := resource.RegisteredPlanDrafts()

	want, err := packageApplyOps(drafts, plan.NewStore(t.TempDir()))
	if err != nil {
		t.Fatalf("packageApplyOps: %v", err)
	}
	got, err := testapply.Ops(drafts, plan.NewStore(t.TempDir()))
	if err != nil {
		t.Fatalf("testapply.Ops: %v", err)
	}
	if len(got) != len(want) || len(want) != len(drafts)+1 {
		t.Fatalf("ops = %d (testapply) and %d (Apply), want %d", len(got), len(want), len(drafts)+1)
	}
	for i := 1; i < len(want); i++ {
		w, g := want[i], got[i]
		if (w.Blob == "") != (g.Blob == "") {
			t.Errorf("op %s: blob %q (testapply) vs %q (Apply)", w.ID, g.Blob, w.Blob)
		}
		w.Blob, g.Blob = "", ""
		if !reflect.DeepEqual(g, w) {
			t.Errorf("op %d differs:\ntestapply %+v\nApply     %+v", i, g, w)
		}
	}
}

// registerParityRecipe registers one resource of each packaging shape: inline
// content, a small (inline) and a large (blob) file source, a tree and a glob
// sync, plus kinds without sources and a dependency edge.
func registerParityRecipe(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(src, "small.conf")
	large := filepath.Join(dir, "large.bin")
	if err := os.WriteFile(small, []byte("small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(large, []byte(strings.Repeat("x", plan.MaxInlineContent+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	content := File(filepath.Join(out, "content"), options.WithContent("hello\n"), options.WithMode(0o600))
	File(filepath.Join(out, "small"), options.WithSource(small), options.DependsOn(content))
	File(filepath.Join(out, "large"), options.WithSource(large))
	Dir(filepath.Join(out, "tree"), options.WithSource(src), options.WithPrune)
	Dir(filepath.Join(out, "glob"), options.WithSourceGlob(filepath.Join(src, "*.conf")))
	Link(filepath.Join(out, "link"), options.WithSymlink(small))
	Command("true", nil, options.DependsOn(content))
}
