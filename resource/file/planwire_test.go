package file

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// TestToOpRecordsOnlyLineArrays pins that recording emits the v14+ add_lines /
// remove_lines arrays and never the pre-v14 singular wire fields, which exist
// on plan.Op only so old recorded plans still apply.
func TestToOpRecordsOnlyLineArrays(t *testing.T) {
	op, err := planHandler{}.ToOp(resource.PlanDraft{
		Kind: string(plan.KindFile),
		ID:   "File[/etc/x]",
		Path: "/etc/x",
		Payload: Payload{
			AddLines:    []string{"a", "b"},
			RemoveLines: []string{"c"},
		},
	})
	if err != nil {
		t.Fatalf("ToOp: %v", err)
	}
	line, err := plan.EncodeOp(op)
	if err != nil {
		t.Fatalf("EncodeOp: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"add_line", "remove_line"} {
		if _, ok := fields[legacy]; ok {
			t.Errorf("recorded op carries legacy %q: %s", legacy, line)
		}
	}
	if string(fields["add_lines"]) != `["a","b"]` || string(fields["remove_lines"]) != `["c"]` {
		t.Errorf("recorded line arrays = %s", line)
	}
}

// TestPlanLinesSkipsUnsetLegacyFields pins the guarded merge: an unset
// singular field must not contribute an empty line, so a v14+ op without
// removals really has no removals.
func TestPlanLinesSkipsUnsetLegacyFields(t *testing.T) {
	add, remove := planLines(plan.Op{AddLines: []string{"a"}})
	if !slices.Equal(add, []string{"a"}) || len(remove) != 0 {
		t.Fatalf("planLines = %q, %q; want [a], []", add, remove)
	}
	add, remove = planLines(plan.Op{AddLines: []string{"a"}, AddLine: "b", RemoveLine: "c"})
	if !slices.Equal(add, []string{"a", "b"}) || !slices.Equal(remove, []string{"c"}) {
		t.Fatalf("planLines legacy = %q, %q; want [a b], [c]", add, remove)
	}
}

// TestApplyDecodedPreV14LineOp decodes a pre-v14 JSONL file op that uses the
// singular add_line/remove_line fields and applies it through the file
// handler, proving old recorded plans still work after the draft fields went.
func TestApplyDecodedPreV14LineOp(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("keep\ndrop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{
		"op": string(plan.KindFile), "path": path, "add_line": "new", "remove_line": "drop",
	})
	if err != nil {
		t.Fatal(err)
	}
	op, err := plan.DecodeOp(raw)
	if err != nil {
		t.Fatalf("DecodeOp: %v", err)
	}
	if err := (planHandler{}).Apply(op, plan.ApplyContext{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "drop") || !strings.Contains(string(got), "keep\n") || !strings.Contains(string(got), "new\n") {
		t.Fatalf("content = %q", got)
	}
}
