package options

import (
	"os"
	"reflect"
	"testing"
)

// TestModeToFlags pins the raw-octal → Go-flag conversion for the
// setuid/setgid/sticky bits, including the already-converted pass-through.
func TestModeToFlags(t *testing.T) {
	tests := []struct {
		name string
		in   os.FileMode
		want os.FileMode
	}{
		{"plain perms", 0o640, 0o640},
		{"raw setuid", 0o4755, 0o755 | os.ModeSetuid},
		{"raw setgid", 0o2755, 0o755 | os.ModeSetgid},
		{"raw sticky", 0o1755, 0o755 | os.ModeSticky},
		{"raw all three", 0o7777, 0o777 | os.ModeSetuid | os.ModeSetgid | os.ModeSticky},
		{"raw special only", 0o4000, os.ModeSetuid},
		{"flag style setuid", 0o755 | os.ModeSetuid, 0o755 | os.ModeSetuid},
		{"flag style mixed", 0o640 | os.ModeSetgid | os.ModeSticky, 0o640 | os.ModeSetgid | os.ModeSticky},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ModeToFlags(tt.in); got != tt.want {
				t.Errorf("ModeToFlags(%#o) = %#o, want %#o", tt.in, got, tt.want)
			}
		})
	}
}

// TestNormalizeMode pins the WithMode normalizer: raw octal special bits are
// converted to Go flag bits and flag-style values pass through idempotently.
func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		name string
		in   os.FileMode
		want os.FileMode
	}{
		{"plain perms unchanged", 0o640, 0o640},
		{"raw setuid converted", 0o4755, 0o755 | os.ModeSetuid},
		{"raw setgid converted", 0o2755, 0o755 | os.ModeSetgid},
		{"raw sticky converted", 0o1755, 0o755 | os.ModeSticky},
		{"flag style idempotent", 0o755 | os.ModeSetuid, 0o755 | os.ModeSetuid},
		{"double normalization idempotent", normalizeMode(0o4755), 0o755 | os.ModeSetuid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeMode(tt.in); got != tt.want {
				t.Errorf("normalizeMode(%#o) = %#o, want %#o", tt.in, got, tt.want)
			}
		})
	}
}

// TestWithModeNormalizesRawSpecialBits ensures the WithMode/WithFileMode
// option closures normalize the value before it reaches SetMode, so every
// consumer (file, dir, future kinds) gets the same flag-form semantics.
func TestWithModeNormalizesRawSpecialBits(t *testing.T) {
	tests := []struct {
		name   string
		opt    Option
		method string
		want   os.FileMode
	}{
		{"WithMode raw setuid", WithMode(0o4755), "SetMode", 0o755 | os.ModeSetuid},
		{"WithMode flag style", WithMode(0o755 | os.ModeSetuid), "SetMode", 0o755 | os.ModeSetuid},
		{"WithFileMode raw setgid", WithFileMode(0o2755), "SetFileMode", 0o755 | os.ModeSetgid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &capTarget{}
			tt.opt(f)

			if len(f.calls) != 1 {
				t.Fatalf("option made %d setter calls, want 1: %v", len(f.calls), f.calls)
			}
			got := f.calls[0]
			if got.method != tt.method {
				t.Errorf("called %q, want %q", got.method, tt.method)
			}
			if !reflect.DeepEqual(got.value, any(tt.want)) {
				t.Errorf("SetMode value = %#v, want %#o", got.value, tt.want)
			}
		})
	}
}
