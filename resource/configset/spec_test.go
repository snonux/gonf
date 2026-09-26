package configset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

func TestRenderSubstitutesOnlyWellFormedTokens(t *testing.T) {
	resolve := func(ref tokenRef) (string, error) {
		if ref.chroot {
			return "/rel/" + ref.key, nil
		}
		return "/abs/" + ref.key, nil
	}
	in := "a " + opt.MemberPath("x") + " b " + opt.MemberChrootPath("y.conf") + " c"
	got, err := render([]byte(in), resolve)
	if err != nil || string(got) != "a /abs/x b /rel/y.conf c" {
		t.Fatalf("render = %q, %v", got, err)
	}
	for name, bad := range map[string]string{
		"unterminated": opt.MemberPathTokenPrefix + "x",
		"unknown kind": "\x00gonf-member-other:x\x00",
		"bad key":      opt.MemberPathTokenPrefix + "../x" + opt.MemberTokenSuffix,
		"empty key":    opt.MemberPath(""),
	} {
		if _, err := render([]byte(bad), resolve); err == nil {
			t.Errorf("%s: render accepted %q", name, bad)
		}
	}
	args := []string{"-f", "--conf=" + opt.MemberPath("x")}
	out, err := renderArgs(args, resolve)
	if err != nil || out[1] != "--conf=/abs/x" || args[1] == out[1] {
		t.Fatalf("renderArgs = %q, %v (caller slice must stay untouched)", out, err)
	}
}

func validSpec() spec {
	return spec{
		name: "set",
		members: []memberSpec{
			{key: "a", path: "/etc/app/a.conf", content: []byte("include " + opt.MemberPath("b"))},
			{key: "b", path: "/etc/app/keys/b.conf"},
		},
		validators: []resource.PlanArgv{{Bin: "check", Args: []string{opt.MemberPath("a")}}},
	}
}

func TestSpecValidationRejectsMisconfiguration(t *testing.T) {
	base := validSpec()
	if err := base.validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	if got := base.stagingParent(); got != "/etc/app" {
		t.Fatalf("staging parent = %q, want deepest common directory /etc/app", got)
	}
	cases := map[string]func(s *spec){
		"bad name":              func(s *spec) { s.name = "a/b" },
		"no members":            func(s *spec) { s.members = nil },
		"no validator":          func(s *spec) { s.validators = nil },
		"empty validator bin":   func(s *spec) { s.validators[0].Bin = "" },
		"token as bin":          func(s *spec) { s.validators[0].Bin = opt.MemberPath("a") },
		"duplicate key":         func(s *spec) { s.members[1].key = "a" },
		"duplicate path":        func(s *spec) { s.members[1].path = "/etc/app/a.conf" },
		"relative path":         func(s *spec) { s.members[0].path = "etc/app/a.conf" },
		"unclean path":          func(s *spec) { s.members[0].path = "/etc/app/../app/a.conf" },
		"unknown reference":     func(s *spec) { s.members[0].content = []byte(opt.MemberPath("zz")) },
		"unknown arg reference": func(s *spec) { s.validators[0].Args = []string{opt.MemberPath("zz")} },
		"chroot token no root":  func(s *spec) { s.members[0].content = []byte(opt.MemberChrootPath("b")) },
		"outside chroot":        func(s *spec) { s.chroot = "/var/nsd" },
		"staging not ancestor":  func(s *spec) { s.stagingDir = "/etc/app/keys" },
		"staging outside root":  func(s *spec) { s.chroot, s.stagingDir = "/etc/app", "/etc" },
		"malformed token":       func(s *spec) { s.members[1].content = []byte(opt.MemberPathTokenPrefix + "b") },
	}
	for name, mutate := range cases {
		s := validSpec()
		mutate(&s)
		if err := s.validate(); err == nil {
			t.Errorf("%s: validate accepted a broken spec", name)
		}
	}
}

// TestValidateExpandedAcceptsUncleanHome: a set declared under the ${HOME}
// token (api.DestHome) validates for HOME=/ (service accounts) and a HOME
// with a trailing slash, because the expansion is clean; before task fb the
// clean-path check rejected //etc/app/a.conf and /home/paul//etc/app/a.conf.
func TestValidateExpandedAcceptsUncleanHome(t *testing.T) {
	for home, root := range map[string]string{"/": "", "/home/paul/": "/home/paul", "/home//paul": "/home/paul"} {
		t.Setenv("HOME", home)
		s := validSpec()
		s.chroot = "${HOME}"
		s.stagingDir = "${HOME}/etc/app"
		for i := range s.members {
			s.members[i].path = "${HOME}" + s.members[i].path
		}
		exp, err := s.validateExpanded()
		if err != nil {
			t.Errorf("HOME=%q: validateExpanded: %v", home, err)
			continue
		}
		wantChroot := root
		if wantChroot == "" {
			wantChroot = "/"
		}
		if exp.chroot != wantChroot || exp.stagingDir != root+"/etc/app" ||
			exp.members[0].path != root+"/etc/app/a.conf" || exp.members[1].path != root+"/etc/app/keys/b.conf" {
			t.Errorf("HOME=%q: expanded chroot=%q staging=%q members=%q,%q", home,
				exp.chroot, exp.stagingDir, exp.members[0].path, exp.members[1].path)
		}
	}
}

// TestValidateExpandedStillRejectsUncleanLiteral: cleaning applies to the
// token value only, so an unclean literal part of a member path is still a
// validation error, as is an unusable (relative) home.
func TestValidateExpandedStillRejectsUncleanLiteral(t *testing.T) {
	t.Setenv("HOME", "/home/paul/")
	s := validSpec()
	s.members[0].path = "${HOME}/etc/app//a.conf"
	if _, err := s.validateExpanded(); err == nil || !strings.Contains(err.Error(), "not a clean path") {
		t.Fatalf("validateExpanded = %v, want not a clean path", err)
	}

	t.Setenv("HOME", "rel/home")
	s = validSpec()
	s.members[0].path = "${HOME}/a.conf"
	if _, err := s.validateExpanded(); err == nil || !strings.Contains(err.Error(), "not absolute") {
		t.Fatalf("validateExpanded = %v, want not absolute home", err)
	}
}

func TestChrootAndRelativeLayoutAreStaged(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	chroot := filepath.Join(t.TempDir(), "var", "nsd")
	etc := filepath.Join(chroot, "etc")
	if err := os.MkdirAll(filepath.Join(etc, "keys"), 0o755); err != nil {
		t.Fatal(err)
	}
	seen := filepath.Join(chroot, "..", "seen")
	// The validator runs in the staged mirror of etc: the relative
	// "keys/key.conf" and the chroot-relative include must both name staged
	// files inside the chroot.
	script := `test -f keys/key.conf && grep -q "^include: $2$" "$1" && case "$2" in /etc/.gonf-configset-nsd+*/candidates/keys/key.conf) : ;; *) exit 9 ;; esac && echo ok > "$3"`
	err := Ensure("nsd",
		opt.ConfigFile("conf", filepath.Join(etc, "nsd.conf"), opt.WithContent("include: "+opt.MemberChrootPath("key")+"\n")),
		opt.ConfigFile("key", filepath.Join(etc, "keys", "key.conf"), opt.WithContent("secret\n"), opt.WithMode(0o600)),
		opt.WithChroot(chroot),
		opt.WithSetValidation("sh", []string{"-c", script, "v", opt.MemberPath("conf"), opt.MemberChrootPath("key"), seen}),
	)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if readFile(t, seen) != "ok\n" {
		t.Fatal("validator did not accept the staged chroot layout")
	}
	if got := readFile(t, filepath.Join(etc, "nsd.conf")); got != "include: /etc/keys/key.conf\n" {
		t.Fatalf("live nsd.conf = %q, want the chroot-relative live include", got)
	}
}

func TestSourceMembersAreReadOnceAtBuild(t *testing.T) {
	src := filepath.Join(t.TempDir(), "aliases")
	if err := os.WriteFile(src, []byte("root: paul\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := build("mail", []opt.ConfigSetOption{
		opt.ConfigFile("aliases", "/etc/mail/aliases", opt.WithSource(src)),
		opt.WithSetValidation("true", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("changed later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := string(c.spec.members[0].content); got != "root: paul\n" || c.spec.members[0].mode != defaultMemberMode {
		t.Fatalf("member = %q mode %v, want the bytes read at build with the File default mode", got, c.spec.members[0].mode)
	}
	if _, err := build("mail", []opt.ConfigSetOption{
		opt.ConfigFile("aliases", "/etc/mail/aliases"), opt.WithSetValidation("true", nil),
	}); err == nil || !strings.Contains(err.Error(), "WithContent or WithSource") {
		t.Fatalf("member without content: err = %v", err)
	}
}

func TestStagingParentMustBeSafe(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "app.conf")
	err := Ensure("app",
		opt.ConfigFile("conf", target, opt.WithContent("x\n")),
		opt.WithSetValidation("true", nil))
	if err == nil || !strings.Contains(err.Error(), "writable by group or other") {
		t.Fatalf("apply error = %v, want the candidate-parent refusal", err)
	}
	mustNotExist(t, target)
}
