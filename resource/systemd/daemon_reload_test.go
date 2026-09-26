package systemd

import (
	"runtime"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/resource"
)

// systemdSet builds a *runners.Set injecting fake as the systemctl runner
// for one testapply.ApplyWithRunners call, replacing the process-global
// internal/testseam.FakeSystemctl fake these tests used before task 4e2.
func systemdSet(fake RunFunc) *runners.Set {
	return &runners.Set{Systemd: &runners.SystemdRunners{Run: fake}}
}

func TestDaemonReloadRunsSystemctl(t *testing.T) {
	requireLinux(t)
	resource.ResetRepository()

	var saw []string
	rs := systemdSet(func(name string, args ...string) (string, string, int, error) {
		saw = append(saw, name+" "+strings.Join(args, " "))
		return "", "", 0, nil
	})

	Present()
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if len(saw) != 1 || saw[0] != "systemctl daemon-reload" {
		t.Fatalf("got %v", saw)
	}
}

func TestDaemonReloadWithUser(t *testing.T) {
	requireLinux(t)
	resource.ResetRepository()

	var saw string
	rs := systemdSet(func(name string, args ...string) (string, string, int, error) {
		saw = name + " " + strings.Join(args, " ")
		return "", "", 0, nil
	})

	Present(opt.WithUser)
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if saw != "systemctl --user daemon-reload" {
		t.Fatalf("got %q", saw)
	}
}

func TestDaemonReloadIfChangedSkips(t *testing.T) {
	requireLinux(t)
	resource.ResetRepository()

	called := false
	rs := systemdSet(func(name string, args ...string) (string, string, int, error) {
		called = true
		return "", "", 0, nil
	})

	noop := testapply.Register("File", "/tmp/stable", testapply.Noting(resource.StatusOK, "File[/tmp/stable]"))
	Present(opt.DependsOn(noop), opt.IfChanged)
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("expected skip when dependency did not change")
	}
}

func TestDaemonReloadIfChangedRuns(t *testing.T) {
	requireLinux(t)
	resource.ResetRepository()

	called := false
	rs := systemdSet(func(name string, args ...string) (string, string, int, error) {
		called = true
		return "", "", 0, nil
	})

	changed := testapply.Register("File", "/tmp/unit", testapply.Noting(resource.StatusChanged, "File[/tmp/unit]"))
	Present(opt.DependsOn(changed), opt.IfChanged)
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected daemon-reload when dependency changed")
	}
}

func TestDaemonReloadOnChangeAndWithWatchMergeRegardlessOfOptionOrder(t *testing.T) {
	requireLinux(t)
	for _, tc := range []struct {
		name string
		opts func(resource.Resource, resource.Resource) []opt.DaemonReloadOption
	}{
		{
			name: "OnChange then legacy WithWatch",
			opts: func(changed, unchanged resource.Resource) []opt.DaemonReloadOption {
				return []opt.DaemonReloadOption{opt.OnChange(changed), opt.WithWatch(unchanged.ID()), opt.IfChanged}
			},
		},
		{
			name: "legacy WithWatch then OnChange",
			opts: func(changed, unchanged resource.Resource) []opt.DaemonReloadOption {
				return []opt.DaemonReloadOption{opt.WithWatch(unchanged.ID()), opt.IfChanged, opt.OnChange(changed)}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()

			called := false
			rs := systemdSet(func(string, ...string) (string, string, int, error) {
				called = true
				return "", "", 0, nil
			})
			changed := testapply.Register("File", "changed", testapply.Noting(resource.StatusChanged, "File[changed]"))
			unchanged := testapply.Register("File", "unchanged", testapply.Noting(resource.StatusOK, "File[unchanged]"))
			Present(tc.opts(changed, unchanged)...)
			if err := testapply.ApplyWithRunners(rs); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !called {
				t.Fatal("daemon-reload must observe the changed OnChange target regardless of WithWatch order")
			}
		})
	}
}

func TestDaemonReloadIfChangedSeesDirectoryChildFile(t *testing.T) {
	requireLinux(t)
	resource.ResetRepository()

	called := false
	rs := systemdSet(func(name string, args ...string) (string, string, int, error) {
		called = true
		return "", "", 0, nil
	})

	dirID := "Directory[/tmp/systemd-user]"
	units := testapply.Register("Directory", "/tmp/systemd-user", func() error {
		resource.Note(dirID, resource.StatusOK)
		resource.Note("File[/tmp/systemd-user/x.timer]", resource.StatusChanged)
		return nil
	})
	Present(opt.DependsOn(units), opt.IfChanged)
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected reload when File under Directory changed")
	}
}

func TestDaemonReloadDryRun(t *testing.T) {
	requireLinux(t)
	resource.ResetRepository()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	rs := systemdSet(func(name string, args ...string) (string, string, int, error) {
		t.Fatal("dry-run must not call systemctl")
		return "", "", 0, nil
	})
	Present()
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
}

// requireLinux skips t off Linux: applying DaemonReload refuses every other
// GOOS, so tests that apply one only make sense on a systemd platform.
func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("DaemonReload applies only on Linux (GOOS=%s)", runtime.GOOS)
	}
}
