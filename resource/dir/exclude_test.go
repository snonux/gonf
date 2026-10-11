package dir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

func TestExclusionsDirectAndRegisteredParity(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, glob := range []bool{false, true} {
			t.Run("sync", func(t *testing.T) {
				resource.ResetForTest()
				t.Cleanup(resource.ResetForTest)
				src, dst := t.TempDir(), t.TempDir()
				for _, name := range []string{"empty", "sub", "private"} {
					if err := os.Mkdir(filepath.Join(src, name), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				for name, value := range map[string]string{"keep": "public", "drop.tmp": "exclude", "secret.tmpl": "private", "sub/keep": "nested", "private/hidden": "excluded"} {
					if err := os.WriteFile(filepath.Join(src, name), []byte(value), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink("missing", filepath.Join(src, "excluded-link")); err != nil {
					t.Fatal(err)
				}
				if !glob {
					if err := os.Symlink("keep", filepath.Join(src, "zz-link")); err != nil {
						t.Fatal(err)
					}
				}
				for _, dir := range []string{"private", "stale/keep"} {
					if err := os.MkdirAll(filepath.Join(dst, dir), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				for name, value := range map[string]string{"drop.tmp": "mine", "secret": "private destination", "stale/keep/local": "mine", "stale/trash": "remove", "trash": "remove", "private/local": "mine"} {
					if err := os.WriteFile(filepath.Join(dst, name), []byte(value), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				patterns := []string{"*.tmp", "private", "secret.tmpl", "excluded-link", "stale/keep"}
				exclusion := opt.WithExclude(patterns...)
				patterns[0] = "*"
				opts := []opt.DirOption{exclusion, opt.WithPrune, opt.WithMode(0o755), opt.WithFileMode(0o600)}
				if glob {
					opts = append(opts, opt.WithSourceGlob(filepath.Join(src, "*")))
				} else {
					opts = append(opts, opt.WithSource(src))
				}
				for run := 0; run < 3; run++ {
					if run == 2 {
						if err := os.WriteFile(filepath.Join(dst, "keep"), []byte("drift"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					resource.ResetForTest()
					if registered {
						Present(dst, opts...)
						if err := testapply.Apply(); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := Ensure(dst, opts...); err != nil {
							t.Fatal(err)
						}
					}
					for name, value := range map[string]string{"keep": "public", "drop.tmp": "mine", "secret": "private destination", "stale/keep/local": "mine", "private/local": "mine"} {
						data, err := os.ReadFile(filepath.Join(dst, name))
						if err != nil || string(data) != value {
							t.Fatalf("%s=%q err=%v", name, data, err)
						}
					}
					if _, err := os.Stat(filepath.Join(dst, "trash")); !os.IsNotExist(err) {
						t.Fatalf("trash not pruned: %v", err)
					}
					if !glob {
						if _, err := os.Stat(filepath.Join(dst, "stale/trash")); !os.IsNotExist(err) {
							t.Fatalf("nested trash not pruned: %v", err)
						}
						if _, err := os.Stat(filepath.Join(dst, "empty")); err != nil {
							t.Fatal(err)
						}
						raw, err := os.Readlink(filepath.Join(dst, "zz-link"))
						if err != nil || raw != "keep" {
							t.Fatalf("symlink=%s %v", raw, err)
						}
					}
					if run == 2 && !resource.AnyChanged("Directory["+dst+"]") {
						t.Fatal("included drift not corrected")
					}
					if run == 1 && resource.AnyChanged("Directory["+dst+"]") {
						t.Fatal("second sync changed contents")
					}
				}
			})
		}
	}
}

func TestExclusionsRefuseMisuse(t *testing.T) {
	for _, opts := range [][]opt.DirOption{
		{opt.WithExclude("["), opt.WithSource("/unused")},
		{opt.WithExclude("private")},
		{opt.WithExclude("private"), opt.WithSource("/unused"), opt.IsAbsent},
	} {
		t.Run("misuse", func(t *testing.T) {
			resource.ResetForTest()
			t.Cleanup(resource.ResetForTest)
			dst := filepath.Join(t.TempDir(), "new")
			Present(dst, opts...)
			if declerr.First() == nil || len(resource.RegisteredIDs()) != 0 {
				t.Fatal("misuse registered")
			}
			if err := Ensure(dst, opts...); err == nil {
				t.Fatal("Ensure accepted misuse")
			}
			if _, err := os.Stat(dst); !os.IsNotExist(err) {
				t.Fatalf("misuse wrote directory: %v", err)
			}
		})
	}
}

func TestExcludedDirectoryIsNotTraversed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	src, dst := t.TempDir(), t.TempDir()
	private := filepath.Join(src, "private")
	if err := os.Mkdir(private, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(private, 0o700) })
	if err := Ensure(dst, opt.WithSource(src), opt.WithExclude("private")); err != nil {
		t.Fatal(err)
	}
	Present(dst, opt.WithSource(src), opt.WithExclude("private"))
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(dst, opt.WithSource(src)); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("included unreadable dir accepted: %v", err)
	}
}

func TestExclusionsDryRunPreservesUnmanagedPaths(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "keep"), []byte("desired"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "private.tmpl"), []byte("private source"), 0o640); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"private": "mine", "keep": "drift", "stale": "remove"} {
		if err := os.WriteFile(filepath.Join(dst, name), []byte(data), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	resource.SetDryRun(true)
	if err := Ensure(dst, opt.WithSource(src), opt.WithExclude("private.tmpl"), opt.WithPrune); err != nil {
		t.Fatal(err)
	}
	if resource.AnyChanged("File[" + filepath.Join(dst, "private") + "]") {
		t.Fatal("excluded path predicted as changed")
	}
	if !resource.AnyChanged("File["+filepath.Join(dst, "keep")+"]") || !resource.AnyChanged("File["+filepath.Join(dst, "stale")+"]") {
		t.Fatal("managed changes not predicted")
	}
	for name, want := range map[string]string{"private": "mine", "keep": "drift", "stale": "remove"} {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil || string(got) != want {
			t.Fatalf("dry run wrote %s: %q %v", name, got, err)
		}
	}
}

func TestSyncHandlerFiltersAnUnfilteredBlob(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	src, dst, planDir := t.TempDir(), t.TempDir(), t.TempDir()
	for name, data := range map[string]string{"keep": "public", "private": "exclude"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dst, "private"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	blob, err := plan.NewStore(planDir).WriteTree("raw", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, patterns := range [][]string{{"["}, {"private"}} {
		op := plan.Op{Op: plan.KindSyncDir, Path: dst, Blob: blob, Prune: true, Payload: plan.SyncDirPayload{Exclude: patterns}}
		err := plan.Apply([]plan.Op{{Op: plan.KindPlan, Version: plan.VersionSyncDirExclude, ID: "raw"}, op}, plan.Facts{}, planDir)
		if patterns[0] == "[" {
			if err == nil {
				t.Fatal("invalid wire pattern accepted")
			}
			if _, err := os.Stat(filepath.Join(dst, "keep")); !os.IsNotExist(err) {
				t.Fatal("invalid pattern wrote managed file")
			}
		} else if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dst, "private"))
		if err != nil || string(got) != "mine" {
			t.Fatalf("unfiltered blob copied excluded bytes: %q %v", got, err)
		}
	}
}
