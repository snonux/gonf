package api

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
)

func TestRecordPlanFilteredSources(t *testing.T) {
	for _, glob := range []bool{false, true} {
		for _, deferred := range []bool{false, true} {
			t.Run(fmtSourceCase(glob, deferred), func(t *testing.T) {
				ResetForTest()
				t.Cleanup(ResetForTest)
				src := t.TempDir()
				mustWrite(t, filepath.Join(src, "keep"), []byte("public bytes"))
				mustWrite(t, filepath.Join(src, "secret"), []byte("excluded secret bytes"))
				out := filepath.Join(t.TempDir(), "out")
				tmp := t.TempDir()
				t.Setenv("TMPDIR", tmp)
				Task("filtered", "", func() {
					if glob {
						SyncDir("/gonfy/lodge", filepath.Join(src, "*"), WithExclude("secret"), WithPrune)
					} else {
						Dir("/gonfy/lodge", WithSource(src), WithExclude("secret"), WithPrune)
					}
				})
				var ops []plan.Op
				var err error
				if deferred {
					var d *DeferredPlan
					d, err = RecordPlanDeferred("filtered", out, "filtered")
					if err != nil {
						t.Fatal(err)
					}
					ops = d.Ops
					if _, err := os.Lstat(out); !os.IsNotExist(err) {
						t.Fatalf("output created before commit: %v", err)
					}
					// Commit must use the recorded snapshot, even if sources change.
					mustWrite(t, filepath.Join(src, "keep"), []byte("changed after record"))
					err = d.CommitBlobs()
				} else {
					ops, err = RecordPlan("filtered", out, "filtered")
				}
				if err != nil {
					t.Fatal(err)
				}
				var sync plan.Op
				for _, op := range ops {
					if op.Op == plan.KindSyncDir {
						sync = op
					}
				}
				payload := plan.PayloadOf[plan.SyncDirPayload](sync)
				if !sync.Prune || payload.Glob != glob || !reflect.DeepEqual(payload.Exclude, []string{"secret"}) {
					t.Fatalf("lost sync settings: %+v %+v", sync, payload)
				}
				blob, err := plan.Resolve(out, sync.Blob)
				if err != nil {
					t.Fatal(err)
				}
				entries, err := os.ReadDir(blob)
				if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
					t.Fatalf("blob entries: %v %v", entries, err)
				}
				data, err := os.ReadFile(filepath.Join(blob, "keep"))
				if err != nil || string(data) != "public bytes" {
					t.Fatalf("snapshot bytes: %q %v", data, err)
				}
				stages, err := os.ReadDir(tmp)
				if err != nil || len(stages) != 0 {
					t.Fatalf("staging not cleaned: %v %v", stages, err)
				}
			})
		}
	}
}

func TestRecordPlanFilteredSourceFailureDoesNotCommit(t *testing.T) {
	for _, failure := range []string{"missing-tree", "invalid-glob", "invalid-exclude", "late-dependency"} {
		t.Run(failure, func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			src := t.TempDir()
			mustWrite(t, filepath.Join(src, "keep"), []byte("private staged bytes"))
			out := filepath.Join(t.TempDir(), "out")
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			Task("filtered", "", func() {
				Dir("/gonfy/first", WithSource(src), WithExclude("secret"))
				switch failure {
				case "missing-tree":
					Dir("/gonfy/bad", WithSource(filepath.Join(src, "missing")), WithExclude("secret"))
				case "invalid-glob":
					SyncDir("/gonfy/bad", filepath.Join(src, "["), WithExclude("secret"))
				case "invalid-exclude":
					Dir("/gonfy/bad", WithSource(src), WithExclude("["))
				case "late-dependency":
					File("/gonfy/bad", WithContent("x"), DependsOn(unregisteredDep("File[/missing]")))
				}
			})
			_, err := RecordPlan("filtered", out, "filtered")
			if err == nil || strings.Contains(err.Error(), "does not support exclusions") {
				t.Fatalf("want actual source/validation error, got %v", err)
			}
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatalf("failed recording committed output: %v", err)
			}
			stages, err := os.ReadDir(tmp)
			if err != nil || len(stages) != 0 {
				t.Fatalf("failed staging not cleaned: %v %v", stages, err)
			}
		})
	}
}

func fmtSourceCase(glob, deferred bool) string {
	source, store := "tree", "staged"
	if glob {
		source = "glob"
	}
	if deferred {
		store = "deferred"
	}
	return source + "/" + store
}
