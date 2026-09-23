// Package link implements the link resource for symbolic and hard links.
package link

import (
	"fmt"
	"os"
	"slices"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

type kind int

const (
	unsetKind kind = iota
	symlinkKind
	hardlinkKind
)

// Link reconciles a symbolic or hard link (IsSymlink/IsHardlink) or removes
// an existing link entry (IsAbsent).
type Link struct {
	embed.DependsOn
	embed.Absence
	embed.Misuse
	resource resource.Resource
	path     string
	target   string
	kind     kind
}

// SetSymlink implements opt.Linkable.
func (l *Link) SetSymlink(target string) {
	l.kind = symlinkKind
	l.target = target
}

// SetHardlink implements opt.Linkable.
func (l *Link) SetHardlink(target string) {
	l.kind = hardlinkKind
	l.target = target
}

// build applies opts to a new Link. An option misuse collected while applying
// them (embed.Misuse) is its error.
func build(path string, opts ...opt.LinkOption) (*Link, error) {
	l := &Link{path: path}
	for _, o := range opts {
		o.Apply(l)
	}
	if err := l.MisuseErr(); err != nil {
		return nil, err
	}
	return l, nil
}

var _ opt.MisuseReporter = (*Link)(nil)

func (l *Link) apply() error {
	switch {
	case l.Absent:
		return ensureAbsent(l.path)
	case l.kind == symlinkKind:
		return ensureSymlink(l)
	case l.kind == hardlinkKind:
		return ensureHardlink(l)
	default:
		return fmt.Errorf("link %s: must specify IsSymlink, IsHardlink, or IsAbsent", l.path)
	}
}

func (l *Link) resourceType() string {
	switch l.kind {
	case symlinkKind:
		return "Symlink"
	case hardlinkKind:
		return "Hardlink"
	default:
		return "Link"
	}
}

// Ensure builds and applies the link resource described by opts, without
// registering it.
func Ensure(path string, opts ...opt.LinkOption) error {
	l, err := build(path, opts...)
	if err != nil {
		return err
	}
	return l.apply()
}

// Present registers a link resource that ensures path is the configured
// symlink or hardlink, and records a plan draft for remote apply. An option
// misuse is reported as a declaration error (resource.Refuse) and nothing is
// registered.
func Present(path string, opts ...opt.LinkOption) resource.Resource {
	l, err := build(path, opts...)
	if err != nil {
		return resource.Refuse("Link", path, err)
	}
	r, ok := resource.Register(l.resourceType(), l.path, l, l.DependsOn.IDs...)
	l.resource = r
	if ok {
		resource.RecordPlanDraft(l.planDraft())
	}
	return l.resource
}

// planDraft records l as a "link" plan draft. Symlink/Hardlink, link's
// exclusive fields, travel in Payload (see Payload, task w62 Layer 1).
func (l *Link) planDraft() resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:   "link",
		ID:     l.resource.ID(),
		Path:   l.path,
		Absent: l.Absent,
		Deps:   l.DependsOn.SortedIDs(),
	}
	var p Payload
	switch l.kind {
	case symlinkKind:
		p.Symlink = l.target
	case hardlinkKind:
		p.Hardlink = l.target
	}
	d.Payload = p
	return d
}

// Absent registers a link resource that ensures path does not exist (the
// entry is removed regardless of its type).
func Absent(path string, opts ...opt.LinkOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(path, opts...)
}

func ensureAbsent(path string) error {
	id := resource.FormatID("Link", path)
	logger.Debug("ensuring link absent: %s", path)

	if _, err := os.Lstat(path); os.IsNotExist(err) {
		logger.Debug("%s already absent", path)
		resource.Note(id, resource.StatusOK)
		return nil
	}

	if resource.DryRun() {
		resource.Note(id, resource.StatusWouldChange)
		logger.Info("dry-run: would remove %s", path)
		return nil
	}

	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			resource.Note(id, resource.StatusOK)
			return nil
		}
		return fmt.Errorf("failed to remove %s: %w", path, err)
	}

	resource.Note(id, resource.StatusChanged)
	logger.Info("removed %s", path)
	return nil
}
