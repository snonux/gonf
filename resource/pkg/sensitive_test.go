package pkg

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// fakePkgSecret is synthetic secret material in a repository URL.
const fakePkgSecret = "fake-repo-password-91ce"

// A sensitive package op (WithSensitive at record time, or a scan match)
// withholds the package manager's failure output on every backend error
// format, while a plain op keeps it. The probe still decides the
// transition, since it only reads the exit code.
func TestSensitivePackageWithholdsFailureOutput(t *testing.T) {
	for _, manager := range []string{"dnf", "freebsd", "openbsd", "netbsd"} {
		t.Run(manager, func(t *testing.T) {
			rs := stubFailingManager(t, manager)
			op := plan.Op{Op: plan.KindPackage, ID: "Package[tool]", Name: "tool",
				Env: map[string]string{"PKG_PATH": "https://u:" + fakePkgSecret + "@repo"}, Sensitive: true}

			err := planHandler{}.Apply(op, plan.ApplyContext{Runners: rs})
			if err == nil || !strings.Contains(err.Error(), "output withheld") {
				t.Fatalf("err = %v, want the withheld failure", err)
			}
			if strings.Contains(err.Error(), fakePkgSecret) {
				t.Fatalf("error leaks the secret: %v", err)
			}

			op.Sensitive = false
			resource.ResetForTest()
			err = planHandler{}.Apply(op, plan.ApplyContext{Runners: rs})
			if err == nil || !strings.Contains(err.Error(), fakePkgSecret) {
				t.Fatalf("plain package: err = %v, want its output", err)
			}
		})
	}
}

// stubFailingManager returns runners making manager the detected package
// manager and its environment-aware runner report "not installed" for every
// probe and a failing mutation that echoes its environment.
func stubFailingManager(t *testing.T, manager string) *runners.Set {
	t.Helper()
	resource.ResetForTest()
	oldDry := resource.DryRun()
	resource.SetDryRun(false)
	pr := &runners.PackageRunners{
		Manager: func() (string, error) { return manager, nil },
		RunEnv: func(env []string, bin string, args ...string) (string, string, int, error) {
			if isPackageProbe(bin, args) {
				return "", "", 1, nil
			}
			return "fetching " + strings.Join(env, " "), "auth failed for " + fakePkgSecret, 1, nil
		},
	}
	t.Cleanup(func() {
		resource.SetDryRun(oldDry)
		resource.ResetForTest()
	})
	return &runners.Set{Package: pr}
}
