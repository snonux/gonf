package options

import (
	"fmt"
	"os"
	"testing"
)

// TestModeToFlags pins the raw-octal to Go-flag conversion of the
// setuid/setgid/sticky bits, including the already-converted pass-through.
func TestModeToFlags(t *testing.T) {
	tests := []struct {
		in, want os.FileMode
	}{
		{0o640, 0o640},
		{0o4755, 0o755 | os.ModeSetuid},
		{0o2750, 0o750 | os.ModeSetgid},
		{0o1777, 0o777 | os.ModeSticky},
		{0o7000, os.ModeSetuid | os.ModeSetgid | os.ModeSticky},
		{0o755 | os.ModeSetuid, 0o755 | os.ModeSetuid},
	}
	for _, tt := range tests {
		if got := ModeToFlags(tt.in); got != tt.want {
			t.Errorf("ModeToFlags(%#o) = %#o, want %#o", tt.in, got, tt.want)
		}
	}
}

// TestNormalizeModeAcceptsBothForms checks the valid inputs: raw octal
// special bits are converted, flag-style values and the 0o7777 maximum pass,
// and normalising twice is idempotent. Invalid bits abort the process and are
// covered by TestFatalOptionMisuse.
func TestNormalizeModeAcceptsBothForms(t *testing.T) {
	tests := []struct {
		in, want os.FileMode
	}{
		{0, 0},
		{0o644, 0o644},
		{0o7777, 0o777 | os.ModeSetuid | os.ModeSetgid | os.ModeSticky},
		{0o2755, 0o755 | os.ModeSetgid},
		{0o755 | os.ModeSticky, 0o755 | os.ModeSticky},
		{NormalizeMode(0o4711), 0o711 | os.ModeSetuid},
	}
	for _, tt := range tests {
		if got := NormalizeMode(tt.in); got != tt.want {
			t.Errorf("NormalizeMode(%#o) = %#o, want %#o", tt.in, got, tt.want)
		}
	}
}

// TestModeToWireRoundTrip pins the canonical plan-wire rendering and that it
// inverts NormalizeMode: a raw octal mode survives normalise-then-render.
func TestModeToWireRoundTrip(t *testing.T) {
	tests := []struct {
		in   os.FileMode
		want string
	}{
		{0o644, "0644"},
		{0, "0"},
		{0o755 | os.ModeSetuid, "04755"},
		{0o750 | os.ModeSetgid, "02750"},
		{0o777 | os.ModeSticky, "01777"},
		{0o777 | os.ModeSetuid | os.ModeSetgid | os.ModeSticky, "07777"},
		// Non-permission type bits (e.g. ModeDir) are not part of the wire.
		{os.ModeDir | 0o700, "0700"},
	}
	for _, tt := range tests {
		if got := ModeToWire(tt.in); got != tt.want {
			t.Errorf("ModeToWire(%#o) = %q, want %q", tt.in, got, tt.want)
		}
	}
	for _, raw := range []os.FileMode{0o600, 0o4755, 0o2711, 0o1777, 0o7777} {
		if got, want := ModeToWire(NormalizeMode(raw)), fmt.Sprintf("%#o", uint32(raw)); got != want {
			t.Errorf("round trip %#o: got %q, want %q", raw, got, want)
		}
	}
}

// TestWithModeNormalizesBeforeTheSetter ensures WithMode/WithFileMode hand
// the resource the flag form, whichever form the recipe wrote.
func TestWithModeNormalizesBeforeTheSetter(t *testing.T) {
	target := &recorder{}
	WithMode(0o4755).Apply(target)
	WithFileMode(0o2640).Apply(target)
	want := []setterCall{
		{method: "SetMode", value: 0o755 | os.ModeSetuid},
		{method: "SetFileMode", value: 0o640 | os.ModeSetgid},
	}
	if len(target.calls) != len(want) {
		t.Fatalf("calls = %#v, want %#v", target.calls, want)
	}
	for i := range want {
		if target.calls[i] != want[i] {
			t.Errorf("call %d = %#v, want %#v", i, target.calls[i], want[i])
		}
	}
}
