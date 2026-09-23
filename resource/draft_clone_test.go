package resource_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/resource"
)

// payloadStub is a resource.DraftPayload used only to exercise PlanDraft's
// generic Payload handling (Clone, the reference-field walk) independently
// of any real kind's payload type (a real one, e.g. resource/cron.Payload,
// lives in its own resource/<kind> package — see task w62 Layer 1). Members
// mirrors the "slice of struct with its own nested slice" shape a real
// payload can hold (e.g. configset.SetPayload's ConfigMembers), so the
// generic walk/clone contract is proven at that depth too, not just a flat
// string slice.
type payloadStub struct {
	Tag     string
	List    []string
	Members []payloadStubMember
}

// payloadStubMember is payloadStub's nested reference-bearing element.
type payloadStubMember struct {
	Key     string
	Content []byte
}

// Clone deep-copies the stub, giving List and each Members' Content their
// own backing storage.
func (p payloadStub) Clone() resource.DraftPayload {
	c := p
	c.List = slices.Clone(p.List)
	c.Members = clonePayloadStubMembers(p.Members)
	return c
}

// clonePayloadStubMembers deep-copies each member's Content (nil stays nil,
// empty stays empty), mirroring resource/configset's cloneConfigMembers.
func clonePayloadStubMembers(members []payloadStubMember) []payloadStubMember {
	if members == nil {
		return nil
	}
	c := make([]payloadStubMember, len(members))
	for i, m := range members {
		m.Content = slices.Clone(m.Content)
		c[i] = m
	}
	return c
}

// fullDraft returns a draft whose every slice, map, pointer and interface
// field, at any depth, is populated (TestFullDraftCoversEveryReferenceField
// keeps it that way when PlanDraft or a struct it nests grows), so the
// copy-contract tests below exercise each shared field kind: string
// slices, the Env map, and a populated Payload, including its own nested
// member-slice shape (payloadStub.Members) — a kind's own pointer-bearing
// fields, e.g. cmd.Payload's Unless/OnlyIf guards or
// configset.SetPayload's ConfigMembers/Validators, are covered by that
// kind's own package tests instead (see resource/cmd/payload_test.go,
// resource/configset/payload_test.go, resource/file/payload_test.go).
func fullDraft(id string) resource.PlanDraft {
	return resource.PlanDraft{
		Kind: "command",
		ID:   id,
		Name: "full",
		Payload: payloadStub{
			Tag:     "full",
			List:    []string{"item"},
			Members: []payloadStubMember{{Key: "k", Content: []byte("body")}},
		},
		Env:   map[string]string{"K": "v"},
		Watch: []string{"File[/w]"},
		Deps:  []string{"Package[dep]"},
	}
}

// noneShared is what a clone may share with its source at the
// resource.PlanDraft level: nothing. A migrated kind's own Payload may
// still deliberately share one immutable reference (e.g.
// file.Payload.Clone() keeps TemplateDataErr, mirroring what PlanDraft.Clone
// itself used to document before TemplateData/TemplateDataErr moved into
// file.Payload — task w62 Layer 1); that is payloadStub's and file's own
// concern to pin (resource/file/payload_test.go), not this generic test's.
var noneShared []string

// referenceFields lists PlanDraft's slice, map, pointer and interface
// fields: the ones a shallow copy would share.
func referenceFields() []string {
	var names []string
	typ := reflect.TypeFor[resource.PlanDraft]()
	for i := range typ.NumField() {
		switch typ.Field(i).Type.Kind() {
		case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface:
			names = append(names, typ.Field(i).Name)
		}
	}
	return names
}

// TestFullDraftCoversEveryReferenceField fails when PlanDraft, or a struct
// it nests (PlanGuardDraft, PlanConfigMember, PlanArgv, or a future one),
// gains a slice/map/pointer/interface field that fullDraft leaves nil or
// empty: Clone must then be taught to copy it, and the tests below must
// exercise it. Slices and maps must be non-empty so their element types
// are walked too.
func TestFullDraftCoversEveryReferenceField(t *testing.T) {
	for _, path := range unpopulatedRefs(reflect.ValueOf(fullDraft("X[x]")), "") {
		t.Errorf("fullDraft leaves reference field %s nil or empty; populate it and make sure PlanDraft.Clone copies it", path)
	}
}

// unpopulatedRefs returns the paths below v of nil or empty reference
// values, recursing through structs, pointers, interfaces, slice/array
// elements and map values.
func unpopulatedRefs(v reflect.Value, path string) []string {
	switch v.Kind() {
	case reflect.Struct:
		var missing []string
		for i := range v.NumField() {
			missing = append(missing, unpopulatedRefs(v.Field(i), joinField(path, v.Type().Field(i).Name))...)
		}
		return missing
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return []string{path}
		}
		return unpopulatedRefs(v.Elem(), path)
	case reflect.Map:
		if v.Len() == 0 {
			return []string{path}
		}
		return unpopulatedRefs(v.MapIndex(v.MapKeys()[0]), path+"[]")
	case reflect.Slice:
		if v.Len() == 0 {
			return []string{path}
		}
		return unpopulatedRefs(v.Index(0), path+"[]")
	case reflect.Array:
		if v.Len() == 0 {
			return nil
		}
		return unpopulatedRefs(v.Index(0), path+"[]")
	}
	return nil
}

// joinField appends a field name to a dotted path.
func joinField(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// TestUnpopulatedRefsFindsNestedGaps is the guard's negative case: a nil or
// empty field nested inside an interface-held payload's own nested slice is
// reported by its full path.
func TestUnpopulatedRefsFindsNestedGaps(t *testing.T) {
	d := fullDraft("X[x]")
	p := d.Payload.(payloadStub)
	p.Members[0].Content = nil
	d.Payload = p
	got := unpopulatedRefs(reflect.ValueOf(d), "")
	want := []string{"Payload.Members[].Content"}
	if !slices.Equal(got, want) {
		t.Fatalf("unpopulatedRefs = %v, want %v", got, want)
	}
}

// TestPlanDraftCloneSharesNothing pins the draft half of the copy
// contract: a clone is equal to its source and shares no mutable storage
// at all (nothing on resource.PlanDraft itself is deliberately shared any
// more — the one exception this used to document, TemplateDataErr, moved
// into file.Payload along with TemplateData, task w62 Layer 1, and is
// file's own Clone contract to pin, see resource/file/payload_test.go).
func TestPlanDraftCloneSharesNothing(t *testing.T) {
	d := fullDraft("X[x]")
	c := d.Clone()
	if !reflect.DeepEqual(c, d) {
		t.Fatalf("clone differs from source:\n got %#v\nwant %#v", c, d)
	}
	if shared := testutil.SharedRefs(d, c); !slices.Equal(shared, noneShared) {
		t.Fatalf("clone shares %v with its source, want only %v", shared, noneShared)
	}

	// Mutation in either direction leaves the other side as it was.
	want := fullDraft("X[x]")
	testutil.Scribble(&c)
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("mutating the clone changed the source: %#v", d)
	}
	c = d.Clone()
	testutil.Scribble(&d)
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("mutating the source changed the clone: %#v", c)
	}
}

// TestSharedRefsDetectsShallowCopy is the negative case: a plain struct
// copy shares every reference field, and the detector the contract tests
// rely on must report each of them (otherwise an empty SharedRefs result
// would prove nothing). An Interface-kind field (Payload) never appears as
// its own bare path — refWalker.walk records no memref for the interface
// header itself, only for reference storage nested inside the concrete
// value it holds (reflect.Value has no addressable "pointer" for a generic
// interface the way it does for Pointer/Map/Slice) — so it is satisfied by
// any shared path with that field as a prefix instead of an exact match.
func TestSharedRefsDetectsShallowCopy(t *testing.T) {
	d := fullDraft("X[x]")
	shallow := d
	shared := testutil.SharedRefs(d, shallow)
	for _, name := range referenceFields() {
		if !slices.ContainsFunc(shared, func(path string) bool {
			return path == name || strings.HasPrefix(path, name+".")
		}) {
			t.Errorf("shallow copy shares %s but SharedRefs did not report it (got %v)", name, shared)
		}
	}
}

// TestPlanDraftClonePreservesNilAndEmpty pins that Clone keeps nil fields
// nil and empty fields empty-but-set, so a cloned draft lowers to the
// byte-identical op.
func TestPlanDraftClonePreservesNilAndEmpty(t *testing.T) {
	if got := (resource.PlanDraft{}).Clone(); !reflect.DeepEqual(got, resource.PlanDraft{}) {
		t.Fatalf("zero draft clone = %#v, want the zero draft", got)
	}
	empty := resource.PlanDraft{
		Env:     map[string]string{},
		Deps:    []string{},
		Payload: payloadStub{List: []string{}, Members: []payloadStubMember{{Content: []byte{}}}},
	}
	c := empty.Clone()
	if !reflect.DeepEqual(c, empty) {
		t.Fatalf("empty draft clone = %#v, want %#v", c, empty)
	}
	p, ok := c.Payload.(payloadStub)
	if c.Env == nil || c.Deps == nil || !ok || p.List == nil || p.Members[0].Content == nil {
		t.Fatalf("clone turned an empty field into nil: %#v", c)
	}
}

// TestRecordPlanDraftIsolatesCallerStoreAndRecorder pins that the caller's
// draft, the stored draft (RegisteredPlanDrafts) and the recorder's draft
// are three independent copies, and that each snapshot is its own copy too.
func TestRecordPlanDraftIsolatesCallerStoreAndRecorder(t *testing.T) {
	resource.ResetRepository()
	t.Cleanup(func() { resource.ResetRepository(); resource.SetPlanDraftRecorder(nil) })
	res := resource.Register("Command", "full", func() error { return nil })
	var recorded []resource.PlanDraft
	resource.SetPlanDraftRecorder(func(d resource.PlanDraft) { recorded = append(recorded, d) })

	d := fullDraft(res.ID())
	want := fullDraft(res.ID())
	resource.RecordPlanDraft(d)
	if len(recorded) != 1 {
		t.Fatalf("recorder saw %d drafts, want 1", len(recorded))
	}
	if shared := testutil.SharedRefs(d, recorded[0]); !slices.Equal(shared, noneShared) {
		t.Fatalf("recorder's draft shares %v with the caller's", shared)
	}

	testutil.Scribble(&d)
	requireStored(t, want, "mutating the caller's draft")
	if !reflect.DeepEqual(recorded[0], want) {
		t.Fatalf("mutating the caller's draft changed the recorder's: %#v", recorded[0])
	}

	testutil.Scribble(&recorded[0])
	requireStored(t, want, "mutating the recorder's draft")

	snap := resource.RegisteredPlanDrafts()
	testutil.Scribble(&snap[0])
	requireStored(t, want, "mutating a snapshot")
}

// TestAmendRegisteredIsolatesCallerStoreAndSink is the same contract for
// AmendRegistered and its record-mode sink.
func TestAmendRegisteredIsolatesCallerStoreAndSink(t *testing.T) {
	resource.ResetRepository()
	t.Cleanup(func() { resource.ResetRepository(); resource.SetPlanDraftAmender(nil) })
	res := resource.Register("Command", "full", func() error { return nil })
	var sunk []resource.PlanDraft
	resource.SetPlanDraftAmender(func(d resource.PlanDraft) error { sunk = append(sunk, d); return nil })

	d := fullDraft(res.ID())
	want := fullDraft(res.ID())
	if err := resource.AmendRegistered(d); err != nil {
		t.Fatalf("AmendRegistered: %v", err)
	}
	testutil.Scribble(&d)
	requireStored(t, want, "mutating the caller's amended draft")
	if len(sunk) != 1 || !reflect.DeepEqual(sunk[0], want) {
		t.Fatalf("mutating the caller's draft changed the sink's: %#v", sunk)
	}
	testutil.Scribble(&sunk[0])
	requireStored(t, want, "mutating the sink's draft")
}

// requireStored fails unless the repository holds exactly want.
func requireStored(t *testing.T, want resource.PlanDraft, after string) {
	t.Helper()
	got := resource.RegisteredPlanDrafts()
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("%s changed the stored draft:\n got %#v\nwant %#v", after, got, want)
	}
}
