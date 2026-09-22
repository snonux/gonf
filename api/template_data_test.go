package api

import (
	"encoding/json"
	"os"
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

// reusedTemplateDataFiles declares one templated File per name under dir,
// reusing and mutating a single data map across the loop: the recipe
// pattern the 882 review probed. Each File must render the value the map
// held when its WithTemplateData was applied.
func reusedTemplateDataFiles(dir string, names ...string) {
	data := map[string]any{}
	for _, name := range names {
		data["name"] = name
		File(filepath.Join(dir, name), options.WithContent("{{.name}}"), options.WithTemplateData(data))
	}
}

// TestReusedTemplateDataRendersPerFile pins that api.Apply (which lowers the
// stored drafts only at apply time) and Run (which lowers them while
// recording) render the same per-file value from a reused, mutated map.
func TestReusedTemplateDataRendersPerFile(t *testing.T) {
	names := []string{"a", "b", "c"}
	check := func(t *testing.T, dir string) {
		t.Helper()
		for _, name := range names {
			got, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != name {
				t.Errorf("file %s rendered %q, want %q", name, got, name)
			}
		}
	}
	t.Run("Apply", func(t *testing.T) {
		ResetForTest()
		t.Cleanup(ResetForTest)
		dir := t.TempDir()
		reusedTemplateDataFiles(dir, names...)
		if err := Apply(); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		check(t, dir)
	})
	t.Run("Run", func(t *testing.T) {
		ResetForTest()
		t.Cleanup(ResetForTest)
		dir := t.TempDir()
		Task("reused_template_data", "reused template data", func() { reusedTemplateDataFiles(dir, names...) })
		if err := Run("reused_template_data"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		check(t, dir)
	})
}

// TestApplyRejectsUnsupportedTemplateData is the api.Apply counterpart of
// TestRecordPlanRejectsUnsupportedTemplateData: the encoding error captured
// at option time fails the lowering before anything is written.
func TestApplyRejectsUnsupportedTemplateData(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	path := filepath.Join(t.TempDir(), "config")
	File(path, options.WithContent("x"), options.WithTemplateData(map[string]any{"bad": func() {}}))
	err := Apply()
	if err == nil || !strings.Contains(err.Error(), "template data must be JSON-compatible") {
		t.Fatalf("Apply error = %v, want JSON-compatible refusal", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("Apply wrote %s despite the refusal (stat: %v)", path, statErr)
	}
}
