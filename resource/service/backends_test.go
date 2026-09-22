package service

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/resource"
)

// svcCall records one invocation of the swapped command runner.
type svcCall struct {
	bin  string
	args []string
}

// fakeSystemdCtl fakes systemctl: is-active/is-enabled report running/enabled
// via their exit code; mutations are recorded and succeed (or exit non-zero
// when failAction is set; fail to start when actionErr is set). probeErr and
// enabledErr make the respective probe fail to start.
func fakeSystemdCtl(running, enabled, probeErr, enabledErr, failAction, actionErr bool, calls *[]svcCall) func(string, ...string) (string, string, int, error) {
	return func(bin string, args ...string) (string, string, int, error) {
		*calls = append(*calls, svcCall{bin: bin, args: args})
		switch {
		case contains(args, "is-active"), contains(args, "is-enabled"):
			if (contains(args, "is-active") && probeErr) || (contains(args, "is-enabled") && enabledErr) {
				return "", "", -1, errors.New("exec: systemctl not found")
			}
			if (contains(args, "is-active") && running) || (contains(args, "is-enabled") && enabled) {
				return "", "", 0, nil
			}
			return "", "", 1, nil
		case actionErr:
			return "", "", -1, errors.New("exec: systemctl not found")
		case failAction:
			return "out", "err", 3, nil
		default:
			return "", "", 0, nil
		}
	}
}

// fakeFreeBSDSvc fakes service(8) on FreeBSD: [name status]/[name enabled]
// probes report state; mutations are recorded and succeed.
func fakeFreeBSDSvc(running, enabled, probeErr, enabledErr, failAction, actionErr bool, calls *[]svcCall) func(string, ...string) (string, string, int, error) {
	return func(bin string, args ...string) (string, string, int, error) {
		*calls = append(*calls, svcCall{bin: bin, args: args})
		if len(args) == 2 && (args[1] == "status" || args[1] == "enabled") {
			if (args[1] == "status" && probeErr) || (args[1] == "enabled" && enabledErr) {
				return "", "", -1, errors.New("exec: service not found")
			}
			if (args[1] == "status" && running) || (args[1] == "enabled" && enabled) {
				return "", "", 0, nil
			}
			return "", "", 1, nil
		}
		if actionErr {
			return "", "", -1, errors.New("exec: service not found")
		}
		if failAction {
			return "out", "err", 3, nil
		}
		return "", "", 0, nil
	}
}

// fakeNetBSDSvc fakes service(8) on NetBSD: [name status] and [-e name]
// probes report state; mutations are recorded and succeed.
func fakeNetBSDSvc(running, enabled, probeErr, enabledErr, failAction, actionErr bool, calls *[]svcCall) func(string, ...string) (string, string, int, error) {
	return func(bin string, args ...string) (string, string, int, error) {
		*calls = append(*calls, svcCall{bin: bin, args: args})
		probe := len(args) == 2 && (args[1] == "status" || args[0] == "-e")
		if probe {
			if (args[1] == "status" && probeErr) || (args[0] == "-e" && enabledErr) {
				return "", "", -1, errors.New("exec: service not found")
			}
			if (args[1] == "status" && running) || (args[0] == "-e" && enabled) {
				return "", "", 0, nil
			}
			return "", "", 1, nil
		}
		if actionErr {
			return "", "", -1, errors.New("exec: service not found")
		}
		if failAction {
			return "out", "err", 3, nil
		}
		return "", "", 0, nil
	}
}

// fakeRcctl fakes rcctl(8) on OpenBSD: check/get probes report state;
// mutations are recorded and succeed.
func fakeRcctl(running, enabled, probeErr, enabledErr, failAction, actionErr bool, calls *[]svcCall) func(string, ...string) (string, string, int, error) {
	return func(bin string, args ...string) (string, string, int, error) {
		*calls = append(*calls, svcCall{bin: bin, args: args})
		probe := len(args) >= 2 && (args[0] == "check" || args[0] == "get")
		if probe {
			if (args[0] == "check" && probeErr) || (args[0] == "get" && enabledErr) {
				return "", "", -1, errors.New("exec: rcctl not found")
			}
			if (args[0] == "check" && running) || (args[0] == "get" && enabled) {
				return "", "", 0, nil
			}
			return "", "", 1, nil
		}
		if actionErr {
			return "", "", -1, errors.New("exec: rcctl not found")
		}
		if failAction {
			return "out", "err", 3, nil
		}
		return "", "", 0, nil
	}
}

// assertSvcActions checks the recorded mutation invocations (everything that
// is not a probe) against wantActions: same count, same binary, same args in
// order. wantActions nil means no action may run.
func assertSvcActions(t *testing.T, calls []svcCall, wantActions [][]string, isProbe func(args []string) bool) {
	t.Helper()
	var actions []svcCall
	for _, c := range calls {
		if !isProbe(c.args) {
			actions = append(actions, c)
		}
	}
	if len(actions) != len(wantActions) {
		t.Fatalf("actions = %v, want %v (all calls: %v)", actions, wantActions, calls)
	}
	for i, want := range wantActions {
		if actions[i].bin != want[0] || !slices.Equal(actions[i].args, want[1:]) {
			t.Errorf("action %d = %s %v, want %s %v", i, actions[i].bin, actions[i].args, want[0], want[1:])
		}
	}
}

func withAbsentSvc(s Service) Service  { s.Absent = true; return s }
func withReloadSvc(s Service) Service  { s.reload = true; return s }
func withRestartSvc(s Service) Service { s.restart = true; return s }
func withUserSvc(s Service) Service    { s.user = true; return s }

// svcState bundles the fake state a table row needs.
type svcState struct {
	running    bool
	enabled    bool
	probeErr   bool // status/is-active probe fails to start
	enabledErr bool // enabled/is-enabled probe fails to start
	failAction bool // action exits non-zero
	actionErr  bool // action fails to start
	dryRun     bool
}

// TestApplySystemdFake pins the systemctl backend: probes gate the action
// list, converged states note ok without mutations, absent stops/disables,
// reload takes precedence over restart, and --user prefixes everything on the
// user bus.
func TestApplySystemdFake(t *testing.T) {
	oldDry := resource.DryRun()
	defer resource.SetDryRun(oldDry)
	// Install a failing guard before the table, so a case that forgets its
	// own fake cannot reach the real systemctl; t's cleanup restores it.
	testseam.FakeServiceRunner(t, func(name string, args ...string) (string, string, int, error) {
		t.Fatalf("unfaked service runner reached: %s %v", name, args)
		return "", "", -1, nil
	})

	tests := []struct {
		name       string
		svc        Service
		state      svcState
		wantSystem [][]string // systemctl action vectors; nil = no mutation
		wantNote   resource.Status
		wantErr    string
	}{
		{
			name:     "present converges when running and enabled",
			svc:      Service{name: "sshd"},
			state:    svcState{running: true, enabled: true},
			wantNote: resource.StatusOK,
		},
		{
			name:     "absent converges when stopped and disabled",
			svc:      withAbsentSvc(Service{name: "sshd"}),
			wantNote: resource.StatusOK,
		},
		{
			name:       "present enables and starts",
			svc:        Service{name: "sshd"},
			wantSystem: [][]string{{"systemctl", "enable", "sshd"}, {"systemctl", "start", "sshd"}},
			wantNote:   resource.StatusChanged,
		},
		{
			name:       "present starts when enabled but stopped",
			svc:        Service{name: "sshd"},
			state:      svcState{enabled: true},
			wantSystem: [][]string{{"systemctl", "start", "sshd"}},
			wantNote:   resource.StatusChanged,
		},
		{
			name:       "present enables when running but disabled",
			svc:        Service{name: "sshd"},
			state:      svcState{running: true},
			wantSystem: [][]string{{"systemctl", "enable", "sshd"}},
			wantNote:   resource.StatusChanged,
		},
		{
			name:       "reload wins while running",
			svc:        withReloadSvc(Service{name: "sshd"}),
			state:      svcState{running: true, enabled: true},
			wantSystem: [][]string{{"systemctl", "reload", "sshd"}},
			wantNote:   resource.StatusChanged,
		},
		{
			name:       "restart when running without reload",
			svc:        withRestartSvc(Service{name: "sshd"}),
			state:      svcState{running: true, enabled: true},
			wantSystem: [][]string{{"systemctl", "restart", "sshd"}},
			wantNote:   resource.StatusChanged,
		},
		{
			name:       "reload takes precedence over restart",
			svc:        withReloadSvc(withRestartSvc(Service{name: "sshd"})),
			state:      svcState{running: true, enabled: true},
			wantSystem: [][]string{{"systemctl", "reload", "sshd"}},
			wantNote:   resource.StatusChanged,
		},
		{
			name:       "absent stops and disables",
			svc:        withAbsentSvc(Service{name: "sshd"}),
			state:      svcState{running: true, enabled: true},
			wantSystem: [][]string{{"systemctl", "stop", "sshd"}, {"systemctl", "disable", "sshd"}},
			wantNote:   resource.StatusChanged,
		},
		{
			name:       "absent stops a running but disabled unit",
			svc:        withAbsentSvc(Service{name: "sshd"}),
			state:      svcState{running: true},
			wantSystem: [][]string{{"systemctl", "stop", "sshd"}},
			wantNote:   resource.StatusChanged,
		},
		{
			name:       "absent disables an enabled but stopped unit",
			svc:        withAbsentSvc(Service{name: "sshd"}),
			state:      svcState{enabled: true},
			wantSystem: [][]string{{"systemctl", "disable", "sshd"}},
			wantNote:   resource.StatusChanged,
		},
		{
			name:       "user bus prefixes --user on probes and actions",
			svc:        withUserSvc(Service{name: "sshd"}),
			wantSystem: [][]string{{"systemctl", "--user", "enable", "sshd"}, {"systemctl", "--user", "start", "sshd"}},
			wantNote:   resource.StatusChanged,
		},
		{
			name:     "dry-run would-act notes would-change without actions",
			svc:      Service{name: "sshd"},
			state:    svcState{dryRun: true},
			wantNote: resource.StatusWouldChange,
		},
		{
			name:     "dry-run converged stays ok",
			svc:      Service{name: "sshd"},
			state:    svcState{running: true, enabled: true, dryRun: true},
			wantNote: resource.StatusOK,
		},
		{
			name:    "probe start failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{probeErr: true},
			wantErr: "systemctl is-active sshd",
		},
		{
			name:    "enabled probe start failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{enabledErr: true},
			wantErr: "systemctl is-enabled sshd",
		},
		{
			name:       "action start failure is an error",
			svc:        Service{name: "sshd"},
			state:      svcState{actionErr: true},
			wantSystem: [][]string{{"systemctl", "enable", "sshd"}},
			wantErr:    "systemctl [enable sshd]: exec: systemctl not found",
		},
		{
			name:       "action failure is an error",
			svc:        Service{name: "sshd"},
			state:      svcState{failAction: true},
			wantSystem: [][]string{{"systemctl", "enable", "sshd"}},
			wantErr:    "systemctl [enable sshd] failed (exit 3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.state.dryRun)

			var calls []svcCall
			testseam.FakeServiceRunner(t, fakeSystemdCtl(tt.state.running, tt.state.enabled, tt.state.probeErr, tt.state.enabledErr, tt.state.failAction, tt.state.actionErr, &calls))

			err := tt.svc.applyWith(systemdBackend{})

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("systemd applyWith err = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("systemd applyWith: %v", err)
			}

			assertSvcActions(t, calls, tt.wantSystem, func(args []string) bool {
				return contains(args, "is-active") || contains(args, "is-enabled")
			})
			assertSvcNote(t, tt.svc.name, tt.wantNote)
		})
	}
}

// TestApplyFreeBSDFake pins the FreeBSD service(8) backend: status/enabled
// probes gate enable/start or stop/disable, and reload precedes restart. The
// fake runner is handed to the backend itself; no package seam is patched.
func TestApplyFreeBSDFake(t *testing.T) {
	oldDry := resource.DryRun()
	defer resource.SetDryRun(oldDry)

	tests := []struct {
		name     string
		svc      Service
		state    svcState
		wantSvc  [][]string // service(8) action vectors; nil = no mutation
		wantNote resource.Status
		wantErr  string
	}{
		{
			name:     "present converges when running and enabled",
			svc:      Service{name: "sshd"},
			state:    svcState{running: true, enabled: true},
			wantNote: resource.StatusOK,
		},
		{
			name:     "absent converges when stopped and disabled",
			svc:      withAbsentSvc(Service{name: "sshd"}),
			wantNote: resource.StatusOK,
		},
		{
			name:     "present enables and starts",
			svc:      Service{name: "sshd"},
			wantSvc:  [][]string{{"service", "sshd", "enable"}, {"service", "sshd", "start"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "present starts when enabled but stopped",
			svc:      Service{name: "sshd"},
			state:    svcState{enabled: true},
			wantSvc:  [][]string{{"service", "sshd", "start"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "present enables when running but disabled",
			svc:      Service{name: "sshd"},
			state:    svcState{running: true},
			wantSvc:  [][]string{{"service", "sshd", "enable"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "reload wins while running",
			svc:      withReloadSvc(Service{name: "sshd"}),
			state:    svcState{running: true, enabled: true},
			wantSvc:  [][]string{{"service", "sshd", "reload"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "restart when running without reload",
			svc:      withRestartSvc(Service{name: "sshd"}),
			state:    svcState{running: true, enabled: true},
			wantSvc:  [][]string{{"service", "sshd", "restart"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "absent stops and disables",
			svc:      withAbsentSvc(Service{name: "sshd"}),
			state:    svcState{running: true, enabled: true},
			wantSvc:  [][]string{{"service", "sshd", "stop"}, {"service", "sshd", "disable"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "dry-run would-act notes would-change without actions",
			svc:      Service{name: "sshd"},
			state:    svcState{dryRun: true},
			wantNote: resource.StatusWouldChange,
		},
		{
			name:     "dry-run converged stays ok",
			svc:      Service{name: "sshd"},
			state:    svcState{running: true, enabled: true, dryRun: true},
			wantNote: resource.StatusOK,
		},
		{
			name:    "probe start failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{probeErr: true},
			wantErr: "service sshd status",
		},
		{
			name:    "enabled probe start failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{enabledErr: true},
			wantErr: "service sshd enabled",
		},
		{
			name:    "action start failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{actionErr: true},
			wantSvc: [][]string{{"service", "sshd", "enable"}},
			wantErr: "service [sshd enable]: exec: service not found",
		},
		{
			name:    "action failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{failAction: true},
			wantSvc: [][]string{{"service", "sshd", "enable"}},
			wantErr: "service [sshd enable] failed (exit 3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.state.dryRun)

			var calls []svcCall
			run := fakeFreeBSDSvc(tt.state.running, tt.state.enabled, tt.state.probeErr, tt.state.enabledErr, tt.state.failAction, tt.state.actionErr, &calls)

			err := tt.svc.applyWith(freebsdBackend{run: run})

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("freebsd applyWith err = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("freebsd applyWith: %v", err)
			}

			assertSvcActions(t, calls, tt.wantSvc, func(args []string) bool {
				return len(args) == 2 && (args[1] == "status" || args[1] == "enabled")
			})
			assertSvcNote(t, tt.svc.name, tt.wantNote)
		})
	}
}

// TestApplyNetBSDFake pins the NetBSD backend: service(8) probes gate the
// actions, enable/disable persist to rc.conf.d, and reload precedes restart.
func TestApplyNetBSDFake(t *testing.T) {
	oldDry := resource.DryRun()
	defer resource.SetDryRun(oldDry)

	// The backend under test gets a temp rc.conf.d override directory and
	// the fake runner as fields, so enable and disable run without touching
	// /etc and without patching any package-level variable.
	rcConfD := t.TempDir()

	tests := []struct {
		name     string
		svc      Service
		state    svcState
		wantSvc  [][]string
		wantNote resource.Status
		wantErr  string
		wantFile string // rc.conf.d/<name> content after apply; "" = no check
	}{
		{
			name:     "present converges when running and enabled",
			svc:      Service{name: "sshd"},
			state:    svcState{running: true, enabled: true},
			wantNote: resource.StatusOK,
		},
		{
			name:     "absent converges when stopped and disabled",
			svc:      withAbsentSvc(Service{name: "sshd"}),
			wantNote: resource.StatusOK,
		},
		{
			name:     "present enables via rc.conf.d and starts",
			svc:      Service{name: "sshd"},
			wantSvc:  [][]string{{netbsdService, "sshd", "start"}},
			wantNote: resource.StatusChanged,
			wantFile: "sshd=YES\n",
		},
		{
			name:     "present starts when enabled but stopped",
			svc:      Service{name: "sshd"},
			state:    svcState{enabled: true},
			wantSvc:  [][]string{{netbsdService, "sshd", "start"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "present enables via rc.conf.d when running but disabled",
			svc:      Service{name: "sshd"},
			state:    svcState{running: true},
			wantNote: resource.StatusChanged,
			wantFile: "sshd=YES\n",
		},
		{
			name:     "reload wins while running",
			svc:      withReloadSvc(Service{name: "sshd"}),
			state:    svcState{running: true, enabled: true},
			wantSvc:  [][]string{{netbsdService, "sshd", "reload"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "restart when running without reload",
			svc:      withRestartSvc(Service{name: "sshd"}),
			state:    svcState{running: true, enabled: true},
			wantSvc:  [][]string{{netbsdService, "sshd", "restart"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "absent stops and disables via rc.conf.d",
			svc:      withAbsentSvc(Service{name: "sshd"}),
			state:    svcState{running: true, enabled: true},
			wantSvc:  [][]string{{netbsdService, "sshd", "stop"}},
			wantNote: resource.StatusChanged,
			wantFile: "sshd=NO\n",
		},
		{
			name:     "dry-run would-act notes would-change without actions",
			svc:      Service{name: "sshd"},
			state:    svcState{dryRun: true},
			wantNote: resource.StatusWouldChange,
		},
		{
			name:     "dry-run converged stays ok",
			svc:      Service{name: "sshd"},
			state:    svcState{running: true, enabled: true, dryRun: true},
			wantNote: resource.StatusOK,
		},
		{
			name:    "probe start failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{probeErr: true},
			wantErr: "service sshd status",
		},
		{
			name:    "enabled probe start failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{enabledErr: true},
			wantErr: "service -e sshd",
		},
		{
			name:    "action start failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{actionErr: true},
			wantSvc: [][]string{{netbsdService, "sshd", "start"}},
			wantErr: "service sshd start: exec: service not found",
		},
		{
			name:    "action failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{failAction: true},
			wantSvc: [][]string{{netbsdService, "sshd", "start"}},
			wantErr: "service sshd start failed (exit 3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.state.dryRun)
			_ = os.Remove(filepath.Join(rcConfD, tt.svc.name))

			var calls []svcCall
			run := fakeNetBSDSvc(tt.state.running, tt.state.enabled, tt.state.probeErr, tt.state.enabledErr, tt.state.failAction, tt.state.actionErr, &calls)

			err := tt.svc.applyWith(netbsdBackend{run: run, rcConfD: rcConfD})

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("netbsd applyWith err = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("netbsd applyWith: %v", err)
			}

			assertSvcActions(t, calls, tt.wantSvc, func(args []string) bool {
				return len(args) == 2 && (args[1] == "status" || args[0] == "-e")
			})
			assertSvcNote(t, tt.svc.name, tt.wantNote)

			if tt.wantFile == "" {
				return
			}
			got, readErr := os.ReadFile(filepath.Join(rcConfD, tt.svc.name))
			if readErr != nil {
				t.Fatalf("rc.conf.d override not written: %v", readErr)
			}
			if string(got) != tt.wantFile {
				t.Errorf("rc.conf.d content = %q, want %q", got, tt.wantFile)
			}
		})
	}
}

// TestApplyRcctlFake pins the OpenBSD rcctl backend: check/get probes gate
// enable/start/stop/disable and reload precedes restart. The fake runner is
// handed to the backend itself; no package seam is patched.
func TestApplyRcctlFake(t *testing.T) {
	oldDry := resource.DryRun()
	defer resource.SetDryRun(oldDry)

	tests := []struct {
		name     string
		svc      Service
		state    svcState
		wantRc   [][]string // rcctl action vectors; nil = no mutation
		wantNote resource.Status
		wantErr  string
	}{
		{
			name:     "present converges when running and enabled",
			svc:      Service{name: "sshd"},
			state:    svcState{running: true, enabled: true},
			wantNote: resource.StatusOK,
		},
		{
			name:     "absent converges when stopped and disabled",
			svc:      withAbsentSvc(Service{name: "sshd"}),
			wantNote: resource.StatusOK,
		},
		{
			name:     "present enables and starts",
			svc:      Service{name: "sshd"},
			wantRc:   [][]string{{"rcctl", "enable", "sshd"}, {"rcctl", "start", "sshd"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "present starts when enabled but stopped",
			svc:      Service{name: "sshd"},
			state:    svcState{enabled: true},
			wantRc:   [][]string{{"rcctl", "start", "sshd"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "present enables when running but disabled",
			svc:      Service{name: "sshd"},
			state:    svcState{running: true},
			wantRc:   [][]string{{"rcctl", "enable", "sshd"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "reload wins while running",
			svc:      withReloadSvc(Service{name: "sshd"}),
			state:    svcState{running: true, enabled: true},
			wantRc:   [][]string{{"rcctl", "reload", "sshd"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "restart when running without reload",
			svc:      withRestartSvc(Service{name: "sshd"}),
			state:    svcState{running: true, enabled: true},
			wantRc:   [][]string{{"rcctl", "restart", "sshd"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "absent stops and disables",
			svc:      withAbsentSvc(Service{name: "sshd"}),
			state:    svcState{running: true, enabled: true},
			wantRc:   [][]string{{"rcctl", "stop", "sshd"}, {"rcctl", "disable", "sshd"}},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "dry-run would-act notes would-change without actions",
			svc:      Service{name: "sshd"},
			state:    svcState{dryRun: true},
			wantNote: resource.StatusWouldChange,
		},
		{
			name:     "dry-run converged stays ok",
			svc:      Service{name: "sshd"},
			state:    svcState{running: true, enabled: true, dryRun: true},
			wantNote: resource.StatusOK,
		},
		{
			name:    "probe start failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{probeErr: true},
			wantErr: "rcctl check sshd",
		},
		{
			name:    "enabled probe start failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{enabledErr: true},
			wantErr: "rcctl get sshd status",
		},
		{
			name:    "action start failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{actionErr: true},
			wantRc:  [][]string{{"rcctl", "enable", "sshd"}},
			wantErr: "rcctl [enable sshd]: exec: rcctl not found",
		},
		{
			name:    "action failure is an error",
			svc:     Service{name: "sshd"},
			state:   svcState{failAction: true},
			wantRc:  [][]string{{"rcctl", "enable", "sshd"}},
			wantErr: "rcctl [enable sshd] failed (exit 3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.state.dryRun)

			var calls []svcCall
			run := fakeRcctl(tt.state.running, tt.state.enabled, tt.state.probeErr, tt.state.enabledErr, tt.state.failAction, tt.state.actionErr, &calls)

			err := tt.svc.applyWith(rcctlBackend{run: run})

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("rcctl applyWith err = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("rcctl applyWith: %v", err)
			}

			assertSvcActions(t, calls, tt.wantRc, func(args []string) bool {
				return len(args) >= 2 && (args[0] == "check" || args[0] == "get")
			})
			assertSvcNote(t, tt.svc.name, tt.wantNote)
		})
	}
}

// assertSvcNote checks the note recorded for Service[name]: PrintSummary
// lists only non-ok notes, so ok means the id must be absent from the
// summary, changed/would-change mean the id must appear with that status.
func assertSvcNote(t *testing.T, name string, want resource.Status) {
	t.Helper()
	var buf strings.Builder
	resource.PrintSummary(&buf)
	summary := buf.String()
	id := "Service[" + name + "]"

	switch want {
	case resource.StatusOK:
		if strings.Contains(summary, id) {
			t.Errorf("expected %s to be noted ok, summary:\n%s", id, summary)
		}
	case resource.StatusChanged, resource.StatusWouldChange:
		wantStr := want.String() + " " + id
		if !strings.Contains(summary, wantStr) {
			t.Errorf("expected %q in summary, got:\n%s", wantStr, summary)
		}
	default:
		t.Fatalf("unexpected want status %v", want)
	}
}

// TestServiceEnsureAndAbsentFake exercises the public constructors on the
// systemd backend: Ensure applies without registering, Absent registers with
// IsAbsent, and WithReload/WithUser reach their setters.
func TestServiceEnsureAndAbsentFake(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd public-API test; BSD backends are covered by their own tables")
	}
	resource.ResetRepository()

	oldDry := resource.DryRun()
	defer resource.SetDryRun(oldDry)

	// Ensure with WithReload on a running unit reloads it.
	var calls []svcCall
	testseam.FakeServiceRunner(t, fakeSystemdCtl(true, true, false, false, false, false, &calls))
	if err := Ensure("sshd", opt.WithReload); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	assertSvcActions(t, calls, [][]string{{"systemctl", "reload", "sshd"}}, func(args []string) bool {
		return contains(args, "is-active") || contains(args, "is-enabled")
	})

	// Ensure with WithUser routes through the user bus.
	calls = nil
	testseam.FakeServiceRunner(t, fakeSystemdCtl(false, false, false, false, false, false, &calls))
	if err := Ensure("sshd", opt.WithUser); err != nil {
		t.Fatalf("Ensure with user bus: %v", err)
	}
	assertSvcActions(t, calls, [][]string{{"systemctl", "--user", "enable", "sshd"}, {"systemctl", "--user", "start", "sshd"}},
		func(args []string) bool {
			return contains(args, "is-active") || contains(args, "is-enabled")
		})

	// Absent registers a service that stops and disables on Apply.
	resource.ResetRepository()
	calls = nil
	testseam.FakeServiceRunner(t, fakeSystemdCtl(true, true, false, false, false, false, &calls))
	Absent("sshd")
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertSvcActions(t, calls, [][]string{{"systemctl", "stop", "sshd"}, {"systemctl", "disable", "sshd"}},
		func(args []string) bool {
			return contains(args, "is-active") || contains(args, "is-enabled")
		})
	assertSvcNote(t, "sshd", resource.StatusChanged)
}
