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
// (logger.Fatal "does not support IfChanged") by Service, Timer and Command.
// embed.ChangeGate must therefore not provide SetIfChanged: if it did, these
// embedders would silently arm a gate with nothing to watch, holding their
// action forever. DaemonReload is the one kind that accepts it.
func TestIfChangedRejectedOutsideDaemonReload(t *testing.T) {
	var erased opt.Option = opt.IfChanged
	for _, tc := range []struct {
		name     string
		register func()
		wantFail bool
	}{
		{name: "Service", wantFail: true, register: func() {
			service.Present("gonf-s62-fitness", opt.ToServiceOptions(erased)...)
		}},
		{name: "Timer", wantFail: true, register: func() {
			timer.Present("gonf-s62-fitness", opt.ToTimerOptions(erased)...)
		}},
		{name: "Command", wantFail: true, register: func() {
			cmd.Present("true", nil, opt.ToCommandOptions(erased)...)
		}},
		{name: "DaemonReload", register: func() {
			systemd.Present(opt.ToDaemonReloadOptions(erased)...)
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

// TestOnlyDaemonReloadImplementsChangeGated pins the interface-level
// counterpart: opt.ChangeGated (SetIfChanged) is implemented by
// DaemonReload alone among the ChangeGate embedders.
func TestOnlyDaemonReloadImplementsChangeGated(t *testing.T) {
	for _, tc := range []struct {
		target any
		want   bool
	}{
		{&service.Service{}, false},
		{&timer.Timer{}, false},
		{&cmd.Cmd{}, false},
		{&systemd.DaemonReloadResource{}, true},
	} {
		_, got := tc.target.(opt.ChangeGated)
		if got != tc.want {
			t.Errorf("%s implements opt.ChangeGated = %t, want %t",
				strings.TrimPrefix(fmt.Sprintf("%T", tc.target), "*"), got, tc.want)
		}
	}
}
