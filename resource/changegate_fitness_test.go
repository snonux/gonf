package resource_test

import (
	"fmt"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/service"
	"github.com/snonux/gonf/resource/systemd"
	"github.com/snonux/gonf/resource/timer"
)

// fatalPanic is the value panicked by the OnFatal hook installed by
// catchFatal, so a logger.Fatal on the option path unwinds into the test
// instead of exiting the process.
type fatalPanic struct{}

// catchFatal runs register and reports whether it hit logger.Fatal. The
// hook panics before Fatal's os.Exit; the panic is recovered here.
func catchFatal(t *testing.T, register func()) (fataled bool) {
	t.Helper()
	unregister := logger.OnFatal(func() { panic(fatalPanic{}) })
	defer unregister()
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(fatalPanic); !ok {
				panic(r)
			}
			fataled = true
		}
	}()
	register()
	return false
}

// TestIfChangedRejectedOutsideDaemonReload pins that the legacy IfChanged
// option, passed through the type-erased opt.Option path, is refused
// (logger.Fatal "change gate armed with nothing to watch") by Service,
// Timer and Command. Every change-gate option lowers to the one
// SetChangeWatch capability, so the refusal is the embed's CheckWatch at
// registration: IfChanged arms the gate without ids and only daemon-reload
// can fall back to its DependsOn ids. Without the check these embedders
// would silently hold their action forever. DaemonReload with a DependsOn
// fallback accepts it; without one it is refused too (b72 correction).
func TestIfChangedRejectedOutsideDaemonReload(t *testing.T) {
	var erased opt.Option = opt.IfChanged
	unit := resource.Resource{Type: "File", Name: "/etc/unit"}
	for _, tc := range []struct {
		name     string
		register func()
		wantFail bool
	}{
		{name: "Service", wantFail: true, register: func() {
			service.Present("gonf-s62-fitness", opt.ToServiceOptions(erased)...)
		}},
		{name: "Service with deps", wantFail: true, register: func() {
			service.Present("gonf-s62-fitness", append(opt.ToServiceOptions(erased), opt.DependsOn(unit))...)
		}},
		{name: "Timer", wantFail: true, register: func() {
			timer.Present("gonf-s62-fitness", opt.ToTimerOptions(erased)...)
		}},
		{name: "Command", wantFail: true, register: func() {
			cmd.Present("true", nil, opt.ToCommandOptions(erased)...)
		}},
		{name: "DaemonReload without fallback", wantFail: true, register: func() {
			systemd.Present(opt.ToDaemonReloadOptions(erased)...)
		}},
		{name: "DaemonReload", register: func() {
			systemd.Present(append(opt.ToDaemonReloadOptions(erased), opt.DependsOn(unit))...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Present only registers (no apply), so nothing touches the host.
			resource.ResetRepository()
			t.Cleanup(resource.ResetRepository)
			if got := catchFatal(t, tc.register); got != tc.wantFail {
				t.Fatalf("IfChanged via erased option fatal = %t, want %t", got, tc.wantFail)
			}
		})
	}
}

// TestEnsureRefusesGateWithNothingToWatch pins the Ensure-side twin of the
// registration check: the non-registering constructors return the
// nothing-to-watch error (naming the resource) instead of applying a gate
// that could never fire.
func TestEnsureRefusesGateWithNothingToWatch(t *testing.T) {
	var erased opt.Option = opt.IfChanged
	for _, tc := range []struct {
		name   string
		ensure func() error
		wantID string
	}{
		{"Service", func() error { return service.Ensure("gonf-b72", opt.ToServiceOptions(erased)...) }, "Service[gonf-b72]"},
		{"Timer", func() error { return timer.Ensure("gonf-b72", opt.ToTimerOptions(erased)...) }, "Timer[gonf-b72.timer]"},
		{"Command", func() error { return cmd.Ensure("true", nil, opt.ToCommandOptions(erased)...) }, "Command[true]"},
		{"DaemonReload", func() error { return systemd.Ensure(opt.ToDaemonReloadOptions(erased)...) }, "DaemonReload[system]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ensure()
			want := tc.wantID + ": change gate armed with nothing to watch"
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("Ensure = %v, want an error starting %q", err, want)
			}
		})
	}
}

// TestEveryGatedKindImplementsTheOneCapability pins the single change-gate
// family: every ChangeGate embedder satisfies opt.ChangeWatchable, and the
// legacy capability names are aliases of it (so this compiles only while
// they are).
func TestEveryGatedKindImplementsTheOneCapability(t *testing.T) {
	var (
		_ opt.ChangeGated = opt.ChangeWatchable(nil)
		_ opt.Watchable   = opt.ChangeWatchable(nil)
	)
	for _, target := range []any{&service.Service{}, &timer.Timer{}, &cmd.Cmd{}, &systemd.DaemonReloadResource{}} {
		if _, ok := target.(opt.ChangeWatchable); !ok {
			t.Errorf("%s does not implement opt.ChangeWatchable",
				strings.TrimPrefix(fmt.Sprintf("%T", target), "*"))
		}
	}
}
