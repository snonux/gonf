package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/pkg"
)

func resourceSetDryRun(t *testing.T) {
	t.Helper()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
}
func header() Op {
	return Op{Op: KindPlan, Version: CurrentVersion, ID: "apply-test"}
}

func TestApplyWhenFactBranches(t *testing.T) {
	root := t.TempDir()
	created := filepath.Join(root, "created")
	skipped := filepath.Join(root, "skipped")

	opsTrue := []Op{
		header(),
		{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "linux"}}},
		{Op: KindEnsureDir, Path: created, Mode: "0750"},
		{Op: KindWhenEnd},
	}
	if err := Apply(opsTrue, Facts{GOOS: "linux"}, ""); err != nil {
		t.Fatalf("true branch: %v", err)
	}
	if _, err := os.Stat(created); err != nil {
		t.Fatalf("true branch should create dir: %v", err)
	}

	opsFalse := []Op{
		header(),
		{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "linux"}}},
		{Op: KindEnsureDir, Path: skipped, Mode: "0750"},
		{Op: KindWhenEnd},
	}
	if err := Apply(opsFalse, Facts{GOOS: "darwin"}, ""); err != nil {
		t.Fatalf("false branch: %v", err)
	}
	if _, err := os.Stat(skipped); !os.IsNotExist(err) {
		t.Fatalf("false branch must not create dir; got err=%v", err)
	}
}

func TestApplyWhenPathExistsBranches(t *testing.T) {
	root := t.TempDir()
	gate := filepath.Join(root, "gate")
	if err := os.Mkdir(gate, 0o750); err != nil {
		t.Fatal(err)
	}
	whenPresent := filepath.Join(root, "when-present")
	whenAbsent := filepath.Join(root, "when-absent")
	missingGate := filepath.Join(root, "no-such-gate")

	opsPresent := []Op{
		header(),
		{Op: KindWhenBegin, All: []Predicate{{PathExists: gate}}},
		{Op: KindEnsureDir, Path: whenPresent, Mode: "0750"},
		{Op: KindWhenEnd},
	}
	if err := Apply(opsPresent, Facts{}, ""); err != nil {
		t.Fatalf("path present: %v", err)
	}
	if _, err := os.Stat(whenPresent); err != nil {
		t.Fatalf("path present should create: %v", err)
	}

	opsAbsent := []Op{
		header(),
		{Op: KindWhenBegin, All: []Predicate{{PathExists: missingGate}}},
		{Op: KindEnsureDir, Path: whenAbsent, Mode: "0750"},
		{Op: KindWhenEnd},
	}
	if err := Apply(opsAbsent, Facts{}, ""); err != nil {
		t.Fatalf("path absent: %v", err)
	}
	if _, err := os.Stat(whenAbsent); !os.IsNotExist(err) {
		t.Fatalf("path absent must not create; got err=%v", err)
	}
}

func TestApplyNestedWhen(t *testing.T) {
	root := t.TempDir()
	innerOK := filepath.Join(root, "inner-ok")
	innerSkip := filepath.Join(root, "inner-skip")
	outerSkip := filepath.Join(root, "outer-skip")

	ops := []Op{
		header(),
		{Op: KindWhenBegin, All: []Predicate{{Fact: "profile", Eq: "fedora"}}},
		{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "linux"}}},
		{Op: KindEnsureDir, Path: innerOK, Mode: "0700"},
		{Op: KindWhenEnd},
		{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "windows"}}},
		{Op: KindEnsureDir, Path: innerSkip, Mode: "0700"},
		{Op: KindWhenEnd},
		{Op: KindWhenEnd},
		{Op: KindWhenBegin, All: []Predicate{{Fact: "profile", Eq: "rocky"}}},
		{Op: KindEnsureDir, Path: outerSkip, Mode: "0700"},
		{Op: KindWhenEnd},
	}
	if err := Apply(ops, Facts{GOOS: "linux", Profile: "fedora"}, ""); err != nil {
		t.Fatalf("nested: %v", err)
	}
	if _, err := os.Stat(innerOK); err != nil {
		t.Fatalf("nested true/true should create: %v", err)
	}
	if _, err := os.Stat(innerSkip); !os.IsNotExist(err) {
		t.Fatalf("nested true/false must not create")
	}
	if _, err := os.Stat(outerSkip); !os.IsNotExist(err) {
		t.Fatalf("outer false must not create")
	}
}

func TestApplySkipDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "link")
	dirPath := filepath.Join(root, "dir")
	// Pre-existing link that would be removed by NoLink if wrongly applied.
	stale := filepath.Join(root, "stale-link")
	if err := os.Symlink(target, stale); err != nil {
		t.Fatal(err)
	}

	ops := []Op{
		header(),
		{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "plan9"}}},
		{Op: KindEnsureDir, Path: dirPath, Mode: "0750"},
		{Op: KindLinkIfExists, Path: linkPath, Target: target},
		{Op: KindLinkIfExists, Path: stale, Target: filepath.Join(root, "missing")},
		{Op: KindWhenEnd},
	}
	if err := Apply(ops, Facts{GOOS: "linux"}, ""); err != nil {
		t.Fatalf("skip apply: %v", err)
	}
	if _, err := os.Stat(dirPath); !os.IsNotExist(err) {
		t.Fatal("skip must not create ensure_dir")
	}
	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Fatal("skip must not create link_if_exists")
	}
	if _, err := os.Lstat(stale); err != nil {
		t.Fatalf("skip must not remove existing link: %v", err)
	}
}

func TestApplyMismatchedWhenEnd(t *testing.T) {
	ops := []Op{
		header(),
		{Op: KindWhenEnd},
	}
	err := Apply(ops, Facts{}, "")
	if err == nil || !strings.Contains(err.Error(), "when_end without matching") {
		t.Fatalf("want mismatched end error, got %v", err)
	}
}

func TestApplyUnclosedWhenBegin(t *testing.T) {
	ops := []Op{
		header(),
		{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "linux"}}},
	}
	err := Apply(ops, Facts{GOOS: "linux"}, "")
	if err == nil || !strings.Contains(err.Error(), "unclosed when_begin") {
		t.Fatalf("want unclosed error, got %v", err)
	}
}

func TestApplyLinkIfExistsBranches(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "notes")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	linkOK := filepath.Join(root, "QuickEdit-Notes")
	linkGone := filepath.Join(root, "QuickEdit-Missing")
	// Existing path that should be removed when target is absent.
	if err := os.Symlink(target, linkGone); err != nil {
		t.Fatal(err)
	}

	opsPresent := []Op{
		header(),
		{Op: KindLinkIfExists, Path: linkOK, Target: target},
	}
	if err := Apply(opsPresent, Facts{}, ""); err != nil {
		t.Fatalf("target present: %v", err)
	}
	got, err := os.Readlink(linkOK)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("symlink = %q, want %q", got, target)
	}

	opsAbsent := []Op{
		header(),
		{Op: KindLinkIfExists, Path: linkGone, Target: filepath.Join(root, "nope")},
	}
	if err := Apply(opsAbsent, Facts{}, ""); err != nil {
		t.Fatalf("target absent: %v", err)
	}
	if _, err := os.Lstat(linkGone); !os.IsNotExist(err) {
		t.Fatalf("target absent should remove path; err=%v", err)
	}
}

func TestApplyEnsureDir(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a", "b")
	ops := []Op{
		header(),
		{Op: KindEnsureDir, Path: path, Mode: "0700"},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("ensure_dir: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o, want 0700", info.Mode().Perm())
	}
}

func TestApplyHostnameContainsAndConjunctive(t *testing.T) {
	root := t.TempDir()
	okPath := filepath.Join(root, "ok")
	failPath := filepath.Join(root, "fail")

	opsOK := []Op{
		header(),
		{
			Op: KindWhenBegin,
			All: []Predicate{
				{Fact: "goos", Eq: "linux"},
				{Fact: "hostname_contains", Eq: "rocky"},
			},
		},
		{Op: KindEnsureDir, Path: okPath, Mode: "0750"},
		{Op: KindWhenEnd},
	}
	if err := Apply(opsOK, Facts{GOOS: "linux", Hostname: "my-rocky-box"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(okPath); err != nil {
		t.Fatal(err)
	}

	opsFail := []Op{
		header(),
		{
			Op: KindWhenBegin,
			All: []Predicate{
				{Fact: "goos", Eq: "linux"},
				{Fact: "hostname_contains", Eq: "rocky"},
			},
		},
		{Op: KindEnsureDir, Path: failPath, Mode: "0750"},
		{Op: KindWhenEnd},
	}
	if err := Apply(opsFail, Facts{GOOS: "linux", Hostname: "fedora-laptop"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(failPath); !os.IsNotExist(err) {
		t.Fatal("conjunctive false must skip")
	}
}

func TestApplyRejectsBadHeader(t *testing.T) {
	err := Apply([]Op{{Op: KindEnsureDir, Path: "/tmp/x"}}, Facts{}, "")
	if err == nil {
		t.Fatal("expected header validation error")
	}
}

func TestApplyLinkDirCommandFileLines(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "link")
	dirPath := filepath.Join(root, "dir")
	filePath := filepath.Join(root, "file.txt")
	marker := filepath.Join(root, "ran")

	ops := []Op{
		header(),
		{Op: KindLink, Path: linkPath, Symlink: target},
		{Op: KindDir, Path: dirPath, Mode: "0700"},
		{Op: KindFile, Path: filePath, AddLine: "hello"},
		{
			Op:   KindCommand,
			Bin:  "touch",
			Args: []string{marker},
			Name: "touch-marker",
		},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("symlink = %q, want %q", got, target)
	}
	if st, err := os.Stat(dirPath); err != nil || !st.IsDir() {
		t.Fatalf("dir: %v %#v", err, st)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("file content %q", data)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("command should create marker: %v", err)
	}
}

func TestApplyCommandUnlessSkips(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "should-not-exist")
	ops := []Op{
		header(),
		{
			Op:   KindCommand,
			Bin:  "touch",
			Args: []string{marker},
			Unless: &Guard{
				Bin:  "true",
				Args: nil,
			},
		},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unless true should skip touch")
	}
}

func TestApplyPackageDryRun(t *testing.T) {
	resourceSetDryRun(t)
	pkg.SetDetectPackageManagerForTest(func() (string, error) { return "dnf", nil })
	t.Cleanup(pkg.ResetDetectPackageManagerForTest)

	ops := []Op{
		header(),
		{Op: KindPackage, Name: "tig"},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("dry-run package: %v", err)
	}
}

func TestApplyFileLineRemove(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f")
	if err := os.WriteFile(path, []byte("keep\ndrop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := []Op{
		header(),
		{Op: KindFile, Path: path, RemoveLine: "drop"},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "drop") {
		t.Fatalf("remove_line failed: %q", data)
	}
	if !strings.Contains(string(data), "keep") {
		t.Fatalf("keep missing: %q", data)
	}
}
