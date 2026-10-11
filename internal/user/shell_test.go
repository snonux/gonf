package user

import (
	"errors"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

func shellProbe(p homePlatform, shell string) scriptedCall {
	call := p.existing("/home/svc")
	line := strings.TrimSuffix(call.stdout, "\n")
	fields := strings.Split(line, ":")
	fields[len(fields)-1] = shell
	call.stdout = strings.Join(fields, ":") + "\n"
	return call
}

func shellUpdate(p homePlatform, shell string) scriptedCall {
	call := p.update(shell)
	for i, arg := range call.args {
		if arg == "--home" {
			call.args[i] = "--shell"
		}
		if arg == "-d" {
			call.args[i] = "-s"
		}
	}
	return call
}

func shellCreation(p homePlatform, shell string) []scriptedCall {
	calls := p.create(shell)
	for i := range calls {
		for j, arg := range calls[i].args {
			if arg == "--home" {
				calls[i].args[j] = "--shell"
			}
			if arg == "-d" {
				calls[i].args[j] = "-s"
			}
		}
	}
	return calls
}

func TestManageShellOnlyChangesRequestedField(t *testing.T) {
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			// Even requested creation-time group/home options never enter the update.
			want := DesiredUser{Name: "svc", Shell: "/usr/local/bin/fish", ManageShell: true, PrimaryGroup: "other", Home: "/elsewhere", CreateHome: true}
			changed, err := ensureHome(t, p, want, []scriptedCall{shellProbe(p, "/bin/sh"), shellUpdate(p, want.Shell)})
			if err != nil || !changed {
				t.Fatalf("Ensure() = %v, changed = %v", err, changed)
			}
			changed, err = ensureHome(t, p, want, []scriptedCall{shellProbe(p, want.Shell)})
			if err != nil || changed {
				t.Fatalf("second Ensure() = %v, changed = %v", err, changed)
			}
		})
	}
}

func TestShellRemainsCreationOnlyWithoutOptIn(t *testing.T) {
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Shell: "relative creation value"}, []scriptedCall{shellProbe(p, "/bin/sh")})
			if err != nil || changed {
				t.Fatalf("Ensure() = %v, changed = %v", err, changed)
			}
		})
	}
}

func TestManageShellCreation(t *testing.T) {
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			want := DesiredUser{Name: "svc", Shell: "/usr/local/bin/fish", ManageShell: true}
			changed, err := ensureHome(t, p, want, append([]scriptedCall{p.missing}, shellCreation(p, want.Shell)...))
			if err != nil || !changed {
				t.Fatalf("Ensure() = %v, changed = %v", err, changed)
			}
		})
	}
}

func TestManageShellDryRun(t *testing.T) {
	original := resource.DryRun()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(original) })
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Shell: "/bin/ksh", ManageShell: true}, []scriptedCall{shellProbe(p, "/bin/sh")})
			if err != nil || !changed {
				t.Fatalf("Ensure() = %v, changed = %v", err, changed)
			}
		})
	}
}

func TestManageShellRejectsInvalidRequestBeforeCommands(t *testing.T) {
	for _, p := range homePlatforms() {
		for _, shell := range []string{"", "fish", "-s", "/", "/bin/../fish", "/bin/fish/", "/bin/f:sh", "/bin/f\nsh", "/bin/f\rsh", "/bin/f\x00sh", "/bin/f\tsh"} {
			t.Run(p.name+"/"+shell, func(t *testing.T) {
				changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Shell: shell, ManageShell: true}, nil)
				if err == nil || changed {
					t.Fatalf("Ensure() = %v, changed = %v", err, changed)
				}
			})
		}
	}
}

func TestManageShellRefusesInvalidProbeWithoutMutation(t *testing.T) {
	for _, p := range homePlatforms() {
		for _, record := range []string{"svc:*:1\n", "other:*:1:1::/home/svc:/bin/sh\n", "svc:*:1:1::/home/svc:/bin/sh\nsecond\n", "svc:*:1:1::/home/svc:/bin/sh:extra\n"} {
			t.Run(p.name+"/"+record, func(t *testing.T) {
				probe := shellProbe(p, "/bin/sh")
				probe.stdout = record
				changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Shell: "/bin/ksh", ManageShell: true}, []scriptedCall{probe})
				if err == nil || changed {
					t.Fatalf("Ensure() = %v, changed = %v", err, changed)
				}
			})
		}
	}
}

func TestManageShellSurfacesProbeAndUpdateErrors(t *testing.T) {
	cause := errors.New("runner stopped")
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			probe := shellProbe(p, "/bin/sh")
			probe.err = cause
			_, err := ensureHome(t, p, DesiredUser{Name: "svc", Shell: "/bin/ksh", ManageShell: true}, []scriptedCall{probe})
			if !errors.Is(err, cause) {
				t.Fatalf("probe error = %v", err)
			}
			update := shellUpdate(p, "/bin/ksh")
			update.code, update.stderr = 1, "permission denied"
			changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Shell: "/bin/ksh", ManageShell: true}, []scriptedCall{shellProbe(p, "/bin/sh"), update})
			if err == nil || !strings.Contains(err.Error(), "permission denied") || changed {
				t.Fatalf("update = %v, changed = %v", err, changed)
			}
		})
	}
}

func TestUnsupportedManageShellCapability(t *testing.T) {
	err := (Capabilities{Platform: "unsupported"}).checkRequest(DesiredUser{Name: "svc", Shell: "/bin/sh", ManageShell: true})
	if err == nil || !strings.Contains(err.Error(), "shell management is not supported on unsupported") {
		t.Fatalf("checkRequest() = %v", err)
	}
}

func TestManageShellInvalidProbeDoesNotChangeGroups(t *testing.T) {
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			probe := shellProbe(p, "/bin/sh")
			probe.stdout = "wrong:*:1:1::/home/svc:/bin/sh\n"
			changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Shell: "/bin/ksh", ManageShell: true, SupplementaryGroups: []string{"newgroup"}}, []scriptedCall{probe})
			if err == nil || changed {
				t.Fatalf("Ensure() = %v, changed = %v", err, changed)
			}
		})
	}
}
