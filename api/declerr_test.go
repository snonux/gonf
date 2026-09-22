package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// These tests pin the declaration-error contract (docs/plan.md, "Error
// handling contract"): DSL misuse never ends the process. It is reported to
// internal/declerr, the constructor returns an inert value so later
// declarations keep running, and the FIRST error surfaces from RecordPlan,
// Run and Apply (and the CLI, see internal/cli) as a returned error.

// requireDeclErr runs declare against clean registries and requires that it
// reported a declaration error containing want, located in this package's
// tests (the recipe line, not gonf's own frames). declerr.First returns the
// error afterwards.
func requireDeclErr(t *testing.T, want string, declare func()) {
	t.Helper()
	ResetForTest()
	ResetInventory()
	t.Cleanup(func() {
		ResetForTest()
		ResetInventory()
	})
	declare()
	err := declerr.First()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("declaration error = %v, want it to contain %q", err, want)
	}
	if loc := declerr.Location(err); !strings.Contains(loc, "_test.go:") {
		t.Fatalf("declaration error location = %q, want the test's own line", loc)
	}
}

// TestDeclarationErrorFirstWinsAndLaterDeclarationsRun: after a misuse the
// recipe keeps declaring (no crash, the valid task is queued), the FIRST
// error is the one kept, and every entry point refuses with it before any
// task body runs or anything is applied.
func TestDeclarationErrorFirstWinsAndLaterDeclarationsRun(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "never-written")
	bodyRan := false
	requireDeclErr(t, "Task: name must not be empty", func() {
		Task("", "", func() {})
		Host("")             // a second, later misuse
		Task("dup", "", nil) // and a third
		Task("ok", "", func() { bodyRan = true; File(dst, options.WithContent("x")) })
		MustHost("nope").SetValue("k", 1) // zero handle: still no crash
	})
	requireQueued(t, "ok")
	first := declerr.First()

	_, recErr := RecordPlanTo("p", plan.NewMemoryStore(), "ok")
	runErr := Run("ok")
	File(dst, options.WithContent("x"))
	applyErr := Apply()
	for name, err := range map[string]error{"RecordPlanTo": recErr, "Run": runErr, "Apply": applyErr} {
		if !errors.Is(err, first) {
			t.Errorf("%s = %v, want the first declaration error %v", name, err, first)
		}
	}
	if bodyRan {
		t.Fatal("a task body ran although the recipe had a declaration error")
	}
	mustNotExist(t, dst)
}

// TestDeclarationErrorInTaskBodyFailsRecordAndCleansUp: misuse inside a task
// body after a blob was packaged fails that record (it is captured into the
// session, not kept for later records), the declarations after it still run,
// Run's temporary plan directory is removed, and nothing is applied.
func TestDeclarationErrorInTaskBodyFailsRecordAndCleansUp(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	src := newStagedSources(t)
	afterRan := false
	Task("misuse", "", func() {
		InstallFile(filepath.Join(src.dst, "big"), src.big)
		File(filepath.Join(src.dst, "bad"), options.WithMode(os.ModeDir|0o644))
		afterRan = true
		File(filepath.Join(src.dst, "after"), options.WithContent("x"))
	})

	_, recErr := RecordPlanTo("p", plan.NewMemoryStore(), "misuse")
	runErr := Run("misuse")
	for name, err := range map[string]error{"RecordPlanTo": recErr, "Run": runErr} {
		if err == nil || !strings.Contains(err.Error(), "outside 0o7777") {
			t.Fatalf("%s = %v, want the WithMode declaration error", name, err)
		}
		if loc := declerr.Location(err); !strings.Contains(loc, "declerr_test.go:") {
			t.Fatalf("%s error location = %q, want this file", name, loc)
		}
	}
	if !afterRan {
		t.Fatal("the declaration after the misuse did not run")
	}
	if err := declerr.First(); err != nil {
		t.Fatalf("a body's declaration error leaked out of its record: %v", err)
	}
	assertNoStagingLeft(t, tmp)
	mustNotExist(t, src.dst)
}

// TestApplyRefusesDirectDeclarationError: resources declared for a direct
// Apply (no recording) with a misuse among them are not applied at all, and
// Apply creates no temporary plan directory.
func TestApplyRefusesDirectDeclarationError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	good := filepath.Join(t.TempDir(), "good")
	requireDeclErr(t, "does not support WithCommand", func() {
		File(good, options.WithContent("x"))
		File(filepath.Join(t.TempDir(), "bad"), options.ToFileOptions(options.WithCommand("true"))...)
	})
	if err := Apply(); err == nil || !strings.Contains(err.Error(), "does not support WithCommand") {
		t.Fatalf("Apply = %v, want the declaration error", err)
	}
	mustNotExist(t, good)
	assertNoStagingLeft(t, tmp)
}

// TestApplyRefusesCronExplicitEmptyUser pins the push-side invariant behind
// task vb2: an explicit WithCronUser("") must never reach a plan op and be
// silently rebuilt as root's job on the destination. Present (through Cron)
// refuses it as a declaration error, so Apply refuses the whole run before
// anything is applied — the cron plan handler's Apply never runs and no
// crontab write happens.
func TestApplyRefusesCronExplicitEmptyUser(t *testing.T) {
	requireDeclErr(t, "WithCronUser must not be empty", func() {
		Cron("x", options.WithCommand("/bin/true"), options.WithCronUser(""))
	})
	if err := Apply(); err == nil || !strings.Contains(err.Error(), "WithCronUser must not be empty") {
		t.Fatalf("Apply = %v, want the empty cron user refusal", err)
	}
}

// task fc2: a task body that fails partway through (Run captures the
// misuse into that recording session, so it never becomes declerr.First)
// must not leave what it registered before failing sitting in the
// repository for a later, separate Apply call to silently apply. Run's own
// temp-dir record already refuses to apply anything itself on a record
// error; this pins the two guards a caller reaches only by calling Apply
// directly afterward, ignoring Run's returned error: RecordPlanTo resets
// the repository on that failure (so there is nothing left to apply), and
// declerr.CapturedAny makes Apply refuse outright rather than silently
// no-op on an empty repository, which would look identical to "there was
// nothing to do" from the caller's side.
func TestApplyRefusesAfterARunFailedMidBody(t *testing.T) {
	ResetForTest()
	ResetInventory()
	t.Cleanup(func() {
		ResetForTest()
		ResetInventory()
	})
	leftover := filepath.Join(t.TempDir(), "leftover")
	Task("bad", "", func() {
		Cron("x", options.WithCommand("/bin/true"), options.WithCronUser("")) // refused mid-body
		Dir(leftover)                                                         // registers fine afterward
	})
	if err := Run("bad"); err == nil || !strings.Contains(err.Error(), "WithCronUser must not be empty") {
		t.Fatalf("Run(bad) = %v, want the cron misuse refusal", err)
	}
	if ids := resource.RegisteredIDs(); len(ids) != 0 {
		t.Fatalf("a failed record left %v registered, want none", ids)
	}
	if !declerr.CapturedAny() {
		t.Fatal("CapturedAny() = false after a record-time misuse was captured into Run's session")
	}
	if err := Apply(); err == nil {
		t.Fatal("Apply() after Run failed mid-body = nil, want a refusal")
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("the leftover directory exists despite the refused record: %v", err)
	}
}

// TestApplyRecoversAfterALaterCleanRecord: CapturedAny must not refuse Apply
// forever once a failed recipe is fixed. After a failed record (as above),
// a later, unrelated RecordPlanTo/Run that completes cleanly clears it, and
// Apply works normally again — an embedding program that keeps running
// after fixing a broken recipe must not stay refused over a mistake it
// already recovered from.
func TestApplyRecoversAfterALaterCleanRecord(t *testing.T) {
	ResetForTest()
	ResetInventory()
	t.Cleanup(func() {
		ResetForTest()
		ResetInventory()
	})
	Task("bad", "", func() {
		Cron("x", options.WithCommand("/bin/true"), options.WithCronUser(""))
	})
	if err := Run("bad"); err == nil {
		t.Fatalf("Run(bad) = nil, want the cron misuse refusal")
	}
	if !declerr.CapturedAny() {
		t.Fatal("CapturedAny() = false after Run(bad) failed")
	}

	dst := filepath.Join(t.TempDir(), "recovered")
	Task("good", "", func() { File(dst, options.WithContent("ok")) })
	if err := Run("good"); err != nil {
		t.Fatalf("Run(good) = %v, want a clean run", err)
	}
	if declerr.CapturedAny() {
		t.Fatal("CapturedAny() = true after a later clean record, want it cleared")
	}

	direct := filepath.Join(t.TempDir(), "direct")
	File(direct, options.WithContent("ok"))
	if err := Apply(); err != nil {
		t.Fatalf("Apply() after recovery = %v, want it to succeed", err)
	}
	if content, err := os.ReadFile(direct); err != nil || string(content) != "ok" {
		t.Fatalf("direct = %q, %v; want Apply to have written it", content, err)
	}
}

// TestValidRecipeReportsNoDeclarationError is the negative control: correct
// declarations, including the Must* lookups of registered names, report
// nothing and record normally.
func TestValidRecipeReportsNoDeclarationError(t *testing.T) {
	ResetForTest()
	ResetInventory()
	t.Cleanup(func() {
		ResetForTest()
		ResetInventory()
	})
	dir := t.TempDir()
	h := Host("h1", WithValue("k", 7))
	Fleet("f", Cluster("c", h).Parallel(2))
	Task("ok", "", func() {
		File(filepath.Join(dir, "a"), options.WithMode(0o640))
		EachKV(List("k", "v"), func(string, string) {})
		_ = MustHostValue[int]("h1", "k")
	})
	_ = MustHost("h1")
	_ = MustCluster("c").HostNames()
	_ = MustFleet("f").HostNames()
	_ = Matching("^ok$")
	if err := declerr.First(); err != nil {
		t.Fatalf("valid recipe reported %v", err)
	}
	if _, err := RecordPlanTo("p", plan.NewMemoryStore(), "ok"); err != nil {
		t.Fatalf("RecordPlanTo = %v", err)
	}
}

// TestDSLMisuseIsDeclarationError covers the remaining registration-time
// misuse sites, each of which used to end the process: the value returned in
// place of the refused one is inert (zero handle, nil list, nothing
// registered) and later calls on it do not crash.
func TestDSLMisuseIsDeclarationError(t *testing.T) {
	cases := []struct {
		name, want string
		declare    func()
	}{
		{"host-empty", "Host: name must not be empty", func() { Host("") }},
		{"host-dup", `Host "h" already registered`, func() { Host("h"); Host("h") }},
		{"with-value-empty", "WithValue: key must not be empty", func() { Host("h", WithValue("", 1)) }},
		{"with-value-dup", `WithValue: key "k" already set`, func() { Host("h", WithValue("k", 1), WithValue("k", 2)) }},
		{"set-value-dup", `Host "h": value key "k" already set`, func() { Host("h").SetValue("k", 1).SetValue("k", 2) }},
		{"set-value-unknown", `SetValue: Host "x" is not registered`, func() { unregisteredHost("x").SetValue("k", 1) }},
		{"cluster-empty", `Cluster "c": must include at least one Host`, func() { Cluster("c") }},
		{"cluster-dup-host", `Cluster "c": duplicate Host "h"`, func() { h := Host("h"); Cluster("c", h, h) }},
		{"cluster-unknown-host", `Cluster "c": Host "ghost" is not registered`, func() { Cluster("c", unregisteredHost("ghost")) }},
		{"parallel-unknown", `Cluster "x" is not registered`, func() { ClusterRef{name: "x"}.Parallel(3) }},
		{"fleet-empty", `Fleet "f": must include at least one Cluster`, func() { Fleet("f") }},
		{"fleet-unknown-cluster", `Fleet "f": Cluster "x" is not registered`, func() { Fleet("f", ClusterRef{name: "x"}) }},
		{"must-host", `Host "x" is not registered`, func() { requireZero(MustHost("x").Name()) }},
		{"must-cluster", `Cluster "x" is not registered`, func() { requireNil(MustCluster("x").HostNames()) }},
		{"must-fleet", `Fleet "x" is not registered`, func() { requireNil(MustFleet("x").ClusterNames()) }},
		{"must-host-value", `MustHostValue: Host "x" is not registered`, func() { requireZero(MustHostValue[string]("x", "k")) }},
		{"matching", `Matching: invalid pattern "("`, func() { requireNil(Matching("(")) }},
		{"each-kv", "ParseKV: odd number of elements (1)", func() { EachKV(List("k"), func(string, string) { panic("called") }) }},
		{"symlink-map", "SymlinkMap: odd number of elements (1)", func() { SymlinkMap("/tmp", "a") }},
		{"secret-provider", "SetSecretProvider: provider must not be nil", func() { SetSecretProvider(nil) }},
		{"cluster-hosts", "ClusterHosts: no cluster on the current task", func() { requireNil(ClusterHosts()) }},
		{"ensure-dir-option", "does not support WithContent", func() {
			EnsureDir(filepath.Join(os.TempDir(), "gonf-declerr-never"), options.ToDirOptions(options.WithContent("x"))...)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireDeclErr(t, tc.want, tc.declare)
			if ids := resource.RegisteredIDs(); len(ids) != 0 {
				t.Fatalf("misuse registered %v", ids)
			}
		})
	}
}

// unregisteredHost returns a handle for name without looking it up, standing
// in for a recipe that kept a handle whose registration was refused.
func unregisteredHost(name string) HostRef { return HostRef{name: name} }

// requireZero and requireNil panic (failing the test) when a refused Must*
// lookup or accessor returned anything but its inert value.
func requireZero[T comparable](v T) {
	var zero T
	if v != zero {
		panic("refused value is not the zero value")
	}
}

func requireNil[T any](v []T) {
	if v != nil {
		panic("refused list is not nil")
	}
}
