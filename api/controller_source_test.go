package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestOptionalControllerDir(t *testing.T) {
	for _, scenario := range []string{"missing", "directory", "symlink", "file", "unreadable", "symlink loop", "parent not directory"} {
		t.Run(scenario, func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			root := t.TempDir()
			path := filepath.Join(root, "source")
			want := false
			wantError := false
			switch scenario {
			case "directory", "symlink", "unreadable":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				want = true
				if scenario == "symlink" {
					link := filepath.Join(root, "link")
					if err := os.Symlink(path, link); err != nil {
						t.Fatal(err)
					}
					path = link
				}
				if scenario == "unreadable" {
					if os.Getuid() == 0 {
						t.Skip("root bypasses directory permissions")
					}
					if err := os.Chmod(path, 0); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
					want = false
					wantError = true
				}
			case "file", "parent not directory":
				if err := os.WriteFile(path, []byte("file"), 0o644); err != nil {
					t.Fatal(err)
				}
				wantError = true
				if scenario == "parent not directory" {
					path = filepath.Join(path, "child")
				}
			case "symlink loop":
				if err := os.Symlink(path, path); err != nil {
					t.Fatal(err)
				}
				wantError = true
			}
			if got := OptionalControllerDir(path, "Gonfy's optional calendar"); got != want {
				t.Fatalf("got %v want %v", got, want)
			}
			err := declerr.First()
			if (err != nil) != wantError {
				t.Fatalf("declaration error=%v", err)
			}
			if err != nil && (!strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "Gonfy's optional calendar")) {
				t.Fatalf("missing context: %v", err)
			}
			if scenario == "unreadable" && !errors.Is(err, os.ErrPermission) {
				t.Fatalf("permission cause lost: %v", err)
			}
			if len(resource.RegisteredIDs()) != 0 {
				t.Fatal("check registered a resource")
			}
		})
	}
}

func TestRequireControllerFile(t *testing.T) {
	for _, scenario := range []string{"missing", "file", "empty file", "symlink", "directory", "unreadable", "symlink loop", "parent not directory"} {
		t.Run(scenario, func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			root := t.TempDir()
			path := filepath.Join(root, "source")
			wantError := true
			switch scenario {
			case "file", "empty file", "symlink", "unreadable", "parent not directory":
				content := []byte("Gonfy's template")
				if scenario == "empty file" {
					content = nil
				}
				if err := os.WriteFile(path, content, 0o644); err != nil {
					t.Fatal(err)
				}
				wantError = false
				if scenario == "symlink" {
					link := filepath.Join(root, "link")
					if err := os.Symlink(path, link); err != nil {
						t.Fatal(err)
					}
					path = link
				}
				if scenario == "unreadable" {
					if os.Getuid() == 0 {
						t.Skip("root bypasses file permissions")
					}
					if err := os.Chmod(path, 0); err != nil {
						t.Fatal(err)
					}
					wantError = true
				}
				if scenario == "parent not directory" {
					path = filepath.Join(path, "child")
					wantError = true
				}
			case "directory":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink loop":
				if err := os.Symlink(path, path); err != nil {
					t.Fatal(err)
				}
			}
			got := RequireControllerFile(path, "Gonfy's banner", "Restore the lodge checkout")
			err := declerr.First()
			if (err != nil) != wantError {
				t.Fatalf("declaration error=%v", err)
			}
			if wantError {
				if got != "" {
					t.Fatalf("inert path=%q", got)
				}
				for _, s := range []string{path, "Gonfy's banner", "Restore the lodge checkout"} {
					if !strings.Contains(err.Error(), s) {
						t.Fatalf("missing %q: %v", s, err)
					}
				}
			} else if got != path {
				t.Fatalf("path=%q want %q", got, path)
			}
			if scenario == "missing" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing cause lost: %v", err)
			}
			if scenario == "unreadable" && !errors.Is(err, os.ErrPermission) {
				t.Fatalf("permission cause lost: %v", err)
			}
		})
	}
}

func TestControllerSourceCheckInputMisuse(t *testing.T) {
	for _, call := range []func(){
		func() { OptionalControllerDir("", "calendar") },
		func() { OptionalControllerDir("/tmp", "") },
		func() { RequireControllerFile("", "banner", "restore checkout") },
		func() { RequireControllerFile("/tmp", "", "restore checkout") },
		func() { RequireControllerFile("/tmp", "banner", "") },
	} {
		t.Run("misuse", func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			call()
			if declerr.First() == nil {
				t.Fatal("misuse accepted")
			}
		})
	}
}

func TestControllerSourceChecksWhileRecording(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	root := t.TempDir()
	source := filepath.Join(root, "banner.txt")
	dest := filepath.Join(root, "installed.txt")
	if err := os.WriteFile(source, []byte("Gonfy's banner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	Task("sources", "", func() {
		if OptionalControllerDir(filepath.Join(root, "missing"), "optional calendar") {
			t.Fatal("missing directory ran block")
		}
		if OptionalControllerDir("${HOME}", "lodge checkout") {
			src := RequireControllerFile("~/banner.txt", "lodge banner", "Restore banner.txt")
			if src != "" {
				File(dest, WithSource(src), WithMode(0o644))
			}
		}
	})
	ops, err := RecordPlan("sources", "", "sources")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.Op == plan.KindWhenBegin {
			t.Fatal("controller check emitted destination guard")
		}
	}
	// The recorded source is packaged already: checks are not re-run on apply.
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPlan(ops, ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "Gonfy's banner\n" {
		t.Fatalf("installed=%q error=%v", got, err)
	}
}

func TestControllerSourceFailureRefusesRecordingAndApply(t *testing.T) {
	for _, record := range []bool{false, true} {
		t.Run("refusal", func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			root := t.TempDir()
			missing := filepath.Join(root, "missing")
			dest := filepath.Join(root, "dest")
			body := func() {
				src := RequireControllerFile(missing, "Gonfy's banner", "Restore the checkout")
				if src != "" {
					File(dest, WithSource(src))
				}
				File(filepath.Join(root, "later"), WithContent("later declaration"))
			}
			if record {
				Task("sources", "", body)
				if _, err := RecordPlan("sources", "", "sources"); err == nil || !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("record error=%v", err)
				}
			} else {
				body()
				if err := Apply(); err == nil || !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("apply error=%v", err)
				}
			}
			for _, path := range []string{dest, filepath.Join(root, "later")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("failure wrote %s: %v", path, err)
				}
			}
		})
	}
}
