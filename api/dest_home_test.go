package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// TestDestHomeShape pins DestHome's output: the ${HOME} token plus the
// cleaned elements, never the controller's home.
func TestDestHomeShape(t *testing.T) {
	t.Setenv("HOME", "/controller/home")
	cases := []struct {
		elem []string
		want string
	}{
		{nil, "${HOME}"},
		{[]string{".bashrc"}, "${HOME}/.bashrc"},
		{[]string{".config", "app/", "x.conf"}, "${HOME}/.config/app/x.conf"},
		{[]string{"a/../b"}, "${HOME}/b"},
	}
	for _, tc := range cases {
		if got := DestHome(tc.elem...); got != tc.want {
			t.Errorf("DestHome(%q) = %q, want %q", tc.elem, got, tc.want)
		}
	}
	if got := Home(".bashrc"); got != "/controller/home/.bashrc" {
		t.Errorf("Home(.bashrc) = %q, want the controller home", got)
	}
}

// TestDestHomeEscapeIsDeclarationError: an element leaving the home would
// drop the token silently, so it is refused.
func TestDestHomeEscapeIsDeclarationError(t *testing.T) {
	requireDeclErr(t, "escapes the home directory", func() {
		DestHome("..", "etc", "passwd")
	})
}

// TestControllerSourceRefusesHomeToken: sources are read on the controller,
// where ${HOME} would be a literal directory name, so every source option
// refuses it; so does a user's WithHome, whose ${HOME} would be the
// applying user's home rather than the account's.
func TestControllerSourceRefusesHomeToken(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dst")
	cases := map[string]func(){
		"InstallFile": func() { InstallFile(dst, DestHome("src")) },
		"SyncDir":     func() { SyncDir(dst, DestHome("assets", "*")) },
		"WithSource":  func() { Dir(dst, options.WithSource(DestHome("tree"))) },
		"WithSourceBase": func() {
			Dir(dst, options.WithSourceGlob("/tmp/*"), options.WithSourceBase(DestHome("x")))
		},
		"ConfigFile source": func() {
			ConfigSet("s", options.ConfigFile("a", "/etc/a", options.WithSource(DestHome("a"))),
				options.WithSetValidation("true", nil))
		},
	}
	for name, declare := range cases {
		t.Run(name, func(t *testing.T) {
			requireDeclErr(t, "a source is read on the controller", declare)
		})
	}
	t.Run("WithHome", func(t *testing.T) {
		requireDeclErr(t, "not the account's", func() {
			User("svc", options.WithHome(DestHome("svc")))
		})
	})
}

// destHomeFixture is a controller home (the recording host's $HOME, holding
// the recipe's sources) and a separate destination home the plan applies
// under.
type destHomeFixture struct {
	controller, dest, src string
}

func newDestHomeFixture(t *testing.T) destHomeFixture {
	t.Helper()
	f := destHomeFixture{controller: t.TempDir(), dest: t.TempDir()}
	f.src = filepath.Join(f.controller, "src")
	writeTestFile(t, filepath.Join(f.src, "assets", "a.conf"), "a\n")
	writeTestFile(t, filepath.Join(f.src, "single"), "single\n")
	if err := os.Mkdir(filepath.Join(f.dest, ".gate"), 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func writeTestFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// declare is the recipe: every destination path field under DestHome, every
// source under the controller home.
func (f destHomeFixture) declare() {
	File(DestHome(".file"), options.WithContent("f\n"))
	Dir(DestHome(".dir"))
	EnsureDir(DestHome(".ensure_dir"))
	EnsureFile(DestHome(".ensure_file"))
	Link(DestHome(".symlink"), options.WithSymlink(DestHome(".file")))
	LinkIfExists(DestHome(".link_if_exists"), DestHome(".dir"))
	SymlinkMap(DestHome(), ".symlink_map", DestHome(".file"))
	SyncDir(DestHome(".sync_dir"), filepath.Join(f.src, "assets", "*"))
	InstallFile(DestHome(".install"), filepath.Join(f.src, "single"))
	// Argv is payload, never expanded: the relative arg lands in the
	// expanded WithDir.
	Command("touch", List(".cmd_ran"),
		options.WithDir(DestHome()), options.Creates(DestHome(".cmd_ran")))
	WhenPathExists(DestHome(".gate"), func() {
		File(DestHome(".gated"), options.WithContent("g\n"))
	})
	WhenPathExists(DestHome(".no_gate"), func() {
		File(DestHome(".never"), options.WithContent("n\n"))
	})
}

// check requires every declared destination path under home.
func (f destHomeFixture) check(t *testing.T, home string) {
	t.Helper()
	for _, name := range []string{".file", ".dir", ".ensure_dir", ".ensure_file", ".install",
		".sync_dir/a.conf", ".cmd_ran", ".gated"} {
		if _, err := os.Stat(filepath.Join(home, name)); err != nil {
			t.Errorf("%s not applied under the destination home: %v", name, err)
		}
	}
	for link, target := range map[string]string{".symlink": ".file", ".link_if_exists": ".dir", ".symlink_map": ".file"} {
		got, err := os.Readlink(filepath.Join(home, link))
		if err != nil || got != filepath.Join(home, target) {
			t.Errorf("%s -> %q (%v), want the expanded %s", link, got, err, filepath.Join(home, target))
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".never")); err == nil {
		t.Error(".never applied although its path_exists guard fails")
	}
}

// TestDestHomeRecordsTokenAndExpandsOnDestination records a recipe with
// DestHome paths under one $HOME and applies the decoded plan under another:
// the plan carries only ${HOME} (never the controller home), and every
// destination path field (file/dir/ensure_*, link symlink target,
// link_if_exists path and target, sync_dir, command dir/creates, the
// path_exists guard) lands under the destination home.
func TestDestHomeRecordsTokenAndExpandsOnDestination(t *testing.T) {
	f := newDestHomeFixture(t)
	ResetForTest()
	t.Cleanup(ResetForTest)
	t.Setenv("HOME", f.controller)
	Task("home", "", f.declare)
	planDir := testutil.PrivateTempDir(t)
	ops, err := RecordPlan("home", planDir, "home")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(raw)
	if strings.Contains(wire, f.controller+"/.") || !strings.Contains(wire, `"path_exists":"${HOME}/.gate"`) {
		t.Fatalf("plan should carry ${HOME} destinations only:\n%s", wire)
	}
	if ops[0].Version != plan.VersionConfigSet {
		t.Errorf("header v%d, want v%d: ${HOME} outside a config_set needs no bump", ops[0].Version, plan.VersionConfigSet)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", f.dest)
	if err := ApplyPlan(decoded, planDir); err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	f.check(t, f.dest)
}

// TestDestHomeDirectApplyExpandsLocally: without recording, this host is
// the destination, so the direct-mode probes (EnsureDir, LinkIfExists,
// WhenPathExists) expand ${HOME} here too.
func TestDestHomeDirectApplyExpandsLocally(t *testing.T) {
	f := newDestHomeFixture(t)
	ResetForTest()
	t.Cleanup(ResetForTest)
	t.Setenv("HOME", f.dest)
	// Direct LinkIfExists probes its target while the recipe declares, so
	// the targets must exist before (unlike the recorded link_if_exists,
	// decided at apply after earlier ops ran).
	writeTestFile(t, filepath.Join(f.dest, ".file"), "f\n")
	if err := os.Mkdir(filepath.Join(f.dest, ".dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.declare()
	if err := Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.check(t, f.dest)
}

// TestDestHomeConfigSet records a config set whose members, chroot and
// staging directory sit under DestHome: the header declares v26
// (VersionHomeToken) and the destination publishes under its own home.
func TestDestHomeConfigSet(t *testing.T) {
	controller, dest := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(dest, ".app"), 0o700); err != nil {
		t.Fatal(err)
	}
	ResetForTest()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		ResetForTest()
	})
	t.Setenv("HOME", controller)
	Task("app", "", func() {
		ConfigSet("app",
			options.ConfigFile("conf", DestHome(".app", "app.conf"), options.WithContent("x\n")),
			options.WithChroot(DestHome()), options.WithStagingDir(DestHome(".app")),
			options.WithSetValidation("test", []string{"-s", options.MemberPath("conf")}))
	})
	ops, err := RecordPlan("app", "", "app")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if ops[0].Version != plan.VersionHomeToken {
		t.Fatalf("header v%d, want v%d", ops[0].Version, plan.VersionHomeToken)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), controller) {
		t.Fatalf("plan leaks the controller home:\n%s", raw)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dest)
	if err := ApplyPlan(decoded, ""); err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, ".app", "app.conf")); err != nil || string(got) != "x\n" {
		t.Fatalf("member = %q (%v), want it published under the destination home", got, err)
	}
}

// TestDestHomeOnChangeFires: a change gate watching a DestHome resource
// fires when that resource changed. The plan records File[${HOME}/...],
// the apply notes the expanded path; the gate must match them, and a
// second, converged apply must hold it.
func TestDestHomeOnChangeFires(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	home := testutil.PrivateTempDir(t)
	t.Setenv("HOME", home)
	marker := filepath.Join(home, "fired")
	Task("gate", "", func() {
		conf := File(DestHome("app.conf"), options.WithContent("a=1\n"))
		Command("sh", []string{"-c", `printf 'x' >> "$1"`, "gonf", marker},
			options.WithName("reload"), options.OnChange(conf))
	})
	for i, want := range []string{"x", "x"} {
		if err := Run("gate"); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		got, err := os.ReadFile(marker)
		if err != nil {
			t.Fatalf("run %d: gated command did not run: %v", i, err)
		}
		if string(got) != want {
			t.Fatalf("run %d: marker = %q, want %q", i, got, want)
		}
	}
}
