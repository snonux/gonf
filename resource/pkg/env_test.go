package pkg

import (
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

type envPkgCall struct {
	env  []string
	bin  string
	args []string
}

func TestPackageWithEnvReachesEveryBackendProbeAndAction(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() {
		resource.SetDryRun(oldDry)
	})
	resource.SetDryRun(false)

	tests := []struct {
		name   string
		apply  func(*Package) error
		latest bool
		want   []string
	}{
		{"dnf install", applyVia(dnfBackend{}), false, []string{"dnf", "install", "-y", "dtail"}},
		{"openbsd PKG_PATH install", applyVia(openbsdBackend{}), false, []string{"pkg_add", "dtail"}},
		{"openbsd PKG_PATH latest installs when absent", applyVia(openbsdBackend{}), true, []string{"pkg_add", "dtail"}},
		{"freebsd install", applyVia(freebsdBackend{}), false, []string{"pkg", "install", "-y", "dtail"}},
		{"netbsd install", applyVia(netbsdBackend{}), false, []string{netbsdPkgin, "-y", "install", "dtail"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []envPkgCall
			p := &Package{name: "dtail", latest: tt.latest}
			p.runFn = func(string, ...string) (string, string, int, error) {
				t.Fatal("unset runner used for package with WithEnv")
				return "", "", 1, nil
			}
			p.runEnvFn = func(env []string, bin string, args ...string) (string, string, int, error) {
				calls = append(calls, envPkgCall{append([]string(nil), env...), bin, append([]string(nil), args...)})
				if isPackageProbe(bin, args) {
					return "", "not installed", 1, nil
				}
				return "", "", 0, nil
			}
			p.SetEnv(map[string]string{"PKG_PATH": "https://pkgrepo.example/openbsd/"})
			if err := tt.apply(p); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if len(calls) != 2 {
				t.Fatalf("calls = %#v, want probe and action", calls)
			}
			gotAction := append([]string{calls[1].bin}, calls[1].args...)
			if !slices.Equal(gotAction, tt.want) {
				t.Errorf("action = %v, want %v", gotAction, tt.want)
			}
			for _, call := range calls {
				if !envContains(call.env, "PKG_PATH=https://pkgrepo.example/openbsd/") {
					t.Errorf("%s %v environment missing PKG_PATH: %v", call.bin, call.args, call.env)
				}
			}
		})
	}
}

func TestPackageWithoutEnvUsesLegacyRunner(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() {
		resource.SetDryRun(oldDry)
	})
	resource.SetDryRun(false)

	var legacyCalls int
	p := &Package{name: "dtail"}
	p.runFn = func(bin string, args ...string) (string, string, int, error) {
		legacyCalls++
		if isPackageProbe(bin, args) {
			return "", "not installed", 1, nil
		}
		return "", "", 0, nil
	}
	p.runEnvFn = func([]string, string, ...string) (string, string, int, error) {
		t.Fatal("environment runner used without WithEnv")
		return "", "", 1, nil
	}

	if err := applyVia(openbsdBackend{})(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if legacyCalls != 2 {
		t.Fatalf("legacy calls = %d, want probe and action", legacyCalls)
	}
}

func isPackageProbe(bin string, args []string) bool {
	return strings.Contains(strings.Join(args, " "), "-e") ||
		(bin == "rpm" && len(args) >= 1 && args[0] == "-q")
}

func envContains(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}
