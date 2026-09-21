package plan

import (
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

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

// A profile predicate lowered from WhenProfile(a, b) (multiple profiles)
// carries In instead of Eq — it must match either profile and reject a
// third, exactly like an OR of two single-profile guards would.
func TestApplyWhenProfileInMatchesEitherValue(t *testing.T) {
	newOps := func(target string) []Op {
		return []Op{
			header(),
			{Op: KindWhenBegin, All: []Predicate{{Fact: "profile", In: []string{"fedora", "rocky"}}}},
			{Op: KindEnsureDir, Path: target, Mode: "0700"},
			{Op: KindWhenEnd},
		}
	}

	for _, tc := range []struct {
		profile string
		want    bool
	}{
		{"fedora", true},
		{"rocky", true},
		{"debian", false},
	} {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := Apply(newOps(target), Facts{GOOS: "linux", Profile: tc.profile}, ""); err != nil {
			t.Fatalf("profile=%s: %v", tc.profile, err)
		}
		_, err := os.Stat(target)
		got := err == nil
		if got != tc.want {
			t.Fatalf("profile=%s: created=%v want=%v (stat err=%v)", tc.profile, got, tc.want, err)
		}
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

// TestApplySortsDepsBeforeDependents pins the plan-path dependency contract:
// an op recorded before its DependsOn target must apply after it, mirroring
// the repository path's topological order. Both commands append to one log,
// so the file content is the apply-order assertion.
func TestApplySortsDepsBeforeDependents(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "order.log")

	ops := []Op{
		header(),
		{
			Op:   KindCommand,
			Bin:  "sh",
			Args: []string{"-c", "echo B >> " + log},
			ID:   "Command[b]",
			Deps: []string{"Command[a]"},
		},
		{
			Op:   KindCommand,
			Bin:  "sh",
			Args: []string{"-c", "echo A >> " + log},
			ID:   "Command[a]",
		},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "A\nB\n"; got != want {
		t.Fatalf("apply order = %q, want %q (dependency Command[a] must run first)", got, want)
	}
}

// TestApplyDepFreeOrderPreserved pins that dep-free plans keep recorded
// order: recorded order is the contract whenever no deps are present, so
// existing plans apply exactly as before the deps field existed.
func TestApplyDepFreeOrderPreserved(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "order.log")

	ops := []Op{
		header(),
		{Op: KindCommand, Bin: "sh", Args: []string{"-c", "echo one >> " + log}, ID: "Command[one]"},
		{Op: KindCommand, Bin: "sh", Args: []string{"-c", "echo two >> " + log}, ID: "Command[two]"},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "one\ntwo\n"; got != want {
		t.Fatalf("dep-free plan reordered: %q, want %q", got, want)
	}
}

// TestApplyLineNumbersFollowRecordedOrder pins that apply-time errors name
// the RECORDED line even when dependency sorting moved the op later in the
// execution order.
func TestApplyLineNumbersFollowRecordedOrder(t *testing.T) {
	ops := []Op{
		header(),
		{Op: KindFile, Path: "/tmp/dep-line-num", Deps: []string{"Command[a]"}}, // line 2: missing content
		{Op: KindCommand, Bin: "true", ID: "Command[a]"},
	}
	err := Apply(ops, Facts{}, "")
	if err == nil {
		t.Fatal("expected file validation error")
	}
	if !strings.Contains(err.Error(), "plan: apply line 2:") {
		t.Fatalf("error must name the recorded line 2, got: %v", err)
	}
}

// TestApplyDependencyCycle pins the repository-path behaviour for cycles on
// the plan path: a circular dependency fails the apply before any mutation.
func TestApplyDependencyCycle(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "marker")

	ops := []Op{
		header(),
		{Op: KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[a]", Deps: []string{"Command[b]"}},
		{Op: KindCommand, Bin: "true", ID: "Command[b]", Deps: []string{"Command[a]"}},
	}
	err := Apply(ops, Facts{}, "")
	if err == nil || !strings.Contains(err.Error(), "circular dependency involving") {
		t.Fatalf("want circular dependency error, got %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("cycle must be refused before any mutation")
	}
}

// TestApplyDependencyDanglingSatisfiedAtChunkLevel pins the chunk-level
// view of dangling deps: a dep that matches no op in the applied body counts
// as satisfied (an earlier privilege chunk or invocation applied it — chunk
// boundaries are invisible to chunk-level Apply). Truly dangling deps are
// refused controller-side, before anything is applied, by the
// plan.ValidateChunkDeps pre-flight wired into api.ApplyChunks,
// remote.PushChunks and api.Apply (see api.TestApplyRejectsDanglingDependency).
func TestApplyDependencyDanglingSatisfiedAtChunkLevel(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "order.log")

	ops := []Op{
		header(),
		{Op: KindCommand, Bin: "sh", Args: []string{"-c", "echo A >> " + log}, ID: "Command[a]", Deps: []string{"File[missing]"}},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("dangling dep must be satisfied at chunk level: %v", err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "A\n"; got != want {
		t.Fatalf("command must have applied after the satisfied dep: %q, want %q", got, want)
	}
}

func TestApplyRefusesInvalidChangeGateBeforeMutation(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "marker")
	cases := []struct {
		name string
		op   Op
		want string
	}{
		{
			name: "empty watch",
			op:   Op{Op: KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[gated]", IfChanged: true},
			want: "watches nothing",
		},
		{
			name: "dangling watch",
			op:   Op{Op: KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[gated]", IfChanged: true, Watch: []string{"File[missing]"}},
			want: "dangling watch",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Apply([]Op{header(), tc.op}, Facts{}, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Apply error = %v, want %q", err, tc.want)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatal("invalid change gate must be rejected before its command mutates")
			}
		})
	}
}

// TestApplyDepInLaterWhenBlockRejected pins the cross-guard rule: an op may
// not depend on a resource recorded inside a LATER when-block (or any later
// segment); apply cannot reorder ops across when_* boundaries.
func TestApplyDepInLaterWhenBlockRejected(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "marker")

	ops := []Op{
		header(),
		{Op: KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[b]", Deps: []string{"Command[a]"}},
		{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "linux"}}},
		{Op: KindCommand, Bin: "true", ID: "Command[a]"},
		{Op: KindWhenEnd},
	}
	err := Apply(ops, Facts{GOOS: "linux"}, "")
	if err == nil || !strings.Contains(err.Error(), "not ordered before it") {
		t.Fatalf("want cross-when-block dep error, got %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("refused plan must not mutate")
	}
}

// TestApplyDepOnEarlierWhenBodySatisfied pins the satisfied side of the
// cross-guard rule: a dep on a resource inside an EARLIER when-block counts
// as ordered before (it applies first), so the dependent outside the block
// runs after it.
func TestApplyDepOnEarlierWhenBodySatisfied(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "order.log")

	ops := []Op{
		header(),
		{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "linux"}}},
		{Op: KindCommand, Bin: "sh", Args: []string{"-c", "echo A >> " + log}, ID: "Command[a]"},
		{Op: KindWhenEnd},
		{Op: KindCommand, Bin: "sh", Args: []string{"-c", "echo B >> " + log}, ID: "Command[b]", Deps: []string{"Command[a]"}},
	}
	if err := Apply(ops, Facts{GOOS: "linux"}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "A\nB\n"; got != want {
		t.Fatalf("apply order = %q, want %q", got, want)
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

func TestApplyFileLineBatchesPreserveOrderAndAcceptLegacyFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f")
	if err := os.WriteFile(path, []byte("keep\nold\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := []Op{
		header(),
		{Op: KindFile, Path: path,
			RemoveLines: []string{"old", "old"}, RemoveLine: "stale",
			AddLines: []string{"second", "first", "second"}, AddLine: "third"},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "keep\nsecond\nfirst\nthird\n"; string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// TestApplyTimerRestartLowering moved to apply_systemd_test.go (package
// plan_test): it stubs resource/systemd's command runner directly, and
// resource/systemd now registers a plan.Handler (see
// resource/systemd/planwire.go) — importing resource/systemd from an
// internal plan test would be an import cycle (plan -> resource/systemd ->
// plan), mirroring why the package-kind apply tests live in apply_pkg_test.go.

func TestApplySystemdTimerRequiresFields(t *testing.T) {
	t.Parallel()
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "st"},
		{Op: KindSystemdTimer, Name: "fit-job"},
	}
	err := Apply(ops, Facts{GOOS: "linux"}, "")
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("want missing command error, got %v", err)
	}
}

// sortedApplyOrderCase is one sortedApplyOrder table fixture: a recorded body
// (without header) plus the expected sequence of op IDs, or an expected error
// substring. IDs double as both the dep targets and the order assertion.
type sortedApplyOrderCase struct {
	name    string
	body    []Op
	wantIDs []string
	wantErr string
}

func sortedApplyOrderFixture() []sortedApplyOrderCase {
	beginLinux := Op{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "linux"}}}
	end := Op{Op: KindWhenEnd}
	cmd := func(id string, deps ...string) Op {
		return Op{Op: KindCommand, Bin: "true", ID: id, Deps: deps}
	}
	return []sortedApplyOrderCase{
		{
			name:    "dep free keeps recorded order",
			body:    []Op{cmd("c"), cmd("b"), cmd("a")},
			wantIDs: []string{"c", "b", "a"},
		},
		{
			name:    "dependent reordered after dependency",
			body:    []Op{cmd("b", "a"), cmd("a")},
			wantIDs: []string{"a", "b"},
		},
		{
			name: "stable: ready ops emitted in recorded order",
			// Ready set starts at {a, c}; c must not overtake a, and d
			// (depending on nothing) stays ahead of b whose dep unblocks last.
			body:    []Op{cmd("c"), cmd("d"), cmd("b", "a"), cmd("a")},
			wantIDs: []string{"c", "d", "a", "b"},
		},
		{
			name: "diamond collapses to dependency order",
			body: []Op{
				cmd("root", "left", "right"),
				cmd("left", "base"),
				cmd("right", "base"),
				cmd("base"),
			},
			wantIDs: []string{"base", "left", "right", "root"},
		},
		{
			name:    "control ops keep recorded positions",
			body:    []Op{beginLinux, cmd("b", "a"), cmd("a"), end, cmd("c")},
			wantIDs: []string{"when-begin", "a", "b", "when-end", "c"},
		},
		{
			name:    "dep on op in earlier when-block satisfied",
			body:    []Op{beginLinux, cmd("a"), end, cmd("b", "a")},
			wantIDs: []string{"when-begin", "a", "when-end", "b"},
		},
		{
			name:    "self dependency is a cycle",
			body:    []Op{cmd("a", "a")},
			wantErr: "circular dependency involving a",
		},
		{
			name:    "two op cycle",
			body:    []Op{cmd("a", "b"), cmd("b", "a")},
			wantErr: "circular dependency involving",
		},
		{
			name:    "dep recorded nowhere satisfied (earlier chunk)",
			body:    []Op{cmd("a", "File[missing]")},
			wantIDs: []string{"a"},
		},
		{
			name:    "dep on later when-block refused",
			body:    []Op{cmd("b", "a"), beginLinux, cmd("a"), end},
			wantErr: "not ordered before it",
		},
		{
			name:    "duplicate dep occurrences stay ordered",
			body:    []Op{cmd("x", "File[same]"), cmd("y"), Op{Op: KindCommand, Bin: "true", ID: "File[same]"}},
			wantIDs: []string{"y", "File[same]", "x"},
		},
	}
}

// TestSortedApplyOrder pins the apply-order algorithm directly: segment
// partitioning, stable Kahn ordering, and the dangling/cycle refusals.
func TestSortedApplyOrder(t *testing.T) {
	for _, tc := range sortedApplyOrderFixture() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sortedApplyOrder(tc.body)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("sortedApplyOrder: %v", err)
			}
			var ids []string
			for _, l := range got {
				if l.op.Op == KindWhenBegin {
					ids = append(ids, "when-begin")
					continue
				}
				if l.op.Op == KindWhenEnd {
					ids = append(ids, "when-end")
					continue
				}
				ids = append(ids, l.op.ID)
			}
			if !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Fatalf("order = %v, want %v", ids, tc.wantIDs)
			}

			// Line numbers must track the recorded position, not the sorted one.
			wantLine := map[string]int{}
			for i, op := range tc.body {
				key := op.ID
				if IsControlKind(op.Op) {
					if op.Op == KindWhenBegin {
						key = "when-begin"
					} else {
						key = "when-end"
					}
				}
				wantLine[key] = i + 2
			}
			for _, l := range got {
				key := l.op.ID
				if IsControlKind(l.op.Op) {
					if l.op.Op == KindWhenBegin {
						key = "when-begin"
					} else {
						key = "when-end"
					}
				}
				if wantLine[key] != l.line {
					t.Fatalf("op %s line = %d, want %d (recorded position)", key, l.line, wantLine[key])
				}
			}
		})
	}
}

// chunkBodies flattens SplitPrivilegeChunks output for ValidateChunkDeps.
func chunkBodies(chunks []Chunk) [][]Op {
	bodies := make([][]Op, len(chunks))
	for i, ch := range chunks {
		bodies[i] = ch.Ops
	}
	return bodies
}

// TestApplyDepInLaterChunkRefusedBeforeApply pins the forward cross-chunk
// rule: a dep recorded in a LATER privilege chunk crosses the elevation
// boundary (apply never reorders chunks), so the controller-side pre-flight
// ValidateChunkDeps must refuse it before any chunk is applied.
func TestApplyDepInLaterChunkRefusedBeforeApply(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "marker")

	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "chunks"},
		{Op: KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[b]", Deps: []string{"Command[a]"}},
		{Op: KindCommand, Bin: "true", ID: "Command[a]", Elevate: true},
	}
	chunks := SplitPrivilegeChunks(ops)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	err := ValidateChunkDeps(chunkBodies(chunks))
	if err == nil {
		t.Fatal("want forward cross-chunk dep refusal")
	}
	for _, want := range []string{"chunk 0", "Command[b]", "Command[a]", "later chunk 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must name %q", err.Error(), want)
		}
	}
	if _, serr := os.Stat(marker); !os.IsNotExist(serr) {
		t.Fatal("refused plan must not mutate before any chunk applies")
	}
}

// TestApplyDepOnEarlierChunkSatisfied pins the backward cross-chunk rule:
// chunks apply in recorded order and never reorder, so a dep on an op from
// an EARLIER privilege chunk is genuinely satisfied. The dependent op must
// apply after its dependency's effect (sh-append order proves it).
func TestApplyDepOnEarlierChunkSatisfied(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "order.log")

	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "chunks"},
		{Op: KindCommand, Bin: "sh", Args: []string{"-c", "echo A >> " + log}, ID: "Command[a]"},
		{Op: KindCommand, Bin: "sh", Args: []string{"-c", "echo B >> " + log}, ID: "Command[b]", Elevate: true, Deps: []string{"Command[a]"}},
	}
	chunks := SplitPrivilegeChunks(ops)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if err := ValidateChunkDeps(chunkBodies(chunks)); err != nil {
		t.Fatalf("backward cross-chunk dep must validate: %v", err)
	}
	for i, ch := range chunks {
		if err := Apply(ch.Ops, Facts{}, ""); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "A\nB\n"; got != want {
		t.Fatalf("apply order = %q, want %q (dependency Command[a] in chunk 0 must precede dependent Command[b] in chunk 1)", got, want)
	}
}

// TestApplyPrintsSummary pins that plan.Apply ends with the collected
// resource summary on stderr (task 712): the report machinery is what the
// summary printing exists for, and dry-run especially needs the
// would-change report.
func TestApplyPrintsSummary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("file fixtures with shell-independent content are Linux-tested")
	}

	// Capture os.Stderr via a pipe (Apply prints to os.Stderr).
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()
	t.Cleanup(func() {
		_ = w.Close()
		os.Stderr = oldStderr
	})

	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "summary"},
		{Op: KindFile, Path: filepath.Join(t.TempDir(), "out"), ContentB64: base64.StdEncoding.EncodeToString([]byte("x"))},
	}
	if err := Apply(ops, Facts{GOOS: "linux"}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	_ = w.Close()

	out := <-done
	if !strings.Contains(out, "summary: 0 ok, 1 changed") {
		t.Errorf("expected a changed summary on stderr, got %q", out)
	}
	if !strings.Contains(out, "changed File[") {
		t.Errorf("expected the changed file id in the summary, got %q", out)
	}
}
