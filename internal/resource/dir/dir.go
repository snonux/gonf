package dir

import (
	"fmt"
	"log"
	"os"
	"os/user"
	"strconv"

	opt "codeberg.org/snonux/gonf/api/options"
	"codeberg.org/snonux/gonf/internal/resource"
)

type Dir struct {
	resource resource.Resource
	path     string
	source   string
	user     string
	group    string
	mode     os.FileMode // this directory's own mode, default 0o750
	fileMode os.FileMode // mode for regular files copied from source, default 0o640
	prune    bool        // reconciles extra dest files during a source copy, and recursive-remove during IsAbsent()
	absent   bool
}

// SetSource implements opt.Sourced.
func (d *Dir) SetSource(source string) { d.source = source }

// SetOwner implements opt.Owner.
func (d *Dir) SetOwner(user string) { d.user = user }

// SetGroup implements opt.Grouped.
func (d *Dir) SetGroup(group string) { d.group = group }

// SetMode implements opt.Moded (the directory's own mode).
func (d *Dir) SetMode(mode os.FileMode) { d.mode = mode }

// SetFileMode implements opt.FileModed (mode for files copied from source).
func (d *Dir) SetFileMode(mode os.FileMode) { d.fileMode = mode }

// SetPrune implements opt.Prunable.
func (d *Dir) SetPrune() { d.prune = true }

// SetAbsent implements opt.Absentable.
func (d *Dir) SetAbsent() { d.absent = true }

func build(path string, opts ...opt.Option) (*Dir, error) {
	curr, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("failed to get current user for default: %w", err)
	}

	d := &Dir{
		path:     path,
		mode:     0o750,
		fileMode: 0o640,
		user:     curr.Username,
		group:    curr.Gid,
	}

	for _, o := range opts {
		o(d)
	}

	return d, nil
}

// apply performs the idempotent OS work for d without registering a
// resource.
func (d *Dir) apply() error {
	if d.absent {
		return ensureAbsent(d)
	}

	if err := ensureDirectorySelf(d); err != nil {
		return err
	}

	if d.source == "" {
		return nil
	}

	if err := copySourceTree(d); err != nil {
		return err
	}

	if d.prune {
		return pruneTree(d)
	}

	return nil
}

// ensureDirectorySelf ensures d.path exists as a directory with the desired
// mode and ownership. It is idempotent: an existing directory only has its
// attributes re-enforced, and an existing non-directory is an error.
func ensureDirectorySelf(d *Dir) error {
	log.Printf("processing directory: %s", d.path)

	info, err := os.Lstat(d.path)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", d.path)
		}
		log.Printf("directory %s already exists", d.path)

	case os.IsNotExist(err):
		log.Printf("creating directory %s with mode %v", d.path, d.mode)
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d.path, err)
		}

	default:
		return fmt.Errorf("failed to stat %s: %w", d.path, err)
	}

	return applyAttributesTo(d.path, d.mode, d.user, d.group)
}

// ensureAbsent removes d.path if it exists. It is idempotent: a missing path
// is not an error. By default non-empty directories are not removed;
// combine with WithPrune() to remove a directory and its contents
// recursively.
func ensureAbsent(d *Dir) error {
	log.Printf("ensuring absent: %s", d.path)

	remove := os.Remove
	if d.prune {
		remove = os.RemoveAll
	}

	if err := remove(d.path); err != nil {
		if os.IsNotExist(err) {
			log.Printf("%s already absent", d.path)
			return nil
		}
		return fmt.Errorf("failed to remove %s: %w", d.path, err)
	}

	log.Printf("removed %s", d.path)
	return nil
}

// applyAttributesTo is dir's own small chmod/chown helper, deliberately not
// shared with the file package so the two packages' attribute-application
// behavior can evolve independently.
func applyAttributesTo(path string, mode os.FileMode, usr, group string) error {
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("failed to chmod %s to %v: %w", path, mode, err)
	}
	log.Printf("set mode %v for %s", mode, path)

	uid, gid := -1, -1

	if usr != "" {
		u, err := user.Lookup(usr)
		if err != nil {
			return fmt.Errorf("failed to lookup user %s: %w", usr, err)
		}
		uid, _ = strconv.Atoi(u.Uid)
	}

	if group != "" {
		gidInt, err := strconv.Atoi(group)
		if err != nil {
			return fmt.Errorf("group must be numeric for now: %s", group)
		}
		gid = gidInt
	}

	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("failed to chown %s to %s:%s: %w", path, usr, group, err)
	}
	log.Printf("set owner %s:%s for %s", usr, group, path)

	return nil
}

// Ensure builds and applies the directory resource described by opts,
// without registering it.
func Ensure(path string, opts ...opt.Option) error {
	d, err := build(path, opts...)
	if err != nil {
		return err
	}
	return d.apply()
}

func Present(path string, opts ...opt.Option) resource.Resource {
	d, err := build(path, opts...)
	if err != nil {
		log.Fatalf("failed to apply directory resource %s: %v", path, err)
	}

	d.resource = resource.Register("Directory", d.path,
		resource.ApplierFunc(func() error { return d.apply() }))

	return d.resource
}

func Absent(path string, opts ...opt.Option) resource.Resource {
	opts = append(opts, opt.IsAbsent)
	return Present(path, opts...)
}
