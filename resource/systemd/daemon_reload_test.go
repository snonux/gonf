package systemd

import (
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/resource"
)

func TestDaemonReloadRunsSystemctl(t *testing.T) {
	resource.ResetRepository()

	var saw []string
	testseam.FakeSystemctl(t, func(name string, args ...string) (string, string, int, error) {
		saw = append(saw, name+" "+strings.Join(args, " "))
		return "", "", 0, nil
	})

	Present()
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
	if len(saw) != 1 || saw[0] != "systemctl daemon-reload" {
		t.Fatalf("got %v", saw)
	}
}

func TestDaemonReloadWithUser(t *testing.T) {
	resource.ResetRepository()

	var saw string
	testseam.FakeSystemctl(t, func(name string, args ...string) (string, string, int, error) {
		saw = name + " " + strings.Join(args, " ")
		return "", "", 0, nil
	})

	Present(opt.WithUser)
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
	if saw != "systemctl --user daemon-reload" {
		t.Fatalf("got %q", saw)
	}
}

func TestDaemonReloadIfChangedSkips(t *testing.T) {
	resource.ResetRepository()

	called := false
	testseam.FakeSystemctl(t, func(name string, args ...string) (string, string, int, error) {
		called = true
		return "", "", 0, nil
	})

	noop := testapply.Register("File", "/tmp/stable", testapply.Noting(resource.StatusOK, "File[/tmp/stable]"))
	Present(opt.DependsOn(noop), opt.IfChanged)
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("expected skip when dependency did not change")
	}
}

func TestDaemonReloadIfChangedRuns(t *testing.T) {
	resource.ResetRepository()

	called := false
	testseam.FakeSystemctl(t, func(name string, args ...string) (string, string, int, error) {
		called = true
		return "", "", 0, nil
	})

	changed := testapply.Register("File", "/tmp/unit", testapply.Noting(resource.StatusChanged, "File[/tmp/unit]"))
	Present(opt.DependsOn(changed), opt.IfChanged)
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected daemon-reload when dependency changed")
	}
}

func TestDaemonReloadOnChangeAndWithWatchMergeRegardlessOfOptionOrder(t *testing.T) {
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
			testseam.FakeSystemctl(t, func(string, ...string) (string, string, int, error) {
				called = true
				return "", "", 0, nil
			})
			changed := testapply.Register("File", "changed", testapply.Noting(resource.StatusChanged, "File[changed]"))
			unchanged := testapply.Register("File", "unchanged", testapply.Noting(resource.StatusOK, "File[unchanged]"))
			Present(tc.opts(changed, unchanged)...)
			if err := testapply.Apply(); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !called {
				t.Fatal("daemon-reload must observe the changed OnChange target regardless of WithWatch order")
			}
		})
	}
}

func TestDaemonReloadIfChangedSeesDirectoryChildFile(t *testing.T) {
	resource.ResetRepository()

	called := false
	testseam.FakeSystemctl(t, func(name string, args ...string) (string, string, int, error) {
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
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected reload when File under Directory changed")
	}
}

func TestDaemonReloadDryRun(t *testing.T) {
	resource.ResetRepository()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	testseam.FakeSystemctl(t, func(name string, args ...string) (string, string, int, error) {
		t.Fatal("dry-run must not call systemctl")
		return "", "", 0, nil
	})
	Present()
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
}
