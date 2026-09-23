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
// resource.PlanDraft into its own per-kind payload type; see docs/plan.md,
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
// needs a kindFitnessTable entry (docs/plan.md, "Draft payload" step).
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

// fillValue reflectively sets every string, bool, int, pointer, error and
// slice reachable from v (which must be addressable) to a non-nil,
// non-zero value, recursing into nested structs (resource.PlanGuardDraft,
// resource.PlanConfigMember, resource.PlanArgv, resource.KeyedLine, ...)
// and slice elements. Unlike a hand-maintained fullPayload() fixture, this
// reaches any field a payload type gains later automatically, which is the
// point: task 1e2's mutation probe found that a new uncopied slice field
// left go build, go vet, staticcheck and go test ./... all green, because
// nothing populated it. A field kind this cannot handle fails the test
// loudly (via t.Fatalf) rather than silently leaving a blind spot.
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
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fillValue(t, v.Field(i))
			}
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
