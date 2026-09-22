package testapply_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/resource"
)

// orderCase is one TestApplyOrder row: the fixture names in registration
// order, each one's dependency names, the name whose work fails, and either
// the wanted apply order or a wanted error substring.
type orderCase struct {
	name      string
	names     []string
	deps      map[string][]string
	fail      string // name whose work fails
	wantOrder []string
	wantErr   string
}

// orderCases carry the dependency cases the retired resource.Apply
// repository test pinned, now through the plan engine: a chain, independent
// resources (applied in ID order), a diamond, a cycle, a dangling dependency
// and a failing resource.
var orderCases = []orderCase{
	{name: "dependency chain", names: []string{"C", "B", "A"},
		deps:      map[string][]string{"A": {"B"}, "B": {"C"}},
		wantOrder: []string{"C", "B", "A"}},
	{name: "independent resources apply in ID order", names: []string{"C", "A", "B"},
		wantOrder: []string{"A", "B", "C"}},
	{name: "diamond dependency", names: []string{"A", "B", "C", "D"},
		deps:      map[string][]string{"B": {"A"}, "C": {"A"}, "D": {"B", "C"}},
		wantOrder: []string{"A", "B", "C", "D"}},
	{name: "circular dependency", names: []string{"A", "B"},
		deps:    map[string][]string{"A": {"B"}, "B": {"A"}},
		wantErr: "circular dependency"},
	{name: "missing dependency", names: []string{"A"},
		deps:    map[string][]string{"A": {"Missing"}},
		wantErr: "T[Missing]"},
	{name: "execution failure", names: []string{"A"}, fail: "A",
		wantErr: "fail A"},
}

// TestApplyOrder applies Register fixtures through Apply and checks the plan
// engine's dependency order and refusals.
func TestApplyOrder(t *testing.T) {
	for _, tc := range orderCases {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			var order []string
			for _, name := range tc.names {
				var deps []string
				for _, d := range tc.deps[name] {
					deps = append(deps, "T["+d+"]")
				}
				testapply.Register("T", name, func() error {
					order = append(order, name)
					if name == tc.fail {
						return errors.New("fail " + name)
					}
					return nil
				}, deps...)
			}
			err := testapply.Apply()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Apply() = %v, want error containing %q", err, tc.wantErr)
				}
				// A refusal (anything but a failing resource) applies nothing.
				if tc.fail == "" && len(order) != 0 {
					t.Fatalf("refused plan applied %v", order)
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply() = %v", err)
			}
			if !slices.Equal(order, tc.wantOrder) {
				t.Errorf("order = %v, want %v", order, tc.wantOrder)
			}
		})
	}
}

// TestApplyRefusals pins what Apply refuses before applying anything: a
// registered resource without a plan draft and an elevated op (the
// privilege split is api.Apply's job).
func TestApplyRefusals(t *testing.T) {
	t.Run("registered without draft", func(t *testing.T) {
		resource.ResetRepository()
		ran := false
		resource.Register("T", "bare", resource.ApplierFunc(func() error { ran = true; return nil }))
		err := testapply.Apply()
		if err == nil || !strings.Contains(err.Error(), "registered resources without plan drafts: T[bare]") || ran {
			t.Fatalf("Apply() = %v (ran %t), want the missing-draft refusal", err, ran)
		}
	})
	t.Run("elevated op", func(t *testing.T) {
		resource.ResetRepository()
		ran := false
		res := testapply.Register("T", "root", func() error { ran = true; return nil })
		resource.RecordPlanDraft(resource.PlanDraft{Kind: "testapply_fixture", ID: res.ID(), Elevate: true})
		err := testapply.Apply()
		if err == nil || !strings.Contains(err.Error(), "T[root] is elevated") || ran {
			t.Fatalf("Apply() = %v (ran %t), want the elevated-op refusal", err, ran)
		}
	})
	t.Run("nothing registered", func(t *testing.T) {
		resource.ResetRepository()
		if err := testapply.Apply(); err != nil {
			t.Fatalf("Apply() = %v, want nil", err)
		}
	})
}
