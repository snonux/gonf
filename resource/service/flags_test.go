package service

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/systemd"
)

// fakeFlagBackend is fakeBackend plus the flagger capability: its stored
// flags start as current, and setFlags records a "flags" pseudo-verb in
// done so a test sees where it ran among the verbs.
type fakeFlagBackend struct {
	fakeBackend
	current string
	probed  int
}

func (f *fakeFlagBackend) flagsMatch(_ unit, want string) (bool, error) {
	f.probed++
	return f.current == want, nil
}

func (f *fakeFlagBackend) setFlags(_ unit, flags string) error {
	f.current = flags
	f.done = append(f.done, "flags")
	return nil
}

func (f *fakeFlagBackend) describeFlags(u unit, flags string) (would, did string) {
	return "fake flags " + flags, "fake flags " + flags
}

// withFlagsSvc returns s with WithFlags(flags) applied.
func withFlagsSvc(s Service, flags string) Service { s.SetFlags(flags); return s }

// TestApplyWithFlagsPolicy pins the flags policy: flags are set after an
// enable and before start/restart, a flags change fires the restart even
// while the change gate holds it, a match changes nothing (so a gated
// restart stays held), and a not-yet-enabled service always gets its flags
// without a probe.
func TestApplyWithFlagsPolicy(t *testing.T) {
	gated := withRestartSvc(Service{name: "d"})
	gated.Gated = true
	for _, tt := range []struct {
		name             string
		svc              Service
		running, enabled bool
		current          string
		wantDone         []verb
		wantProbed       int
		wantNote         resource.Status
	}{
		{name: "enable, flags, start", svc: withFlagsSvc(Service{name: "d"}, "-v"),
			wantDone: []verb{verbEnable, "flags", verbStart}, wantNote: resource.StatusChanged},
		{name: "flags before start", svc: withFlagsSvc(Service{name: "d"}, "-v"), enabled: true,
			wantDone: []verb{"flags", verbStart}, wantProbed: 1, wantNote: resource.StatusChanged},
		{name: "flags change fires a held restart", svc: withFlagsSvc(gated, "-v"), running: true, enabled: true,
			wantDone: []verb{"flags", verbRestart}, wantProbed: 1, wantNote: resource.StatusChanged},
		{name: "flags change without restart only sets flags", svc: withFlagsSvc(Service{name: "d"}, "-v"), running: true, enabled: true,
			wantDone: []verb{"flags"}, wantProbed: 1, wantNote: resource.StatusChanged},
		{name: "matching flags keep the gate holding", svc: withFlagsSvc(gated, "-v"), running: true, enabled: true, current: "-v",
			wantProbed: 1, wantNote: resource.StatusSkipped},
		{name: "matching empty flags are converged", svc: withFlagsSvc(Service{name: "d"}, ""), running: true, enabled: true,
			wantProbed: 1, wantNote: resource.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			fb := &fakeFlagBackend{fakeBackend: fakeBackend{isRunning: tt.running, isEnabled: tt.enabled}, current: tt.current}
			if err := tt.svc.applyWith(fb); err != nil {
				t.Fatalf("applyWith: %v", err)
			}
			if !slices.Equal(fb.done, tt.wantDone) || fb.probed != tt.wantProbed {
				t.Errorf("done = %v probed %d, want %v probed %d", fb.done, fb.probed, tt.wantDone, tt.wantProbed)
			}
			assertStatus(t, "Service[d]", tt.wantNote)
		})
	}
}

// TestFlagsRefusedOnSystemd pins that WithFlags on a backend without a
// flagger (systemd) fails before any probe or action.
func TestFlagsRefusedOnSystemd(t *testing.T) {
	var calls [][]string
	fake := func(name string, args ...string) (string, string, int, error) {
		calls = append(calls, append([]string{name}, args...))
		return "", "", 0, nil
	}
	s := withFlagsSvc(Service{name: "httpd"}, "")
	err := s.applyWith(systemdBackend{client: systemd.NewClient(&runners.SystemdRunners{Run: fake})})
	if !errors.Is(err, errFlagsUnsupported) || !strings.Contains(err.Error(), "service[httpd]: WithFlags is only supported on the BSD rc backends") {
		t.Fatalf("err = %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("systemctl ran before the refusal: %v", calls)
	}
}

// recordingRunner fakes a BSD runner: out answers each probe (keyed by the
// joined argv), and every other call is recorded as a mutation.
func recordingRunner(out map[string]string, calls *[][]string) runner {
	return func(name string, args ...string) (string, string, int, error) {
		argv := strings.Join(append([]string{name}, args...), " ")
		if stdout, ok := out[argv]; ok {
			return stdout, "", 0, nil
		}
		*calls = append(*calls, append([]string{name}, args...))
		return "", "", 0, nil
	}
}

// TestRcctlFlags pins the OpenBSD flags commands: rcctl get NAME flags as
// the probe, and rcctl set NAME flags with the flags as one argument, or
// none for empty flags (rcctl then writes the plain NAME_flags= line).
func TestRcctlFlags(t *testing.T) {
	for _, tt := range []struct {
		flags, current string
		want           [][]string
	}{
		{"", "", nil},
		{"", "-v", [][]string{{"rcctl", "set", "httpd", "flags"}}},
		{"-d -v", "", [][]string{{"rcctl", "set", "httpd", "flags", "-d -v"}}},
		{"-d -v", "-d -v", nil},
	} {
		resource.ResetReport()
		var calls [][]string
		run := recordingRunner(map[string]string{
			"rcctl check httpd": "", "rcctl get httpd status": "", "rcctl get httpd flags": tt.current + "\n",
		}, &calls)
		s := withFlagsSvc(Service{name: "httpd"}, tt.flags)
		if err := s.applyWith(rcctlBackend{run: run}); err != nil {
			t.Fatalf("applyWith(%q over %q): %v", tt.flags, tt.current, err)
		}
		if !reflect.DeepEqual(calls, tt.want) {
			t.Fatalf("flags %q over %q: calls %v, want %v", tt.flags, tt.current, calls, tt.want)
		}
	}
}

// TestFreeBSDFlags pins the FreeBSD flags commands: sysrc -n -i NAME_flags
// as the probe (an unset variable prints nothing, matching empty flags) and
// sysrc NAME_flags=FLAGS as one argv.
func TestFreeBSDFlags(t *testing.T) {
	for _, tt := range []struct {
		flags, current string
		want           [][]string
	}{
		{"", "", nil},
		{"-4", "", [][]string{{"sysrc", "nginx_flags=-4"}}},
		{"", "-4", [][]string{{"sysrc", "nginx_flags="}}},
	} {
		resource.ResetReport()
		var calls [][]string
		run := recordingRunner(map[string]string{
			"service nginx status": "", "service nginx enabled": "", "sysrc -n -i nginx_flags": tt.current + "\n",
		}, &calls)
		s := withFlagsSvc(Service{name: "nginx"}, tt.flags)
		if err := s.applyWith(freebsdBackend{run: run}); err != nil {
			t.Fatalf("applyWith: %v", err)
		}
		if !reflect.DeepEqual(calls, tt.want) {
			t.Fatalf("flags %q over %q: calls %v, want %v", tt.flags, tt.current, calls, tt.want)
		}
	}
	s := withFlagsSvc(Service{name: "my-daemon"}, "-v")
	err := s.applyWith(freebsdBackend{run: recordingRunner(map[string]string{
		"service my-daemon status": "", "service my-daemon enabled": ""}, new([][]string))})
	if err == nil || !strings.Contains(err.Error(), "shell identifier") {
		t.Fatalf("a non-identifier service name must be refused, err = %v", err)
	}
}

// netbsdFlagsBackend returns a netbsdBackend over temporary rc.conf files
// holding rcConf, defaults and override (a missing file when empty), with a
// running, enabled service.
func netbsdFlagsBackend(t *testing.T, rcConf, defaults, override string) netbsdBackend {
	t.Helper()
	dir := t.TempDir()
	b := netbsdBackend{
		run: recordingRunner(map[string]string{
			netbsdService + " nsd status": "", netbsdService + " -e nsd": "",
		}, new([][]string)),
		rcConfD:        filepath.Join(dir, "rc.conf.d"),
		rcConf:         filepath.Join(dir, "rc.conf"),
		rcConfDefaults: filepath.Join(dir, "defaults"),
	}
	for path, content := range map[string]string{b.rcConf: rcConf, b.rcConfDefaults: defaults, filepath.Join(b.rcConfD, "nsd"): override} {
		if content == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

// TestNetBSDFlags pins the NetBSD rc.conf edit: the effective value is read
// with rc.conf.d/NAME over rc.conf over defaults, an equal value (however
// quoted) changes nothing, a different one replaces every assignment with
// one single-quoted line in place (mode kept), and a differing rc.conf.d
// override is refused rather than shadowed.
func TestNetBSDFlags(t *testing.T) {
	for _, tt := range []struct {
		name, rcConf, defaults, flags, want string
	}{
		{"unset matches empty", "hostname=x\n", "", "", "hostname=x\n"},
		{"quoted value matches", "nsd_flags=\"-c /etc/nsd.conf\" # main\n", "", "-c /etc/nsd.conf", "nsd_flags=\"-c /etc/nsd.conf\" # main\n"},
		{"defaults value matches", "", "nsd_flags='-4'\n", "-4", ""},
		{"replace in place", "a=1\nnsd_flags=-4\nb=2\n  nsd_flags=\"-6\"\n", "", "-c 'x'", "a=1\nnsd_flags='-c '\\''x'\\'''\nb=2\n"},
		{"append", "a=1\n", "nsd_flags=-4\n", "", "a=1\nnsd_flags=''\n"},
		{"unparseable is rewritten", "nsd_flags=\"$X\"\n", "", "$X", "nsd_flags='$X'\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			b := netbsdFlagsBackend(t, tt.rcConf, tt.defaults, "")
			s := withFlagsSvc(Service{name: "nsd"}, tt.flags)
			if err := s.applyWith(b); err != nil {
				t.Fatalf("applyWith: %v", err)
			}
			got, err := os.ReadFile(b.rcConf)
			if err != nil && tt.want != "" {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("rc.conf = %q, want %q", got, tt.want)
			}
			if info, err := os.Stat(b.rcConf); err == nil && tt.rcConf != "" && info.Mode().Perm() != 0o640 {
				t.Fatalf("rc.conf mode = %v, want 0640 kept", info.Mode().Perm())
			}
		})
	}
	b := netbsdFlagsBackend(t, "nsd_flags=-4\n", "", "nsd=YES\nnsd_flags=-6\n")
	s := withFlagsSvc(Service{name: "nsd"}, "-4")
	if err := s.applyWith(b); err == nil || !strings.Contains(err.Error(), "overrides") {
		t.Fatalf("a differing rc.conf.d override must be refused, err = %v", err)
	}
}

// TestParseShellWord pins the rc.conf value evaluator the NetBSD backend
// compares with: quoting forms it evaluates and the syntax it refuses.
func TestParseShellWord(t *testing.T) {
	for raw, want := range map[string]string{
		``: ``, `-v`: `-v`, `'-a -b'`: `-a -b`, `"-a \"b\" \$c"`: `-a "b" $c`, `a\ b`: `a b`,
		`"x"'y'z`: `xyz`, `-v # comment`: `-v`, `"a\nb"`: `a\nb`,
	} {
		if got, ok := parseShellWord(raw); !ok || got != want {
			t.Errorf("parseShellWord(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{`$X`, `"$X"`, "`id`", `-v; rm -rf /`, `'open`, `"open`, `a b`, `x\`} {
		if _, ok := parseShellWord(raw); ok {
			t.Errorf("parseShellWord(%q) must refuse", raw)
		}
	}
}
