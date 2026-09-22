package api

import (
	"encoding/base64"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	internalexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/options"
)

const (
	inetdClassFixture  = "inetd:\\\n    :maxproc=10:\\\n    :tc=daemon:\n"
	daemonClassFixture = "# raised for relayd's many keypairs\ndaemon:\\\n\t:openfiles-max=4096:\\\n\t:openfiles-cur=4096:\\\n\t:tc=default:\n"
)

// loginClassTasks wraps a recipe body as the single task "demo_class".
type loginClassTasks struct {
	body func()
}

func (loginClassTasks) DescClass() string { return "composed login class" }
func (t loginClassTasks) Class()          { t.body() }

// recordLoginClass records body as one task and returns its plan ops after a
// JSONL round trip, i.e. exactly what a destination would decode.
func recordLoginClass(t *testing.T, body func()) []plan.Op {
	t.Helper()
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})
	RegisterMethods(loginClassTasks{body: body}, WithPrefix("demo_"))
	ops, err := RecordPlan("class", testutil.PrivateTempDir(t), "demo_class")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	wire, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	decoded, err := plan.DecodePlanBytes(wire)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}
	return decoded
}

func opIDs(ops []plan.Op) []string {
	ids := make([]string, 0, len(ops))
	for _, op := range ops {
		ids = append(ids, string(op.Op)+":"+op.ID)
	}
	return ids
}

func TestLoginClassRecordsRequirementBlockAndFragment(t *testing.T) {
	src := filepath.Join(t.TempDir(), "inetd")
	writeFixtureFile(t, src, inetdClassFixture)

	var handle Resource
	ops := recordLoginClass(t, func() { handle = LoginClass("inetd", src) })

	want := []string{
		"plan:class",
		"when_begin:when.require_goos:openbsd:LoginClass[inetd]",
		"file:File[/etc/login.conf.d/inetd.db]",
		"file:File[/etc/login.conf.d/inetd]",
		"when_end:",
	}
	if got := opIDs(ops); !reflect.DeepEqual(got, want) {
		t.Fatalf("ops = %v, want %v (no cap_mkdb, no command gate)", got, want)
	}
	if ops[0].Version < plan.VersionWhenRequire {
		t.Fatalf("plan version = %d, want >= VersionWhenRequire for the require field", ops[0].Version)
	}
	gate := ops[1]
	if !reflect.DeepEqual(gate.All, []plan.Predicate{{Fact: "goos", Eq: "openbsd"}}) ||
		!strings.Contains(gate.Require, "LoginClass[inetd]: only OpenBSD reads per-class fragments") {
		t.Fatalf("requirement block = %#v", gate)
	}
	if db := ops[2]; !db.Absent {
		t.Fatalf("stale database op = %#v, want a removal", db)
	}
	fragment := ops[3]
	if fragment.Mode != "0644" || fragment.Owner != "root" || fragment.Group != "wheel" || fragment.Absent {
		t.Fatalf("fragment = %s %s:%s absent=%v, want 0644 root:wheel present",
			fragment.Mode, fragment.Owner, fragment.Group, fragment.Absent)
	}
	wantHandle := []string{"File[/etc/login.conf.d/inetd.db]", "File[/etc/login.conf.d/inetd]"}
	if got := handle.Dependencies(); !reflect.DeepEqual(got, wantHandle) {
		t.Fatalf("change handle = %v, want %v", got, wantHandle)
	}
}

// TestLoginClassChangeHandleGatesDaemonRestart mirrors the frontend inetd
// recipe: the restart watches only the class files and the other inputs.
func TestLoginClassChangeHandleGatesDaemonRestart(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, filepath.Join(dir, "inetd"), inetdClassFixture)
	writeFixtureFile(t, filepath.Join(dir, "inetd.conf"), "daytime stream tcp nowait root internal\n")

	ops := recordLoginClass(t, func() {
		class := LoginClass("inetd", filepath.Join(dir, "inetd"))
		config := InstallFile("/etc/inetd.conf", filepath.Join(dir, "inetd.conf"), options.WithMode(0o644))
		Service("inetd", options.WithRestart, options.OnChange(class, config))
	})
	svc := ops[len(ops)-1]
	want := []string{"File[/etc/login.conf.d/inetd.db]", "File[/etc/login.conf.d/inetd]", "File[/etc/inetd.conf]"}
	if svc.Op != plan.KindService || !svc.Restart || !svc.IfChanged || !reflect.DeepEqual(svc.Watch, want) {
		t.Fatalf("service = %#v, want restart gated on %v", svc, want)
	}
}

func TestLoginClassCallerOptionsRefineDefaultsAndContent(t *testing.T) {
	dir := t.TempDir()
	wrong := filepath.Join(dir, "wrong")
	writeFixtureFile(t, wrong, "other:\\\n\t:tc=default:\n")
	right := filepath.Join(dir, "daemon")
	writeFixtureFile(t, right, daemonClassFixture)

	// The caller's WithSource wins over src, so the installed (and
	// validated) content is the correct fragment, not the mismatching src.
	ops := recordLoginClass(t, func() {
		LoginClass("daemon", wrong, options.WithSource(right), options.WithMode(0o640), options.WithGroup("_daemon"))
	})
	fragment := ops[3]
	if fragment.ID != "File[/etc/login.conf.d/daemon]" || fragment.Mode != "0640" ||
		fragment.Owner != "root" || fragment.Group != "_daemon" {
		t.Fatalf("fragment = %#v, want caller overrides on top of defaults", fragment)
	}

	// WithContent works without any src.
	ops = recordLoginClass(t, func() { LoginClass("staff", "", options.WithContent("staff|Staff:\\\n\t:tc=default:\n")) })
	if ops[3].ID != "File[/etc/login.conf.d/staff]" || !ops[3].HasContent {
		t.Fatalf("inline content fragment = %#v", ops[3])
	}
}

func TestLoginClassRecordNames(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{inetdClassFixture, []string{"inetd"}},
		{daemonClassFixture, []string{"daemon"}},
		{"\n# only a comment\n", nil},
		{"staff|Staff members:\\\n\t:tc=default:\n", []string{"staff", "Staff members"}},
		{"bare\\\n\t:tc=default:\n", []string{"bare"}},
		{"first:\\\n\t:tc=default:\n\n# second\nsecond|alias:\\\n\t:tc=first:\n", []string{"first", "second", "alias"}},
		// Continuation lines belong to the record they continue: a
		// continued line that does not start with ':' must not become a
		// record (and so a class) of its own.
		{"inetd:\\\nmaxproc=10:\\\ntc=daemon:\n", []string{"inetd"}},
		{"a|b\\\n|c:\\\n\t:tc=default:\n", []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		if got := loginClassRecordNames(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("loginClassRecordNames(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestLoginClassRecordsJoinsContinuations pins the logical-record join: the
// continued physical lines form one record, comments and blanks vanish.
func TestLoginClassRecordsJoinsContinuations(t *testing.T) {
	got := loginClassRecords("# c\ninetd:\\\nmaxproc=10:\\\ntc=daemon:\n\nb:x:\n")
	want := []string{"inetd:maxproc=10:tc=daemon:", "b:x:"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loginClassRecords = %q, want %q", got, want)
	}
}

// TestLoginClassValidatesTheSourceFileItInstalls: a caller's WithSource path
// is read verbatim by the file resource ("~" is not expanded there), so
// validation must read the same verbatim path. The working directory holds a
// literal "~/relayd" with the right class, while $HOME/relayd holds a wrong
// one: expanding in validation would inspect (and reject) a file that is not
// the one installed.
func TestLoginClassValidatesTheSourceFileItInstalls(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	const good = "relayd:\\\n\t:tc=daemon:\n"
	writeFixtureFile(t, filepath.Join(cwd, "~", "relayd"), good)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFixtureFile(t, filepath.Join(home, "relayd"), "other:\\\n\t:tc=default:\n")

	ops := recordLoginClass(t, func() {
		LoginClass("relayd", "", options.WithSource("~/relayd"))
	})
	fragment := ops[3]
	content, err := base64.StdEncoding.DecodeString(fragment.ContentB64)
	if fragment.ID != "File[/etc/login.conf.d/relayd]" || err != nil || string(content) != good {
		t.Fatalf("fragment %s content = %q (%v), want the validated verbatim-path file %q", fragment.ID, content, err, good)
	}
}

func TestLoginClassAcceptsClassInLaterRecordOrAlias(t *testing.T) {
	src := filepath.Join(t.TempDir(), "relayd")
	writeFixtureFile(t, src, "base:\\\n\t:tc=default:\n\nrelay|relayd:\\\n\t:tc=base:\n")
	ops := recordLoginClass(t, func() { LoginClass("relayd", src) })
	if ops[3].ID != "File[/etc/login.conf.d/relayd]" {
		t.Fatalf("ops = %v", opIDs(ops))
	}
}

// loginClassApplyFixture records a plan into a private fragment directory:
// an unrelated file written BEFORE the class (to prove the refusal happens
// before any mutation), the class, and a change-gated watcher command. Owner
// and group are the invoking user's so an unprivileged test can apply.
type loginClassApplyFixture struct {
	ops                    []plan.Op
	fragment, db, earlier  string
	watcherRuns, otherRuns *int
}

func newLoginClassApplyFixture(t *testing.T, absent bool) *loginClassApplyFixture {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "inetd")
	writeFixtureFile(t, src, inetdClassFixture)
	fragmentDir := filepath.Join(dir, "login.conf.d")
	if err := os.Mkdir(fragmentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := loginClassDir
	loginClassDir = fragmentDir
	t.Cleanup(func() { loginClassDir = old })

	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Fatal(err)
	}
	f := &loginClassApplyFixture{
		fragment: filepath.Join(fragmentDir, "inetd"),
		db:       filepath.Join(fragmentDir, "inetd.db"),
		earlier:  filepath.Join(dir, "earlier"),
	}
	f.ops = recordLoginClass(t, func() {
		File(f.earlier, options.WithContent("x\n"), options.WithMode(0o600))
		var class Resource
		if absent {
			class = NoLoginClass("inetd")
		} else {
			class = LoginClass("inetd", src, options.WithOwner(u.Username), options.WithGroup(g.Name))
		}
		Command("restart-watcher", nil, options.OnChange(class))
	})
	f.watcherRuns, f.otherRuns = fakeWatcherRunner(t)
	return f
}

// fakeWatcherRunner counts executions of the watcher command and of anything
// else; nothing real is executed.
func fakeWatcherRunner(t *testing.T) (*int, *int) {
	t.Helper()
	watcher, other := 0, 0
	cmd.SetRunnersForTest(func(_ internalexec.Opts, name string, _ ...string) (string, string, int, error) {
		if name == "restart-watcher" {
			watcher++
		} else {
			other++
		}
		return "", "", 0, nil
	}, nil)
	t.Cleanup(cmd.ResetRunnersForTest)
	return &watcher, &other
}

func (f *loginClassApplyFixture) apply(t *testing.T, goos string) error {
	t.Helper()
	return plan.Apply(f.ops, plan.Facts{GOOS: goos}, "")
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists (or stat failed): %v", path, err)
	}
}

func TestLoginClassAppliesOnOpenBSDAndRestartsOnlyOnChange(t *testing.T) {
	f := newLoginClassApplyFixture(t, false)
	if err := f.apply(t, "openbsd"); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if got, err := os.ReadFile(f.fragment); err != nil || string(got) != inetdClassFixture {
		t.Fatalf("fragment = %q, %v", got, err)
	}
	if *f.watcherRuns != 1 || *f.otherRuns != 0 {
		t.Fatalf("first apply: watcher %d, other commands %d; want 1 and 0 (no cap_mkdb)", *f.watcherRuns, *f.otherRuns)
	}

	*f.watcherRuns = 0
	if err := f.apply(t, "openbsd"); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if *f.watcherRuns != 0 {
		t.Fatalf("unchanged second apply ran the watcher %d times", *f.watcherRuns)
	}

	// A stale compiled database shadows the text: it is removed and that
	// removal fires the watcher, even though the fragment is unchanged.
	writeFixtureFile(t, f.db, "stale")
	if err := f.apply(t, "openbsd"); err != nil {
		t.Fatalf("stale-db apply: %v", err)
	}
	mustNotExist(t, f.db)
	if *f.watcherRuns != 1 {
		t.Fatalf("stale-db removal ran the watcher %d times, want 1", *f.watcherRuns)
	}

	// Drift in the live fragment is repaired and fires the watcher again.
	writeFixtureFile(t, f.fragment, "inetd:\\\n\t:maxproc=1:\n")
	*f.watcherRuns = 0
	if err := f.apply(t, "openbsd"); err != nil {
		t.Fatalf("repair apply: %v", err)
	}
	if *f.watcherRuns != 1 {
		t.Fatalf("repair ran the watcher %d times, want 1", *f.watcherRuns)
	}
}

func TestLoginClassDryRunOnOpenBSDPredictsWithoutWriting(t *testing.T) {
	f := newLoginClassApplyFixture(t, false)
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	if err := f.apply(t, "openbsd"); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	mustNotExist(t, f.fragment)
	mustNotExist(t, f.earlier)
	if *f.watcherRuns != 0 {
		t.Fatalf("dry run executed the watcher")
	}
}

// TestLoginClassRefusesOffOpenBSD covers apply and dry run on each
// unsupported destination: the refusal names that destination's GOOS (the
// fake facts, not this test host), happens before the unrelated earlier file
// is written, and never reaches the watcher.
func TestLoginClassRefusesOffOpenBSD(t *testing.T) {
	for _, goos := range []string{"freebsd", "netbsd", "linux"} {
		for _, dry := range []bool{false, true} {
			name := goos
			if dry {
				name += "-dry-run"
			}
			t.Run(name, func(t *testing.T) {
				f := newLoginClassApplyFixture(t, false)
				resource.SetDryRun(dry)
				t.Cleanup(func() { resource.SetDryRun(false) })

				err := f.apply(t, goos)
				if err == nil {
					t.Fatal("apply succeeded on a platform without login.conf.d fragments")
				}
				for _, want := range []string{"goos=" + goos, "LoginClass[inetd]: only OpenBSD", "nothing was applied"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q misses %q", err, want)
					}
				}
				mustNotExist(t, f.fragment)
				mustNotExist(t, f.earlier)
				if *f.watcherRuns != 0 || *f.otherRuns != 0 {
					t.Fatalf("refused plan ran commands: watcher %d, other %d", *f.watcherRuns, *f.otherRuns)
				}
				var ids []string
				for _, op := range f.ops {
					if op.ID != "" {
						ids = append(ids, op.ID)
					}
				}
				if resource.AnyChanged(ids...) {
					t.Fatalf("refused plan reported a (would-)change among %v", ids)
				}
			})
		}
	}
}

func TestLoginClassRequirementIgnoredInInactiveScope(t *testing.T) {
	src := filepath.Join(t.TempDir(), "inetd")
	writeFixtureFile(t, src, inetdClassFixture)
	ops := recordLoginClass(t, func() {
		WhenHostname("no-such-host-substring", func() { LoginClass("inetd", src) })
	})
	if err := plan.Apply(ops, plan.Facts{GOOS: "freebsd", Hostname: "freebsd-box"}, ""); err != nil {
		t.Fatalf("requirement inside an inactive host block must not refuse: %v", err)
	}
}

// TestLoginClassUnderPathConditionRefusedAtRecord: a requirement may only sit
// under host-fact conditions, so LoginClass inside WhenPathExists is refused
// when the plan is recorded, naming the requirement and the condition.
func TestLoginClassUnderPathConditionRefusedAtRecord(t *testing.T) {
	src := filepath.Join(t.TempDir(), "inetd")
	writeFixtureFile(t, src, inetdClassFixture)
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})
	RegisterMethods(loginClassTasks{body: func() {
		WhenPathExists("/etc/login.conf.d", func() { LoginClass("inetd", src) })
	}}, WithPrefix("demo_"))
	out := testutil.PrivateTempDir(t)
	_, err := RecordPlan("class", out, "demo_class")
	want := "requirement when.require_goos:openbsd:LoginClass[inetd] is nested under " +
		"when.path_exists:/etc/login.conf.d (path_exists /etc/login.conf.d)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("RecordPlan = %v, want refusal containing %q", err, want)
	}
	if _, statErr := os.Stat(filepath.Join(out, "plan.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("refused plan was written: %v", statErr)
	}
}

func TestNoLoginClassRemovesFragmentAndStaleDB(t *testing.T) {
	f := newLoginClassApplyFixture(t, true)
	var removal *plan.Op
	for i := range f.ops {
		if f.ops[i].Path == f.fragment {
			removal = &f.ops[i]
		}
	}
	if removal == nil || !removal.Absent || removal.HasContent {
		t.Fatalf("removal op = %#v in %v", removal, opIDs(f.ops))
	}
	writeFixtureFile(t, f.fragment, inetdClassFixture)
	writeFixtureFile(t, f.db, "stale")
	if err := f.apply(t, "openbsd"); err != nil {
		t.Fatalf("removal apply: %v", err)
	}
	mustNotExist(t, f.fragment)
	mustNotExist(t, f.db)
	if *f.watcherRuns != 1 {
		t.Fatalf("removal ran the watcher %d times, want 1", *f.watcherRuns)
	}
	*f.watcherRuns = 0
	if err := f.apply(t, "openbsd"); err != nil || *f.watcherRuns != 0 {
		t.Fatalf("second removal apply: err %v, watcher %d; want a no-op", err, *f.watcherRuns)
	}
	if err := f.apply(t, "netbsd"); err == nil || !strings.Contains(err.Error(), "goos=netbsd") {
		t.Fatalf("removal on netbsd = %v, want the OpenBSD refusal", err)
	}
}

// TestLoginClassRegistrationMisuseFailsFast runs each registration-time
// Fatal in a helper process and asserts the specific message.
func TestLoginClassRegistrationMisuseFailsFast(t *testing.T) {
	cases := []struct{ caseName, want string }{
		{"empty", `invalid class name ""`},
		{"traversal", `invalid class name "../passwd"`},
		{"slash", `invalid class name "a/b"`},
		{"separator", `invalid class name "a:b"`},
		{"dotdb", `invalid class name "daemon.db"`},
		{"mismatch", `defines classes ["daemon"] but not "relayd"`},
		{"content-mismatch", `defines classes ["other"] but not "daemon"`},
		{"lines", `WithLine(s)/WithoutLine(s) are not supported`},
		{"no-content", `LoginClass "daemon": no content`},
	}
	if runtime.GOOS != "openbsd" {
		cases = append(cases, struct{ caseName, want string }{"direct", "requirement not met on this host (goos=" + runtime.GOOS + ")"})
	}
	before := countFatalHelperTempDirs(t)
	for _, tc := range cases {
		t.Run(tc.caseName, func(t *testing.T) {
			c := exec.Command(os.Args[0], "-test.run=^TestLoginClassFatalHelperProcess$", "-test.timeout=60s")
			// The helper calls t.TempDir() itself and then exits via
			// logger.Fatal (os.Exit), which skips its own deferred cleanup
			// (t.TempDir()'s directory is removed via t.Cleanup, which never
			// runs). t.TempDir() creates its directory under $GOTMPDIR (see
			// testing.common.makeTempDir), so pointing the child's GOTMPDIR at
			// a directory this (surviving) test owns means that directory -
			// and whatever the child created below it - is removed by this
			// test's own t.TempDir() cleanup instead of leaking under the
			// real $GOTMPDIR/temp root.
			c.Env = append(os.Environ(), "GONF_API_LOGINCLASS_MISUSE="+tc.caseName, "GOTMPDIR="+t.TempDir())
			out, err := c.CombinedOutput()
			if err == nil {
				t.Fatalf("misuse %q exited 0; output:\n%s", tc.caseName, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Fatalf("misuse %q output misses %q:\n%s", tc.caseName, tc.want, out)
			}
		})
	}
	assertNoNewFatalHelperTempDirs(t, before)
}

// TestLoginClassFatalHelperProcess triggers one misuse per invocation for
// TestLoginClassRegistrationMisuseFailsFast; it must never exit 0 when armed.
// Nothing is recorded, so "direct" exercises the same-host (non-plan) path.
func TestLoginClassFatalHelperProcess(t *testing.T) {
	name := os.Getenv("GONF_API_LOGINCLASS_MISUSE")
	if name == "" {
		return
	}
	loginClassDir = t.TempDir()
	src := filepath.Join(t.TempDir(), "daemon")
	writeFixtureFile(t, src, daemonClassFixture)
	switch name {
	case "content-mismatch":
		LoginClass("daemon", src, options.WithContent("other:\\\n\t:tc=default:\n"))
	case "lines":
		LoginClass("daemon", src, options.WithLine(":maxproc=1:"))
	case "no-content":
		LoginClass("daemon", "")
	case "direct":
		LoginClass("daemon", src)
	default:
		classes := map[string]string{
			"empty": "", "traversal": "../passwd", "slash": "a/b", "separator": "a:b",
			"dotdb": "daemon.db", "mismatch": "relayd",
		}
		LoginClass(classes[name], src)
	}
}

func TestLoginClassSkipsNameCheckForUnknownOrTemplatedContent(t *testing.T) {
	// Neither may abort registration: a templated name is only known after
	// rendering, and the file resource owns missing-source errors.
	validateLoginClassContent("relayd", "{{.Class}}:\\\n\t:tc=default:\n", true)
	validateLoginClassContent("relayd", "", false)
}
