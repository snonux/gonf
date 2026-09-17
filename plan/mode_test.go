package plan

import (
	"os"
	"testing"
)

// TestParseMode pins the octal wire-mode parsing: raw special bits
// (0o4000/0o2000/0o1000) map to the os.ModeSetuid/ModeSetgid/ModeSticky flag
// bits, plain permissions stay untouched, and modes with bits above 0o7777
// are rejected loudly instead of being silently truncated by chmod.
func TestParseMode(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    os.FileMode
		wantErr bool
	}{
		{"plain three digits", "0640", 0o640, false},
		{"plain without leading zero", "755", 0o755, false},
		{"raw setuid", "4755", 0o755 | os.ModeSetuid, false},
		{"raw setgid", "2755", 0o755 | os.ModeSetgid, false},
		{"raw sticky", "1755", 0o755 | os.ModeSticky, false},
		{"raw all three", "7777", 0o777 | os.ModeSetuid | os.ModeSetgid | os.ModeSticky, false},
		{"special bit only", "4000", os.ModeSetuid, false},
		{"bit above 0o7777", "17555", 0, true},
		{"way above", "77777", 0, true},
		{"not octal", "999", 0, true},
		{"empty", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMode(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseMode(%q) = %#o, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMode(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseMode(%q) = %#o, want %#o", tt.in, got, tt.want)
			}
		})
	}
}

// TestFormatMode pins the plan-wire serialization: three digits for plain
// permissions, four digits when setuid/setgid/sticky are carried as Go flag
// bits — the same form parseMode accepts, so record → apply preserves the
// special bits.
func TestFormatMode(t *testing.T) {
	tests := []struct {
		name string
		in   os.FileMode
		want string
	}{
		{"plain perms", 0o640, "0640"},
		{"dir flag ignored", 0o755 | os.ModeDir, "0755"},
		{"setuid", 0o755 | os.ModeSetuid, "04755"},
		{"setgid", 0o755 | os.ModeSetgid, "02755"},
		{"sticky", 0o755 | os.ModeSticky, "01755"},
		{"all special bits", 0o644 | os.ModeSetuid | os.ModeSetgid | os.ModeSticky, "07644"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatMode(tt.in); got != tt.want {
				t.Errorf("FormatMode(%#o) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestModeWireRoundTrip walks every combination of the setuid/setgid/sticky
// flag bits across a few permission masks and requires
// ParseMode(FormatMode(mode)) == mode: record-side serialization and
// apply-side parsing are exact inverses, so a requested special bit survives
// the plan wire format untouched.
func TestModeWireRoundTrip(t *testing.T) {
	perms := []os.FileMode{0, 0o644, 0o755, 0o700, 0o777}
	specials := []struct {
		name  string
		flags os.FileMode
	}{
		{"none", 0},
		{"setuid", os.ModeSetuid},
		{"setgid", os.ModeSetgid},
		{"sticky", os.ModeSticky},
		{"setuid+setgid+sticky", os.ModeSetuid | os.ModeSetgid | os.ModeSticky},
	}

	for _, perm := range perms {
		for _, sp := range specials {
			mode := perm | sp.flags
			wire := FormatMode(mode)
			got, err := ParseMode(wire)
			if err != nil {
				t.Fatalf("ParseMode(FormatMode(%#o)=%q): %v", mode, wire, err)
			}
			if got != mode {
				t.Errorf("round trip of %#o broke: %q -> %#o", mode, wire, got)
			}
		}
	}
}
