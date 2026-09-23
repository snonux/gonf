package configset

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/resource"
)

// fullSetPayload returns a SetPayload with every reference field populated
// (ConfigMembers with Content, Validators with Args), so
// TestSetPayloadCloneSharesNothing exercises the same deep-copy contract
// resource.PlanDraft.Clone pins generically at the resource package level
// (draft_clone_test.go) for a payload with only a plain slice — this test
// proves configset.SetPayload's own nested-slice fields are copied too,
// since resource.PlanDraft.Clone only ever calls Payload.Clone()
// generically and cannot see into a concrete payload type.
func fullSetPayload() SetPayload {
	return SetPayload{
		ConfigMembers: []resource.PlanConfigMember{{Key: "k", Path: "/k", Content: []byte("body")}},
		Validators:    []resource.PlanArgv{{Bin: "check", Args: []string{"-f"}}},
		Chroot:        "/chroot",
		StagingDir:    "/staging",
	}
}

// TestSetPayloadCloneSharesNothing pins that SetPayload.Clone deep-copies
// ConfigMembers (each member's Content) and Validators (each one's Args): a
// clone is equal to its source and shares no mutable storage, and mutating
// either side afterwards leaves the other exactly as it was.
func TestSetPayloadCloneSharesNothing(t *testing.T) {
	p := fullSetPayload()
	c := p.Clone()
	if !reflect.DeepEqual(c, resource.DraftPayload(p)) {
		t.Fatalf("clone differs from source:\n got %#v\nwant %#v", c, p)
	}
	if shared := testutil.SharedRefs(p, c); len(shared) != 0 {
		t.Fatalf("clone shares %v with its source", shared)
	}

	want := fullSetPayload()
	cc := c.(SetPayload)
	testutil.Scribble(&cc)
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("mutating the clone changed the source: %#v", p)
	}

	c = p.Clone()
	testutil.Scribble(&p)
	cc = c.(SetPayload)
	if !reflect.DeepEqual(cc, want) {
		t.Fatalf("mutating the source changed the clone: %#v", cc)
	}
}

// TestSetPayloadClonePreservesNilAndEmpty pins that Clone keeps nil
// ConfigMembers/Validators nil, and an empty-but-non-nil one empty-but-set.
func TestSetPayloadClonePreservesNilAndEmpty(t *testing.T) {
	if got := (SetPayload{}).Clone(); !reflect.DeepEqual(got, resource.DraftPayload(SetPayload{})) {
		t.Fatalf("zero payload clone = %#v, want the zero payload", got)
	}
	empty := SetPayload{
		ConfigMembers: []resource.PlanConfigMember{{Content: []byte{}}},
		Validators:    []resource.PlanArgv{},
	}
	got := empty.Clone().(SetPayload)
	if !reflect.DeepEqual(got, empty) {
		t.Fatalf("empty payload clone = %#v, want %#v", got, empty)
	}
	if got.ConfigMembers[0].Content == nil || got.Validators == nil {
		t.Fatalf("clone turned an empty field into nil: %#v", got)
	}
}

// TestMemberPayloadClone pins MemberPayload's Clone: it has no reference
// fields, so a clone must equal its source with no shared-storage check
// needed (a plain value has nothing to alias).
func TestMemberPayloadClone(t *testing.T) {
	p := MemberPayload{Member: "keys"}
	if got := p.Clone(); !reflect.DeepEqual(got, resource.DraftPayload(p)) {
		t.Fatalf("clone = %#v, want %#v", got, p)
	}
}
