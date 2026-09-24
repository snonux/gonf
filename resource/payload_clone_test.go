package resource_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/configset"
	"github.com/snonux/gonf/resource/cron"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/link"
	"github.com/snonux/gonf/resource/pkg"
	"github.com/snonux/gonf/resource/service"
	"github.com/snonux/gonf/resource/systemdtimer"
	"github.com/snonux/gonf/resource/user"
)

// payloadCase names one resource.DraftPayload implementation under test:
// typ is its concrete (non-pointer) reflect.Type, and shared lists the
// field paths it deliberately keeps shared with its source after Clone
// (see file.Payload's TemplateDataErr, an immutable error value nothing
// writes through); every other case shares nothing, so shared is nil.
type payloadCase struct {
	name   string
	typ    reflect.Type
	shared []string
}

// payloadCases is every resource.DraftPayload implementation in the module
// (task w62 Layer 1 moved each kind's exclusive plan-draft fields off
// resource.PlanDraft into its own per-kind payload type; see docs/design/plan.md,
// "The PlanDraft/Op split"). Task 1e2 restores a mechanical Clone-coverage
// guard over these real payloads: TestPayloadCloneContract and
// TestPayloadClonePreservesNilAndEmpty below replace the seven
// hand-maintained fullPayload()/fullSetPayload() fixtures this table
// superseded (formerly in resource/cmd, cron, file, systemdtimer, user and
// configset's own payload_test.go files) and newly cover dir.SyncPayload,
// pkg.Payload, link.Payload, link.IfExistsPayload and service.Payload,
// which had no Clone-contract test at all before. There is no reflective
// way to enumerate DraftPayload implementers across packages (Go has no
// interface-implementer registry), so a new resource/<kind> package that
// adds one must add its entry here by hand, the same way a new plan.Kind
// needs a kindFitnessTable entry (docs/design/plan.md, "Draft payload" step).
var payloadCases = []payloadCase{
	{name: "cmd.Payload", typ: reflect.TypeFor[cmd.Payload]()},
	{name: "configset.SetPayload", typ: reflect.TypeFor[configset.SetPayload]()},
	{name: "configset.MemberPayload", typ: reflect.TypeFor[configset.MemberPayload]()},
	{name: "cron.Payload", typ: reflect.TypeFor[cron.Payload]()},
	{name: "dir.SyncPayload", typ: reflect.TypeFor[dir.SyncPayload]()},
	{name: "file.Payload", typ: reflect.TypeFor[file.Payload](), shared: []string{"TemplateDataErr"}},
	{name: "link.Payload", typ: reflect.TypeFor[link.Payload]()},
	{name: "link.IfExistsPayload", typ: reflect.TypeFor[link.IfExistsPayload]()},
	{name: "pkg.Payload", typ: reflect.TypeFor[pkg.Payload]()},
	{name: "service.Payload", typ: reflect.TypeFor[service.Payload]()},
	{name: "systemdtimer.Payload", typ: reflect.TypeFor[systemdtimer.Payload]()},
	{name: "user.Payload", typ: reflect.TypeFor[user.Payload]()},
}

// errorType is the interface type fillValue recognizes: the only
// interface-kind field any payload holds today is file.Payload's
// TemplateDataErr.
var errorType = reflect.TypeFor[error]()

// unexportedReferenceField reports the name and type of typ's first
// unexported field whose kind is Slice, Map or Pointer, or ok=false when it
// has none. It looks at typ's own fields only, one level: fillValue's
// reflect.Struct case already recurses into every EXPORTED struct field, so
// a nested exported struct's unexported reference-typed field is still
// caught -- fillValue's recursive call reaches that nested struct's own
// reflect.Struct case, which runs this same check again there.
//
// It is a plain, *testing.T-free function (not folded directly into
// fillValue's t.Fatalf call) precisely so
// TestFillValueCatchesUnexportedReferenceTypedField can assert its verdict
// directly: a subtest that deliberately triggers a real t.Fatalf always
// marks its parent test (and so the whole `go test ./...` run) failed too,
// which would make a "prove this fails loudly" test itself break the gate
// suite it is supposed to keep green (draft_clone_test.go's unpopulatedRefs
// is the same kind of extracted, directly-assertable predicate, for the
// same reason).
func unexportedReferenceField(typ reflect.Type) (name string, fieldType reflect.Type, ok bool) {
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.IsExported() {
			continue
		}
		switch field.Type.Kind() {
		case reflect.Slice, reflect.Map, reflect.Pointer:
			return field.Name, field.Type, true
		}
	}
	return "", nil, false
}

// fillValue reflectively sets every string, bool, int, pointer, error and
// slice reachable from v (which must be addressable) to a non-nil,
// non-zero value, recursing into nested structs (resource.PlanGuardDraft,
// resource.PlanConfigMember, resource.PlanArgv, resource.KeyedLine, ...)
// and slice elements. Unlike a hand-maintained fullPayload() fixture, this
// reaches any EXPORTED field a payload type gains later automatically,
// which is the point: task 1e2's mutation probe found that a new uncopied
// slice field left go build, go vet, staticcheck and go test ./... all
// green, because nothing populated it. A field kind this cannot handle
// fails the test loudly (via t.Fatalf) rather than silently leaving a
// blind spot.
//
// Limitation (task re2, finding b): this package is resource_test, an
// external test package, so it cannot Set() an unexported field's Value
// through plain reflect (that panics) without resorting to unsafe tricks
// this guard deliberately does not use. An unexported SCALAR field (string,
// bool, int, ...) carries no aliasing risk for Clone, so leaving it at its
// zero value is harmless. An unexported slice/map/pointer field is exactly
// task 882's original aliasing-bug shape, though: a payload's own
// constructor could populate one internally without ever exposing it to
// this fixture, and a Clone that forgot to deep-copy it would go
// undetected here. No payload type declares one today (confirmed by
// inspection across every case in payloadCases), so this is a currently
// latent gap rather than an active blind spot — but rather than merely
// documenting that limitation, this case FAILS LOUDLY the moment any
// payload gains such a field, so the gap cannot silently reopen: extend
// this helper (or the payload's own hand-written Clone test) before
// trusting the guard again.
func fillValue(t *testing.T, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		v.SetUint(1)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillValue(t, v.Elem())
	case reflect.Interface:
		if v.Type() != errorType {
			t.Fatalf("fillValue: %s is an unhandled interface type", v.Type())
		}
		v.Set(reflect.ValueOf(errors.New("x")))
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			v.SetBytes([]byte("x"))
			return
		}
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillValue(t, v.Index(0))
	case reflect.Struct:
		if name, fieldType, found := unexportedReferenceField(v.Type()); found {
			t.Fatalf("fillValue: %s has unexported field %q of reference type %s, which this guard cannot populate or verify Clone copies — this reproduces task 882's original aliasing-bug shape (an internally populated field Clone forgets to deep-copy); extend fillValue (see its doc comment) or add a dedicated Clone test for this field before trusting this payload's Clone-coverage guard", v.Type(), name, fieldType)
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fillValue(t, v.Field(i))
			}
			// An unexported scalar field carries no aliasing risk for
			// Clone (copying it by value, which every Payload's Clone
			// does via a struct copy, is already correct), so it is left
			// at its zero value.
		}
	default:
		t.Fatalf("fillValue: %s has unhandled kind %s", v.Type(), v.Kind())
	}
}

// fillEmpty reflectively sets every slice reachable from v (addressable) to
// its non-nil, zero-length form and every pointer to a non-nil pointee
// (recursively emptied too), leaving scalars and error fields at their
// zero value. It builds the "empty but set" fixture
// TestPayloadClonePreservesNilAndEmpty needs: Clone must keep a non-nil
// empty slice non-nil and empty, not collapse it to nil.
func fillEmpty(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillEmpty(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 0, 0))
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fillEmpty(v.Field(i))
			}
		}
	}
}

// newPayload builds a fresh, addressable *typ and fills it with fill, then
// returns it boxed as a resource.DraftPayload.
func newPayload(t *testing.T, typ reflect.Type, fill func(reflect.Value)) resource.DraftPayload {
	t.Helper()
	p := reflect.New(typ)
	fill(p.Elem())
	return p.Elem().Interface().(resource.DraftPayload)
}

// scribblePayload mutates payload in place (through a fresh addressable
// copy sharing its backing storage) via testutil.Scribble.
func scribblePayload(typ reflect.Type, payload resource.DraftPayload) {
	p := reflect.New(typ)
	p.Elem().Set(reflect.ValueOf(payload))
	testutil.Scribble(p.Interface())
}

// TestPayloadCloneContract is task 1e2's restored Clone-coverage guard: for
// every resource.DraftPayload implementation in payloadCases, unpopulatedRefs
// (the same 882-era helper TestFullDraftCoversEveryReferenceField uses in
// draft_clone_test.go) confirms fillValue actually reached every reference
// field, then a fully populated value must clone equal to its source, share
// no mutable storage with it beyond the case's declared exceptions
// (testutil.SharedRefs), and stay unaffected by a mutation
// (testutil.Scribble) on either side afterwards. Because fillValue reaches
// every field reflectively instead of through a hand-maintained fixture, a
// NEW slice/map/pointer field on any payload that Clone forgets to copy
// fails here immediately — this is the guard task 882 originally added at
// the resource.PlanDraft level and w62 Layer 1 lost when the kind-exclusive
// fields moved into these per-kind payloads (see the task's mutation
// probe: adding an uncopied `Extra []string` to cron.Payload without
// copying it in Clone left go build, go vet, staticcheck and go test
// ./... all green before this test existed).
func TestPayloadCloneContract(t *testing.T) {
	for _, tc := range payloadCases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPayload(t, tc.typ, func(v reflect.Value) { fillValue(t, v) })
			if missing := unpopulatedRefs(reflect.ValueOf(p), tc.name); len(missing) > 0 {
				t.Fatalf("fillValue leaves %v nil or empty; extend fillValue in resource/payload_clone_test.go so this payload's Clone-coverage guard actually exercises it", missing)
			}
			c := p.Clone()
			if !reflect.DeepEqual(c, p) {
				t.Fatalf("clone differs from source:\n got %#v\nwant %#v", c, p)
			}
			if shared := testutil.SharedRefs(p, c); !slices.Equal(shared, tc.shared) {
				t.Fatalf("clone shares %v with its source, want only %v", shared, tc.shared)
			}

			want := newPayload(t, tc.typ, func(v reflect.Value) { fillValue(t, v) })

			// Mutating the clone must leave the source unchanged.
			scribblePayload(tc.typ, c)
			if !reflect.DeepEqual(p, want) {
				t.Fatalf("mutating the clone changed the source: %#v", p)
			}

			// Mutating the source must leave a clone taken beforehand unchanged.
			c = p.Clone()
			scribblePayload(tc.typ, p)
			if !reflect.DeepEqual(c, want) {
				t.Fatalf("mutating the source changed the clone: %#v", c)
			}
		})
	}
}

// TestPayloadClonePreservesNilAndEmpty generalizes each kind's own former
// TestPayloadClonePreservesNilAndEmpty: for every case in payloadCases, the
// zero value clones to the zero value (nil stays nil), and a value whose
// reference fields are set to their non-nil, zero-length form clones to
// the byte-identical value (empty stays empty, not nil), so a cloned
// payload lowers to the byte-identical op either way.
func TestPayloadClonePreservesNilAndEmpty(t *testing.T) {
	for _, tc := range payloadCases {
		t.Run(tc.name, func(t *testing.T) {
			zero := reflect.New(tc.typ).Elem().Interface().(resource.DraftPayload)
			if got := zero.Clone(); !reflect.DeepEqual(got, zero) {
				t.Fatalf("zero payload clone = %#v, want the zero payload", got)
			}

			empty := newPayload(t, tc.typ, fillEmpty)
			if got := empty.Clone(); !reflect.DeepEqual(got, empty) {
				t.Fatalf("empty payload clone = %#v, want %#v", got, empty)
			}
		})
	}
}

// payloadStubWithUnexportedSlice is a throwaway resource.DraftPayload used
// only to reproduce, in isolation, the exact latent shape task re2 finding
// (b) is about: a payload with an unexported, reference-typed field
// (hidden) that its own constructor could populate internally without ever
// exposing it to fillValue -- exactly task 882's original aliasing-bug
// shape, had Clone forgotten to deep-copy it. It is never added to
// payloadCases (no real payload type declares such a field today, per that
// finding); it exists purely so
// TestFillValueCatchesUnexportedReferenceTypedField can hand
// unexportedReferenceField a concrete type that must trip it.
type payloadStubWithUnexportedSlice struct {
	Exported string
	hidden   []string
}

// Clone deep-copies hidden correctly -- this stub is never run through
// TestPayloadCloneContract, so whether Clone gets hidden right or wrong is
// beside the point here; only unexportedReferenceField's ability to spot
// the field's shape is under test (TestFillValueCatchesUnexportedReferenceTypedField).
// hidden is read here (staticcheck's U1000 would otherwise flag a field
// this stub never uses for anything but its reflect.Type shape).
func (p payloadStubWithUnexportedSlice) Clone() resource.DraftPayload {
	c := p
	c.hidden = slices.Clone(p.hidden)
	return c
}

// TestFillValueCatchesUnexportedReferenceTypedField proves finding (b)'s
// fix: a payload that declares an unexported slice/map/pointer field is now
// CAUGHT rather than silently skipped. It asserts unexportedReferenceField
// directly (see that function's doc comment for why: fillValue's own
// t.Fatalf, exercised through a live *testing.T, cannot be asserted without
// marking this very test -- and so `go test ./...` -- failed, which would
// defeat the point of a passing regression test). unexportedReferenceField
// is exactly what fillValue's reflect.Struct case calls before it does
// anything else, so a positive verdict here is a positive verdict for
// fillValue's real t.Fatalf path against the exact same type.
func TestFillValueCatchesUnexportedReferenceTypedField(t *testing.T) {
	typ := reflect.TypeFor[payloadStubWithUnexportedSlice]()
	name, fieldType, ok := unexportedReferenceField(typ)
	if !ok {
		t.Fatalf("unexportedReferenceField(%s) = false, want it to catch the unexported %q slice field -- the Clone-coverage guard's blind spot (task re2, finding b) has reopened", typ, "hidden")
	}
	if name != "hidden" {
		t.Fatalf("unexportedReferenceField(%s) name = %q, want %q", typ, name, "hidden")
	}
	if fieldType != reflect.TypeFor[[]string]() {
		t.Fatalf("unexportedReferenceField(%s) fieldType = %s, want []string", typ, fieldType)
	}

	// The negative case: every real payload in payloadCases has none today
	// (finding (b) confirmed this by inspection), so the guard must stay
	// silent for a struct with no unexported reference-typed field --
	// otherwise TestPayloadCloneContract would fail loudly on every real
	// payload, not just a future one that actually grows such a field.
	for _, tc := range payloadCases {
		if _, _, found := unexportedReferenceField(tc.typ); found {
			t.Errorf("unexportedReferenceField(%s) unexpectedly found an unexported reference-typed field; TestPayloadCloneContract's fillValue call would already be failing loudly for it", tc.typ)
		}
	}
}
