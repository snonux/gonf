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

// TestLegacyGateOptionsRejectedOutsideDaemonReload pins that the legacy
// IfChanged and WithWatch options, passed through the type-erased
// opt.Option path, are refused (logger.Fatal "does not support ...") by
// Service, Timer and Command, also next to OnChange or WatchChanges (which
// would otherwise give the gate something to watch). IfChanged arms through
// the one SetChangeWatch capability but requires opt.ChangeGated, and
// WithWatch requires opt.Watchable (SetWatch); only daemon-reload
// implements those. DaemonReload accepts them.
func TestLegacyGateOptionsRejectedOutsideDaemonReload(t *testing.T) {
	unit := resource.Resource{Type: "File", Name: "/etc/unit"}
	for _, legacy := range []struct {
		name   string
		erased opt.Option
	}{
		{"IfChanged", opt.IfChanged},
		{"WithWatch", opt.WithWatch("File[/etc/unit]")},
	} {
		for _, tc := range []struct {
			name     string
			register func(opt.Option)
			wantFail bool
		}{
			{name: "Service", wantFail: true, register: func(o opt.Option) {
				service.Present("gonf-s62-fitness", opt.ToServiceOptions(o)...)
			}},
			{name: "Service with OnChange", wantFail: true, register: func(o opt.Option) {
				service.Present("gonf-s62-fitness", append(opt.ToServiceOptions(o), opt.OnChange(unit))...)
			}},
			{name: "Timer with WatchChanges", wantFail: true, register: func(o opt.Option) {
				timer.Present("gonf-s62-fitness", append(opt.ToTimerOptions(o), opt.WatchChanges("File[/etc/unit]"))...)
			}},
			{name: "Command with OnChange", wantFail: true, register: func(o opt.Option) {
				cmd.Present("true", nil, append(opt.ToCommandOptions(o), opt.OnChange(unit))...)
			}},
			{name: "DaemonReload", register: func(o opt.Option) {
				systemd.Present(append(opt.ToDaemonReloadOptions(o), opt.DependsOn(unit))...)
			}},
		} {
			t.Run(legacy.name+"/"+tc.name, func(t *testing.T) {
				// Present only registers (no apply), so nothing touches the host.
				resource.ResetRepository()
				t.Cleanup(resource.ResetRepository)
				if got := catchFatal(t, func() { tc.register(legacy.erased) }); got != tc.wantFail {
					t.Fatalf("%s via erased option fatal = %t, want %t", legacy.name, got, tc.wantFail)
				}
			})
		}
	}
}

// TestEveryGatedKindImplementsTheOneCapability pins the single change-gate
// family: every ChangeGate embedder satisfies opt.ChangeWatchable, and only
// daemon-reload also satisfies opt.ChangeGated (which embeds opt.Watchable,
// the legacy WithWatch slot the legacy spellings need).
func TestEveryGatedKindImplementsTheOneCapability(t *testing.T) {
	var _ opt.Watchable = opt.ChangeGated(nil)
	for _, tc := range []struct {
		target any
		gated  bool
	}{
		{&service.Service{}, false},
		{&timer.Timer{}, false},
		{&cmd.Cmd{}, false},
		{&systemd.DaemonReloadResource{}, true},
	} {
		name := strings.TrimPrefix(fmt.Sprintf("%T", tc.target), "*")
		if _, ok := tc.target.(opt.ChangeWatchable); !ok {
			t.Errorf("%s does not implement opt.ChangeWatchable", name)
		}
		if _, ok := tc.target.(opt.ChangeGated); ok != tc.gated {
			t.Errorf("%s implements opt.ChangeGated = %t, want %t", name, ok, tc.gated)
		}
	}
}
