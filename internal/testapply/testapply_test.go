package testapply_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/plan"
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
		resource.Register("T", "bare", func() error { ran = true; return nil })
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

// TestApplyDeclErrGuard pins the two guards Apply mirrors from api.Apply
// (api/resource.go): a declaration error reported earlier (here, a
// resource-already-registered error, exactly the kind the review's probe
// used) must refuse the apply before anything runs, and so must an active
// plan-record session. Without these guards Apply silently applied whatever
// subset of the registered resources still had a draft, even though the
// declaration error meant the registered set was known incomplete.
func TestApplyDeclErrGuard(t *testing.T) {
	t.Run("declaration error refuses before applying", func(t *testing.T) {
		resource.ResetRepository()
		declerr.Reset()
		t.Cleanup(declerr.Reset)

		ran := false
		testapply.Register("T", "A", func() error { ran = true; return nil })
		// A duplicate registration under the same ID reports a
		// resource-already-registered declaration error (resource.Register,
		// resource/resource.go) without registering the second declaration,
		// exactly the review's probe scenario.
		resource.Register("T", "A", func() error { return nil })
		if declerr.First() == nil {
			t.Fatalf("declerr.First() = nil, want the duplicate-registration error")
		}

		err := testapply.Apply()
		if err == nil || !errors.Is(err, declerr.First()) || ran {
			t.Fatalf("Apply() = %v (ran %t), want it to refuse with the declaration error and apply nothing", err, ran)
		}
	})

	t.Run("plan recording refuses before applying", func(t *testing.T) {
		resource.ResetRepository()
		declerr.Reset()
		t.Cleanup(declerr.Reset)

		ran := false
		testapply.Register("T", "A", func() error { ran = true; return nil })

		plan.SetRecording(true)
		t.Cleanup(func() { plan.SetRecording(false) })

		err := testapply.Apply()
		if err == nil || ran {
			t.Fatalf("Apply() = %v (ran %t), want it to refuse while plan recording is active", err, ran)
		}
	})
}

// leakingPayload implements both resource.SourceFilePayload and
// resource.SourceDirPayload the way a future resource kind's own payload
// could by accident (task 0e2's reproduction added a like-named accessor to
// cron.Payload and watched every recorded cron op silently gain the target
// file's bytes). It drives TestPackageSourceGatedOnKind below, which proves
// this package's packageSource (unexported; driven through the public Ops)
// ignores it for a kind that never meant to carry a source.
type leakingPayload struct{ path, dir, glob string }

func (leakingPayload) Clone() resource.DraftPayload      { return leakingPayload{} }
func (p leakingPayload) SourceFilePath() string          { return p.path }
func (p leakingPayload) SourceDirGlob() (string, string) { return p.dir, p.glob }

// TestPackageSourceGatedOnKind is the task 0e2 regression: before the fix,
// packageSource type-asserted resource.SourceFilePayload/
// resource.SourceDirPayload against whatever concrete type a draft's Payload
// held, with no check that the draft's Kind was ever meant to carry a
// source, so leakingPayload's bytes would have ended up as this op's
// content_b64/blob. "testapply_fixture" (fixtureKind, from fixture.go) is
// used as the mismatched kind because it is the one kind this external test
// package can register a plan.Handler for without importing a
// resource/<kind> package back — the same reason this package itself stays
// kind-neutral (see the package doc).
func TestPackageSourceGatedOnKind(t *testing.T) {
	resource.ResetRepository()
	testapply.Register("T", "noop", func() error { return nil }) // installs fixtureHandler
	leaking := leakingPayload{path: "/etc/shadow", dir: "/etc", glob: "*.conf"}
	draft := resource.PlanDraft{Kind: "testapply_fixture", ID: "T[leak]", Payload: leaking}

	ops, err := testapply.Ops([]resource.PlanDraft{draft}, plan.NewMemoryStore())
	if err != nil {
		t.Fatalf("Ops() = %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("ops = %#v, want header plus one op", ops)
	}
	if op := ops[1]; op.ContentB64 != "" || op.Blob != "" {
		t.Fatalf("op = %#v, want no content_b64/blob (leakingPayload must not be consulted for kind %q)", op, draft.Kind)
	}
}
