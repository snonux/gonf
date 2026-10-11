package api

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/snonux/gonf/plan"
)

func TestDirectoryExclusionsPackageOnlyIncludedBytes(t *testing.T) {
	for _, glob := range []bool{false, true} {
		t.Run("record", func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			src, dst := t.TempDir(), t.TempDir()
			for name, data := range map[string]string{"keep": "public bytes", "secret": "must never reach the blob", "old.tmp": "excluded bytes"} {
				if err := os.WriteFile(filepath.Join(src, name), []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dst, "secret"), []byte("unmanaged"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dst, "trash"), []byte("stale"), 0o644); err != nil {
				t.Fatal(err)
			}
			patterns := List("secret", "*.tmp")
			option := WithExclude(patterns...)
			patterns[0] = "keep"
			Task("sync", "", func() {
				if glob {
					SyncDir(dst, filepath.Join(src, "*"), option, WithPrune)
				} else {
					Dir(dst, WithSource(src), option, WithPrune)
				}
			})
			store := plan.NewMemoryStore()
			ops, err := RecordPlanTo("exclude", store, "sync")
			if err != nil {
				t.Fatal(err)
			}
			if ops[0].Version != plan.VersionSyncDirExclude {
				t.Fatalf("header=%d", ops[0].Version)
			}
			var sync plan.Op
			for _, op := range ops {
				if op.Op == plan.KindSyncDir {
					sync = op
				}
			}
			if !reflect.DeepEqual(plan.PayloadOf[plan.SyncDirPayload](sync).Exclude, List("secret", "*.tmp")) {
				t.Fatalf("patterns lost: %+v", sync)
			}
			entries, ok := store.TreeBlob(sync.Blob)
			if !ok || len(entries) != 1 || entries[0].Rel != "keep" || string(entries[0].Data) != "public bytes" {
				t.Fatalf("excluded bytes entered blob: %+v", entries)
			}
			planDir := t.TempDir()
			disk := plan.NewStore(planDir)
			for _, ref := range store.Refs() {
				entries, ok := store.TreeBlob(ref)
				if !ok {
					t.Fatal("expected tree")
				}
				if _, err := disk.WriteEntries(filepath.Base(ref), entries); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := plan.EncodePlan(ops)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := plan.DecodePlanBytes(raw)
			if err != nil {
				t.Fatal(err)
			}
			for run := 0; run < 2; run++ {
				if err := ApplyPlan(decoded, planDir); err != nil {
					t.Fatal(err)
				}
			}
			got, err := os.ReadFile(filepath.Join(dst, "secret"))
			if err != nil || string(got) != "unmanaged" {
				t.Fatalf("excluded path changed: %q %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(dst, "trash")); !os.IsNotExist(err) {
				t.Fatalf("stale not pruned: %v", err)
			}
		})
	}
}
