package configset

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
	opt "github.com/snonux/gonf/resource/options"
)

// fixture is a temp "system": etc is the staging parent holding a mail-like
// set whose main config references its table through a member placeholder.
type fixture struct {
	t        *testing.T
	root     string
	etc      string
	log      string // validator invocation log
	failFlag string // validator fails while this file exists
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	root := t.TempDir()
	f := &fixture{t: t, root: root, etc: filepath.Join(root, "etc"), log: filepath.Join(root, "validator.log"), failFlag: filepath.Join(root, "fail")}
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
	resource.ResetReport()
	return Ensure("mail", f.options(aliases)...)
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

func outcomeOf(t *testing.T, key string) bool {
	t.Helper()
	changed, ok := memberOutcome("mail", key)
	if !ok {
		t.Fatalf("no outcome recorded for member %s", key)
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
	if !outcomeOf(t, "aliases") || !outcomeOf(t, "smtpd.conf") || !resource.AnyChanged(setID("mail")) {
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
	if outcomeOf(t, "aliases") || outcomeOf(t, "smtpd.conf") || resource.AnyChanged(setID("mail")) {
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
	if !outcomeOf(t, "aliases") || outcomeOf(t, "smtpd.conf") {
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
	f := newFixture(t)
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
	if !outcomeOf(t, "smtpd.conf") || outcomeOf(t, "aliases") {
		t.Fatal("drift repair must report exactly the repaired member")
	}
	if runs := f.validatorRuns(); len(runs) != 2 {
		t.Fatalf("validator runs = %d, want the repair to be validated too", len(runs))
	}
}

func TestValidationFailureLeavesLiveUntouched(t *testing.T) {
	f := newFixture(t)
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
	if _, ok := memberOutcome("mail", "aliases"); ok {
		t.Fatal("a failed set must not record member outcomes")
	}
	if err := applyMember("mail", "aliases"); err == nil {
		t.Fatal("a member handle must fail when its set did not apply")
	}
}

func TestValidationFailureOnFirstApplyCreatesNothing(t *testing.T) {
	f := newFixture(t)
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
	if !outcomeOf(t, "aliases") || !resource.AnyChanged(setID("mail")) {
		t.Fatal("dry-run must report would-change for the set and its members")
	}
	f.noStagingLeft()
}

func TestNonRegularLivePathIsRefusedBeforeAnyWrite(t *testing.T) {
	f := newFixture(t)
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
	f := newFixture(t)
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
	if outcomeOf(t, "aliases") {
		t.Fatal("a metadata-only repair must not arm member change gates (same as File)")
	}
}

func TestTargetRefusesContentOptions(t *testing.T) {
	if _, err := file.NewTarget("/etc/x", opt.WithContent("x")); err == nil {
		t.Fatal("NewTarget must refuse content options")
	}
}
