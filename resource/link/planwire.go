package link

import (
	"fmt"
	"os"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// linkHandler and linkIfExistsHandler are the plan.Handlers for link's two
// op kinds ("link" — see (*Link).planDraft — and "link_if_exists", drafted
// by api.LinkIfExists since it has no standalone resource.Present path).
// They are separate types, one per Kind; see resource/pkg/planwire.go for
// why record-time ToOp and apply-time Apply live together in the resource
// package instead of api/plan.go's draftToOp and plan/apply.go's
// applyLink/applyLinkIfExists.
type linkHandler struct{}
type linkIfExistsHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindLink, linkHandler{})
	plan.RegisterHandler(plan.KindLinkIfExists, linkIfExistsHandler{})
}

// ToOp lowers a "link" resource draft to a plan.Op.
func (linkHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:       plan.KindLink,
		ID:       d.ID,
		Path:     d.Path,
		Symlink:  d.Symlink,
		Hardlink: d.Hardlink,
		Absent:   d.Absent,
		Deps:     d.Deps,
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
	switch {
	case op.Symlink != "":
		target, err := plan.ExpandPath(op.Symlink)
		if err != nil {
			return err
		}
		return Ensure(path, opt.WithSymlink(target))
	case op.Hardlink != "":
		target, err := plan.ExpandPath(op.Hardlink)
		if err != nil {
			return err
		}
		return Ensure(path, opt.WithHardlink(target))
	default:
		return fmt.Errorf("link: missing symlink or hardlink target")
	}
}

// ToOp lowers a "link_if_exists" resource draft to a plan.Op.
func (linkIfExistsHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:     plan.KindLinkIfExists,
		ID:     d.ID,
		Path:   d.Path,
		Target: d.Target,
		Deps:   d.Deps,
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
	target, err := plan.ExpandPath(op.Target)
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
