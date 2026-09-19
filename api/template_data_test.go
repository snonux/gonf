package api

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource/options"
)

func TestRecordPlanCarriesTemplateData(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	path := filepath.Join(t.TempDir(), "config")
	Task("template_data", "template data", func() {
		File(path, options.WithContent("{{.name}}"), options.WithTemplateData(map[string]any{"name": "relay"}))
	})
	ops, err := RecordPlan("template-data", "", "template_data")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if len(ops) != 2 || string(ops[1].TemplateData) != `{"name":"relay"}` {
		t.Fatalf("template_data = %s, want serialized map", ops[1].TemplateData)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(decoded[1].TemplateData, &data); err != nil {
		t.Fatal(err)
	}
	if data["name"] != "relay" {
		t.Errorf("decoded template data = %#v", data)
	}
}

func TestRecordPlanRejectsUnsupportedTemplateData(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("bad_template_data", "bad template data", func() {
		File(filepath.Join(t.TempDir(), "config"), options.WithContent("x"), options.WithTemplateData(map[string]any{"bad": func() {}}))
	})
	_, err := RecordPlan("template-data", "", "bad_template_data")
	if err == nil || !strings.Contains(err.Error(), "template data must be JSON-compatible") {
		t.Fatalf("RecordPlan error = %v, want JSON-compatible refusal", err)
	}
}
