package hostfacts

import (
	"os"
	"path/filepath"
	"testing"
)

// useOSRelease points the os-release lookup at content for one test; an
// empty content means no os-release file at all (macOS, the BSDs).
func useOSRelease(t *testing.T, content string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "os-release")
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := osReleasePath
	osReleasePath = p
	t.Cleanup(func() { osReleasePath = prev })
}

func TestProfile(t *testing.T) {
	cases := []struct {
		name, host, goos, osRelease, want string
	}{
		{"darwin without os-release", "mac", "darwin", "", "darwin"},
		{"darwin wins over a rocky hostname", "rocky-mac", "darwin", "", "darwin"},
		{"fedora", "earth", "linux", "NAME=Fedora\nID=fedora\n", "fedora"},
		{"rhel family folds to rocky", "box", "linux", `ID="almalinux"` + "\n", "rocky"},
		{"rocky hostname", "Rocky1", "linux", "ID=fedora\n", "rocky"},
		{"other distro id", "box", "linux", "ID=arch\n", "arch"},
		{"empty id", "box", "linux", "ID=\n", Unknown},
		{"bsd without os-release", "f0", "freebsd", "", Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useOSRelease(t, tc.osRelease)
			if got := Profile(tc.host, tc.goos); got != tc.want {
				t.Fatalf("Profile(%q, %q) = %q, want %q", tc.host, tc.goos, got, tc.want)
			}
		})
	}
}
