package user

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/resource"
)

// fullPayload returns a Payload with every reference field populated
// (SupplementaryGroups), so TestPayloadCloneSharesNothing exercises the
// same deep-copy contract resource.PlanDraft.Clone pins generically at the
// resource package level for a payload with only a plain slice — this test
// proves user.Payload's own SupplementaryGroups slice is copied too, since
// resource.PlanDraft.Clone only ever calls Payload.Clone() generically.
func fullPayload() Payload {
	return Payload{
		PrimaryGroup:        "svc",
		SupplementaryGroups: []string{"wheel", "audio"},
		Home:                "/var/lib/svc",
		CreateHome:          true,
		Shell:               "/sbin/nologin",
		LoginClass:          "daemon",
		System:              true,
		ManageHome:          true,
	}
}

// TestPayloadCloneSharesNothing pins that Payload.Clone deep-copies
// SupplementaryGroups: a clone is equal to its source and shares no
// mutable storage, and mutating either side afterwards leaves the other
// exactly as it was.
func TestPayloadCloneSharesNothing(t *testing.T) {
	p := fullPayload()
	c := p.Clone()
	if !reflect.DeepEqual(c, resource.DraftPayload(p)) {
		t.Fatalf("clone differs from source:\n got %#v\nwant %#v", c, p)
	}
	if shared := testutil.SharedRefs(p, c); len(shared) != 0 {
		t.Fatalf("clone shares %v with its source", shared)
	}

	want := fullPayload()
	cc := c.(Payload)
	testutil.Scribble(&cc)
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("mutating the clone changed the source: %#v", p)
	}

	c = p.Clone()
	testutil.Scribble(&p)
	cc = c.(Payload)
	if !reflect.DeepEqual(cc, want) {
		t.Fatalf("mutating the source changed the clone: %#v", cc)
	}
}

// TestPayloadClonePreservesNilAndEmpty pins that Clone keeps a nil
// SupplementaryGroups nil, and an empty-but-non-nil one empty-but-set.
func TestPayloadClonePreservesNilAndEmpty(t *testing.T) {
	if got := (Payload{}).Clone(); !reflect.DeepEqual(got, resource.DraftPayload(Payload{})) {
		t.Fatalf("zero payload clone = %#v, want the zero payload", got)
	}
	empty := Payload{SupplementaryGroups: []string{}}
	got := empty.Clone().(Payload)
	if !reflect.DeepEqual(got, empty) {
		t.Fatalf("empty payload clone = %#v, want %#v", got, empty)
	}
	if got.SupplementaryGroups == nil {
		t.Fatalf("clone turned an empty field into nil: %#v", got)
	}
}
