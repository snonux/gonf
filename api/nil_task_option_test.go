package api

import (
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/plan"
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
// to line of the calling test file.
func requireDeclErrAt(t *testing.T, want string, line *int, declare func()) {
	t.Helper()
	_, file, _, _ := runtime.Caller(1)
	requireDeclErr(t, want, declare)
	suffix := fmt.Sprintf("%s:%d", filepath.Base(file), *line)
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

// PtrMarker is a marker with a pointer-receiver StructTaskOptions that
// does not handle a nil receiver.
type PtrMarker struct{ profile string }

func (m *PtrMarker) StructTaskOptions() TaskOptions {
	return TaskOptions{WhenProfile(m.profile)} // dereferences m
}

// nilPtrRecvMarkerStruct embeds a nil pointer-receiver marker.
type nilPtrRecvMarkerStruct struct {
	*PtrMarker
}

func (nilPtrRecvMarkerStruct) Ping() {}

// NilBase is an exported marker by promotion from its embedded
// *RequiresRoot; nilBase is the unexported twin.
type NilBase struct{ *RequiresRoot }

type nilBase struct{ *RequiresRoot }

// nestedNilMarker reaches RequiresRoot through the exported marker field
// NilBase; nestedNilUnexported through the unexported nilBase, i.e. the
// promoted-method fallback.
type nestedNilMarker struct{ NilBase }

func (nestedNilMarker) Ping() {}

type nestedNilUnexported struct{ nilBase }

func (nestedNilUnexported) Ping() {}

// NilSafe is a pointer-receiver marker that handles a nil receiver, which
// is legal Go: a nil *NilSafe is called, not refused.
type NilSafe struct{ operational bool }

func (m *NilSafe) StructTaskOptions() TaskOptions {
	if m == nil {
		return TaskOptions{Privileged()}
	}
	if m.operational {
		return TaskOptions{Privileged(), Operational()}
	}
	return TaskOptions{Privileged()}
}

// NilSafeBase promotes NilSafe through an embedded nil-able pointer.
type NilSafeBase struct{ *NilSafe }

type nilSafeEmbed struct{ *NilSafe }

func (nilSafeEmbed) Ping() {}

type nilSafeNested struct{ NilSafeBase }

func (nilSafeNested) Ping() {}

// nilBeforeNilSafe has a nil pointer EARLIER in the chain than the
// nil-safe marker: reaching NilSafeBase's field dereferences it.
type nilBeforeNilSafe struct{ *NilSafeBase }

func (nilBeforeNilSafe) Ping() {}

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
		{"nil pointer-receiver marker", nilPtrRecvMarkerStruct{}, "RegisterMethods(api.nilPtrRecvMarkerStruct): marker PtrMarker is nil and its StructTaskOptions does not handle a nil receiver"},
		{"nested nil marker", nestedNilMarker{}, "RegisterMethods(api.nestedNilMarker): marker NilBase.RequiresRoot is nil"},
		{"nested nil unexported marker", nestedNilUnexported{}, "RegisterMethods(api.nestedNilUnexported): marker nilBase.RequiresRoot is nil"},
		{"StructOption field holding a nil pointer-receiver marker", ifaceMarkerStruct{StructOption: (*PtrMarker)(nil)}, "RegisterMethods(api.ifaceMarkerStruct): marker StructOption is nil and its StructTaskOptions does not handle a nil receiver"},
		{"nil pointer before a nil-safe marker", nilBeforeNilSafe{}, "RegisterMethods(api.nilBeforeNilSafe): marker NilSafeBase is nil"},
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
		{"RegisterOnCluster nil struct pointer", `RegisterOnCluster("nilc"): item 3 is nil`, func(line *int) {
			Cluster("nilc", Host("n1"))
			*line = thisLine() + 1
			RegisterOnCluster("nilc", nilGroup{}, nilGroupB{}, (*nilGroup)(nil))
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

// twoMarkerStruct embeds a value-receiver marker and a pointer-receiver one
// by value; both markers' options apply.
type twoMarkerStruct struct {
	RequiresRoot
	PtrMarker
}

func (twoMarkerStruct) Ping() {}

// TestRegisterMethodsPointerReceiverMarkerCollected: a value field whose
// marker has a pointer-receiver StructTaskOptions is a marker too, so its
// guard is not silently dropped next to another marker.
func TestRegisterMethodsPointerReceiverMarkerCollected(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(twoMarkerStruct{PtrMarker: PtrMarker{profile: "gonfy"}}, WithPrefix("tm_"))
	if err := declerr.First(); err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate("tm_ping")
	if !ok {
		t.Fatal("tm_ping not queued")
	}
	want := []plan.Predicate{{Fact: "profile", Eq: "gonfy"}}
	if !c.privileged || !reflect.DeepEqual(c.planWhen, want) {
		t.Fatalf("tm_ping: privileged=%v planWhen=%+v, want privileged and %+v", c.privileged, c.planWhen, want)
	}
}

// ptrMarkerOverride, valueMarkerOverride and ptrRecvOverride declare their
// own StructTaskOptions next to an embedded marker: Go method resolution
// picks the struct's own method, so the markers are not collected.
type ptrMarkerOverride struct{ PtrMarker }

func (ptrMarkerOverride) StructTaskOptions() TaskOptions { return TaskOptions{Privileged()} }
func (ptrMarkerOverride) Ping()                          {}

type valueMarkerOverride struct{ RequiresRoot }

func (valueMarkerOverride) StructTaskOptions() TaskOptions { return TaskOptions{Operational()} }
func (valueMarkerOverride) Ping()                          {}

type ptrRecvOverride struct{ RequiresRoot }

func (*ptrRecvOverride) StructTaskOptions() TaskOptions { return TaskOptions{Operational()} }
func (ptrRecvOverride) Ping()                           {}

// TestRegisterMethodsOwnStructTaskOptionsOverridesMarkers: a struct's own
// StructTaskOptions (value or pointer receiver) is the only struct-level
// default; the embedded markers' options are not added.
func TestRegisterMethodsOwnStructTaskOptionsOverridesMarkers(t *testing.T) {
	cases := []struct {
		name            string
		v               any
		wantPrivileged  bool
		wantOperational bool
	}{
		{"pointer marker + own", ptrMarkerOverride{PtrMarker: PtrMarker{profile: "gonfy"}}, true, false},
		{"value marker + own", valueMarkerOverride{}, false, true},
		{"value marker + own pointer receiver", ptrRecvOverride{}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetForHostsState(t)
			RegisterMethods(tc.v, WithPrefix("ov_"))
			if err := declerr.First(); err != nil {
				t.Fatal(err)
			}
			c, ok := findCandidate("ov_ping")
			if !ok {
				t.Fatal("ov_ping not queued")
			}
			if c.privileged != tc.wantPrivileged || c.operational != tc.wantOperational || len(c.planWhen) != 0 {
				t.Fatalf("ov_ping: privileged=%v operational=%v planWhen=%+v, want privileged=%v operational=%v and no guard",
					c.privileged, c.operational, c.planWhen, tc.wantPrivileged, tc.wantOperational)
			}
		})
	}
}

// ownWithNilMarker declares its own StructTaskOptions next to a nil
// *RequiresRoot, which is then never called.
type ownWithNilMarker struct{ *RequiresRoot }

func (ownWithNilMarker) StructTaskOptions() TaskOptions { return TaskOptions{Operational()} }
func (ownWithNilMarker) Ping()                          {}

// OwnBase declares its own StructTaskOptions, which ends the promotion
// chain before its nil *RequiresRoot.
type OwnBase struct{ *RequiresRoot }

func (OwnBase) StructTaskOptions() TaskOptions { return TaskOptions{Operational()} }

type ownBaseChain struct{ OwnBase }

func (ownBaseChain) Ping() {}

// amb1 and amb2 are same-depth unexported markers: the promoted selector
// is ambiguous, so the struct has no StructTaskOptions at all.
type amb1 struct{}

func (amb1) StructTaskOptions() TaskOptions { return TaskOptions{Privileged()} }

type amb2 struct{}

func (amb2) StructTaskOptions() TaskOptions { return TaskOptions{Privileged()} }

type ambiguousMarkers struct {
	amb1
	amb2
}

func (ambiguousMarkers) Ping() {}

// nilSafeByValueOpts embeds a pointer-receiver marker by value and has an
// Opts() companion: the marker composes first, Opts() after it.
type nilSafeByValueOpts struct{ NilSafe }

func (nilSafeByValueOpts) Opts() TaskOptions { return TaskOptions{Unprivileged()} }
func (nilSafeByValueOpts) Ping()             {}

// nilSafePtrOpts embeds a (non-nil) pointer-receiver marker by pointer,
// and nilSafeInnerOpts reaches one by value through the exported embed
// NilSafeInner: both are marker fields and compose before Opts().
type nilSafePtrOpts struct{ *NilSafe }

func (nilSafePtrOpts) Opts() TaskOptions { return TaskOptions{Unprivileged()} }
func (nilSafePtrOpts) Ping()             {}

type NilSafeInner struct{ NilSafe }

type nilSafeInnerOpts struct{ NilSafeInner }

func (nilSafeInnerOpts) Opts() TaskOptions { return TaskOptions{Unprivileged()} }
func (nilSafeInnerOpts) Ping()             {}

// TestRegisterMethodsStructDefaultsResolve pins the struct-level defaults
// of cases that must register cleanly: a nil pointer-receiver marker that
// handles a nil receiver (embedded, nested, or held in a StructOption
// field), a nil marker next to the struct's own StructTaskOptions or behind
// an intermediate type declaring its own, ambiguous same-depth markers (no
// options), and a pointer-receiver marker embedded by value, by pointer or
// through an exported embed, which composes before Opts() so Opts()'s
// Unprivileged() wins.
func TestRegisterMethodsStructDefaultsResolve(t *testing.T) {
	cases := []struct {
		name            string
		v               any
		wantPrivileged  bool
		wantOperational bool
	}{
		{"nil-safe marker embedded", nilSafeEmbed{}, true, false},
		{"nil-safe marker nested", nilSafeNested{}, true, false},
		{"nil-safe marker in StructOption field", ifaceMarkerStruct{StructOption: (*NilSafe)(nil)}, true, false},
		{"non-nil pointer-receiver marker", nilSafeEmbed{NilSafe: &NilSafe{operational: true}}, true, true},
		{"own method next to nil marker", ownWithNilMarker{}, false, true},
		{"intermediate own method before nil marker", ownBaseChain{}, false, true},
		{"ambiguous same-depth markers", ambiguousMarkers{}, false, false},
		{"by-value pointer-receiver marker then Opts", nilSafeByValueOpts{}, false, false},
		{"pointer-embedded pointer-receiver marker then Opts", nilSafePtrOpts{NilSafe: &NilSafe{}}, false, false},
		{"pointer-receiver marker promoted through an exported embed then Opts", nilSafeInnerOpts{}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetForHostsState(t)
			RegisterMethods(tc.v, WithPrefix("sd_"))
			if err := declerr.First(); err != nil {
				t.Fatal(err)
			}
			c, ok := findCandidate("sd_ping")
			if !ok {
				t.Fatal("sd_ping not queued")
			}
			if c.privileged != tc.wantPrivileged || c.operational != tc.wantOperational {
				t.Fatalf("sd_ping: privileged=%v operational=%v, want privileged=%v operational=%v",
					c.privileged, c.operational, tc.wantPrivileged, tc.wantOperational)
			}
		})
	}
}
