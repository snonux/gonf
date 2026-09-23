package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/clihost"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// refuseElevation installs a fake elevated runner that fails the test when
// reached: every case here must be refused before any chunk applies.
func refuseElevation(t *testing.T, mode privilege.Mode) *[]elevatedCall {
	t.Helper()
	return fakeElevation(t, mode, func([]plan.Op, string) error {
		t.Error("elevated runner must not run when Apply refuses up front")
		return nil
	})
}

// fakeEUID pins processEUID for one test.
func fakeEUID(t *testing.T, euid int) {
	t.Helper()
	old := processEUID
	t.Cleanup(func() { processEUID = old })
	processEUID = func() int { return euid }
}

// requireNothingApplied asserts the refusal left marker unwritten and the
// elevated runner uncalled.
func requireNothingApplied(t *testing.T, marker string, calls *[]elevatedCall) {
	t.Helper()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("%s was written although Apply must refuse before any chunk", marker)
	}
	if len(*calls) != 0 {
		t.Fatalf("elevated runner ran %d times, want 0", len(*calls))
	}
}

// registerCycle registers the unprivileged Command[x] <-> Command[y] cycle.
func registerCycle() {
	Command("true", nil, options.WithName("x"), options.DependsOn(unregisteredDep("Command[y]")))
	Command("true", nil, options.WithName("y"), options.DependsOn(unregisteredDep("Command[x]")))
}

// TestApplyRefusesCycleWithElevatedOpsBeforeAnyChunk: with an elevated op
// and an unrelated unprivileged cycle, the elevated chunk must not run as
// root before the cycle is found; Apply names the cycle and applies nothing.
func TestApplyRefusesCycleWithElevatedOpsBeforeAnyChunk(t *testing.T) {
	calls := refuseElevation(t, privilege.Sudo)
	marker := filepath.Join(t.TempDir(), "marker")
	File(marker, options.WithContent("x"))
	Command("true", nil, options.WithName("a"), options.WithElevate)
	registerCycle()

	err := Apply()
	if err == nil || !strings.HasPrefix(err.Error(), "Apply: circular dependency: Command[x] -> Command[y] -> Command[x]") {
		t.Fatalf("Apply() error = %v, want the x/y cycle named with the Apply: prefix", err)
	}
	requireNothingApplied(t, marker, calls)
}

// TestApplyCycleDoesNotBlameValidDep: an unrelated cycle used to leave the
// ID order in place, so the split refused elevated a's valid dep on File[z]
// as a forward cross-chunk dep. The refusal must name the cycle instead.
func TestApplyCycleDoesNotBlameValidDep(t *testing.T) {
	calls := refuseElevation(t, privilege.Sudo)
	z := filepath.Join(t.TempDir(), "z")
	fz := File(z, options.WithContent("z"))
	Command("true", nil, options.WithName("a"), options.WithElevate, options.DependsOn(fz))
	registerCycle()

	err := Apply()
	if err == nil || !strings.Contains(err.Error(), "circular dependency: Command[x] -> Command[y]") ||
		strings.Contains(err.Error(), fz.ID()) {
		t.Fatalf("Apply() error = %v, want the cycle named and %s not blamed", err, fz.ID())
	}
	requireNothingApplied(t, z, calls)
}

// TestApplyWithoutElevatedOpsIsNotSorted pins that plans without elevated ops
// keep main's path: no orderForPrivilegeSplit, so a cycle is reported by the
// plan engine in its own words (not Apply's "circular dependency: a -> b"),
// still before anything is applied.
func TestApplyWithoutElevatedOpsIsNotSorted(t *testing.T) {
	calls := refuseElevation(t, privilege.Sudo)
	marker := filepath.Join(t.TempDir(), "marker")
	File(marker, options.WithContent("x"))
	registerCycle()

	err := Apply()
	if err == nil || !strings.Contains(err.Error(), "plan: circular dependency involving") ||
		strings.Contains(err.Error(), "Apply: circular dependency:") {
		t.Fatalf("Apply() error = %v, want the plan engine's unsorted cycle error", err)
	}
	requireNothingApplied(t, marker, calls)
}

// TestApplyRefusesModeNoneWithoutRootUpFront: with privilege mode none a
// non-root process cannot elevate, so Apply refuses before the unprivileged
// chunk writes its File (it used to write it and fail at the re-exec).
func TestApplyRefusesModeNoneWithoutRootUpFront(t *testing.T) {
	calls := refuseElevation(t, privilege.None)
	fakeEUID(t, 1000)
	marker := filepath.Join(t.TempDir(), "marker")
	File(marker, options.WithContent("x"))
	Command("true", nil, options.WithName("root-only"), options.WithElevate)

	err := Apply()
	if err == nil || !strings.HasPrefix(err.Error(), "Apply: privileged ops Command[root-only] need elevation") ||
		!strings.Contains(err.Error(), "privilege mode is none") {
		t.Fatalf("Apply() error = %v, want the up-front mode-none refusal", err)
	}
	requireNothingApplied(t, marker, calls)
}

// TestApplyModeNoneAsRootRunsInProcess: mode none as root needs no re-exec
// (and so no CLI host): the elevated chunk applies in-process.
func TestApplyModeNoneAsRootRunsInProcess(t *testing.T) {
	calls := refuseElevation(t, privilege.None)
	fakeEUID(t, 0)
	t.Cleanup(clihost.SetForTest(false))
	marker := filepath.Join(t.TempDir(), "marker")
	Command("touch", []string{marker}, options.WithElevate)

	if err := Apply(); err != nil {
		t.Fatalf("Apply() as root with mode none: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("elevated command not applied in-process: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("elevated runner ran %d times, want 0 (in-process as root)", len(*calls))
	}
}

// TestApplyRefusesElevationWithoutCLIHost pins the recursion guard: in a
// process that never ran gonf's CLI, the sudo/doas re-exec would run the
// program's own main as root again, so Apply refuses before any chunk.
func TestApplyRefusesElevationWithoutCLIHost(t *testing.T) {
	calls := refuseElevation(t, privilege.Sudo)
	t.Cleanup(clihost.SetForTest(false))
	marker := filepath.Join(t.TempDir(), "marker")
	File(marker, options.WithContent("x"))
	Command("true", nil, options.WithName("root-only"), options.WithElevate)

	err := Apply()
	if err == nil || !strings.HasPrefix(err.Error(), "Apply: privileged ops Command[root-only]: ") ||
		!strings.Contains(err.Error(), "cli.CLI()") {
		t.Fatalf("Apply() error = %v, want the missing-CLI-host refusal", err)
	}
	requireNothingApplied(t, marker, calls)
}

// TestApplyChunksRefusesElevationUpFront pins the same two refusals on the
// Run path (RunContext applies through ApplyChunksContext): the unprivileged
// chunk 0 must not apply before the elevated chunk is refused.
func TestApplyChunksRefusesElevationUpFront(t *testing.T) {
	cases := []struct {
		name  string
		mode  privilege.Mode
		euid  int
		cli   bool
		wants string
	}{
		{name: "mode none, not root", mode: privilege.None, euid: 1000, cli: true, wants: "privilege mode is none"},
		{name: "no CLI host", mode: privilege.Doas, euid: 1000, cli: false, wants: "cli.CLI()"},
		{name: "no CLI host, even as root with sudo", mode: privilege.Sudo, euid: 0, cli: false, wants: "cli.CLI()"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := refuseElevation(t, tc.mode)
			fakeEUID(t, tc.euid)
			t.Cleanup(clihost.SetForTest(tc.cli))
			dir := t.TempDir()
			marker := filepath.Join(dir, "marker")
			ops := []plan.Op{
				{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "run"},
				{Op: plan.KindCommand, ID: "Command[user]", Payload: plan.CommandPayload{Bin: "touch", Args: []string{marker}}},
				{Op: plan.KindCommand, ID: "Command[root]", Elevate: true, Payload: plan.CommandPayload{Bin: "true"}},
			}
			err := ApplyChunks(ops, dir, tc.mode)
			if err == nil || !strings.Contains(err.Error(), "Command[root]") || !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("ApplyChunks() error = %v, want the up-front refusal naming Command[root] and %q", err, tc.wants)
			}
			requireNothingApplied(t, marker, calls)
		})
	}
}

// TestApplyRefusesSelfDependencyWithElevatedOps pins that a resource
// depending on itself is refused as a cycle before any chunk applies, in
// both chunk orders: after the elevated chunk (the elevated chunk used to run
// as root first) and on the elevated op itself (the unprivileged chunk used
// to mutate first).
func TestApplyRefusesSelfDependencyWithElevatedOps(t *testing.T) {
	cases := []struct {
		name         string
		elevatedSelf bool
	}{
		{name: "unprivileged self-dependency", elevatedSelf: false},
		{name: "elevated self-dependency", elevatedSelf: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := refuseElevation(t, privilege.Sudo)
			marker := filepath.Join(t.TempDir(), "marker")
			File(marker, options.WithContent("x"))
			self := options.DependsOn(unregisteredDep("Command[b]"))
			if tc.elevatedSelf {
				Command("true", nil, options.WithName("a"))
				Command("true", nil, options.WithName("b"), options.WithElevate, self)
			} else {
				Command("true", nil, options.WithName("a"), options.WithElevate)
				Command("true", nil, options.WithName("b"), self)
			}
			err := Apply()
			if err == nil || !strings.HasPrefix(err.Error(), "Apply: circular dependency: Command[b] -> Command[b]") {
				t.Fatalf("Apply() error = %v, want the Command[b] self-dependency named", err)
			}
			requireNothingApplied(t, marker, calls)
		})
	}
}
