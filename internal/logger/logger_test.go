package logger

import (
	"reflect"
	"testing"
)

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
