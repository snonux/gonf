package cron

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/resource"
)

// fullPayload returns a Payload with every reference field populated
// (CronEnv), so TestPayloadCloneSharesNothing exercises the same deep-copy
// contract resource.PlanDraft.Clone pins generically at the resource
// package level for a payload with only a plain slice — this test proves
// cron.Payload's own CronEnv slice is copied too, since
// resource.PlanDraft.Clone only ever calls Payload.Clone() generically and
// cannot see into a concrete payload type.
func fullPayload() Payload {
	return Payload{
		CronUser:      "root",
		LegacyCommand: "/usr/local/bin/old.sh",
		Schedule:      "0 2 * * *",
		CronEnv:       []string{"PATH=/usr/bin"},
	}
}

// TestPayloadCloneSharesNothing pins that Payload.Clone deep-copies
// CronEnv: a clone is equal to its source and shares no mutable storage,
// and mutating either side afterwards leaves the other exactly as it was.
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

// TestPayloadClonePreservesNilAndEmpty pins that Clone keeps a nil CronEnv
// nil, and an empty-but-non-nil one empty-but-set.
func TestPayloadClonePreservesNilAndEmpty(t *testing.T) {
	if got := (Payload{}).Clone(); !reflect.DeepEqual(got, resource.DraftPayload(Payload{})) {
		t.Fatalf("zero payload clone = %#v, want the zero payload", got)
	}
	empty := Payload{CronEnv: []string{}}
	got := empty.Clone().(Payload)
	if !reflect.DeepEqual(got, empty) {
		t.Fatalf("empty payload clone = %#v, want %#v", got, empty)
	}
	if got.CronEnv == nil {
		t.Fatalf("clone turned an empty field into nil: %#v", got)
	}
}
