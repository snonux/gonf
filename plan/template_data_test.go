package plan_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
)

func TestApplyFileTemplateDataUsesDestinationFactsAndEnvironment(t *testing.T) {
	t.Setenv("GONF_TEMPLATE_DATA_TOKEN", "destination")
	path := filepath.Join(t.TempDir(), "config")
	data, err := json.Marshal(map[string]any{"servers": []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "template-data"},
		{Op: plan.KindFile, Path: path, ContentB64: "e3tyYW5nZSAuc2VydmVyc319e3sufX0se3tlbmR9fXx7ey5Hb25mLkhvc3RuYW1lfX18e3suR29uZi5Qcm9maWxlfX18e3suR09ORl9URU1QTEFURV9EQVRBX1RPS0VOfX0=", HasContent: true, Template: true, TemplateData: data},
	}
	if err := plan.Apply(ops, plan.Facts{GOOS: "freebsd", Profile: "edge", Hostname: "host-a"}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a,b,|host-a|edge|destination"; string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestPushWirePreservesTemplateData(t *testing.T) {
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "template-data"},
		{Op: plan.KindFile, Path: "/tmp/config", ContentB64: "eA==", HasContent: true, Template: true, TemplateData: json.RawMessage(`{"items":["a","b"]}`)},
	}
	var wire bytes.Buffer
	if err := plan.EncodePush(&wire, ops, nil); err != nil {
		t.Fatalf("EncodePush: %v", err)
	}
	payload, err := plan.DecodePush(&wire, "")
	if err != nil {
		t.Fatalf("DecodePush: %v", err)
	}
	if got, want := string(payload.Ops[1].TemplateData), `{"items":["a","b"]}`; got != want {
		t.Errorf("template_data = %s, want %s", got, want)
	}
}

func TestApplyFileTemplateDataMissingKeyFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "template-data"},
		{Op: plan.KindFile, Path: path, ContentB64: "e3suTWlzc2luZ319", HasContent: true, Template: true},
	}
	err := plan.Apply(ops, plan.Facts{}, "")
	if err == nil || !strings.Contains(err.Error(), "map has no entry for key \"Missing\"") {
		t.Fatalf("Apply error = %v, want missing-key error", err)
	}
}

func TestApplyFileTemplateDataPreservesLargeInteger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "template-data"},
		{Op: plan.KindFile, Path: path, ContentB64: "e3suRGF0YS5JRH19", HasContent: true, Template: true, TemplateData: json.RawMessage(`{"ID":9007199254740993}`)},
	}
	if err := plan.Apply(ops, plan.Facts{}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "9007199254740993"; string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}
