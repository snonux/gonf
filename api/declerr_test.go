package api

import (
	"errors"
	"os"
	"path/filepath"
	"regexp/syntax"
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
// lastRecordFailure makes Apply refuse outright — naming the cause and
// its declaration site (task uc2) — rather than silently no-op on an
// empty repository, which would look identical to "there was nothing to
// do" from the caller's side.
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
	if lastRecordFailure == nil {
		t.Fatal("lastRecordFailure = nil after a record-time misuse was captured into Run's session")
	}
	err := Apply()
	if err == nil {
		t.Fatal("Apply() after Run failed mid-body = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "WithCronUser must not be empty") {
		t.Fatalf("Apply() error = %v, want it to carry the cron misuse cause", err)
	}
	if !strings.Contains(err.Error(), "declared at") {
		t.Fatalf("Apply() error = %v, want it to name the misuse's declaration site", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("the leftover directory exists despite the refused record: %v", err)
	}
}

// TestApplyRecoversAfterALaterCleanRecord: lastRecordFailure must not
// refuse Apply forever once a failed recipe is fixed. After a failed
// record (as above), a later, unrelated RecordPlanTo/Run that completes
// cleanly clears it, and Apply works normally again — an embedding program that keeps
// running after fixing a broken recipe must not stay refused over a mistake
// it already recovered from.
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
	if lastRecordFailure == nil {
		t.Fatal("lastRecordFailure = nil after Run(bad) failed")
	}

	dst := filepath.Join(t.TempDir(), "recovered")
	Task("good", "", func() { File(dst, options.WithContent("ok")) })
	if err := Run("good"); err != nil {
		t.Fatalf("Run(good) = %v, want a clean run", err)
	}
	if lastRecordFailure != nil {
		t.Fatalf("lastRecordFailure = %v after a later clean record, want it cleared", lastRecordFailure)
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

// task tc2: fc2's guard must cover every reason a record can fail, not only
// declared misuse. A task recursion cycle never reaches declerr at all (it
// is stashed in recSession.recordingCycleErr and returned directly by
// RecordPlanTo/Run), so it is a different failure shape than
// TestApplyRefusesAfterARunFailedMidBody's cron misuse — but the same
// leftover-registration hazard applies: the outer body's own registration,
// made before the self-recursive Run call fails the record, must not
// survive for a later Apply to silently apply.
func TestApplyRefusesAfterARunFailedOnATaskCycle(t *testing.T) {
	ResetForTest()
	ResetInventory()
	t.Cleanup(func() {
		ResetForTest()
		ResetInventory()
	})
	leftover := filepath.Join(t.TempDir(), "leftover")
	Task("cyclic", "", func() {
		Dir(leftover)     // registers fine before the cycle is detected
		_ = Run("cyclic") // self-recursion: fails the record, never reports to declerr
	})
	err := Run("cyclic")
	if err == nil || !strings.Contains(err.Error(), "cyclic -> cyclic") {
		t.Fatalf("Run(cyclic) = %v, want the cycle error naming the chain", err)
	}
	if declerr.First() != nil {
		t.Fatalf("a task-cycle failure incorrectly reached declerr.First: %v", declerr.First())
	}
	if ids := resource.RegisteredIDs(); len(ids) != 0 {
		t.Fatalf("a failed record left %v registered, want none", ids)
	}
	if lastRecordFailure == nil {
		t.Fatal("lastRecordFailure = nil after Run(cyclic) failed on a task cycle, not declared misuse")
	}
	applyErr := Apply()
	if applyErr == nil {
		t.Fatal("Apply() after Run failed on a task cycle = nil, want a refusal")
	}
	if !strings.Contains(applyErr.Error(), "cyclic -> cyclic") {
		t.Fatalf("Apply() error = %v, want it to carry the cycle cause", applyErr)
	}
	if strings.Contains(applyErr.Error(), "declared at") {
		t.Fatalf("Apply() error = %v, a task-cycle error has no declerr location to name", applyErr)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("the leftover directory exists despite the refused record: %v", err)
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
		{"plan-recipient-empty", "WithPlanRecipient: recipient must not be empty",
			func() { Host("h", WithPlanRecipient("")) }},
		{"plan-recipient-comment-only", "WithPlanRecipient: recipient must not be empty",
			func() { Host("h", WithPlanRecipient("# not a key")) }},
		{"plan-recipient-classic", "recipient refused",
			func() {
				Host("h", WithPlanRecipient("age1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))
			}},
		{"plan-recipient-malformed", "malformed age1pq recipient",
			func() { Host("h", WithPlanRecipient("age1pq1notavalidkey")) }},
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

// TestDeclarationErrorUnwrapsToCause pins the behavior fixed by task hc2:
// declerr.Reportf's call sites wrap their cause with %w (not %v), so the
// cause stays reachable through errors.Is/errors.As even after the
// declaration-error report. Matching's invalid-pattern error is a concrete
// stdlib type (*syntax.Error) that %v would have flattened into an
// unreachable string; %w keeps it reachable.
func TestDeclarationErrorUnwrapsToCause(t *testing.T) {
	requireDeclErr(t, `Matching: invalid pattern "("`, func() { requireNil(Matching("(")) })
	var synErr *syntax.Error
	err := declerr.First()
	if !errors.As(err, &synErr) {
		t.Fatalf("errors.As(%v, *syntax.Error) = false, want true (the regexp.Compile cause should stay reachable)", err)
	}
}

// TestApplyRefusesEvenAfterANewUnrelatedRegistrationFollowsARecordFailure
// pins task ad2's finding: File(a, ...) registers directly at top level,
// then an unrelated task's body fails to record (declared misuse). Because
// recording runs each task body against a fresh resource repository
// (runTaskBody), a's registration is already gone by the time Run returns —
// regardless of this guard, and regardless of whether RecordPlanTo wipes or
// rolls back on that failure — so nothing can restore it. What matters is
// that a later, unrelated File(b, ...) registration must not let Apply()
// slip through and report success: an empty-repository-only guard (task
// tc2's narrowing, reverted by ad2) would see the non-empty [b] and apply
// it, silently dropping a's earlier declaration with no sign anything was
// wrong. The unconditional guard refuses instead, every time, until a later
// record actually succeeds.
func TestApplyRefusesEvenAfterANewUnrelatedRegistrationFollowsARecordFailure(t *testing.T) {
	ResetForTest()
	ResetInventory()
	t.Cleanup(func() {
		ResetForTest()
		ResetInventory()
	})
	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	File(a, options.WithContent("a"))
	Task("bad", "", func() {
		Cron("x", options.WithCommand("/bin/true"), options.WithCronUser(""))
	})
	if err := Run("bad"); err == nil || !strings.Contains(err.Error(), "WithCronUser must not be empty") {
		t.Fatalf("Run(bad) = %v, want the cron misuse refusal", err)
	}
	File(b, options.WithContent("b"))
	if err := Apply(); err == nil {
		t.Fatal("Apply() after an unrelated record failure, with a new registration following it, = nil, want a refusal")
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Fatalf("a should not exist: %v", err)
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Fatalf("b must not be applied while the guard refuses: %v", err)
	}

	// Recovery: a later, unrelated CLEAN record is what actually clears
	// the guard — not merely registering something new directly. Neither
	// a nor b needs re-registering: both already survive Run("good")'s
	// restore (task id2 — a genuinely survived the earlier failure too,
	// not just b), so re-registering either here would collide.
	Task("good", "", func() {})
	if err := Run("good"); err != nil {
		t.Fatalf("Run(good) = %v, want a clean run", err)
	}
	if err := Apply(); err != nil {
		t.Fatalf("Apply() after recovery = %v, want it to succeed", err)
	}
	if content, err := os.ReadFile(a); err != nil || string(content) != "a" {
		t.Fatalf("a = %q, %v; want it written after recovery (task id2: it survived the earlier failure intact)", content, err)
	}
	if content, err := os.ReadFile(b); err != nil || string(content) != "b" {
		t.Fatalf("b = %q, %v; want it written after recovery", content, err)
	}
}

// TestApplyDoesNotBlindlyApplyAWhenGuardedFragmentAfterARecord pins task
// bd2: a successful RecordPlanTo used to leave its last recorded scope's
// registrations (with their drafts) sitting in the repository — including
// the members of a WhenHostname-guarded fragment whose OPS correctly carry
// when_begin/when_end, but whose live registration carries no such guard.
// A later, separate api.Apply in the same process would lower and apply
// that registration directly, bypassing the guard entirely. RecordPlanTo
// now rolls the repository back to its pre-call snapshot on success too,
// so nothing survives for a blind Apply() to find.
func TestApplyDoesNotBlindlyApplyAWhenGuardedFragmentAfterARecord(t *testing.T) {
	ResetForTest()
	ResetInventory()
	t.Cleanup(func() {
		ResetForTest()
		ResetInventory()
	})
	guarded := filepath.Join(t.TempDir(), "guarded")
	Task("t", "", func() {
		WhenHostname("definitely-not-this-host", func() {
			File(guarded, options.WithContent("nope"))
		})
	})
	if _, err := RecordPlanTo("p", plan.NewMemoryStore(), "t"); err != nil {
		t.Fatalf("RecordPlanTo(t) = %v, want a clean record", err)
	}
	if ids := resource.RegisteredIDs(); len(ids) != 0 {
		t.Fatalf("a successful record left %v registered, want none", ids)
	}
	if err := Apply(); err != nil {
		t.Fatalf("Apply() after a successful record with nothing registered = %v, want nil (nothing to do)", err)
	}
	if _, err := os.Stat(guarded); !os.IsNotExist(err) {
		t.Fatalf("the when-guarded file must not have been written: %v", err)
	}
}

// TestApplyDoesNotApplyAWhenGuardedFragmentThatCollidesWithATopLevelID pins
// task id2: an earlier, ID-based RollbackTo(kept []string) restored bd2's
// bug in full whenever the guarded fragment happened to register the SAME
// ID as something declared before the record started — pruning down to a
// set of names keeps a same-ID registration made during the record (with
// its OWN, guarded-content value), rather than restoring the original,
// because the name alone does not distinguish "the pre-record resource" from
// "a same-named resource the record body redeclared." File(path,
// WithContent("toplevel")) at top level, then a WhenHostname-guarded
// fragment for a NON-matching host redeclaring the exact same path with
// different content, must leave the TOP-LEVEL content in place after the
// record and a later Apply — never the guarded fragment's, and never
// nothing at all.
func TestApplyDoesNotApplyAWhenGuardedFragmentThatCollidesWithATopLevelID(t *testing.T) {
	ResetForTest()
	ResetInventory()
	t.Cleanup(func() {
		ResetForTest()
		ResetInventory()
	})
	path := filepath.Join(t.TempDir(), "motd")
	File(path, options.WithContent("toplevel"))
	Task("t", "", func() {
		WhenHostname("definitely-not-this-host", func() {
			File(path, options.WithContent("guarded"))
		})
	})
	if _, err := RecordPlanTo("p", plan.NewMemoryStore(), "t"); err != nil {
		t.Fatalf("RecordPlanTo(t) = %v, want a clean record", err)
	}
	if ids := resource.RegisteredIDs(); len(ids) != 1 || ids[0] != "File["+path+"]" {
		t.Fatalf("registered = %v, want exactly the top-level File[%s], restored intact", ids, path)
	}
	if err := Apply(); err != nil {
		t.Fatalf("Apply() after the record = %v, want it to succeed", err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "toplevel" {
		t.Fatalf("content = %q, %v; want the top-level declaration's content, not the guarded fragment's and not absent", content, err)
	}
}

// TestApplySeesATopLevelDeclarationAfterAnUnrelatedEmptyTaskRecords pins
// task kd2: File(f, ...) at top level, then RecordPlanTo/Run for a
// completely unrelated, EMPTY task, used to leave f's registration gone —
// runTaskBody's per-task-body fresh repository wiped it, and (before task
// id2) nothing restored it, so a recipe that declares resources at top
// level and also runs any task in the same process silently converged
// nothing and reported success. resource.SnapshotRepository (id2) restores
// the pre-call state on every RecordPlanTo outcome, including a clean one,
// so f's registration survives an unrelated task's record intact.
func TestApplySeesATopLevelDeclarationAfterAnUnrelatedEmptyTaskRecords(t *testing.T) {
	ResetForTest()
	ResetInventory()
	t.Cleanup(func() {
		ResetForTest()
		ResetInventory()
	})
	f := filepath.Join(t.TempDir(), "f")
	File(f, options.WithContent("f"))
	Task("empty", "", func() {})
	if err := Run("empty"); err != nil {
		t.Fatalf("Run(empty) = %v, want a clean run", err)
	}
	if ids := resource.RegisteredIDs(); len(ids) != 1 || ids[0] != "File["+f+"]" {
		t.Fatalf("registered = %v, want exactly the top-level File[%s], surviving the unrelated empty task's record", ids, f)
	}
	if err := Apply(); err != nil {
		t.Fatalf("Apply() after the unrelated record = %v, want it to succeed", err)
	}
	if content, err := os.ReadFile(f); err != nil || string(content) != "f" {
		t.Fatalf("f = %q, %v; want it written, not silently dropped", content, err)
	}
}

// TestRunTaskBodyPanicDoesNotLeaveAHalfRegisteredSetForApply pins tasks
// cd2/jd2: a task body panic (a nil-map write, here — a genuine
// programmer-bug invariant, not a recipe error) used to skip RecordPlanTo's
// normal return entirely, so neither the repository restore nor
// lastRecordFailure ever ran; a caller that recovers the panic (as this
// test, or go test's own per-test recovery, does) would find whatever the
// body registered before panicking still sitting in the repository, with
// lastRecordFailure == nil, and a later Apply() would silently apply that
// half-declared set alongside whatever else got registered afterward — the
// exact partial-convergence-reported-as-success shape ad2 closed, on the
// one path ad2's guard did not otherwise cover.
//
// RecordPlanTo now recovers, restores the repository to its pre-call
// snapshot, sets lastRecordFailure, and re-panics — so the process-ending
// behavior for an uninstrumented caller is unchanged, but a caller that
// does recover finds nothing left over from the panicked body AND stays
// refused until a later, clean record actually succeeds (task jd2 — an
// earlier version of this fix deliberately left lastRecordFailure unset,
// reasoning that a caller sophisticated enough to recover a panic had
// already taken responsibility for it; that reasoning conflated two
// different invariants — see TestPanickingTaskDoesNotLeakIntoLaterDraftErrors,
// which only pins that no STALE TASK NAME leaks into a later, unrelated
// draft error, not that Apply must succeed after a recovered panic).
func TestRunTaskBodyPanicDoesNotLeaveAHalfRegisteredSetForApply(t *testing.T) {
	ResetForTest()
	ResetInventory()
	t.Cleanup(func() {
		ResetForTest()
		ResetInventory()
	})
	half := filepath.Join(t.TempDir(), "half")
	var nilMap map[string]string
	Task("panics", "", func() {
		File(half, options.WithContent("half")) // registers fine before the panic
		nilMap["x"] = "boom"                    // nil map write: panics
	})
	func() {
		defer func() { _ = recover() }()
		_ = Run("panics")
		t.Fatal("Run(panics) returned instead of the task body's panic propagating")
	}()
	if ids := resource.RegisteredIDs(); len(ids) != 0 {
		t.Fatalf("a panicked record left %v registered, want none", ids)
	}
	if lastRecordFailure == nil {
		t.Fatal("lastRecordFailure = nil after a recovered task-body panic, want it set")
	}

	// A fresh registration alone must not be enough to slip past the
	// refusal (ad2's guard is unconditional): Apply() must still refuse,
	// naming the panic, not silently apply the fresh registration.
	good := filepath.Join(t.TempDir(), "good")
	File(good, options.WithContent("ok"))
	err := Apply()
	if err == nil {
		t.Fatal("Apply() after a recovered task-body panic and a fresh registration = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "task body panicked") {
		t.Fatalf("Apply() error = %v, want it to name the panic", err)
	}
	if _, err := os.Stat(half); !os.IsNotExist(err) {
		t.Fatalf("the half-registered file must not have been written: %v", err)
	}
	if _, err := os.Stat(good); !os.IsNotExist(err) {
		t.Fatalf("good must not be applied while the guard refuses: %v", err)
	}

	// Recovery: a later, unrelated CLEAN record is what actually clears
	// the guard — not merely registering something new directly. good
	// needs no re-registration: it already survives Run("healthy")'s
	// restore, since it was registered before that call started.
	Task("healthy", "", func() {})
	if err := Run("healthy"); err != nil {
		t.Fatalf("Run(healthy) = %v, want a clean run", err)
	}
	if err := Apply(); err != nil {
		t.Fatalf("Apply() after recovery = %v, want it to succeed", err)
	}
	if content, err := os.ReadFile(good); err != nil || string(content) != "ok" {
		t.Fatalf("good = %q, %v; want it written after recovery", content, err)
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
