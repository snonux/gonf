package api

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/resource"
)

// Task hb regression tests: a nil TaskOption used to panic with a nil-func
// call when Task applied it. Every path that collects options now reports it
// as a declaration error at the recipe line and refuses the task (or the
// struct), since the missing option may have been a guard or Privileged().

// thisLine returns the caller's line, so a test can name the line of the
// DSL call that follows it.
func thisLine() int {
	_, _, line, _ := runtime.Caller(1)
	return line
}

// requireDeclErrAt is requireDeclErr that also pins the reported location
// to line of this file.
func requireDeclErrAt(t *testing.T, want string, line *int, declare func()) {
	t.Helper()
	requireDeclErr(t, want, declare)
	suffix := fmt.Sprintf("nil_task_option_test.go:%d", *line)
	if loc := declerr.Location(declerr.First()); !strings.HasSuffix(loc, suffix) {
		t.Fatalf("declaration error location = %q, want the DSL call (…%s)", loc, suffix)
	}
}

// requireRefusedAfterClear clears the sticky declaration error and checks
// that none of names was queued, activated or can be recorded: the refusal
// holds even once the error no longer blocks the process.
func requireRefusedAfterClear(t *testing.T, names ...string) {
	t.Helper()
	if err := resource.ResetDeclarationError(); err == nil {
		t.Fatal("ResetDeclarationError returned nil, want the reported error")
	}
	Activate(DetectFacts())
	active := map[string]bool{}
	for _, info := range Tasks() {
		active[info.Name] = true
	}
	for _, name := range names {
		requireNotQueued(t, name)
		if active[name] {
			t.Fatalf("task %q is activated, want it refused", name)
		}
		if _, err := RecordPlan("hb", "", name); err == nil {
			t.Fatalf("RecordPlan(%q) succeeded, want the refused task unknown", name)
		}
	}
}

// TestTaskNilOptionIsDeclarationError: a nil among valid options, or a nil
// option variable, refuses that task at the Task call; a later valid task is
// still queued, so the recipe keeps declaring.
func TestTaskNilOptionIsDeclarationError(t *testing.T) {
	var line int
	requireDeclErrAt(t, `Task "nil_opt": option 2 is a nil TaskOption`, &line, func() {
		line = thisLine() + 1
		Task("nil_opt", "", func() {}, Privileged(), nil, WhenLinux())
		Task("after_nil_opt", "", func() {})
	})
	requireQueued(t, "after_nil_opt")
	requireRefusedAfterClear(t, "nil_opt")

	requireDeclErrAt(t, `Task "nil_var": option 1 is a nil TaskOption`, &line, func() {
		var guard TaskOption // e.g. a guard a helper failed to build
		line = thisLine() + 1
		Task("nil_var", "", func() {}, guard)
	})
	requireRefusedAfterClear(t, "nil_var")
}

// nilOptsMethod has a valid Ping and an OptsBroken companion with a nil
// among valid options.
type nilOptsMethod struct{}

func (nilOptsMethod) Ping()                   {}
func (nilOptsMethod) Broken()                 {}
func (nilOptsMethod) OptsBroken() TaskOptions { return TaskOptions{Privileged(), nil} }

// nilOptsStruct's struct-level Opts default holds a nil.
type nilOptsStruct struct{}

func (nilOptsStruct) Ping()             {}
func (nilOptsStruct) Opts() TaskOptions { return TaskOptions{nil} }

// NilMarker is a StructOption marker returning a nil option.
type NilMarker struct{}

func (NilMarker) StructTaskOptions() TaskOptions { return TaskOptions{Privileged(), WhenLinux(), nil} }

// nilMarkerStruct embeds NilMarker.
type nilMarkerStruct struct {
	NilMarker
}

func (nilMarkerStruct) Ping() {}

// nilDirectStructOpts defines StructTaskOptions itself (no marker field).
type nilDirectStructOpts struct{}

func (nilDirectStructOpts) Ping()                          {}
func (nilDirectStructOpts) StructTaskOptions() TaskOptions { return TaskOptions{nil} }

// TestRegisterMethodsNilCompanionOptionIsDeclarationError: a nil returned
// by a companion is reported at the RegisterMethods call (like a companion
// with the wrong signature). A per-method OptsX skips only that method; a
// struct-level Opts, marker or StructTaskOptions registers nothing of the
// struct.
func TestRegisterMethodsNilCompanionOptionIsDeclarationError(t *testing.T) {
	var line int
	requireDeclErrAt(t, "RegisterMethods: OptsBroken returned a nil TaskOption (option 2)", &line, func() {
		line = thisLine() + 1
		RegisterMethods(nilOptsMethod{}, WithPrefix("nm_"))
	})
	requireQueued(t, "nm_ping")
	requireRefusedAfterClear(t, "nm_broken")

	cases := []struct {
		name string
		v    any
		want string
	}{
		{"struct Opts", nilOptsStruct{}, "RegisterMethods: Opts returned a nil TaskOption (option 1)"},
		{"marker", nilMarkerStruct{}, "RegisterMethods: marker NilMarker: StructTaskOptions returned a nil TaskOption (option 3)"},
		{"direct StructTaskOptions", nilDirectStructOpts{}, "RegisterMethods: StructTaskOptions returned a nil TaskOption (option 1)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var line int
			requireDeclErrAt(t, tc.want, &line, func() {
				line = thisLine() + 1
				RegisterMethods(tc.v, WithPrefix("ns_"))
			})
			requireRefusedAfterClear(t, "ns_ping")
		})
	}
}

// nilGroup is the struct the group-option cases register.
type nilGroup struct{}

func (nilGroup) Ping() {}
func (nilGroup) Pong() {}

// TestRegisterMethodsNilGroupOptionIsDeclarationError: a nil TaskOption
// passed as a RegisterOption, or inside WithGroupWhen, is reported at the
// RegisterMethods (or RegisterOnCluster) call with its position, and no
// method of the struct is registered.
func TestRegisterMethodsNilGroupOptionIsDeclarationError(t *testing.T) {
	var nilOpt TaskOption
	cases := []struct {
		name    string
		want    string
		declare func(line *int)
	}{
		{"direct", "RegisterMethods: option 2: nil TaskOption (passed directly or inside WithGroupWhen)", func(line *int) {
			*line = thisLine() + 1
			RegisterMethods(nilGroup{}, WithPrefix("ng_"), nilOpt, WhenLinux())
		}},
		{"WithGroupWhen", "RegisterMethods: option 1: nil TaskOption (passed directly or inside WithGroupWhen)", func(line *int) {
			*line = thisLine() + 1
			RegisterMethods(nilGroup{}, WithGroupWhen(WhenLinux(), nil), WithPrefix("ng_"))
		}},
		{"RegisterOnCluster", `RegisterOnCluster("nilc"): item 2: nil TaskOption (passed directly or inside WithGroupWhen)`, func(line *int) {
			*line = thisLine() + 1
			RegisterOnCluster("nilc", nilGroup{}, nilOpt)
		}},
		{"RegisterOnCluster WithGroupWhen", `RegisterOnCluster("nilc"): item 3: nil TaskOption (passed directly or inside WithGroupWhen)`, func(line *int) {
			*line = thisLine() + 1
			RegisterOnCluster("nilc", nilGroup{}, Operational(), WithGroupWhen(nil))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var line int
			requireDeclErrAt(t, tc.want, &line, func() { tc.declare(&line) })
			requireRefusedAfterClear(t, "ng_ping", "ng_pong", DefaultPrefix(nilGroup{})+"ping", DefaultPrefix(nilGroup{})+"pong")
		})
	}
}
