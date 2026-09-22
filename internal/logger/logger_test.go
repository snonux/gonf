package logger

import (
	"reflect"
	"strings"
	"testing"
)

// TestRedirectUnprefixed pins the redirect internal/testutil.CaptureLog
// builds on: output is written without timestamps at the requested level,
// lines above it are dropped, and restore reinstates the previous
// destination and level.
func TestRedirectUnprefixed(t *testing.T) {
	SetLevel(LevelWarn)
	t.Cleanup(func() { SetLevel(LevelInfo) })
	var buf strings.Builder
	restore := RedirectUnprefixed(&buf, LevelInfo)
	output := buf.String
	Info("hello %s", "world")
	Debug("dropped")
	restore()
	if got := output(); got != "hello world\n" {
		t.Errorf("captured %q, want %q", got, "hello world\n")
	}
	if GetLevel() != LevelWarn {
		t.Errorf("level after restore = %v, want LevelWarn", GetLevel())
	}
	Info("after restore")
	if got := output(); got != "hello world\n" {
		t.Errorf("capture kept writing after restore: %q", got)
	}
}

// TestOnFatalHooksRunNewestFirstOnce pins the hook contract Fatal relies on
// (Fatal itself calls os.Exit, so runFatalHooks is exercised directly; the
// end-to-end exit is covered by api's TestRecordPlanFatalRemovesStaging).
func TestOnFatalHooksRunNewestFirstOnce(t *testing.T) {
	var order []string
	OnFatal(func() { order = append(order, "first") })
	OnFatal(func() { order = append(order, "second") })

	runFatalHooks()
	if want := []string{"second", "first"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("hook order = %v, want %v", order, want)
	}
	runFatalHooks() // a second Fatal must not re-run consumed hooks
	if len(order) != 2 {
		t.Fatalf("hooks ran again: %v", order)
	}
}

// TestOnFatalUnregister: a hook removed by its unregister function (the
// normal-return path of the code that registered it) must not run at Fatal,
// and unregistering twice or after a run is harmless.
func TestOnFatalUnregister(t *testing.T) {
	ran := map[string]bool{}
	keep := OnFatal(func() { ran["keep"] = true })
	gone := OnFatal(func() { ran["gone"] = true })
	gone()
	gone()

	runFatalHooks()
	if !ran["keep"] || ran["gone"] {
		t.Fatalf("ran = %v, want only the registered hook", ran)
	}
	keep() // after the run: no-op
}
