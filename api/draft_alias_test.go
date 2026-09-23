package api

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/systemdtimer"
)

// withAllReferences overlays onto a kind's fitness draft every slice, map
// and pointer field some handler lowers (Deps, Watch with IfChanged set,
// Env), leaving the kind-specific fields (config members, supplementary
// groups, a migrated kind's Payload such as cron's CronEnv, ...) as the
// fixture set them so the handler still accepts the draft, except command's
// Payload (Args, Unless, OnlyIf), systemd_timer's Payload (After, Wants),
// and file's Payload (AddLines, RemoveLines, ValidationArgs, TemplateData),
// which are actively grown/overwritten here too so this generic loop keeps
// exercising a POINTER-bearing payload's aliasing safety, not just a
// slice-only one.
func withAllReferences(d resource.PlanDraft) resource.PlanDraft {
	exit := 1
	d.Deps = append(d.Deps, "Package[alias-dep]")
	d.IfChanged = true
	d.Watch = append(d.Watch, "File[/alias]")
	d.Env = map[string]string{"ALIAS": "1"}
	if p, ok := d.Payload.(cmd.Payload); ok {
		p.Args = append(p.Args, "alias")
		p.Unless = &resource.PlanGuardDraft{Bin: "test", Args: []string{"-e", "/u"}, ExpectExit: &exit}
		p.OnlyIf = &resource.PlanGuardDraft{Bin: "test", Args: []string{"-e", "/o"}}
		d.Payload = p
	}
	if p, ok := d.Payload.(systemdtimer.Payload); ok {
		p.After = append(p.After, "alias.target")
		p.Wants = append(p.Wants, "alias.target")
		d.Payload = p
	}
	if p, ok := d.Payload.(file.Payload); ok {
		p.AddLines = []string{"add"}
		p.RemoveLines = []string{"remove"}
		p.ValidationArgs = []string{"-c", "x"}
		p.TemplateData = json.RawMessage(`{"alias":1}`)
		p.TemplateDataSet = true
		d.Payload = p
	}
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
	p := d.Payload.(cmd.Payload)
	return plan.Op{
		Op: plan.KindCommand, Env: d.Env, Deps: d.Deps,
		Payload: plan.CommandPayload{
			Bin:    p.Bin,
			Args:   p.Args,
			Unless: &plan.Guard{Bin: p.Unless.Bin, Args: p.Unless.Args, ExpectExit: p.Unless.ExpectExit},
		},
	}, nil
}

func (aliasingHandler) Apply(plan.Op, plan.ApplyContext) error { return nil }

// TestSharedRefsCatchesAliasingToOp is the negative case: a handler that
// aliases the draft must be reported for each shared field, and mutating
// its draft must visibly change the op, or the test above would prove
// nothing.
func TestSharedRefsCatchesAliasingToOp(t *testing.T) {
	d := withAllReferences(resource.PlanDraft{Kind: "command", Payload: cmd.Payload{Bin: "true"}})
	op, err := aliasingHandler{}.ToOp(d)
	if err != nil {
		t.Fatal(err)
	}
	shared := testutil.SharedRefs(d, op)
	for _, want := range []string{"Payload.Args", "Env", "Deps", "Payload.Unless.Args", "Payload.Unless.ExpectExit"} {
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
