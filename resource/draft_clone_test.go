package resource_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/resource"
)

// fullDraft returns a draft whose every slice, map and pointer field is
// populated (TestFullDraftCoversEveryReferenceField keeps it that way when
// PlanDraft grows), so the copy-contract tests below exercise each shared
// field kind: string slices, the Env map, the guard pointers with their
// Args and *int, and the nested config-member bytes and validator argv.
func fullDraft(id string) resource.PlanDraft {
	exit := 3
	return resource.PlanDraft{
		Kind:                "command",
		ID:                  id,
		Name:                "full",
		TemplateData:        []string{"opaque"},
		TemplateDataSet:     true,
		ValidationArgs:      []string{"-c", "{{candidate}}"},
		SupplementaryGroups: []string{"wheel"},
		AddLines:            []string{"add"},
		RemoveLines:         []string{"remove"},
		Args:                []string{"arg"},
		Env:                 map[string]string{"K": "v"},
		Unless:              &resource.PlanGuardDraft{Bin: "test", Args: []string{"-e", "/x"}, ExpectExit: &exit},
		OnlyIf:              &resource.PlanGuardDraft{Bin: "test", Args: []string{"-d", "/y"}},
		CronEnv:             []string{"A=1"},
		After:               []string{"network.target"},
		Wants:               []string{"network.target"},
		Watch:               []string{"File[/w]"},
		ConfigMembers:       []resource.PlanConfigMember{{Key: "k", Path: "/k", Content: []byte("body")}},
		Validators:          []resource.PlanArgv{{Bin: "check", Args: []string{"-f"}}},
		Deps:                []string{"Package[dep]"},
	}
}

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

// TestFullDraftCoversEveryReferenceField fails when PlanDraft gains a
// slice/map/pointer field that fullDraft leaves nil: Clone must then be
// taught to copy it, and the tests below must exercise it.
func TestFullDraftCoversEveryReferenceField(t *testing.T) {
	v := reflect.ValueOf(fullDraft("X[x]"))
	for _, name := range referenceFields() {
		if v.FieldByName(name).IsNil() {
			t.Errorf("fullDraft leaves reference field %s nil; populate it and make sure PlanDraft.Clone copies it", name)
		}
	}
}

// TestPlanDraftCloneSharesNothing pins the draft half of the copy
// contract: a clone is equal to its source but shares no mutable storage,
// except the documented opaque TemplateData.
func TestPlanDraftCloneSharesNothing(t *testing.T) {
	d := fullDraft("X[x]")
	c := d.Clone()
	if !reflect.DeepEqual(c, d) {
		t.Fatalf("clone differs from source:\n got %#v\nwant %#v", c, d)
	}
	if got, want := testutil.SharedRefs(d, c), []string{"TemplateData"}; !slices.Equal(got, want) {
		t.Fatalf("clone shares %v with its source, want only %v", got, want)
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
// would prove nothing).
func TestSharedRefsDetectsShallowCopy(t *testing.T) {
	d := fullDraft("X[x]")
	shallow := d
	shared := testutil.SharedRefs(d, shallow)
	for _, name := range referenceFields() {
		if !slices.Contains(shared, name) {
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
		Args: []string{}, Env: map[string]string{}, Deps: []string{},
		Unless:        &resource.PlanGuardDraft{Args: []string{}},
		ConfigMembers: []resource.PlanConfigMember{{Content: []byte{}}},
		Validators:    []resource.PlanArgv{},
	}
	c := empty.Clone()
	if !reflect.DeepEqual(c, empty) {
		t.Fatalf("empty draft clone = %#v, want %#v", c, empty)
	}
	if c.Args == nil || c.Env == nil || c.Deps == nil || c.Unless.Args == nil ||
		c.ConfigMembers[0].Content == nil || c.Validators == nil {
		t.Fatalf("clone turned an empty field into nil: %#v", c)
	}
}

// TestRecordPlanDraftIsolatesCallerStoreAndRecorder pins that the caller's
// draft, the stored draft (RegisteredPlanDrafts) and the recorder's draft
// are three independent copies, and that each snapshot is its own copy too.
func TestRecordPlanDraftIsolatesCallerStoreAndRecorder(t *testing.T) {
	resource.ResetRepository()
	t.Cleanup(func() { resource.ResetRepository(); resource.SetPlanDraftRecorder(nil) })
	res := resource.Register("Command", "full", resource.ApplierFunc(func() error { return nil }))
	var recorded []resource.PlanDraft
	resource.SetPlanDraftRecorder(func(d resource.PlanDraft) { recorded = append(recorded, d) })

	d := fullDraft(res.ID())
	want := fullDraft(res.ID())
	resource.RecordPlanDraft(d)
	if len(recorded) != 1 {
		t.Fatalf("recorder saw %d drafts, want 1", len(recorded))
	}
	if shared := testutil.SharedRefs(d, recorded[0]); !slices.Equal(shared, []string{"TemplateData"}) {
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
	res := resource.Register("Command", "full", resource.ApplierFunc(func() error { return nil }))
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
