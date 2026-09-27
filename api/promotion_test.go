package api

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/internal/declerr"
)

// Task hb: promoted methods reached through nil embedded fields, and the
// cost of finding the promotion chain.

// NilThenPanics handles a nil receiver, then panics for an unrelated
// reason; PanicMarker panics on a non-nil receiver. Neither panic is about
// a nil receiver, so both must propagate unchanged.
type NilThenPanics struct{}

func (m *NilThenPanics) StructTaskOptions() TaskOptions {
	if m == nil {
		var s []int
		_ = s[len(s)+1] // index out of range, not a nil dereference
	}
	return nil
}

type nilThenPanicsStruct struct{ *NilThenPanics }

func (nilThenPanicsStruct) Ping() {}

type PanicMarker struct{}

func (PanicMarker) StructTaskOptions() TaskOptions { panic("gonfy's dam burst") }

type panicMarkerStruct struct{ PanicMarker }

func (panicMarkerStruct) Ping() {}

// TestRegisterMethodsMarkerPanicPropagates: callNilReceiver recovers only
// a nil pointer dereference; any other panic of a marker, with a nil or a
// non-nil receiver, reaches the caller unchanged.
func TestRegisterMethodsMarkerPanicPropagates(t *testing.T) {
	cases := []struct {
		name  string
		v     any
		check func(r any) bool
	}{
		{"nil receiver, index out of range", nilThenPanicsStruct{}, func(r any) bool {
			re, ok := r.(runtime.Error)
			return ok && strings.Contains(re.Error(), "index out of range")
		}},
		{"non-nil receiver, explicit panic", panicMarkerStruct{}, func(r any) bool { return r == "gonfy's dam burst" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetForHostsState(t)
			defer func() {
				r := recover()
				if r == nil || !tc.check(r) {
					t.Fatalf("recovered %#v, want the marker's own panic", r)
				}
			}()
			RegisterMethods(tc.v, WithPrefix("pp_"))
			t.Fatalf("RegisterMethods returned (declaration error %v), want the marker's panic", declerr.First())
		})
	}
}

// Rec1..Rec4 form a mutually recursive embed graph: each embeds the other
// three by pointer, and Rec1 also embeds RequiresRoot. A per-path walk of
// it is exponential; promotionPath's breadth-first search expands each
// type once.
type Rec1 struct {
	*Rec2
	*Rec3
	*Rec4
	RequiresRoot
}

type Rec2 struct {
	*Rec1
	*Rec3
	*Rec4
}

type Rec3 struct {
	*Rec1
	*Rec2
	*Rec4
}

type Rec4 struct {
	*Rec1
	*Rec2
	*Rec3
}

type recGraph struct{ Rec2 }

func (recGraph) Ping() {}

// TestRegisterMethodsRecursiveEmbedGraphIsFast: registering a struct over
// the recursive graph finishes promptly (the bound is generous; it only
// catches exponential behaviour) and refuses the nil marker chain.
func TestRegisterMethodsRecursiveEmbedGraphIsFast(t *testing.T) {
	resetForHostsState(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		RegisterMethods(recGraph{}, WithPrefix("rg_"))
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RegisterMethods over a recursive embed graph did not finish in 10s")
	}
	err := declerr.First()
	if err == nil || !strings.Contains(err.Error(), "RegisterMethods(api.recGraph): marker Rec2.Rec1 is nil") {
		t.Fatalf("declaration error = %v, want the nil Rec2.Rec1 marker", err)
	}
	requireNotQueued(t, "rg_ping")
}

// NilCompBase declares a struct-level Opts companion; NilOptsXBase,
// NilWhenXBase and NilDescXBase declare Ping's per-method companions and
// NilTaskBase a task method. Each is embedded as a nil pointer below.
type NilCompBase struct{}

func (NilCompBase) Opts() TaskOptions { return TaskOptions{Privileged()} }

type NilOptsXBase struct{}

func (NilOptsXBase) OptsPing() TaskOptions { return TaskOptions{Privileged()} }

type NilWhenXBase struct{}

func (NilWhenXBase) WhenPing() TaskOption { return WhenLinux() }

type NilWhenFactsBase struct{}

func (NilWhenFactsBase) WhenPing(Facts) bool { return true }

type NilDescXBase struct{}

func (NilDescXBase) DescPing() string { return "ping gonfy's lodge" }

type NilTaskBase struct{}

func (NilTaskBase) Pong() {}

type nilOptsCompanion struct{ *NilCompBase }

func (nilOptsCompanion) Ping() {}

type nilOptsXCompanion struct{ *NilOptsXBase }

func (nilOptsXCompanion) Ping() {}

type nilWhenXCompanion struct{ *NilWhenXBase }

func (nilWhenXCompanion) Ping() {}

type nilWhenFactsCompanion struct{ *NilWhenFactsBase }

func (nilWhenFactsCompanion) Ping() {}

type nilDescXCompanion struct{ *NilDescXBase }

func (nilDescXCompanion) Ping() {}

type nilTaskMethod struct{ *NilTaskBase }

func (nilTaskMethod) Ping() {}

// TestRegisterMethodsNilPromotedCompanionIsDeclarationError: a companion or
// task method promoted through a nil embedded pointer is a declaration
// error naming it and the nil embed, not a panic. Opts() refuses the whole
// struct; a per-method companion or a task method skips only that task.
func TestRegisterMethodsNilPromotedCompanionIsDeclarationError(t *testing.T) {
	cases := []struct {
		name    string
		v       any
		want    string
		refused []string
		queued  []string
	}{
		{"Opts", nilOptsCompanion{}, "RegisterMethods(api.nilOptsCompanion): Opts is promoted through the nil embedded field NilCompBase", []string{"nc_ping"}, nil},
		{"OptsX", nilOptsXCompanion{}, "RegisterMethods(api.nilOptsXCompanion): OptsPing is promoted through the nil embedded field NilOptsXBase", []string{"nc_ping"}, nil},
		{"WhenX", nilWhenXCompanion{}, "RegisterMethods(api.nilWhenXCompanion): WhenPing is promoted through the nil embedded field NilWhenXBase", []string{"nc_ping"}, nil},
		{"WhenX(Facts)", nilWhenFactsCompanion{}, "RegisterMethods(api.nilWhenFactsCompanion): WhenPing is promoted through the nil embedded field NilWhenFactsBase", []string{"nc_ping"}, nil},
		{"DescX", nilDescXCompanion{}, "RegisterMethods(api.nilDescXCompanion): DescPing is promoted through the nil embedded field NilDescXBase", []string{"nc_ping"}, nil},
		{"task method", nilTaskMethod{}, "RegisterMethods(api.nilTaskMethod): Pong is promoted through the nil embedded field NilTaskBase", []string{"nc_pong"}, []string{"nc_ping"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var line int
			requireDeclErrAt(t, tc.want, &line, func() {
				line = thisLine() + 1
				RegisterMethods(tc.v, WithPrefix("nc_"))
			})
			for _, name := range tc.queued {
				requireQueued(t, name)
			}
			requireRefusedAfterClear(t, tc.refused...)
		})
	}
}

// SafeCompBase declares Opts on its pointer receiver and handles a nil
// one; DerefCompBase dereferences its receiver.
type SafeCompBase struct{ extra bool }

func (b *SafeCompBase) Opts() TaskOptions {
	if b == nil || !b.extra {
		return TaskOptions{Privileged()}
	}
	return TaskOptions{Privileged(), Operational()}
}

type DerefCompBase struct{ profile string }

func (b *DerefCompBase) Opts() TaskOptions { return TaskOptions{WhenProfile(b.profile)} }

type nilSafeCompanion struct{ *SafeCompBase }

func (nilSafeCompanion) Ping() {}

type nilDerefCompanion struct{ *DerefCompBase }

func (nilDerefCompanion) Ping() {}

// TestRegisterMethodsNilSafePromotedCompanion: a nil pointer-receiver
// companion is called, like a nil-safe marker; one that dereferences its
// nil receiver is reported instead of panicking.
func TestRegisterMethodsNilSafePromotedCompanion(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(nilSafeCompanion{}, WithPrefix("ns_"))
	if err := declerr.First(); err != nil {
		t.Fatal(err)
	}
	if c, ok := findCandidate("ns_ping"); !ok || !c.privileged {
		t.Fatalf("ns_ping candidate = %+v (queued %v), want queued and privileged", c, ok)
	}

	var line int
	requireDeclErrAt(t, "RegisterMethods(api.nilDerefCompanion): Opts is called on the nil embedded field DerefCompBase and panicked with a nil pointer dereference (probably its nil receiver)", &line, func() {
		line = thisLine() + 1
		RegisterMethods(nilDerefCompanion{}, WithPrefix("nd_"))
	})
	requireRefusedAfterClear(t, "nd_ping")
}

// LateBase carries a task method and its WhenX(Facts) predicate, both of
// which run after RegisterMethods; lateStruct embeds it by pointer.
type LateBase struct{ ran *bool }

func (b LateBase) Pong()             { *b.ran = true }
func (LateBase) WhenPong(Facts) bool { return true }

type lateStruct struct{ *LateBase }

// TestRegisterMethodsLateInitialisedEmbed: a struct registered by pointer
// reads its embedded fields when a task method or WhenX(Facts) predicate
// runs, so an embed set after RegisterMethods is fine; the same struct
// registered by value is a copy whose nil embed can never be set, so it is
// refused.
func TestRegisterMethodsLateInitialisedEmbed(t *testing.T) {
	resetForHostsState(t)
	var l lateStruct
	RegisterMethods(&l, WithPrefix("lt_"))
	if err := declerr.First(); err != nil {
		t.Fatal(err)
	}
	ran := false
	l.LateBase = &LateBase{ran: &ran}
	Activate(DetectFacts())
	if _, err := RecordPlan("hb-late", "", "lt_pong"); err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if !ran {
		t.Fatal("lt_pong's body did not run")
	}

	var line int
	requireDeclErrAt(t, "RegisterMethods(api.lateStruct): Pong is promoted through the nil embedded field LateBase", &line, func() {
		line = thisLine() + 1
		RegisterMethods(lateStruct{}, WithPrefix("lv_"))
	})
	requireRefusedAfterClear(t, "lv_pong")
}

// ReproInner is reached from reproOuter through two embedded pointers;
// only the inner one is nil at registration.
type ReproInner struct{ ran *bool }

func (i ReproInner) Pong()             { *i.ran = true }
func (ReproInner) WhenPong(Facts) bool { return true }

type ReproMid struct{ *ReproInner }

type reproOuter struct{ *ReproMid }

// LateHolder holds the nil *LateBase by value inside the registered copy.
type LateHolder struct{ *LateBase }

type byValueNested struct{ LateHolder }

// TestRegisterMethodsByValueSharedEmbed: a by-value registration copies
// only the struct itself; a nil embed behind a non-nil embedded pointer is
// shared with the recipe and may still be set, so it is not refused (as on
// main), while a nil embed held in the copy, directly or in a by-value
// embedded struct, is.
func TestRegisterMethodsByValueSharedEmbed(t *testing.T) {
	resetForHostsState(t)
	mid := &ReproMid{}
	RegisterMethods(reproOuter{mid}, WithPrefix("rp_"))
	if err := declerr.First(); err != nil {
		t.Fatal(err)
	}
	ran := false
	mid.ReproInner = &ReproInner{ran: &ran}
	Activate(DetectFacts())
	if _, err := RecordPlan("hb-repro", "", "rp_pong"); err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if !ran {
		t.Fatal("rp_pong's body did not run")
	}

	var line int
	requireDeclErrAt(t, "RegisterMethods(api.byValueNested): Pong is promoted through the nil embedded field LateHolder.LateBase", &line, func() {
		line = thisLine() + 1
		RegisterMethods(byValueNested{}, WithPrefix("bn_"))
	})
	requireRefusedAfterClear(t, "bn_pong")
}
