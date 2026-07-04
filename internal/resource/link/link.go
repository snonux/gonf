package link

import (
	"fmt"
	"log"
	"os"

	"codeberg.org/snonux/gonf/internal/resource"
)

type kind int

const (
	unsetKind kind = iota
	symlinkKind
	hardlinkKind
)

type Link struct {
	path   string
	target string
	kind   kind
	absent bool
}

type Option func(*Link)

func IsSymlink(target string) Option {
	return func(l *Link) {
		l.kind = symlinkKind
		l.target = target
	}
}

func IsHardlink(target string) Option {
	return func(l *Link) {
		l.kind = hardlinkKind
		l.target = target
	}
}

func IsAbsent() Option {
	return func(l *Link) {
		l.absent = true
	}
}

func build(path string, opts ...Option) *Link {
	l := &Link{path: path}
	for _, opt := range opts {
		opt(l)
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
func Ensure(path string, opts ...Option) error {
	return build(path, opts...).apply()
}

func Have(path string, opts ...Option) resource.Resource {
	l := build(path, opts...)
	res := resource.Register(l.resourceType(), l.path)

	if err := l.apply(); err != nil {
		log.Fatalf("failed to apply link resource %s: %v", path, err)
	}

	return res
}

func Absent(path string, opts ...Option) resource.Resource {
	opts = append(opts, IsAbsent())
	return Have(path, opts...)
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
