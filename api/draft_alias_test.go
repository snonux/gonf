package api

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// withAllReferences overlays onto a kind's fitness draft every slice, map
// and pointer field some handler lowers (Deps, Watch with IfChanged set,
// Args, Env, the guards, the line/cron/unit lists, ValidationArgs), leaving
// the kind-specific fields (config members, supplementary groups, ...) as
// the fixture set them so the handler still accepts the draft.
func withAllReferences(d resource.PlanDraft) resource.PlanDraft {
	exit := 1
	d.Deps = append(d.Deps, "Package[alias-dep]")
	d.IfChanged = true
	d.Watch = append(d.Watch, "File[/alias]")
	d.Args = append(d.Args, "alias")
	d.Env = map[string]string{"ALIAS": "1"}
	d.Unless = &resource.PlanGuardDraft{Bin: "test", Args: []string{"-e", "/u"}, ExpectExit: &exit}
	d.OnlyIf = &resource.PlanGuardDraft{Bin: "test", Args: []string{"-e", "/o"}}
	d.AddLines = []string{"add"}
	d.RemoveLines = []string{"remove"}
	d.CronEnv = append(d.CronEnv, "ALIAS=1")
	d.After = append(d.After, "alias.target")
	d.Wants = append(d.Wants, "alias.target")
	d.ValidationArgs = []string{"-c", "x"}
	return d
}

// TestHandlersToOpDoNotAliasDraft pins the op half of the copy contract
// (plan.Handler.ToOp): for every registered draft kind, the lowered op
// shares no slice, map or pointer with its draft, so mutating the draft
// afterwards leaves the op's wire form byte-identical.
func TestHandlersToOpDoNotAliasDraft(t *testing.T) {
	for kind, fx := range kindFitnessTable() {
		if fx.draft == nil {
			continue // control kind: no resource draft, no handler ToOp
		}
		t.Run(string(kind), func(t *testing.T) {
			d := withAllReferences(*fx.draft)
			h, ok := plan.HandlerFor(plan.Kind(d.Kind))
			if !ok {
				t.Fatalf("no handler for draft kind %q", d.Kind)
			}
			op, err := h.ToOp(d)
			if err != nil {
				t.Fatalf("ToOp: %v", err)
			}
			if shared := testutil.SharedRefs(d, op); len(shared) > 0 {
				t.Fatalf("op shares %v with its draft; ToOp must copy what it takes", shared)
			}
			before := mustMarshalOp(t, op)
			testutil.Scribble(&d)
			if after := mustMarshalOp(t, op); after != before {
				t.Fatalf("mutating the draft changed the op:\nbefore %s\n after %s", before, after)
			}
		})
	}
}

// aliasingHandler is a deliberately broken ToOp that passes the draft's
// slices, map and guard args straight through.
type aliasingHandler struct{}

func (aliasingHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op: plan.KindCommand, Bin: d.Bin, Args: d.Args, Env: d.Env, Deps: d.Deps,
		Unless: &plan.Guard{Bin: d.Unless.Bin, Args: d.Unless.Args, ExpectExit: d.Unless.ExpectExit},
	}, nil
}

func (aliasingHandler) Apply(plan.Op, plan.ApplyContext) error { return nil }

// TestSharedRefsCatchesAliasingToOp is the negative case: a handler that
// aliases the draft must be reported for each shared field, and mutating
// its draft must visibly change the op, or the test above would prove
// nothing.
func TestSharedRefsCatchesAliasingToOp(t *testing.T) {
	d := withAllReferences(resource.PlanDraft{Kind: "command", Bin: "true"})
	op, err := aliasingHandler{}.ToOp(d)
	if err != nil {
		t.Fatal(err)
	}
	shared := testutil.SharedRefs(d, op)
	for _, want := range []string{"Args", "Env", "Deps", "Unless.Args", "Unless.ExpectExit"} {
		if !slices.Contains(shared, want) {
			t.Errorf("SharedRefs missed aliased %s (got %v)", want, shared)
		}
	}
	before := mustMarshalOp(t, op)
	testutil.Scribble(&d)
	if mustMarshalOp(t, op) == before {
		t.Fatal("mutating an aliased draft left the op unchanged; Scribble does not reach shared storage")
	}
}

// mustMarshalOp returns op's JSON wire form.
func mustMarshalOp(t *testing.T, op plan.Op) string {
	t.Helper()
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal op: %v", err)
	}
	return string(raw)
}
