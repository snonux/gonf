package link

import (
	"fmt"
	"os"
	"slices"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// linkHandler and linkIfExistsHandler are the plan.Handlers for link's two
// op kinds ("link" — see (*Link).planDraft — and "link_if_exists", drafted
// by api.LinkIfExists since it has no standalone resource.Present path).
// They are separate types, one per Kind; see resource/pkg/planwire.go for
// why record-time ToOp and apply-time Apply live together in the resource
// package instead of api/packager.go's draftToOp and plan/apply.go's
// applyLink/applyLinkIfExists.
type linkHandler struct{}
type linkIfExistsHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindLink, linkHandler{})
	plan.RegisterHandler(plan.KindLinkIfExists, linkIfExistsHandler{})
}

// ToOp lowers a "link" resource draft to a plan.Op. Symlink/Hardlink come
// from d.Payload (Payload, task w62 Layer 1) and are set on plan.LinkPayload
// (task 5e2 Layer 2); a "link" draft without one is a record-time bug
// (planDraft always sets it), reported like any other handler error rather
// than panicking.
func (linkHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	p, ok := d.Payload.(Payload)
	if !ok {
		return plan.Op{}, fmt.Errorf("link: draft missing link.Payload (got %T)", d.Payload)
	}
	return plan.Op{
		Op:     plan.KindLink,
		ID:     d.ID,
		Path:   d.Path,
		Absent: d.Absent,
		Deps:   slices.Clone(d.Deps),
		Payload: plan.LinkPayload{
			Symlink:  p.Symlink,
			Hardlink: p.Hardlink,
		},
	}, nil
}

// Apply creates, replaces, or removes the destination symlink/hardlink,
// mirroring the resource's own symlink/hardlink/absent handling exactly.
func (linkHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	path, err := plan.ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("link: missing path")
	}
	if op.Absent {
		return Ensure(path, opt.IsAbsent)
	}
	// p reads as the zero LinkPayload for a decoded or mistyped Payload (see
	// plan.PayloadOf's doc comment) — both fields read as unset, which the
	// "missing symlink or hardlink target" error below already turns into a
	// clean error.
	p := plan.PayloadOf[plan.LinkPayload](op)
	switch {
	case p.Symlink != "":
		target, err := plan.ExpandPath(p.Symlink)
		if err != nil {
			return err
		}
		return Ensure(path, opt.WithSymlink(target))
	case p.Hardlink != "":
		target, err := plan.ExpandPath(p.Hardlink)
		if err != nil {
			return err
		}
		return Ensure(path, opt.WithHardlink(target))
	default:
		return fmt.Errorf("link: missing symlink or hardlink target")
	}
}

// ToOp lowers a "link_if_exists" resource draft to a plan.Op. Target comes
// from d.Payload (IfExistsPayload, task w62 Layer 1) and is set on
// plan.LinkIfExistsPayload (task 5e2 Layer 2); a "link_if_exists" draft
// without one is a record-time bug, reported like any other handler error
// rather than panicking.
func (linkIfExistsHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	p, ok := d.Payload.(IfExistsPayload)
	if !ok {
		return plan.Op{}, fmt.Errorf("link_if_exists: draft missing link.IfExistsPayload (got %T)", d.Payload)
	}
	return plan.Op{
		Op:      plan.KindLinkIfExists,
		ID:      d.ID,
		Path:    d.Path,
		Deps:    slices.Clone(d.Deps),
		Payload: plan.LinkIfExistsPayload{Target: p.Target},
	}, nil
}

// Apply symlinks path to target when target exists on the destination host,
// otherwise ensures path is absent — the existence check is evaluated here,
// at apply time on the destination, mirroring api.LinkIfExists's direct
// (non-plan) controller-side check.
func (linkIfExistsHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	path, err := plan.ExpandPath(op.Path)
	if err != nil {
		return err
	}
	// p reads as the zero LinkIfExistsPayload for a decoded or mistyped
	// Payload (see plan.PayloadOf's doc comment).
	p := plan.PayloadOf[plan.LinkIfExistsPayload](op)
	target, err := plan.ExpandPath(p.Target)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("link_if_exists: missing path")
	}
	if target == "" {
		return fmt.Errorf("link_if_exists: missing target")
	}
	_, err = os.Stat(target)
	switch {
	case err == nil:
		return Ensure(path, opt.WithSymlink(target))
	case os.IsNotExist(err):
		return Ensure(path, opt.IsAbsent)
	default:
		return fmt.Errorf("link_if_exists: stat target %s: %w", target, err)
	}
}
