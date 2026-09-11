// Package dir implements the directory resource, including copying and
// pruning source trees.
package dir

import (
	"fmt"
	"github.com/snonux/gonf/internal/logger"
	"os"
	"os/user"
	"strconv"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
)

type Dir struct {
	embed.DependsOn
	embed.Absence
	resource   resource.Resource
	path       string
	source     string
	sourceGlob string
	user       string
	group      string
	mode       os.FileMode // this directory's own mode, default 0o750
	fileMode   os.FileMode // mode for regular files copied from source, default 0o640
	prune      bool        // reconciles extra dest files during a source copy, and recursive-remove during IsAbsent()
}

// SetSource implements opt.Sourced.
func (d *Dir) SetSource(source string) { d.source = source }

// SetSourceGlob implements opt.SourceGlobable.
func (d *Dir) SetSourceGlob(pattern string) { d.sourceGlob = pattern }

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

var (
	_ opt.Sourced        = (*Dir)(nil)
	_ opt.SourceGlobable = (*Dir)(nil)
)

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

	if d.source != "" && d.sourceGlob != "" {
		logger.Fatal("directory %s: WithSource and WithSourceGlob are mutually exclusive", path)
	}

	return d, nil
}

// apply performs the idempotent OS work for d without registering a
// resource.
func (d *Dir) apply() error {
	if d.Absent {
		return ensureAbsent(d)
	}

	if err := ensureDirectorySelf(d); err != nil {
		return err
	}

	switch {
	case d.sourceGlob != "":
		if err := copySourceGlob(d); err != nil {
			return err
		}
		if d.prune {
			return pruneGlob(d)
		}
	case d.source != "":
		if err := copySourceTree(d); err != nil {
			return err
		}
		if d.prune {
			return pruneTree(d)
		}
	}

	return nil
}

// ensureDirectorySelf ensures d.path exists as a directory with the desired
// mode and ownership. It is idempotent: an existing directory only has its
// attributes re-enforced, and an existing non-directory is an error.
func ensureDirectorySelf(d *Dir) error {
	id := fmt.Sprintf("Directory[%s]", d.path)
	logger.Debug("processing directory: %s", d.path)

	info, err := os.Lstat(d.path)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", d.path)
		}
		logger.Debug("directory %s already exists", d.path)
		resource.Note(id, resource.StatusOK)
		if resource.DryRun() {
			return nil
		}

	case os.IsNotExist(err):
		if resource.DryRun() {
			resource.Note(id, resource.StatusWouldChange)
			logger.Info("dry-run: would create directory %s", d.path)
			return nil
		}
		logger.Debug("creating directory %s with mode %v", d.path, d.mode)
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d.path, err)
		}
		resource.Note(id, resource.StatusChanged)
		logger.Info("created directory %s", d.path)

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
	id := fmt.Sprintf("Directory[%s]", d.path)
	logger.Debug("ensuring absent: %s", d.path)

	if _, err := os.Lstat(d.path); os.IsNotExist(err) {
		logger.Debug("%s already absent", d.path)
		resource.Note(id, resource.StatusOK)
		return nil
	}

	if resource.DryRun() {
		resource.Note(id, resource.StatusWouldChange)
		logger.Info("dry-run: would remove %s", d.path)
		return nil
	}

	remove := os.Remove
	if d.prune {
		remove = os.RemoveAll
	}

	if err := remove(d.path); err != nil {
		if os.IsNotExist(err) {
			resource.Note(id, resource.StatusOK)
			return nil
		}
		return fmt.Errorf("failed to remove %s: %w", d.path, err)
	}

	resource.Note(id, resource.StatusChanged)
	logger.Info("removed %s", d.path)
	return nil
}

// applyAttributesTo is dir's own small chmod/chown helper, deliberately not
// shared with the file package so the two packages' attribute-application
// behavior can evolve independently.
func applyAttributesTo(path string, mode os.FileMode, usr, group string) error {
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("failed to chmod %s to %v: %w", path, mode, err)
	}
	logger.Debug("set mode %v for %s", mode, path)

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
	logger.Debug("set owner %s:%s for %s", usr, group, path)

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
		logger.Fatal("failed to apply directory resource %s: %v", path, err)
	}

	d.resource = resource.Register("Directory", d.path,
		resource.ApplierFunc(func() error { return d.apply() }), d.DependsOn.IDs...)

	return d.resource
}

func Absent(path string, opts ...opt.Option) resource.Resource {
	opts = append(opts, opt.IsAbsent)
	return Present(path, opts...)
}
