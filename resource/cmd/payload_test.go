package cmd

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/resource"
)

// fullPayload returns a Payload with every reference field populated
// (Args, and the Unless/OnlyIf guards with their own Args and ExpectExit),
// so TestPayloadCloneSharesNothing exercises the same deep-copy contract
// resource.PlanDraft.Clone pins generically at the resource package level
// (draft_clone_test.go) for a payload with only a plain slice — this test
// is what actually proves cmd.Payload's pointer-bearing fields (Unless,
// OnlyIf) are copied too, since resource.PlanDraft.Clone only ever calls
// Payload.Clone() generically and cannot see into a concrete payload type.
func fullPayload() Payload {
	exit := 3
	return Payload{
		Bin:     "true",
		Args:    []string{"arg"},
		Dir:     "/tmp",
		Creates: "/tmp/marker",
		Unless: &resource.PlanGuardDraft{
			Bin: "test", Args: []string{"-e", "/x"}, ExpectExit: &exit,
		},
		OnlyIf: &resource.PlanGuardDraft{
			Bin: "test", Args: []string{"-d", "/y"}, ExpectExit: &exit,
		},
	}
}

// TestPayloadCloneSharesNothing pins that Payload.Clone deep-copies Args and
// the Unless/OnlyIf guards (each guard's own Args and ExpectExit included):
// a clone is equal to its source and shares no mutable storage, and
// mutating either side afterwards leaves the other exactly as it was.
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

// TestPayloadClonePreservesNilAndEmpty pins that Clone keeps a nil Args (or
// nil guards) nil, and an empty-but-non-nil Args empty-but-set, so a cloned
// payload lowers to the byte-identical op.
func TestPayloadClonePreservesNilAndEmpty(t *testing.T) {
	if got := (Payload{}).Clone(); !reflect.DeepEqual(got, resource.DraftPayload(Payload{})) {
		t.Fatalf("zero payload clone = %#v, want the zero payload", got)
	}
	empty := Payload{Args: []string{}, Unless: &resource.PlanGuardDraft{Args: []string{}}}
	got := empty.Clone().(Payload)
	if !reflect.DeepEqual(got, empty) {
		t.Fatalf("empty payload clone = %#v, want %#v", got, empty)
	}
	if got.Args == nil || got.Unless.Args == nil {
		t.Fatalf("clone turned an empty field into nil: %#v", got)
	}
}
