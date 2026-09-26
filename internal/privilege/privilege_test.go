package privilege

import (
	"slices"
	"strings"
	"testing"
)

// TestWrapApplyCmdRemoteIgnoresControllerEuid pins the remote doctrine of
// task 612: WrapApplyCmd must NEVER consult the controller's euid. With the
// hook stubbed to report root, the None+elevate case must still error — on a
// non-root machine the bug fixed in 612 (controller-root unwrapping) is
// otherwise invisible.
func TestWrapApplyCmdRemoteIgnoresControllerEuid(t *testing.T) {
	orig := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = orig })

	got, err := WrapApplyCmd(None, true, "apply -")
	if err == nil || !strings.Contains(err.Error(), "-privilege=none") {
		t.Fatalf("None+elevate must error with the -privilege=none message even as root, got %q, %v", got, err)
	}
}

// TestWrapArgvLocal locks the LOCAL re-exec semantics: None unwraps when
// this process is root (euid is the correct authority locally) and errors
// otherwise.
func TestWrapArgvLocal(t *testing.T) {
	orig := geteuid
	t.Cleanup(func() { geteuid = orig })

	geteuid = func() int { return 0 }
	argv, err := WrapArgv(None, true, []string{"gonf", "apply", "-"})
	if err != nil {
		t.Fatalf("local None+elevate as root must unwrap: %v", err)
	}
	if len(argv) != 3 || argv[0] != "gonf" {
		t.Errorf("expected unwrapped argv, got %v", argv)
	}

	geteuid = func() int { return 1000 }
	argv, err = WrapArgv(None, true, []string{"gonf", "apply", "-"})
	if err == nil || !strings.Contains(err.Error(), "sudo or doas") {
		t.Fatalf("local None+elevate as non-root must error, got %v, %q", argv, err)
	}

	geteuid = func() int { return 1000 }
	argv, err = WrapArgv(Sudo, true, []string{"/bin/true", "arg"})
	if err != nil || argv[0] != "sudo" || argv[1] != "-n" {
		t.Fatalf("sudo wrap: %q %v", argv, err)
	}
	argv, err = WrapArgv(None, false, []string{"/bin/true", "arg"})
	if err != nil || len(argv) != 2 {
		t.Fatalf("plain: %q %v", argv, err)
	}
}

// WrapApplyCmd builds the remote SSH command: the None+elevate case must
// error regardless of the controller's euid (the remote login's privilege
// is not knowable here).
func TestWrapApplyCmd(t *testing.T) {
	tests := []struct {
		name      string
		mode      Mode
		elevate   bool
		applyArgs string
		want      string
		wantErr   bool
	}{
		{"none_plain", None, false, "apply -", "gonf apply -", false},
		{"none_elevate", None, true, "apply -", "", true},
		{"sudo_elevate", Sudo, true, "apply -", "sudo -n gonf apply -", false},
		{"doas_elevate", Doas, true, "apply -", "doas gonf apply -", false},
		{"doas_plain", Doas, false, "apply -n -", "gonf apply -n -", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WrapApplyCmd(tc.mode, tc.elevate, tc.applyArgs)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "-privilege=none") {
					t.Fatalf("want the -privilege=none error, got %q, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestParseMode(t *testing.T) {
	m, err := ParseMode("Doas")
	if err != nil || m != Doas {
		t.Fatal(m, err)
	}
}

// TestParseModeTable pins the accepted spellings (case-insensitive, trimmed)
// and the error for anything else.
func TestParseModeTable(t *testing.T) {
	tests := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", None, false},
		{"none", None, false},
		{"  SUDO ", Sudo, false},
		{"sudo", Sudo, false},
		{"Doas", Doas, false},
		{"doas", Doas, false},
		{"bogus", None, true},
		{"sudoo", None, true},
	}

	for _, tc := range tests {
		t.Run("'"+tc.in+"'", func(t *testing.T) {
			m, err := ParseMode(tc.in)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "unknown mode") {
					t.Fatalf("ParseMode(%q) err = %v, want unknown mode", tc.in, err)
				}
				return
			}
			if err != nil || m != tc.want {
				t.Fatalf("ParseMode(%q) = %v, %v; want %v", tc.in, m, err, tc.want)
			}
		})
	}
}

// TestModeString pins the flag spelling of every Mode value, including the
// unknown-mode default.
func TestModeString(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{None, "none"},
		{Sudo, "sudo"},
		{Doas, "doas"},
		{Mode(99), "none"},
	}

	for _, tc := range tests {
		if got := tc.mode.String(); got != tc.want {
			t.Errorf("Mode(%d).String() = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

// TestWrapApplyCmdInvalidMode pins the defensive default for an out-of-range
// Mode on the remote path.
func TestWrapApplyCmdInvalidMode(t *testing.T) {
	got, err := WrapApplyCmd(Mode(99), true, "apply -")
	if err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("WrapApplyCmd(Mode(99), true, …) err = %v, want invalid mode", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty command on error", got)
	}
}

// TestWrapArgvModes pins the remaining WrapArgv branches: doas wrapping, the
// defensive invalid-mode default, and the plain non-elevated pass-through.
func TestWrapArgvModes(t *testing.T) {
	argv, err := WrapArgv(Doas, true, []string{"gonf", "apply", "-"})
	if err != nil || !slices.Equal(argv, []string{"doas", "gonf", "apply", "-"}) {
		t.Fatalf("doas wrap = %q, %v", argv, err)
	}

	argv, err = WrapArgv(Mode(99), true, []string{"gonf"})
	if err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("invalid mode err = %v, want invalid mode", err)
	}
	if argv != nil {
		t.Errorf("got %v, want nil argv on error", argv)
	}

	argv, err = WrapArgv(Sudo, false, []string{"gonf"})
	if err != nil || !slices.Equal(argv, []string{"gonf"}) {
		t.Fatalf("plain pass-through = %v, %v", argv, err)
	}
}

// WrapArgv serves the LOCAL re-exec path: as root, None+elevate unwraps
// (apply runs in-process); as non-root it errors. The local decision must
// keep depending on the controller's euid.

func TestWrapApplyBinCmdQuotesBin(t *testing.T) {
	got, err := WrapApplyBinCmd(Sudo, true, "/opt/my tools/gonf", "apply -relayed -")
	if err != nil {
		t.Fatal(err)
	}
	if want := "sudo -n '/opt/my tools/gonf' apply -relayed -"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
