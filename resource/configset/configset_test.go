package configset

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
	opt "github.com/snonux/gonf/resource/options"
)

// fixture is a temp "system": etc is the staging parent holding a mail-like
// set whose main config references its table through a member placeholder.
// Every fixture has its own system operations (sys, which a test overrides
// field by field to inject failures) and its own outcome store, so no test
// changes state another test sees.
type fixture struct {
	t        *testing.T
	root     string
	etc      string
	log      string // validator invocation log
	failFlag string // validator fails while this file exists
	sys      *system
	outcomes *outcomeStore
	// parallel fixtures leave the process-wide report and dry-run flag of
	// package resource alone; their tests must not assert on them.
	parallel bool
}

// newFixture returns a fixture for a serial test that may use the
// process-wide report (resource.AnyChanged) or dry-run flag: it resets them
// now and after the test.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	return makeFixture(t, false)
}

// newParallelFixture marks the test parallel and returns a fixture that never
// touches the process-wide report: everything such a test checks lives in its
// temp directory, its system and its outcome store.
func newParallelFixture(t *testing.T) *fixture {
	t.Helper()
	t.Parallel()
	return makeFixture(t, true)
}

func makeFixture(t *testing.T, parallel bool) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		t: t, root: root, etc: filepath.Join(root, "etc"),
		log: filepath.Join(root, "validator.log"), failFlag: filepath.Join(root, "fail"),
		sys: newSystem(), outcomes: newOutcomeStore(), parallel: parallel,
	}
	if err := os.MkdirAll(filepath.Join(f.etc, "mail"), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// validatorScript runs from argv only (no interpolation of recipe text): it
// logs its arguments, proves the staged config references the STAGED table
// (not the live one), and fails on demand.
const validatorScript = `conf="$1"; table="$2"
printf 'run %s %s\n' "$conf" "$table" >> "$3"
grep -qxF "table aliases file:$table" "$conf" || { echo "config does not reference staged table" >&2; exit 3; }
test -f "$table" || { echo "staged table missing" >&2; exit 4; }
test ! -e "$4" || { echo "syntax error in candidate" >&2; exit 1; }`

func (f *fixture) aliasesPath() string { return filepath.Join(f.etc, "mail", "aliases") }
func (f *fixture) confPath() string    { return filepath.Join(f.etc, "mail", "smtpd.conf") }

// options returns the set options with the given aliases content.
func (f *fixture) options(aliases string) []opt.ConfigSetOption {
	return []opt.ConfigSetOption{
		opt.ConfigFile("aliases", f.aliasesPath(), opt.WithContent(aliases), opt.WithMode(0o644)),
		opt.ConfigFile("smtpd.conf", f.confPath(),
			opt.WithContent("table aliases file:"+opt.MemberPath("aliases")+"\n"), opt.WithMode(0o640)),
		opt.WithSetValidation("sh", []string{"-c", validatorScript, "validator",
			opt.MemberPath("smtpd.conf"), opt.MemberPath("aliases"), f.log, f.failFlag}),
	}
}

func (f *fixture) apply(aliases string) error {
	f.t.Helper()
	return f.ensure("mail", f.options(aliases)...)
}

// ensure applies set name with the fixture's system and outcome store,
// starting from an empty process-wide report unless the fixture is parallel.
func (f *fixture) ensure(name string, opts ...opt.ConfigSetOption) error {
	f.t.Helper()
	if !f.parallel {
		resource.ResetReport()
	}
	return ensure(name, f.sys, f.outcomes, opts)
}

func (f *fixture) validatorRuns() []string {
	data, err := os.ReadFile(f.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s exists (err=%v), want absent", path, err)
	}
}

// noStagingLeft asserts that no staging directory survived the apply.
func (f *fixture) noStagingLeft() {
	f.t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.etc, "mail"))
	if err != nil {
		f.t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagePrefix) {
			f.t.Fatalf("staging directory %s left behind", e.Name())
		}
	}
}

// outcomeOf returns the recorded outcome of member key of the mail set.
func (f *fixture) outcomeOf(key string) bool {
	f.t.Helper()
	changed, ok := f.outcomes.member("mail", key)
	if !ok {
		f.t.Fatalf("no outcome recorded for member %s", key)
	}
	return changed
}

func TestApplyStagesValidatesAndPublishesCompleteSet(t *testing.T) {
	f := newFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := readFile(t, f.aliasesPath()); got != "root: paul\n" {
		t.Fatalf("aliases = %q", got)
	}
	// The live config references the LIVE table; the validator saw staged paths.
	if got, want := readFile(t, f.confPath()), "table aliases file:"+f.aliasesPath()+"\n"; got != want {
		t.Fatalf("smtpd.conf = %q, want %q", got, want)
	}
	runs := f.validatorRuns()
	if len(runs) != 1 || strings.Contains(runs[0], f.aliasesPath()) || !strings.Contains(runs[0], stagePrefix+"mail+") {
		t.Fatalf("validator runs = %q, want one run on staged candidates only", runs)
	}
	info, err := os.Stat(f.aliasesPath())
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("aliases mode = %v, %v; want 0644", info.Mode().Perm(), err)
	}
	f.noStagingLeft()
	if !f.outcomeOf("aliases") || !f.outcomeOf("smtpd.conf") || !resource.AnyChanged(setID("mail")) {
		t.Fatal("first apply must report the set and both members as changed")
	}
}

func TestReplayIsNoOpWithoutValidatorRun(t *testing.T) {
	f := newFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if runs := f.validatorRuns(); len(runs) != 1 {
		t.Fatalf("validator ran %d times, want only on the first apply", len(runs))
	}
	if f.outcomeOf("aliases") || f.outcomeOf("smtpd.conf") || resource.AnyChanged(setID("mail")) {
		t.Fatal("replay must report nothing changed")
	}
}

func TestMemberLevelChangeReports(t *testing.T) {
	f := newFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.apply("root: paul\npostmaster: root\n"); err != nil {
		t.Fatal(err)
	}
	if !f.outcomeOf("aliases") || f.outcomeOf("smtpd.conf") {
		t.Fatal("only the aliases member may report a change")
	}
	if !resource.AnyChanged(setID("mail")) {
		t.Fatal("the aggregate set must report a change when any member changed")
	}
	if runs := f.validatorRuns(); len(runs) != 2 {
		t.Fatalf("validator runs = %d, want 2 (a changed table revalidates the whole set)", len(runs))
	}
}

func TestLiveDriftIsValidatedAndRepaired(t *testing.T) {
	f := newParallelFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.confPath(), []byte("hand edit\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f.confPath()); !strings.HasPrefix(got, "table aliases") {
		t.Fatalf("drift not repaired: %q", got)
	}
	if !f.outcomeOf("smtpd.conf") || f.outcomeOf("aliases") {
		t.Fatal("drift repair must report exactly the repaired member")
	}
	if runs := f.validatorRuns(); len(runs) != 2 {
		t.Fatalf("validator runs = %d, want the repair to be validated too", len(runs))
	}
}

func TestValidationFailureLeavesLiveUntouched(t *testing.T) {
	f := newParallelFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, f.aliasesPath())
	if err := os.WriteFile(f.failFlag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := f.apply("broken\n")
	if err == nil || !strings.Contains(err.Error(), "syntax error in candidate") || !strings.Contains(err.Error(), "nothing published") {
		t.Fatalf("apply error = %v, want the validator's output and a nothing-published statement", err)
	}
	if got := readFile(t, f.aliasesPath()); got != before {
		t.Fatalf("live aliases changed to %q after failed validation", got)
	}
	f.noStagingLeft()
	if _, ok := f.outcomes.member("mail", "aliases"); ok {
		t.Fatal("a failed set must not record member outcomes")
	}
	if err := applyMember(f.outcomes, "mail", "aliases"); err == nil {
		t.Fatal("a member handle must fail when its set did not apply")
	}
}

func TestValidationFailureOnFirstApplyCreatesNothing(t *testing.T) {
	f := newParallelFixture(t)
	if err := os.WriteFile(f.failFlag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.apply("root: paul\n"); err == nil {
		t.Fatal("apply must fail")
	}
	mustNotExist(t, f.aliasesPath())
	mustNotExist(t, f.confPath())
	f.noStagingLeft()
}

func TestDryRunReportsWithoutStagingOrWriting(t *testing.T) {
	f := newFixture(t)
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	mustNotExist(t, f.aliasesPath())
	if runs := f.validatorRuns(); len(runs) != 0 {
		t.Fatalf("dry-run ran the validator: %q", runs)
	}
	if !f.outcomeOf("aliases") || !resource.AnyChanged(setID("mail")) {
		t.Fatal("dry-run must report would-change for the set and its members")
	}
	f.noStagingLeft()
}

func TestNonRegularLivePathIsRefusedBeforeAnyWrite(t *testing.T) {
	f := newParallelFixture(t)
	if err := os.Symlink("/etc/passwd", f.confPath()); err != nil {
		t.Fatal(err)
	}
	err := f.apply("root: paul\n")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("apply error = %v, want refusal of the symlink", err)
	}
	mustNotExist(t, f.aliasesPath())
	if len(f.validatorRuns()) != 0 {
		t.Fatal("validator must not run when a member path is refused")
	}
}

func TestMetadataDriftRepairedWithoutChangeReport(t *testing.T) {
	f := newParallelFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.aliasesPath(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.aliasesPath())
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, %v; want repaired 0644", info.Mode().Perm(), err)
	}
	if f.outcomeOf("aliases") {
		t.Fatal("a metadata-only repair must not arm member change gates (same as File)")
	}
}

func TestTargetRefusesContentOptions(t *testing.T) {
	if _, err := file.NewTarget("/etc/x", opt.WithContent("x")); err == nil {
		t.Fatal("NewTarget must refuse content options")
	}
}

// Member outcomes live in the store of the apply that recorded them, not in
// package state: a member handle reading another store fails as if its set
// had never applied, while the set's own store serves it.
func TestOutcomesAreScopedToTheirStore(t *testing.T) {
	f := newParallelFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if _, ok := newOutcomeStore().member("mail", "aliases"); ok {
		t.Fatal("a fresh store must not see another apply's outcomes")
	}
	if err := applyMember(newOutcomeStore(), "mail", "aliases"); err == nil || !strings.Contains(err.Error(), "has not been applied") {
		t.Fatalf("member handle on a foreign store: err = %v, want the not-applied failure", err)
	}
	if !f.outcomeOf("aliases") {
		t.Fatal("the set's own store must hold the aliases change")
	}
	if _, ok := f.outcomes.member("mail", "no-such-member"); ok {
		t.Fatal("an unknown member must have no outcome")
	}
}

// Only a set handler and the member handler created with it (newHandlers)
// share outcomes; the member handler of another pair fails.
func TestPlanHandlerPairsShareOnlyTheirOwnStore(t *testing.T) {
	f := newParallelFixture(t)
	c, err := build("mail", f.options("root: paul\n"))
	if err != nil {
		t.Fatal(err)
	}
	set, member := newHandlers(f.sys)
	op, err := set.ToOp(c.spec.planDraft(setID("mail"), nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Apply(op, plan.ApplyContext{}); err != nil {
		t.Fatal(err)
	}
	memberOp := plan.Op{Op: plan.KindConfigSetMember, Name: "mail", Member: "aliases"}
	if err := member.Apply(memberOp, plan.ApplyContext{}); err != nil {
		t.Fatalf("paired member handler: %v", err)
	}
	_, otherMember := newHandlers(f.sys)
	if err := otherMember.Apply(memberOp, plan.ApplyContext{}); err == nil {
		t.Fatal("a member handler of another pair must not see this set's outcomes")
	}
}

// Present wires the set's applier and its member appliers to one store of
// their own, so the legacy resource.Apply path reports each member handle.
func TestPresentMemberHandlesReadTheirSetsOutcomes(t *testing.T) {
	f := newFixture(t)
	h := Present("mail", f.options("root: paul\n")...)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if !resource.AnyChanged(h.Member("aliases").ID()) || !resource.AnyChanged(h.Member("smtpd.conf").ID()) {
		t.Fatal("both member handles must report the first publication")
	}
	if got := readFile(t, f.aliasesPath()); got != "root: paul\n" {
		t.Fatalf("aliases = %q", got)
	}
}
