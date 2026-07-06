package link

import (
	"fmt"
	"log"
	"os"

	opt "codeberg.org/snonux/gonf/api/option"
	"codeberg.org/snonux/gonf/internal/resource"
)

type kind int

const (
	unsetKind kind = iota
	symlinkKind
	hardlinkKind
)

type Link struct {
	resource resource.Resource
	path     string
	target   string
	kind     kind
	absent   bool
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

// SetAbsent implements opt.Absentable.
func (l *Link) SetAbsent() { l.absent = true }

func build(path string, opts ...opt.Option) *Link {
	l := &Link{path: path}
	for _, o := range opts {
		o(l)
	}
	return l
}

// apply performs the idempotent OS work for l without registering a
// resource. Kind-vs-absent validation happens here (not in build) to match
// the pre-split behavior where a resource was registered before its target
// was validated.
func (l *Link) apply() error {
	switch {
	case l.absent:
		return ensureAbsent(l.path)
	case l.kind == symlinkKind:
		return ensureSymlink(l)
	case l.kind == hardlinkKind:
		return ensureHardlink(l)
	default:
		return fmt.Errorf("link %s: must specify IsSymlink, IsHardlink, or IsAbsent", l.path)
	}
}

// resourceType returns the registry type name for l. Absent links with no
// kind specified register generically as "Link", since removal doesn't
// depend on knowing the prior kind.
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
// registering it. Used by other resource packages (e.g. dir) to recreate an
// individual symlink without it becoming its own top-level resource.
func Ensure(path string, opts ...opt.Option) error {
	return build(path, opts...).apply()
}

func Present(path string, opts ...opt.Option) resource.Resource {
	l := build(path, opts...)
	l.resource = resource.Register(l.resourceType(), l.path,
		resource.ApplierFunc(func() error { return l.apply() }))

	return l.resource
}

func Absent(path string, opts ...opt.Option) resource.Resource {
	opts = append(opts, opt.IsAbsent())
	return Present(path, opts...)
}

func ensureAbsent(path string) error {
	log.Printf("ensuring link absent: %s", path)

	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			log.Printf("%s already absent", path)
			return nil
		}
		return fmt.Errorf("failed to remove %s: %w", path, err)
	}

	log.Printf("removed %s", path)
	return nil
}
