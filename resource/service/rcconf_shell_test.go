package service

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// posixShell returns the command line of the strictest POSIX shell
// installed, closest to NetBSD's sh: dash, busybox sh, else bash --posix.
// It returns nil when none is.
func posixShell() []string {
	for _, candidate := range [][]string{{"dash"}, {"busybox", "sh"}, {"bash", "--posix"}} {
		if path, err := exec.LookPath(candidate[0]); err == nil {
			return append([]string{path}, candidate[1:]...)
		}
	}
	return nil
}

// checkShellSyntax runs shell -n over content, written into dir.
func checkShellSyntax(shell []string, dir, content string) error {
	path := filepath.Join(dir, "syntax.rc.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	out, err := exec.Command(shell[0], append(shell[1:], "-n", path)...).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

// shellSyntax runs a syntax-only check (-n) over content with posixShell
// and returns its failure; it skips t without a shell. bash --posix, the
// fallback on hosts without dash (Rocky Linux), accepts a few forms
// NetBSD's sh would not ($'...'); the texts under test do not depend on
// that difference.
func shellSyntax(t *testing.T, content string) error {
	t.Helper()
	shell := posixShell()
	if shell == nil {
		t.Skip("no POSIX shell installed; cannot check rc.conf syntax")
	}
	return checkShellSyntax(shell, t.TempDir(), content)
}

// requireShellSyntax fails t unless the shell accepts content (-n).
func requireShellSyntax(t *testing.T, content string) {
	t.Helper()
	if err := shellSyntax(t, content); err != nil {
		t.Fatalf("sh -n rejects the result: %v\n%s", err, content)
	}
}

// rcEvalVars are the variables the generated rc.conf files may set.
var rcEvalVars = []string{"nsd_flags", "a", "b", "sshd", "motd", "nsd", "x"}

// evalRcFile sources content with the shell in dir (holding ./inc) and
// returns the rcEvalVars values, "<unset>" for an unset one.
func evalRcFile(t *testing.T, shell []string, dir, content string) map[string]string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "rc.conf"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `. ./rc.conf; for v in ` + strings.Join(rcEvalVars, " ") +
		`; do eval "printf '%s' \"\${$v-<unset>}\""; printf '\000'; done`
	cmd := exec.Command(shell[0], append(shell[1:], "-c", script)...)
	cmd.Dir, cmd.Env = dir, []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sourcing %q: %v", content, err)
	}
	values := bytes.Split(out, []byte{0})
	if len(values) != len(rcEvalVars)+1 {
		t.Fatalf("sourcing %q printed %q", content, out)
	}
	got := make(map[string]string, len(rcEvalVars))
	for i, name := range rcEvalVars {
		got[name] = string(values[i])
	}
	return got
}

// rcGenValues are value spellings the generator assigns.
var rcGenValues = []string{
	"-a", "'-a b'", `"-a  b"`, "\"-a \\\n-b\"", "'x\ny'", `"$(echo "a b")"`, "-a\\\nb",
	`"${X:-it's}"`, "''", `"q'uote"`, "\"$(echo \\ #)\"",
}

// rcGenPieces are the generator's statements over a name n and a value v,
// POSIX sh built from side-effect-free builtins only.
var rcGenPieces = []func(n, v string) string{
	func(n, v string) string { return n + "=" + v },
	func(n, v string) string { return n + "=" + v + " # it's" },
	func(n, v string) string { return "a=1; " + n + "=" + v },
	func(n, v string) string { return n + "=" + v + "; b=2" },
	func(n, v string) string { return "nsd=YES " + n + "=" + v },
	func(n, v string) string { return "export " + n + "=" + v },
	func(n, v string) string { return "a=1 \\\n" + n + "=" + v },
	func(n, v string) string { return n + "=" + v + " \\\n  sshd=NO" },
	func(n, v string) string { return "if true; then\n" + n + "=" + v + "\nfi" },
	func(n, v string) string { return "if false; then " + n + "=" + v + "; fi" },
	func(n, v string) string { return "true &&\n" + n + "=" + v },
	func(n, v string) string { return "false ||\n" + n + "=" + v },
	func(n, v string) string { return "false &&\n  " + n + "=" + v },
	func(n, v string) string { return "true |\n" + n + "=" + v },
	func(n, v string) string { return ": <<EOF\nit's " + n + "=" + v + "\nEOF" },
	func(n, v string) string { return ": <<'EOF'\n" + n + "=" + v + "\nEOF" },
	func(n, v string) string { return "# " + n + "=" + v },
	func(n, v string) string { return ". ./inc" },
	func(n, v string) string { return "unset " + n },
	func(n, v string) string { return "x=$(echo \\ #) ; " + n + "=" + v + " ; motd=')' #'" },
	func(n, v string) string { return "f() { :; }" },
	func(n, v string) string { return "(:)" },
	func(n, v string) string { return "! false" },
	func(n, v string) string { return "x=$((1<<2))" },
	func(n, v string) string { return "x=$((" + n + "=5))" },
	func(n, v string) string { return "nsd_\\\nflags=" + v },
}

// genRcFile returns a small random rc.conf built from rcGenPieces.
func genRcFile(r *rand.Rand) string {
	names := []string{"nsd_flags", "nsd_flags", "nsd_flags", "a", "b", "sshd", "motd"}
	var lines []string
	for range 1 + r.IntN(5) {
		piece := rcGenPieces[r.IntN(len(rcGenPieces))]
		lines = append(lines, piece(names[r.IntN(len(names))], rcGenValues[r.IntN(len(rcGenValues))]))
	}
	return strings.Join(lines, "\n") + "\n"
}

// TestRcConfSemantics is the shell oracle for the rc.conf reader and
// rewriter, over deterministic generated files: whenever the reader
// evaluates the variable, sh(1) sourcing the file agrees; whenever a
// rewrite is not refused, the result passes sh -n and, sourced, sets the
// variable to exactly the wanted flags and every other variable as the
// original did.
func TestRcConfSemantics(t *testing.T) {
	shell := posixShell()
	if shell == nil {
		t.Skip("no POSIX shell installed; cannot evaluate rc.conf files")
	}
	const want = "-x 'y' $z"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inc"), []byte("sshd=inc; nsd_flags=inc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(7, 7))
	var reads, rewrites int
	for range 200 {
		content := genRcFile(r)
		if checkShellSyntax(shell, dir, content) != nil {
			continue
		}
		before := evalRcFile(t, shell, dir, content)
		scan, err := scanRcAssignments(content, "nsd_flags")
		if err == nil {
			if n := len(scan.found); n > 0 {
				if value, ok := parseShellWord(scan.found[n-1].value); ok {
					reads++
					if before["nsd_flags"] != value {
						t.Errorf("read %q from %q, sh sets %q", value, content, before["nsd_flags"])
					}
				}
			} else if !scan.includesOther && before["nsd_flags"] != "<unset>" {
				t.Errorf("read no assignment in %q, sh sets %q", content, before["nsd_flags"])
			}
		}
		updated, err := replaceRcAssignment(content, "nsd_flags", want)
		if err != nil {
			continue
		}
		rewrites++
		if err := checkShellSyntax(shell, dir, updated); err != nil {
			t.Errorf("rewrite of %q = %q fails sh -n: %v", content, updated, err)
			continue
		}
		after := evalRcFile(t, shell, dir, updated)
		for _, name := range rcEvalVars {
			if wantValue := map[bool]string{true: want, false: before[name]}[name == "nsd_flags"]; after[name] != wantValue {
				t.Errorf("rewrite of %q = %q: sh sets %s=%q, want %q", content, updated, name, after[name], wantValue)
			}
		}
	}
	t.Logf("%d evaluated reads, %d rewrites checked with %s", reads, rewrites, strings.Join(shell, " "))
	if reads < 25 || rewrites < 50 {
		t.Fatalf("the generator exercised too little: %d reads, %d rewrites", reads, rewrites)
	}
}

// TestNetBSDStockFiles runs WithFlags over NetBSD's own files: the stock
// /etc/defaults/rc.conf (rev 1.170) and /etc/rc.conf (rev 1.97), copied
// unchanged into testdata/netbsd. The defaults must read cleanly for each
// service (including the one-line "NAME=NO NAME_flags=..." style), and a
// typical rc.conf on top of the stock one must be rewritten in place,
// keep the stock text, pass sh -n and then match.
func TestNetBSDStockFiles(t *testing.T) {
	defaults, err := os.ReadFile(filepath.Join("testdata", "netbsd", "defaults.rc.conf"))
	if err != nil {
		t.Fatal(err)
	}
	stockBytes, err := os.ReadFile(filepath.Join("testdata", "netbsd", "rc.conf"))
	if err != nil {
		t.Fatal(err)
	}
	stock := string(stockBytes)
	typical := stock + "rc_configured=YES\nhostname=pi0\nsshd=YES\ndhcpcd=YES dhcpcd_flags=\"-qM\"\n" +
		"nsd=YES\nnsd_flags=\"-c /var/nsd/etc/nsd.conf\"\nntpd=YES ntpd_flags=\"-g\"  # sync\n"
	for svc, stockFlags := range map[string]string{
		"dhcpcd": "-qM", "named": "", "ntpd": "", "sshd": "", "unbound": "", "postfix": "",
		"ifwatchd": "-u /etc/ppp/ip-up -d /etc/ppp/ip-down pppoe0",
	} {
		t.Run(svc, func(t *testing.T) {
			b := netbsdFlagsBackend(t, stock, string(defaults), "")
			if match, err := b.flagsMatch(unit{name: svc}, stockFlags); err != nil || !match {
				t.Fatalf("stock files: flagsMatch(%q) = %v, %v; want a match", stockFlags, match, err)
			}
			if err := os.WriteFile(b.rcConf, []byte(typical), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := b.setFlags(unit{name: svc}, "-X y"); err != nil {
				t.Fatalf("setFlags: %v", err)
			}
			got, err := os.ReadFile(b.rcConf)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(got), stock) {
				t.Fatalf("the stock rc.conf text changed:\n%s", got)
			}
			requireShellSyntax(t, string(got))
			if match, err := b.flagsMatch(unit{name: svc}, "-X y"); err != nil || !match {
				t.Fatalf("after setFlags: flagsMatch = %v, %v\n%s", match, err, got)
			}
		})
	}
	// nsd's default flags use an expansion, which never matches, and are
	// rewritten in the typical rc.conf where they are set.
	b := netbsdFlagsBackend(t, typical, string(defaults), "")
	if err := b.setFlags(unit{name: "nsd"}, "-X y"); err != nil {
		t.Fatalf("nsd setFlags: %v", err)
	}
	if got, _ := os.ReadFile(b.rcConf); !strings.Contains(string(got), "\nnsd_flags='-X y'\nntpd=YES ntpd_flags=\"-g\"  # sync\n") {
		t.Fatalf("nsd rewrite:\n%s", got)
	}
}
