package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/options"
)

type templateTestServer struct {
	Name  string
	Ports []int
}

func TestEnsureTemplateDataStructuredAndFunctions(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "config")
	err := Ensure(path, options.WithContent(`{{upper .Name}} {{join .Ports ","}} {{.Data.Name}} {{trim " x "}}`), options.WithTemplateData(templateTestServer{
		Name:  "relay",
		Ports: []int{80, 443},
	}))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "RELAY 80,443 relay x"; string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestEnsureTemplateDataMissingKeyFails(t *testing.T) {
	resource.ResetRepository()
	err := Ensure(filepath.Join(t.TempDir(), "config"), options.WithContent(`{{.Missing}}`), options.WithTemplateData(map[string]any{}))
	if err == nil || !strings.Contains(err.Error(), "map has no entry for key \"Missing\"") {
		t.Fatalf("Ensure error = %v, want missing-key error", err)
	}
}

func TestEnsureTemplateDataReservesDataContext(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "config")
	err := Ensure(path,
		options.WithContent(`{{.Data.Name}}|{{.Data.Data}}|{{.Data}}`),
		options.WithTemplateData(map[string]any{"Name": "relay", "Data": "user-value"}),
	)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "relay|user-value|map[Data:user-value Name:relay]"; string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestEnsureTemplateDataPreservesLargeInteger(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "config")
	err := Ensure(path,
		options.WithContent(`{{.Data.ID}}`),
		options.WithTemplateData(map[string]any{"ID": int64(9_007_199_254_740_993)}),
	)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "9007199254740993"; string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestEnsureTemplateDataUsesLocalFacts(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "config")
	wantFacts := localTemplateFacts()
	err := Ensure(path, options.WithContent(`{{.Gonf.GOOS}}|{{.Gonf.Profile}}|{{.Gonf.Hostname}}`), options.WithTemplateData(map[string]any{}))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := wantFacts.GOOS + "|" + wantFacts.Profile + "|" + wantFacts.Hostname; string(got) != want {
		t.Errorf("facts = %q, want %q", got, want)
	}
}

// TestPlanApplyRendersLocalFactsIncludingProfile pins that a templated File
// applied through the plan path (testapply.Apply, which resource/<kind>
// tests use in place of the retired resource.Apply, e72) renders
// {{.Gonf.Profile}} from a real detected profile, not an empty string.
// internal/testapply's local plan.Facts detect Profile the same way
// localTemplateFacts (used by the direct Ensure path, see
// TestEnsureTemplateDataUsesLocalFacts above) does, so both paths must
// render identically on this host.
func TestPlanApplyRendersLocalFactsIncludingProfile(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "config")
	wantFacts := localTemplateFacts()
	Present(path, options.WithContent(`{{.Gonf.GOOS}}|{{.Gonf.Profile}}|{{.Gonf.Hostname}}`), options.WithTemplateData(map[string]any{}))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("testapply.Apply: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// hostfacts.Profile (what localTemplateFacts uses) never returns "":
	// an empty /etc/os-release ID= is itself mapped to "unknown", so this
	// comparison cannot pass by both sides coincidentally being empty and
	// does catch testapply's Profile being left unfilled.
	if want := wantFacts.GOOS + "|" + wantFacts.Profile + "|" + wantFacts.Hostname; string(got) != want {
		t.Errorf("facts = %q, want %q", got, want)
	}
}
