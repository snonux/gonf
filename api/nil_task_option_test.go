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

// nilPtrMarkerStruct embeds the RequiresRoot marker through a nil pointer.
type nilPtrMarkerStruct struct {
	*RequiresRoot
}

func (nilPtrMarkerStruct) Ping() {}

// ifaceMarkerStruct embeds the StructOption interface itself, which may be
// left unset or hold a nil pointer.
type ifaceMarkerStruct struct {
	StructOption
}

func (ifaceMarkerStruct) Ping() {}

// TestRegisterMethodsNilCompanionOptionIsDeclarationError: a nil returned
// by a companion, or a nil-pointer marker, is reported at the
// RegisterMethods call naming the struct type (like a companion with the
// wrong signature). A per-method OptsX skips only that method; a
// struct-level Opts, marker or StructTaskOptions registers nothing of the
// struct.
func TestRegisterMethodsNilCompanionOptionIsDeclarationError(t *testing.T) {
	var line int
	requireDeclErrAt(t, "RegisterMethods(api.nilOptsMethod): OptsBroken returned a nil TaskOption (option 2)", &line, func() {
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
		{"struct Opts", nilOptsStruct{}, "RegisterMethods(api.nilOptsStruct): Opts returned a nil TaskOption (option 1)"},
		{"marker", nilMarkerStruct{}, "RegisterMethods(api.nilMarkerStruct): marker NilMarker: StructTaskOptions returned a nil TaskOption (option 3)"},
		{"nil-pointer marker", nilPtrMarkerStruct{}, "RegisterMethods(api.nilPtrMarkerStruct): marker RequiresRoot is nil"},
		{"nil-pointer marker via pointer", &nilPtrMarkerStruct{}, "RegisterMethods(api.nilPtrMarkerStruct): marker RequiresRoot is nil"},
		{"nil interface marker", ifaceMarkerStruct{}, "RegisterMethods(api.ifaceMarkerStruct): marker StructOption is nil"},
		{"interface marker holding nil pointer", ifaceMarkerStruct{StructOption: (*RequiresRoot)(nil)}, "RegisterMethods(api.ifaceMarkerStruct): marker StructOption is nil"},
		{"direct StructTaskOptions", nilDirectStructOpts{}, "RegisterMethods(api.nilDirectStructOpts): StructTaskOptions returned a nil TaskOption (option 1)"},
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

// nilGroup and nilGroupB are the structs the group-option cases register.
type nilGroup struct{}

func (nilGroup) Ping() {}
func (nilGroup) Pong() {}

type nilGroupB struct{}

func (nilGroupB) Ping() {}

// TestRegisterMethodsNilGroupOptionIsDeclarationError: a nil
// RegisterOption, a nil TaskOption passed as one, or a nil inside
// WithGroupWhen is reported at the RegisterMethods (or RegisterOnCluster)
// call with its position, and no method of any struct of the call is
// registered — a nil option may stand for a guard such as OnCluster, so
// skipping it would register the methods unguarded.
func TestRegisterMethodsNilGroupOptionIsDeclarationError(t *testing.T) {
	var nilOpt TaskOption
	var nilReg RegisterOption
	cases := []struct {
		name    string
		want    string
		declare func(line *int)
	}{
		{"nil TaskOption", "RegisterMethods: option 2 is a nil TaskOption", func(line *int) {
			*line = thisLine() + 1
			RegisterMethods(nilGroup{}, WithPrefix("ng_"), nilOpt, WhenLinux())
		}},
		{"nil RegisterOption", "RegisterMethods: option 2 is a nil RegisterOption", func(line *int) {
			Cluster("nilc", Host("n1"))
			*line = thisLine() + 1
			RegisterMethods(nilGroup{}, WithPrefix("ng_"), nilReg, OnCluster("nilc"))
		}},
		{"WithGroupWhen", "RegisterMethods: option 1 is WithGroupWhen with a nil TaskOption (option 2)", func(line *int) {
			*line = thisLine() + 1
			RegisterMethods(nilGroup{}, WithGroupWhen(WhenLinux(), nil), WithPrefix("ng_"))
		}},
		{"RegisterOnCluster nil TaskOption", `RegisterOnCluster("nilc"): item 3 is a nil TaskOption`, func(line *int) {
			Cluster("nilc", Host("n1"))
			*line = thisLine() + 1
			RegisterOnCluster("nilc", nilGroup{}, nilGroupB{}, nilOpt)
		}},
		{"RegisterOnCluster WithGroupWhen", `RegisterOnCluster("nilc"): item 3 is WithGroupWhen with a nil TaskOption (option 1)`, func(line *int) {
			Cluster("nilc", Host("n1"))
			*line = thisLine() + 1
			RegisterOnCluster("nilc", nilGroup{}, Operational(), WithGroupWhen(nil), nilGroupB{})
		}},
		{"RegisterOnCluster nil item", `RegisterOnCluster("nilc"): item 2 is nil`, func(line *int) {
			Cluster("nilc", Host("n1"))
			*line = thisLine() + 1
			RegisterOnCluster("nilc", nilGroup{}, nilReg, nilGroupB{})
		}},
	}
	refused := []string{"ng_ping", "ng_pong",
		DefaultPrefix(nilGroup{}) + "ping", DefaultPrefix(nilGroup{}) + "pong", DefaultPrefix(nilGroupB{}) + "ping"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var line int
			requireDeclErrAt(t, tc.want, &line, func() { tc.declare(&line) })
			requireRefusedAfterClear(t, refused...)
		})
	}
}

// TestRegisterMethodsInterfaceMarkerSet: the nil-marker guard does not
// refuse an embedded StructOption interface that holds a real marker; its
// options still apply.
func TestRegisterMethodsInterfaceMarkerSet(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(ifaceMarkerStruct{StructOption: RequiresRoot{}}, WithPrefix("im_"))
	if err := declerr.First(); err != nil {
		t.Fatal(err)
	}
	if c, ok := findCandidate("im_ping"); !ok || !c.privileged {
		t.Fatalf("im_ping candidate = %+v (queued %v), want queued and privileged", c, ok)
	}
}
