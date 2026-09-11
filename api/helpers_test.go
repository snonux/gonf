package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

func TestExpandAndHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/gonf-home-test")
	if got := Expand("~"); got != "/tmp/gonf-home-test" {
		t.Fatalf("Expand(~) = %q", got)
	}
	if got := Expand("~/foo/bar"); got != "/tmp/gonf-home-test/foo/bar" {
		t.Fatalf("Expand(~/foo/bar) = %q", got)
	}
	if got := Home(".config", "helix"); got != "/tmp/gonf-home-test/.config/helix" {
		t.Fatalf("Home = %q", got)
	}
}

func TestAndOr(t *testing.T) {
	linux := func(f Facts) bool { return f.GOOS == "linux" }
	fedora := ProfileIs("fedora")

	if !And(linux, fedora)(Facts{GOOS: "linux", Profile: "fedora"}) {
		t.Fatal("And should pass")
	}
	if And(linux, fedora)(Facts{GOOS: "darwin", Profile: "fedora"}) {
		t.Fatal("And should fail on darwin")
	}
	if !Or(linux, fedora)(Facts{GOOS: "darwin", Profile: "fedora"}) {
		t.Fatal("Or should pass on fedora")
	}
	if Or(linux, fedora)(Facts{GOOS: "darwin", Profile: "rocky"}) {
		t.Fatal("Or should fail")
	}
}

func TestAggregate(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")

	Task("demo_a", "", func() {
		File(a, options.WithContent("a"))
	})
	Aggregate("demo", "all", "^demo_")

	Activate(Facts{})
	if err := Run("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDir(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	dir := t.TempDir()
	existing := filepath.Join(dir, "already")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}

	EnsureDir(existing, options.WithMode(0o700))
	missing := filepath.Join(dir, "newdir")
	EnsureDir(missing, options.WithMode(0o700))

	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(missing)
	if err != nil || !info.IsDir() {
		t.Fatalf("missing dir: %v", err)
	}
}

func TestLinkIfExistsAndSymlinkMap(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "links")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}

	SymlinkMap(parent,
		"ok", target,
		"missing", filepath.Join(dir, "nope"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(parent, "ok")); err != nil {
		t.Fatal("ok link missing")
	}
	if _, err := os.Lstat(filepath.Join(parent, "missing")); !os.IsNotExist(err) {
		t.Fatal("missing link should be absent")
	}
}

func TestSyncDirAndInstallFile(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "dst")
	srcFile := filepath.Join(srcDir, "a.conf")
	if err := os.WriteFile(srcFile, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	SyncDir(dstDir, filepath.Join(srcDir, "*"))
	outFile := filepath.Join(t.TempDir(), "out.txt")
	InstallFile(outFile, srcFile)

	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "a.conf")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outFile); err != nil {
		t.Fatal(err)
	}
}

func TestGitGlobalRegisters(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	GitGlobal("user.email", "test@example.com")
	// Smoke: Apply may fail without git; just ensure a resource was registered.
	// Run Apply in dry-run to avoid mutating real git config.
	resource.SetDryRun(true)
	defer resource.SetDryRun(false)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
}
