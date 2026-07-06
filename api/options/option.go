// Package option provides interface-based, resource-agnostic configuration
// options shared by the file, dir, and link resource packages.
package options

import (
	"log"
	"os"
)

// Option configures a resource. It is applied to the concrete resource value
// (e.g. *file.File) during construction.
type Option func(any)

// Capability interfaces. A resource implements only the setters it supports.
type (
	Owner      interface{ SetOwner(string) }
	Grouped    interface{ SetGroup(string) }
	Moded      interface{ SetMode(os.FileMode) }
	Sourced    interface{ SetSource(string) }
	Contented  interface{ SetContent(string) }
	FileModed  interface{ SetFileMode(os.FileMode) }
	Prunable   interface{ SetPrune() }
	Absentable interface{ SetAbsent() }
	Linkable   interface {
		SetSymlink(target string)
		SetHardlink(target string)
	}
)

// WithOwner sets the owning user of the resource.
func WithOwner(owner string) Option {
	return func(t any) {
		r, ok := t.(Owner)
		if !ok {
			log.Fatalf("%T does not support WithOwner", t)
		}
		r.SetOwner(owner)
	}
}

// WithGroup sets the owning group of the resource.
func WithGroup(group string) Option {
	return func(t any) {
		r, ok := t.(Grouped)
		if !ok {
			log.Fatalf("%T does not support WithGroup", t)
		}
		r.SetGroup(group)
	}
}

// WithMode sets the resource's own file mode.
func WithMode(mode os.FileMode) Option {
	return func(t any) {
		r, ok := t.(Moded)
		if !ok {
			log.Fatalf("%T does not support WithMode", t)
		}
		r.SetMode(mode)
	}
}

// WithSource sets the source path the resource is populated from.
func WithSource(source string) Option {
	return func(t any) {
		r, ok := t.(Sourced)
		if !ok {
			log.Fatalf("%T does not support WithSource", t)
		}
		r.SetSource(source)
	}
}

// WithContent sets literal content for the resource.
func WithContent(content string) Option {
	return func(t any) {
		r, ok := t.(Contented)
		if !ok {
			log.Fatalf("%T does not support WithContent", t)
		}
		r.SetContent(content)
	}
}

// WithFileMode sets the mode applied to regular files copied from a source
// tree (distinct from the resource's own mode).
func WithFileMode(mode os.FileMode) Option {
	return func(t any) {
		r, ok := t.(FileModed)
		if !ok {
			log.Fatalf("%T does not support WithFileMode", t)
		}
		r.SetFileMode(mode)
	}
}

// WithPrune enables reconciliation of extra destination entries during a
// source copy, and recursive removal during IsAbsent().
var WithPrune = func(t any) {
	r, ok := t.(Prunable)
	if !ok {
		log.Fatalf("%T does not support WithPrune", t)
	}
	r.SetPrune()
}

func WithPruneFunc() Option { return WithPrune }

// IsAbsent marks the resource for removal.
var IsAbsent = func(t any) {
	r, ok := t.(Absentable)
	if !ok {
		log.Fatalf("%T does not support IsAbsent", t)
	}
	r.SetAbsent()
}

func IsAbsentFunc() Option { return IsAbsent }

// WithSymlink makes the resource a symbolic link pointing at target.
func WithSymlink(target string) Option {
	return func(t any) {
		r, ok := t.(Linkable)
		if !ok {
			log.Fatalf("%T does not support WithSymlink", t)
		}
		r.SetSymlink(target)
	}
}

// WithHardlink makes the resource a hard link pointing at target.
func WithHardlink(target string) Option {
	return func(t any) {
		r, ok := t.(Linkable)
		if !ok {
			log.Fatalf("%T does not support WithHardlink", t)
		}
		r.SetHardlink(target)
	}
}
