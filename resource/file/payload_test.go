package file

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/resource"
)

// fullPayload returns a Payload with every reference field populated, so
// TestPayloadCloneSharesNothing exercises the same deep-copy contract
// resource.PlanDraft.Clone used to pin directly (before TemplateData and
// friends moved here, task w62 Layer 1): a clone is equal to its source and
// shares no mutable storage except the immutable TemplateDataErr value.
func fullPayload() Payload {
	return Payload{
		ContentB64:      "Zml0",
		SourcePath:      "/src",
		HasContent:      true,
		Template:        true,
		TemplateParam:   "/src",
		TemplateData:    json.RawMessage(`{"k":["v"]}`),
		TemplateDataErr: errors.New("immutable"),
		TemplateDataSet: true,
		ValidationBin:   "check",
		ValidationArgs:  []string{"-c", "{{candidate}}"},
		AddLines:        []string{"add"},
		RemoveLines:     []string{"remove"},
		KeyedLines:      []resource.KeyedLine{{Key: "k=", Line: "k=v"}},
	}
}

// onlyErr is what a clone may share with its source: the immutable
// TemplateDataErr error value (see Payload.Clone).
var onlyErr = []string{"TemplateDataErr"}

// TestPayloadCloneSharesNothing pins that Payload.Clone deep-copies every
// slice (TemplateData, ValidationArgs, AddLines, RemoveLines, KeyedLines):
// a clone is equal to its source and shares no mutable storage beyond the
// immutable TemplateDataErr, and mutating either side afterwards leaves the
// other exactly as it was.
func TestPayloadCloneSharesNothing(t *testing.T) {
	p := fullPayload()
	c := p.Clone()
	if !reflect.DeepEqual(c, resource.DraftPayload(p)) {
		t.Fatalf("clone differs from source:\n got %#v\nwant %#v", c, p)
	}
	if shared := testutil.SharedRefs(p, c); !reflect.DeepEqual(shared, onlyErr) {
		t.Fatalf("clone shares %v with its source, want only %v", shared, onlyErr)
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

// TestPayloadClonePreservesNilAndEmpty pins that Clone keeps nil slices nil
// and empty-but-non-nil ones empty-but-set, so a cloned payload lowers to
// the byte-identical op.
func TestPayloadClonePreservesNilAndEmpty(t *testing.T) {
	if got := (Payload{}).Clone(); !reflect.DeepEqual(got, resource.DraftPayload(Payload{})) {
		t.Fatalf("zero payload clone = %#v, want the zero payload", got)
	}
	empty := Payload{
		TemplateData:   json.RawMessage{},
		ValidationArgs: []string{},
		AddLines:       []string{},
		RemoveLines:    []string{},
		KeyedLines:     []resource.KeyedLine{},
	}
	got := empty.Clone().(Payload)
	if !reflect.DeepEqual(got, empty) {
		t.Fatalf("empty payload clone = %#v, want %#v", got, empty)
	}
	if got.TemplateData == nil || got.ValidationArgs == nil || got.AddLines == nil ||
		got.RemoveLines == nil || got.KeyedLines == nil {
		t.Fatalf("clone turned an empty field into nil: %#v", got)
	}
}

// TestPayloadSourceFilePath pins that SourceFilePath returns exactly
// SourcePath, and that Payload satisfies resource.SourceFilePayload
// (checked by the assignment below), since api/packager.go and
// internal/testapply find it only through that kind-neutral interface,
// never by importing this package.
func TestPayloadSourceFilePath(t *testing.T) {
	var _ resource.SourceFilePayload = Payload{}
	p := Payload{SourcePath: "/src/x.conf"}
	if got := p.SourceFilePath(); got != "/src/x.conf" {
		t.Fatalf("SourceFilePath() = %q, want /src/x.conf", got)
	}
}
