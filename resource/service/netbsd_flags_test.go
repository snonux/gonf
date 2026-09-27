package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/atomicfile"
	"github.com/snonux/gonf/resource"
)

// TestShellSyntaxCatchesOrphanedContinuation proves requireShellSyntax has
// teeth: the rc.conf the old line-by-line rewrite left behind (task 7b), a
// replaced first line and an orphaned closing-quote line, fails sh -n.
func TestShellSyntaxCatchesOrphanedContinuation(t *testing.T) {
	if err := shellSyntax(t, "nsd_flags='-x'\n  -b\"\n"); err == nil {
		t.Fatal("sh -n must reject an orphaned continuation line")
	}
}

// netbsdRcDefaultsHeader is how NetBSD's /etc/rc.conf starts.
const netbsdRcDefaultsHeader = "if [ -r /etc/defaults/rc.conf ]; then\n\t. /etc/defaults/rc.conf\nfi\n"

// TestNetBSDFlagsRewrite pins WithFlags over rc.conf texts it reads as
// sh(1) does (task 7b): a value spanning lines is read across them (so an
// equal one changes nothing) and replaced as a whole, quoting forms and
// here-documents never make it misplace a statement boundary, and only the
// assignment word changes: export, other statements and comments sharing
// its line, and every other line, are kept byte for byte. Every result is
// valid sh.
func TestNetBSDFlagsRewrite(t *testing.T) {
	for _, tt := range []struct {
		name, rcConf, flags, want string
	}{
		{"double-quoted continuation replaced whole",
			"a=1\nnsd_flags=\"-a \\\n  -b\"\nb=2\n", "-x", "a=1\nnsd_flags='-x'\nb=2\n"},
		{"single-quoted newline replaced whole",
			"nsd_flags='-a\n-b'\nb=2\n", "-x", "nsd_flags='-x'\nb=2\n"},
		{"unquoted continuation replaced whole",
			"nsd_flags=-a\\\n-b # x\nb=2\n", "-x", "nsd_flags='-x' # x\nb=2\n"},
		{"later multi-line assignment dropped whole",
			"nsd_flags=-4\nb=2\nnsd_flags=\"-a\n-b\"\nc=3\n", "-x", "nsd_flags='-x'\nb=2\nc=3\n"},
		{"double-quoted continuation matches",
			"nsd_flags=\"-a \\\n-b\"\n", "-a -b", "nsd_flags=\"-a \\\n-b\"\n"},
		{"single-quoted newline matches",
			"nsd_flags='-a\n-b'\n", "-a\n-b", "nsd_flags='-a\n-b'\n"},
		{"unquoted continuation matches",
			"nsd_flags=-a\\\nb\n", "-ab", "nsd_flags=-a\\\nb\n"},
		{"statement inside another quoted value is not one",
			"motd='hi\nnsd=NO # x\n'\nnsd_flags=-4\n", "-x", "motd='hi\nnsd=NO # x\n'\nnsd_flags='-x'\n"},
		{"apostrophe in a comment opens no quote",
			"# don't touch\nnsd_flags=-4 # it's set\nb=2\n", "-x", "# don't touch\nnsd_flags='-x' # it's set\nb=2\n"},
		{"hash inside a word is no comment",
			"motd=a#'\nnsd=NO\n'\nnsd_flags=-6\n", "-x", "motd=a#'\nnsd=NO\n'\nnsd_flags='-x'\n"},
		{"dollar-single-quoted escape",
			"nsd_flags=$'-a\\'b'\nnsd=YES\nsshd=YES\n# don't edit below\nx=1\n", "-x",
			"nsd_flags='-x'\nnsd=YES\nsshd=YES\n# don't edit below\nx=1\n"},
		{"quotes nested in a parameter expansion",
			"nsd_flags=\"${X:-\"it's\"}\"\nnsd=YES\n# don't\n", "-x", "nsd_flags='-x'\nnsd=YES\n# don't\n"},
		{"single-line command substitution with nested quotes",
			"motd=\"$(echo \")\" 'x')\"\nnsd_flags=\"$(echo \"a b\")\"\nc=1\n", "-x",
			"motd=\"$(echo \")\" 'x')\"\nnsd_flags='-x'\nc=1\n"},
		{"backquotes, arithmetic and case as an argument",
			"a=`echo \"'\"`\nb=$((1+(2)))\nc=$((1<<2))\nd=$(echo case)\nnsd_flags=-4\n", "-x",
			"a=`echo \"'\"`\nb=$((1+(2)))\nc=$((1<<2))\nd=$(echo case)\nnsd_flags='-x'\n"},
		{"here-document body skipped",
			"cat >/dev/null <<EOF\nit's\nnsd_flags=-9\nEOF\nnsd_flags=-4\n", "-x",
			"cat >/dev/null <<EOF\nit's\nnsd_flags=-9\nEOF\nnsd_flags='-x'\n"},
		{"tab-stripped quoted here-document",
			"cat >/dev/null <<-'E O'\n\tit's\n\tE O\nnsd_flags=-4\n", "-4",
			"cat >/dev/null <<-'E O'\n\tit's\n\tE O\nnsd_flags=-4\n"},
		{"NetBSD rc.conf header",
			netbsdRcDefaultsHeader + "nsd=YES\nnsd_flags=-4\n", "-x", netbsdRcDefaultsHeader + "nsd=YES\nnsd_flags='-x'\n"},
		{"after other assignments on its line, matching",
			"foo=bar \\\nnsd_flags=-6\na=1; nsd=YES\n", "-6", "foo=bar \\\nnsd_flags=-6\na=1; nsd=YES\n"},
		{"after a semicolon, matching", "a=1; nsd_flags=-6\n", "-6", "a=1; nsd_flags=-6\n"},
		{"exported, matching", "export nsd_flags=-6\n", "-6", "export nsd_flags=-6\n"},
		{"readonly, matching", "readonly a=1 nsd_flags=-6\n", "-6", "readonly a=1 nsd_flags=-6\n"},
		{"other statement sharing the line, matching", "nsd_flags=-4; nsd=YES\n", "-4", "nsd_flags=-4; nsd=YES\n"},
		{"defaults style: enable and flags on one line",
			"nsd=YES nsd_flags=\"-4\" # see nsd(8)\nsshd=YES\n", "-x", "nsd=YES nsd_flags='-x' # see nsd(8)\nsshd=YES\n"},
		{"after a semicolon", "a=1; nsd_flags=-6 # c\n", "-x", "a=1; nsd_flags='-x' # c\n"},
		{"before a semicolon", "nsd_flags=-4; nsd=YES\n", "-x", "nsd_flags='-x'; nsd=YES\n"},
		{"before a continued assignment", "a=1\nnsd_flags=-4 \\\nnsd=YES\n", "-x", "a=1\nnsd_flags='-x' \\\nnsd=YES\n"},
		{"after a continued assignment", "foo=bar \\\nnsd_flags=-6\n", "-x", "foo=bar \\\nnsd_flags='-x'\n"},
		{"exported", "export nsd_flags=-6 # e\n", "-x", "export nsd_flags='-x' # e\n"},
		{"with a redirection", "nsd_flags=-6 >/dev/null\n", "-x", "nsd_flags='-x' >/dev/null\n"},
		{"later duplicates: alone dropped, shared set in place",
			"nsd_flags=-4 # first\na=1; nsd_flags=-6\nnsd_flags=-8 # dup\nb=2\n", "-x",
			"nsd_flags='-x' # first\na=1; nsd_flags='-x'\nb=2\n"},
		{"appended after a file without a final newline", "a=1", "-x", "a=1\nnsd_flags='-x'\n"},
		{"sourcing before the assignment", ". /etc/rc.conf.local\nnsd_flags=-4\n", "-x", ". /etc/rc.conf.local\nnsd_flags='-x'\n"},
		{"unset of another variable after it", "nsd_flags=-4\nunset nsd_flags_x\n", "-x", "nsd_flags='-x'\nunset nsd_flags_x\n"},
		{"escaped blank before # in a command substitution",
			"nsd_flags=-4\nx=$(echo \\ #) ; y='\\n' ; nsd_flags=-9 ; z=')' #'\n", "-x",
			"nsd_flags='-x'\nx=$(echo \\ #) ; y='\\n' ; nsd_flags='-x' ; z=')' #'\n"},
		{"escaped blank before # in a command substitution, matching",
			"nsd_flags=-4\nx=$(echo \\ #) ; y='\\n' ; nsd_flags=-9 ; z=')' #'\n", "-9",
			"nsd_flags=-4\nx=$(echo \\ #) ; y='\\n' ; nsd_flags=-9 ; z=')' #'\n"},
		{"list continued over a line without assignments",
			"true &&\n  : ok\nnsd_flags=-4\n", "-x", "true &&\n  : ok\nnsd_flags='-x'\n"},
		{"backslash-newline inside the name", "a=1\nnsd_\\\nflags=-9\n", "-x", "a=1\nnsd_flags='-x'\n"},
		{"arithmetic comparison is no assignment",
			"x=$((nsd_flags==1))\nnsd_flags=-4\n", "-x", "x=$((nsd_flags==1))\nnsd_flags='-x'\n"},
		{"quoted here-document body with a backslash",
			": <<'EOF'\na \\\nEOF\nnsd_flags=-4\n", "-x", ": <<'EOF'\na \\\nEOF\nnsd_flags='-x'\n"},
		{"final value read after the last assignment",
			"nsd_flags=-a\nnsd_flags=\"$nsd_flags -b\"\nx=\"$nsd_flags\"\n", "-x",
			"nsd_flags='-x'\nx=\"$nsd_flags\"\n"},
		{"variable reference is no assignment",
			"x=\"$nsd_flags ${nsd_flags}\"\nnsd_flags_extra=1\nnsd_flags=-4\n", "-x",
			"x=\"$nsd_flags ${nsd_flags}\"\nnsd_flags_extra=1\nnsd_flags='-x'\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			b := netbsdFlagsBackend(t, tt.rcConf, "", "")
			s := withFlagsSvc(Service{name: "nsd"}, tt.flags)
			if err := s.applyWith(b); err != nil {
				t.Fatalf("applyWith: %v", err)
			}
			got, err := os.ReadFile(b.rcConf)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("rc.conf = %q, want %q", got, tt.want)
			}
			requireShellSyntax(t, string(got))
		})
	}
}

// TestNetBSDFlagsFailsClosed pins the refusals: text the lexer cannot
// bound, an assignment whose effect cannot be told, and a rewrite that
// would drop or orphan other shell text all fail with the file and line
// named, and rc.conf stays untouched.
func TestNetBSDFlagsFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name, rcConf, defaults, override string
		wantErr                          error
		file                             func(netbsdBackend) string
		line                             string
	}{
		{"multi-line command substitution", "nsd_flags=\"$(echo \"a\nb\")\"\nc=1\n", "", "",
			errRcUnmanaged, rcConfPath, "line 1"},
		{"multi-line unevaluable value", "a=1\nnsd_flags=\"$X\n-b\"\n", "", "",
			errRcUnmanaged, rcConfPath, "line 2"},
		{"prefix of a command", "nsd_flags=-6 /bin/true\n", "", "", errRcUnmanaged, rcConfPath, "line 1"},
		{"inside if on one line", "if true; then nsd_flags=-6; fi\n", "", "", errRcUnmanaged, rcConfPath, "line 1"},
		{"inside if block", "if true; then\n  nsd_flags=-6\nfi\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"inside case", "case x in\nx) nsd_flags=-6 ;;\nesac\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"inside a function", "f() {\n nsd_flags=-6\n}\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"inside a subshell", "(\nnsd_flags=-6\n)\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"eval", "a=1\neval nsd_flags=-6\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"and list", "false && nsd_flags=-6\n", "", "", errRcUnmanaged, rcConfPath, "line 1"},
		{"or list", "nsd_flags=-6 || true\n", "", "", errRcUnmanaged, rcConfPath, "line 1"},
		{"background job", "a=1\nnsd_flags=-6 &\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"pipeline", "true | nsd_flags=-6\n", "", "", errRcUnmanaged, rcConfPath, "line 1"},
		{"after a function definition", "f() { :; }\nnsd_flags=-4\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"after a case with a fi) pattern", "case x in fi) ;; esac\nnsd_flags=-4\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"after a subshell", "(cd /)\nnsd_flags=-4\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"subshell after then", "if true; then (\n:\n) fi\nnsd_flags=-4\n", "", "", errRcUnmanaged, rcConfPath, "line 4"},
		{"after a !-command", "! false\nnsd_flags=-4\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"sourcing after it", "nsd_flags=-4\n. /etc/rc.conf.local\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"source after it", "nsd_flags=-4\nsource /x\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"eval after it", "nsd_flags=-4\neval x=1\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"unset after it", "nsd_flags=-4\nunset nsd_flags\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"read after it", "nsd_flags=-4\nread nsd_flags </dev/null\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"for after it", "nsd_flags=-4\nfor nsd_flags in a; do :; done\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"sourcing after it in the override", "nsd_flags=-4\n", "", "nsd_flags=-4\n. /x\n", errRcUnmanaged, overridePath, "line 2"},
		{"continuation at end of file", "b=2\nnsd_flags=-4\\\n", "", "", errRcSyntax, rcConfPath, "line 2"},
		{"append after a continued command", "echo hi \\\n", "", "", errRcSyntax, rcConfPath, "line 1"},
		{"append after a backslash at end of file", "a=1\\", "", "", errRcSyntax, rcConfPath, "line 1"},
		{"CRLF line endings", "a=1\nnsd_flags=-4\r\n", "", "", errRcSyntax, rcConfPath, "line 2"},
		{"assignment in a quoted value", "motd='hi nsd_flags=-9'\nnsd_flags=-4\n", "", "", errRcUnmanaged, rcConfPath, "line 1"},
		{"assigning arithmetic", "x=$((nsd_flags=9))\n", "", "", errRcUnmanaged, rcConfPath, "line 1"},
		{"assigning arithmetic operator", "nsd_flags=-4\nx=$((nsd_flags+=1))\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"and list continued on the next line", "nsd_flags=-4\nfalse &&\nnsd_flags=-6\n", "", "", errRcUnmanaged, rcConfPath, "line 3"},
		{"or list continued on the next line", "nsd_flags=-4\ntrue ||\nnsd_flags=-6\n", "", "", errRcUnmanaged, rcConfPath, "line 3"},
		{"list continued then more", "nsd_flags=-4\nfalse &&\nnsd_flags=-6\nsshd=YES", "", "", errRcUnmanaged, rcConfPath, "line 3"},
		{"pipeline continued on the next line", "true |\n\nnsd_flags=-6\n", "", "", errRcUnmanaged, rcConfPath, "line 3"},
		{"file ends after &&", "a=1\ntrue &&\n", "", "", errRcSyntax, rcConfPath, "line 2"},
		{"escaped dot command after it", "nsd_flags=-4\n\\. ./inc\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"quoted dot command after it", "nsd_flags=-4\n\".\" ./inc\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"command dot after it", "nsd_flags=-4\ncommand . ./inc\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"continued unset after it", "nsd_flags=-4\nuns\\\net nsd_flags\n", "", "", errRcUnmanaged, rcConfPath, "line 2"},
		{"continued line in an unquoted here-document", ": <<EOF\na \\\nEOF\nEOF\nnsd_flags=-4\n", "", "",
			errRcSyntax, rcConfPath, "line 2"},
		{"intermediate value read", "nsd_flags=\"-a\"\nx=\"$nsd_flags -b\"\nnsd_flags=\"-c\"\n", "", "",
			errRcUnmanaged, rcConfPath, "line 2"},
		{"intermediate value read on the same line", "nsd_flags=-a\nnsd_flags=-b x=\"${nsd_flags}\"\nnsd_flags=-c\n", "", "",
			errRcUnmanaged, rcConfPath, "line 2"},
		{"intermediate value read by a here-document", "nsd_flags=-a\ncat >/dev/null <<EOF\n$nsd_flags\nEOF\nnsd_flags=-c\n", "", "",
			errRcUnmanaged, rcConfPath, "line 3"},
		{"assigning expansion", "x=${nsd_flags:=-6}\n", "", "", errRcUnmanaged, rcConfPath, "line 1"},
		{"unterminated double quote", "a=1\nb=2\nnsd_flags=\"-a \\\n-b\n", "", "",
			errRcSyntax, rcConfPath, "line 3"},
		{"unterminated dollar-single quote", "nsd_flags=-4\nx=$'a\\'\n", "", "", errRcSyntax, rcConfPath, "line 2"},
		{"unterminated here-document", "nsd_flags=-4\ncat <<EOF\nit's\n", "", "", errRcSyntax, rcConfPath, "line 2"},
		{"here-document in command substitution", "x=$(cat <<EOF\na\nEOF\n)\nnsd_flags=-4\n", "", "",
			errRcSyntax, rcConfPath, "line 1"},
		{"case in command substitution", "x=$(case a in a) echo;; esac)\nnsd_flags=-4\n", "", "",
			errRcSyntax, rcConfPath, "line 1"},
		{"unterminated quote in the override", "nsd_flags=-4\n", "", "a=1\nb=2\nnsd_flags='x\n",
			errRcSyntax, overridePath, "line 3"},
		{"unterminated quote in the defaults", netbsdRcDefaultsHeader, "a=1\nb=2\nnsd_flags=\"x\n", "",
			errRcSyntax, defaultsPath, "line 3"},
		{"conditional assignment in the override", "nsd_flags=-4\n", "", "if true; then nsd_flags=-6; fi\n",
			errRcUnmanaged, overridePath, "line 1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			b := netbsdFlagsBackend(t, tt.rcConf, tt.defaults, tt.override)
			s := withFlagsSvc(Service{name: "nsd"}, "-x")
			err := s.applyWith(b)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("applyWith err = %v, want %v", err, tt.wantErr)
			}
			if msg := err.Error(); !strings.Contains(msg, tt.file(b)) || !strings.Contains(msg, tt.line+":") {
				t.Fatalf("error %q must name %s and %s", msg, tt.file(b), tt.line)
			}
			if got, _ := os.ReadFile(b.rcConf); string(got) != tt.rcConf {
				t.Fatalf("rc.conf changed to %q", got)
			}
		})
	}
}

func rcConfPath(b netbsdBackend) string   { return b.rcConf }
func overridePath(b netbsdBackend) string { return filepath.Join(b.rcConfD, "nsd") }
func defaultsPath(b netbsdBackend) string { return b.rcConfDefaults }

// TestNetBSDFlagsOverrideAfterOtherStatement pins that an rc.conf.d
// override is found even when it is not first on its line, so the
// override refusal is not skipped.
func TestNetBSDFlagsOverrideAfterOtherStatement(t *testing.T) {
	for _, override := range []string{"nsd=YES; nsd_flags=-6\n", "nsd=YES \\\n nsd_flags=-6\n", "export nsd_flags=-6\n"} {
		resource.ResetReport()
		b := netbsdFlagsBackend(t, "nsd_flags=-4\n", "", override)
		s := withFlagsSvc(Service{name: "nsd"}, "-4")
		if err := s.applyWith(b); err == nil || !strings.Contains(err.Error(), "overrides") {
			t.Errorf("override %q must be refused, err = %v", override, err)
		}
	}
}

// TestNetBSDFlagsIncludes pins that a file not assigning the variable but
// sourcing other files, using eval, or unsetting or reading the variable
// is not trusted to leave it unset: rc.conf and the defaults then never
// match (the appended assignment settles the value), and such an override
// is refused. The stock rc.conf header sourcing /etc/defaults/rc.conf is
// trusted.
func TestNetBSDFlagsIncludes(t *testing.T) {
	for _, tt := range []struct{ name, rcConf, defaults, want string }{
		{"rc.conf sources another file", ". /etc/rc.conf.local\n", "nsd_flags=-x\n", ". /etc/rc.conf.local\nnsd_flags='-x'\n"},
		{"rc.conf evals", "eval a=1\n", "nsd_flags=-x\n", "eval a=1\nnsd_flags='-x'\n"},
		{"defaults source another file", netbsdRcDefaultsHeader, ". /etc/defaults/md.conf\n", netbsdRcDefaultsHeader + "nsd_flags='-x'\n"},
		{"rc.conf unsets it", netbsdRcDefaultsHeader + "unset nsd_flags\n", "nsd_flags=-x\n",
			netbsdRcDefaultsHeader + "unset nsd_flags\nnsd_flags='-x'\n"},
		{"rc.conf reads it", netbsdRcDefaultsHeader + "read nsd_flags </dev/null\n", "nsd_flags=-x\n",
			netbsdRcDefaultsHeader + "read nsd_flags </dev/null\nnsd_flags='-x'\n"},
		{"defaults unset it", netbsdRcDefaultsHeader, "unset nsd_flags\n", netbsdRcDefaultsHeader + "nsd_flags='-x'\n"},
		{"stock header is trusted", netbsdRcDefaultsHeader, "nsd_flags=-x\n", netbsdRcDefaultsHeader},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			b := netbsdFlagsBackend(t, tt.rcConf, tt.defaults, "")
			s := withFlagsSvc(Service{name: "nsd"}, "-x")
			if err := s.applyWith(b); err != nil {
				t.Fatalf("applyWith: %v", err)
			}
			if got, _ := os.ReadFile(b.rcConf); string(got) != tt.want {
				t.Fatalf("rc.conf = %q, want %q", got, tt.want)
			}
		})
	}
	for _, override := range []string{"nsd=YES\n. /etc/nsd.rc\n", "unset nsd_flags\n", "for nsd_flags in -x; do :; done\n"} {
		resource.ResetReport()
		b := netbsdFlagsBackend(t, "nsd_flags=-x\n", "", override)
		s := withFlagsSvc(Service{name: "nsd"}, "-x")
		if err := s.applyWith(b); err == nil || !strings.Contains(err.Error(), "WithFlags cannot tell its value") {
			t.Errorf("override %q must be refused, err = %v", override, err)
		}
	}
}

// TestNetBSDSetFlagsRefusesOnItsOwn pins that setFlags refuses unsafe
// rewrites itself, not only behind the flagsMatch probe.
func TestNetBSDSetFlagsRefusesOnItsOwn(t *testing.T) {
	for content, wantErr := range map[string]error{
		"nsd_flags=\"-a \\\n-b\n":          errRcSyntax,
		"if true; then nsd_flags=-4; fi\n": errRcUnmanaged,
		"echo \\\n":                        errRcSyntax,
		"nsd_flags=\"$(echo \"a\nb\")\"\n": errRcUnmanaged,
	} {
		b := netbsdFlagsBackend(t, content, "", "")
		if err := b.setFlags(unit{name: "nsd"}, "-x"); !errors.Is(err, wantErr) || !strings.Contains(err.Error(), b.rcConf) {
			t.Errorf("setFlags over %q err = %v, want %v naming %s", content, err, wantErr, b.rcConf)
		}
		if got, _ := os.ReadFile(b.rcConf); string(got) != content {
			t.Errorf("rc.conf changed to %q", got)
		}
	}
}

// TestLexRcWords pins the word boundaries the lexer finds for the quoting
// forms that span blanks, operators or newlines.
func TestLexRcWords(t *testing.T) {
	for src, want := range map[string][]string{
		"a='x y' b":                {"a='x y'", "b"},
		"a=$'x\\' y' b":            {"a=$'x\\' y'", "b"},
		"a=\"$'x\" b":              {"a=\"$'x\"", "b"},
		"a=\"${X:-\"it's\"}\" b":   {"a=\"${X:-\"it's\"}\"", "b"},
		"a=${X:-'}'} b":            {"a=${X:-'}'}", "b"},
		"a=\"$(echo \")\"; x)\" b": {"a=\"$(echo \")\"; x)\"", "b"},
		"a=$(echo a # )\n) b":      {"a=$(echo a # )\n)", "b"},
		"a=b\\\nc d":               {"a=b\\\nc", "d"},
		"a=b#c #d":                 {"a=b#c"},
		"a=`echo \"'\"` b":         {"a=`echo \"'\"`", "b"},
	} {
		tokens, err := lexRc(src)
		if err != nil {
			t.Errorf("lexRc(%q): %v", src, err)
			continue
		}
		var got []string
		for _, tok := range tokens {
			if tok.kind == rcWord {
				got = append(got, tok.text)
			}
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("lexRc(%q) words = %q, want %q", src, got, want)
		}
	}
	tokens, err := lexRc("cat <<'E'\nx ' \"\nE\na=1\n")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, tok := range tokens {
		kinds = append(kinds, tok.text)
	}
	if got, want := strings.Join(kinds, "|"), "cat|<<|'E'|\n|a=1|\n"; got != want {
		t.Errorf("here-document tokens = %q, want %q", got, want)
	}
	if tokens[3].end != len("cat <<'E'\nx ' \"\nE\n") {
		t.Errorf("newline token must extend over the here-document body, end = %d", tokens[3].end)
	}
}

// FuzzReplaceRcAssignment surrounds one known assignment line with
// arbitrary text. Whenever the file reads cleanly it checks that found
// assignments lie inside the file; that a rewrite reads back with every
// assignment evaluating to the wanted flags; that a second rewrite is a
// no-op; that a rewrite of a file passing sh -n passes it too (with a
// POSIX shell installed); and, when the known line is the only
// assignment, that the result is exactly the input with that one word
// replaced (an oracle built independently of replaceRcAssignment).
func FuzzReplaceRcAssignment(f *testing.F) {
	for _, seed := range [][2]string{
		{"a=1", "b=2"}, {"echo hi \\", ""}, {"", "\\"}, {"x=\"$(echo \"", "\")\""},
		{"nsd_flags=\"-a \\", "-b\""}, {"f() { :; }", ""}, {"(", ")"}, {"case x in fi) ;; esac", ""},
		{"if true; then", "fi"}, {"cat <<-E\n\tx\n\tE", "nsd_flags=1"}, {"a=$(echo \"$(b)\" `c`)", ""},
		{"x=$'\\''", ""}, {"nsd=YES", "nsd_flags=-6; a=1"}, {"# it's", ". /x"}, {"a=1 \\", ""},
		{"false &&", "sshd=YES"}, {"true ||", ""}, {"true |", ""}, {"x=$(echo \\ #) ; y='", "' ; z=')' #'"},
		{"nsd_\\", "flags=1"}, {": <<EOF\na \\", "EOF\nEOF"},
	} {
		f.Add(seed[0], seed[1])
	}
	const name, flags = "nsd_flags", "-x 'y'"
	shell := posixShell() // input passes sh -n => output passes sh -n
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, before, after string) {
		content := before + "\nnsd_flags=-4\n" + after
		scan, err := scanRcAssignments(content, name)
		if err != nil {
			return
		}
		found := scan.found
		lineCount := strings.Count(content, "\n") + 1
		for _, a := range found {
			if a.first < 0 || a.first > a.line || a.line >= a.lastEnd || a.lastEnd > lineCount ||
				a.start < 0 || a.start >= a.end || a.end > len(content) {
				t.Fatalf("assignment %+v outside %q", a, content)
			}
		}
		updated, err := replaceRcAssignment(content, name, flags)
		if err != nil {
			return
		}
		rescan, err := scanRcAssignments(updated, name)
		again := rescan.found
		if err != nil || len(again) == 0 {
			t.Fatalf("rewrite of %q does not read back: %q, %v", content, updated, err)
		}
		for _, a := range again {
			if got, ok := parseShellWord(a.value); !ok || got != flags {
				t.Fatalf("rewrite of %q reads back %s=%s", content, name, a.value)
			}
		}
		if second, err := replaceRcAssignment(updated, name, flags); err != nil || second != updated {
			t.Fatalf("second rewrite of %q is no no-op: %q, %v", updated, second, err)
		}
		if shell != nil && checkShellSyntax(shell, dir, content) == nil {
			if err := checkShellSyntax(shell, dir, updated); err != nil {
				t.Fatalf("rewrite of %q (valid sh) = %q fails sh -n: %v", content, updated, err)
			}
		}
		if len(found) == 1 && found[0].start == len(before)+1 {
			if want := before + "\nnsd_flags=" + shellQuote(flags) + "\n" + after; updated != want || len(again) != 1 {
				t.Fatalf("rewrite of %q = %q, want %q", content, updated, want)
			}
		}
	})
}

// TestNetBSDSetFlagsKeepsModeOwnerAndSymlink pins the durable rc.conf
// replacement (task bb): the new rc.conf keeps the old one's mode and
// ownership, a symlinked rc.conf stays a symlink with its target
// rewritten, and no temporary file is left beside either.
func TestNetBSDSetFlagsKeepsModeOwnerAndSymlink(t *testing.T) {
	t.Run("plain file", func(t *testing.T) {
		b := netbsdFlagsBackend(t, "nsd_flags=-4\n", "", "")
		if err := os.Chmod(b.rcConf, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := b.setFlags(unit{name: "nsd"}, "-x"); err != nil {
			t.Fatalf("setFlags: %v", err)
		}
		requireRcConf(t, b.rcConf, "nsd_flags='-x'\n", 0o600)
		requireNoRcTempFiles(t, filepath.Dir(b.rcConf))
	})
	t.Run("symlink", func(t *testing.T) {
		b := netbsdFlagsBackend(t, "", "", "")
		realPath := filepath.Join(t.TempDir(), "rc.conf.real")
		if err := os.WriteFile(realPath, []byte("nsd_flags=-4\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realPath, b.rcConf); err != nil {
			t.Fatal(err)
		}
		if err := b.setFlags(unit{name: "nsd"}, "-x"); err != nil {
			t.Fatalf("setFlags: %v", err)
		}
		if info, err := os.Lstat(b.rcConf); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("rc.conf is no longer a symlink (%v, %v)", info, err)
		}
		requireRcConf(t, realPath, "nsd_flags='-x'\n", 0o640)
		requireNoRcTempFiles(t, filepath.Dir(realPath))
		requireNoRcTempFiles(t, filepath.Dir(b.rcConf))
	})
	t.Run("relative symlink", func(t *testing.T) {
		b := netbsdFlagsBackend(t, "", "", "")
		dir := filepath.Dir(b.rcConf)
		if err := os.WriteFile(filepath.Join(dir, "rc.conf.real"), []byte("nsd_flags=-4\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("rc.conf.real", b.rcConf); err != nil {
			t.Fatal(err)
		}
		if err := b.setFlags(unit{name: "nsd"}, "-x"); err != nil {
			t.Fatalf("setFlags: %v", err)
		}
		requireRcSymlink(t, b.rcConf, "rc.conf.real")
		requireRcConf(t, filepath.Join(dir, "rc.conf.real"), "nsd_flags='-x'\n", 0o640)
		requireNoRcTempFiles(t, dir)
	})
	t.Run("chained symlinks", func(t *testing.T) {
		b := netbsdFlagsBackend(t, "", "", "")
		realDir := t.TempDir()
		realPath := filepath.Join(realDir, "rc.conf")
		if err := os.WriteFile(realPath, []byte("nsd_flags=-4\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		middle := filepath.Join(filepath.Dir(b.rcConf), "rc.conf.link")
		if err := os.Symlink(realPath, middle); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("rc.conf.link", b.rcConf); err != nil {
			t.Fatal(err)
		}
		if err := b.setFlags(unit{name: "nsd"}, "-x"); err != nil {
			t.Fatalf("setFlags: %v", err)
		}
		requireRcSymlink(t, b.rcConf, "rc.conf.link")
		requireRcSymlink(t, middle, realPath)
		requireRcConf(t, realPath, "nsd_flags='-x'\n", 0o600)
		requireNoRcTempFiles(t, realDir)
		requireNoRcTempFiles(t, filepath.Dir(b.rcConf))
	})
	t.Run("supplementary group and set-gid mode", func(t *testing.T) {
		gid, ok := supplementaryGroup()
		if !ok {
			t.Skip("the caller has no supplementary group")
		}
		b := netbsdFlagsBackend(t, "nsd_flags=-4\n", "", "")
		if err := os.Chown(b.rcConf, -1, gid); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(b.rcConf, 0o640|os.ModeSetgid); err != nil {
			t.Fatal(err)
		}
		want := atomicfile.Owner{UID: os.Getuid(), GID: gid}
		if rc, err := readRcFile(b.rcConf); err != nil || rc.owner == nil || *rc.owner != want {
			t.Fatalf("readRcConf owner = %v (%v), want %+v", rc.owner, err, want)
		}
		if err := b.setFlags(unit{name: "nsd"}, "-x"); err != nil {
			t.Fatalf("setFlags: %v", err)
		}
		info, err := os.Stat(b.rcConf)
		if err != nil {
			t.Fatal(err)
		}
		if owner, _ := atomicfile.OwnerOf(info); owner != want {
			t.Errorf("rc.conf owner = %+v, want %+v", owner, want)
		}
		if info.Mode()&os.ModeSetgid == 0 {
			t.Errorf("rc.conf mode = %v, lost its set-gid bit", info.Mode())
		}
	})
	t.Run("missing rc.conf created 0644", func(t *testing.T) {
		b := netbsdFlagsBackend(t, "", "", "")
		if err := b.setFlags(unit{name: "nsd"}, "-x"); err != nil {
			t.Fatalf("setFlags: %v", err)
		}
		requireRcConf(t, b.rcConf, "nsd_flags='-x'\n", 0o644)
	})
}

// TestNetBSDSetFlagsWriteFailureKeepsRcConf pins the negative paths of the
// rc.conf replacement: a write that cannot complete (a read-only /etc) or a
// rc.conf that is a dangling symlink or not a regular file fails with the
// path named, and the original is left as it was, with no temporary file.
func TestNetBSDSetFlagsWriteFailureKeepsRcConf(t *testing.T) {
	t.Run("read-only directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		b := netbsdFlagsBackend(t, "nsd_flags=-4\n", "", "")
		dir := filepath.Dir(b.rcConf)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if err := b.setFlags(unit{name: "nsd"}, "-x"); err == nil || !strings.Contains(err.Error(), b.rcConf) {
			t.Fatalf("setFlags err = %v, want an error naming %s", err, b.rcConf)
		}
		requireRcConf(t, b.rcConf, "nsd_flags=-4\n", 0o640)
		requireNoRcTempFiles(t, dir)
	})
	t.Run("dangling symlink", func(t *testing.T) {
		b := netbsdFlagsBackend(t, "", "", "")
		if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), b.rcConf); err != nil {
			t.Fatal(err)
		}
		if err := b.setFlags(unit{name: "nsd"}, "-x"); err == nil || !strings.Contains(err.Error(), b.rcConf) {
			t.Fatalf("setFlags err = %v, want an error naming %s", err, b.rcConf)
		}
		if info, err := os.Lstat(b.rcConf); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("dangling rc.conf symlink replaced (%v, %v)", info, err)
		}
	})
	t.Run("not a regular file", func(t *testing.T) {
		b := netbsdFlagsBackend(t, "", "", "")
		if err := os.Mkdir(b.rcConf, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := b.setFlags(unit{name: "nsd"}, "-x"); err == nil || !strings.Contains(err.Error(), b.rcConf) {
			t.Fatalf("setFlags err = %v, want an error naming %s", err, b.rcConf)
		}
		requireNoRcTempFiles(t, filepath.Dir(b.rcConf))
	})
}

func requireRcConf(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("%s = %q, want %q", path, got, content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("%s mode = %v, want %v", path, info.Mode().Perm(), mode)
	}
}

func requireRcSymlink(t *testing.T, path, target string) {
	t.Helper()
	got, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("%s is no longer a symlink: %v", path, err)
	}
	if got != target {
		t.Errorf("%s -> %s, want -> %s", path, got, target)
	}
}

func requireNoRcTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".gonftmp") {
			t.Errorf("leftover temporary file %s in %s", e.Name(), dir)
		}
	}
}

// supplementaryGroup returns a group of the caller other than its primary.
func supplementaryGroup() (int, bool) {
	groups, err := os.Getgroups()
	if err != nil {
		return 0, false
	}
	for _, g := range groups {
		if g != os.Getgid() {
			return g, true
		}
	}
	return 0, false
}
