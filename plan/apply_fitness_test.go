package plan

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// TestApplyActiveKnowsEveryResourceKind pins the applyActive dispatch to the
// AllKinds inventory: every resource kind must hit a real handler, never the
// "unknown op" default. Each zero-valued fixture op must fail the handler's
// own validation (missing path/name/bin/…) BEFORE any mutation, so the check
// is side-effect free; KindDaemonReload has no required field, so its fixture
// uses IfChanged with a watch id that cannot have changed, which skips before
// touching systemd. Control kinds (plan header, when_begin, when_end) never
// reach applyActive and are skipped here.
func TestApplyActiveKnowsEveryResourceKind(t *testing.T) {
	for _, k := range AllKinds() {
		switch k {
		case KindPlan, KindWhenBegin, KindWhenEnd:
			continue
		case KindDaemonReload:
			resource.ResetReport()
			err := applyActive(Op{Op: k, IfChanged: true, Watch: []string{"no-such-resource"}}, "")
			if err != nil && strings.Contains(err.Error(), "unknown op") {
				t.Errorf("applyActive has no handler for kind %q: %v", k, err)
			}
			continue
		}

		err := applyActive(Op{Op: k}, "")
		if err == nil {
			t.Errorf("applyActive(%q) with a zero op must fail validation, got nil", k)
			continue
		}
		if strings.Contains(err.Error(), "unknown op") {
			t.Errorf("applyActive lacks a handler for kind %q (fell through to default): %v", k, err)
		}
	}
}

// TestApplyActiveRejectsUnknownKind guards the other direction: the default
// branch must stay an error, so a decoded op with an undeclared kind cannot
// silently no-op.
func TestApplyActiveRejectsUnknownOp(t *testing.T) {
	err := applyActive(Op{Op: "not_a_kind", Name: "x"}, "")
	if err == nil {
		t.Fatal("expected unknown-op error")
	}
	if !strings.Contains(err.Error(), `unknown op "not_a_kind"`) {
		t.Fatalf("wrong error: %v", err)
	}
}
