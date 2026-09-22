// Package dir implements the directory resource, including copying and
// pruning source trees.
package dir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

// Dir reconciles a directory's existence, mode, and ownership, and
// optionally mirrors a controller-local WithSource tree or WithSourceGlob
// pattern into it.
//
// The Sensitivity embed backs WithSensitive: the plan's secret scan never
// reads a synced tree, so a tree holding secret material is declared with
// it. The recorded op is then sensitive, and every copied entry is written
// as a sensitive File (template and validator details withheld). On a
// directory without a source it only marks the op.
type Dir struct {
	embed.DependsOn
	embed.Absence
	embed.Sensitivity
	embed.Misuse
	resource   resource.Resource
	path       string
	source     string
	sourceGlob string
	// sourceBase is the recipe's declared source directory passed by plan
	// apply (opt.WithSourceBase): the stable {{.Param}} base for .tmpl
	// entries copied from a synced tree or glob blob (glob entries use
	// their basename below it). Empty on the direct path, which derives
	// Param from the real source paths as before.
	sourceBase string
	// planFacts are the plan apply's destination facts
	// (plan.ApplyContext.Facts), set only by the sync_dir handler via
	// ensureWithPlanFacts. Non-nil, every copied file renders
	// {{.Gonf.*}} from them (file.EnsureWithPlanFacts), exactly like a
	// single-file op of the same apply; nil on the direct path, where
	// file.Ensure detects facts locally as before.
	planFacts *plan.Facts
	user      string
	group     string
	userSet   bool        // WithOwner was called explicitly (build()'s default does not count)
	groupSet  bool        // WithGroup was called explicitly (build()'s default does not count)
	mode      os.FileMode // this directory's own mode, default 0o750
	fileMode  os.FileMode // mode for regular files copied from source, default 0o640
	prune     bool        // reconciles extra dest files during a source copy, and recursive-remove during IsAbsent()
}

// SetSource implements opt.Sourced.
func (d *Dir) SetSource(source string) { d.source = source }

// SetSourceGlob implements opt.SourceGlobable.
func (d *Dir) SetSourceGlob(pattern string) { d.sourceGlob = pattern }

// SetSourceBase implements opt.SourceBaseable. Plan apply passes the
// recipe's declared source directory so .tmpl entries inside a synced tree
// render {{.Param}} with a stable identity (declared dir plus the entry's
// relative path) instead of the ephemeral blob-extraction path.
func (d *Dir) SetSourceBase(base string) { d.sourceBase = base }

// SetOwner implements opt.Owner. It marks ownership as explicitly configured
// so plan recording carries it to the destination (build()'s user.Current()
// default stays unrecorded to avoid churning remote hosts to the ssh user).
func (d *Dir) SetOwner(user string) {
	d.user = user
	d.userSet = true
}

// SetGroup implements opt.Grouped. It marks group ownership as explicitly
// configured so plan recording carries it to the destination.
func (d *Dir) SetGroup(group string) {
	d.group = group
	d.groupSet = true
}

// SetMode implements opt.Moded (the directory's own mode).
func (d *Dir) SetMode(mode os.FileMode) { d.mode = mode }

// SetFileMode implements opt.FileModed (mode for files copied from source).
func (d *Dir) SetFileMode(mode os.FileMode) { d.fileMode = mode }

// SetPrune implements opt.Prunable.
func (d *Dir) SetPrune() { d.prune = true }

var (
	// Register takes the value as a resource.Applier; asserting it here reports a
	// renamed or re-signed Apply at the declaration, not at the Register call.
	_ resource.Applier   = (*Dir)(nil)
	_ opt.Sourced        = (*Dir)(nil)
	_ opt.SourceGlobable = (*Dir)(nil)
	_ opt.SourceBaseable = (*Dir)(nil)
	_ opt.Sensitivable   = (*Dir)(nil)
	_ opt.MisuseReporter = (*Dir)(nil)
)

func build(path string, opts ...opt.DirOption) (*Dir, error) {
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
		o.Apply(d)
	}
	// An option misuse (e.g. WithContent on a directory) is this build's error.
	if err := d.MisuseErr(); err != nil {
		return nil, err
	}

	if d.source != "" && d.sourceGlob != "" {
		return nil, fmt.Errorf("directory %s: WithSource and WithSourceGlob are mutually exclusive", path)
	}

	return d, nil
}

// apply performs the idempotent OS work for d without registering a
// resource.
// Apply runs the directory reconciliation directly. It makes the value Present
// registers a resource.Applier; since the repository apply was retired (task
// e72) nothing calls it through the repository, and the plan engine applies
// the kind through its plan handler instead.
func (d *Dir) Apply() error { return d.apply() }

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
//
// Like the file resource, the dir resource never follows a symlink at its
// target path. A directory cannot be replaced atomically the way a file can
// (rename-over would lose the managed children), so a symlink at the final
// path component is refused with a loud error instead of being replaced; a
// legit symlinked directory must be managed by renaming it out of the way
// in the configuration itself. The kernel-side backstop for the Lstat →
// attribute window is applyAttributesTo's O_NOFOLLOW|O_DIRECTORY open.
func ensureDirectorySelf(d *Dir) error {
	id := resource.FormatID("Directory", d.path)
	logger.Debug("processing directory: %s", d.path)

	info, err := os.Lstat(d.path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; dir resources never follow or manage a symlinked directory", d.path)
		}
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
		// os.MkdirAll resolves intermediate path components through the
		// kernel like any other path lookup, so an intermediate symlinked
		// directory is the admin's configured path. The FINAL component is
		// what applyAttributesTo's O_NOFOLLOW|O_DIRECTORY open protects: a
		// symlink planted there between MkdirAll and the open is refused
		// (ELOOP) instead of followed.
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
	id := resource.FormatID("Directory", d.path)
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
//
// It opens the path with O_NOFOLLOW|O_DIRECTORY and applies the changes to
// the opened file descriptor, so the kernel refuses — ELOOP for a symlink,
// ENOTDIR for any other non-directory — to open a symlink planted at path
// instead of following it: a planted symlink is never followed, and a swap
// into the window between the caller's Lstat/MkdirAll check and this open
// cannot escalate either (the swapped-in entry is simply refused). A symlink
// at the target path is never replaced by the dir resource (a rename-over
// would lose the managed children); it is refused loudly by the caller's
// Lstat check and by this open.
//
// O_NONBLOCK is not needed here: unlike FIFOs, directories cannot block
// indefinitely on open.
//
// The owner is resolved via user.Lookup (name) and the group via a numeric
// parse first and user.LookupGroup (name) second, so both WithGroup("1")
// and WithGroup("daemon") work; an unresolvable group is an error.
//
// Chown runs BEFORE chmod deliberately, mirroring file's applyAttributesTo:
// ending on the chmod means a requested setgid/setuid bit (Go
// ModeSetgid/ModeSetuid flags inside mode) is the final state on disk and
// cannot be cleared by the chown that follows. For directories Linux leaves
// the group-inheritance setgid bit alone on chown, but the chmod-last order
// keeps both packages correct regardless.
func applyAttributesTo(path string, mode os.FileMode, usr, group string) error {
	fd, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		// POSIX denies the owner O_RDONLY on a directory whose mode lacks
		// owner-read (e.g. 0o000-0o300), so a non-root run cannot open its
		// own unreadable directory for the fd-based application above.
		// That is the one case where the old path-based os.Chmod worked and
		// this open does not, so fall back to the path (still refusing
		// symlinks; see applyAttributesViaPath).
		if errors.Is(err, fs.ErrPermission) {
			return applyAttributesViaPath(path, mode, usr, group, err)
		}
		return fmt.Errorf("failed to open %s for attribute changes: %w", path, err)
	}
	defer func() { _ = fd.Close() }()

	uid, gid, err := ownerIDs(usr, group)
	if err != nil {
		return err
	}

	if err := fd.Chown(uid, gid); err != nil {
		return fmt.Errorf("failed to chown %s to %s:%s: %w", path, usr, group, err)
	}
	logger.Debug("set owner %s:%s for %s", usr, group, path)

	// Chmod last: Go maps ModeSetuid/ModeSetgid/ModeSticky to the raw
	// S_ISUID/S_ISGID/S_ISVTX syscall bits, so the full mode (including any
	// special bits normalized into mode) lands as the final state.
	if err := fd.Chmod(mode); err != nil {
		return fmt.Errorf("failed to chmod %s to %v: %w", path, mode, err)
	}
	logger.Debug("set mode %v for %s", mode, path)

	return nil
}

// ownerIDs resolves a configured user/group pair into the numeric ids for
// the chown calls: -1 for unset values leaves the respective owner
// unchanged. Shared by the fd-based applyAttributesTo and its path-based
// fallback. Mirrors file's helper; the two stay independent so the packages
// can evolve separately.
func ownerIDs(usr, group string) (uid, gid int, err error) {
	uid, gid = -1, -1

	if usr != "" {
		u, err := user.Lookup(usr)
		if err != nil {
			return -1, -1, fmt.Errorf("failed to lookup user %s: %w", usr, err)
		}
		parsedUID, err := strconv.Atoi(u.Uid)
		if err != nil {
			return -1, -1, fmt.Errorf("failed to parse uid %s for user %s: %w", u.Uid, usr, err)
		}
		uid = parsedUID
	}

	if group != "" {
		if gid, err = resolveGroupID(group); err != nil {
			return -1, -1, err
		}
	}

	return uid, gid, nil
}

// applyAttributesViaPath applies ownership and mode to the directory at
// path with path-based os.Chown/os.Chmod calls. It is the fallback for the
// permission-denied case of applyAttributesTo's O_NOFOLLOW|O_DIRECTORY
// open, which POSIX restricts to callers with owner-read access on the
// target mode (non-root cannot open its own 0o000-0o300 directory, root's
// CAP_DAC_OVERRIDE makes the open succeed — so the fallback is reachable
// only by non-root runs).
//
// The fallback cannot widen the symlink guarantee: an Lstat first refuses
// (by returning the original open error) when a symlink sits at path, so
// the path-based calls — which would follow a final symlink — never run on
// one, and a non-directory is refused the same way. The residual race
// between Lstat and chmod is bounded for a non-root caller: unprivileged
// chown/chmod only succeed on entries the caller already owns, so a swap
// into that window cannot touch anything the caller does not own (the same
// exposure the pre-O_NOFOLLOW path-based implementation had on every apply).
//
// Chown runs before chmod, mirroring the fd-based path: the special bits
// must survive the chown, and the chmod is the final state.
func applyAttributesViaPath(path string, mode os.FileMode, usr, group string, openErr error) error {
	info, err := os.Lstat(path)
	if err != nil {
		// The entry is gone or unstatable since the open failed: surface
		// the original open error.
		return fmt.Errorf("failed to open %s for attribute changes: %w", path, openErr)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		// A symlink at the target is never followed, and a non-directory
		// is not what the caller verified either: surface the original
		// open error instead of path-based chown/chmod.
		return fmt.Errorf("failed to open %s for attribute changes: %w", path, openErr)
	}

	uid, gid, err := ownerIDs(usr, group)
	if err != nil {
		return err
	}

	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("failed to chown %s to %s:%s: %w", path, usr, group, err)
	}
	logger.Debug("set owner %s:%s for %s (path fallback)", usr, group, path)

	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("failed to chmod %s to %v: %w", path, mode, err)
	}
	logger.Debug("set mode %v for %s (path fallback)", mode, path)

	return nil
}

// resolveGroupID resolves a configured group to a numeric gid: numeric
// strings pass through strconv.Atoi, anything else is looked up by name via
// os/user (works with and without cgo on the supported unix targets). Both
// paths wrap failures with the offending group name. Mirrors file's helper;
// the two stay independent so the packages can evolve separately.
func resolveGroupID(group string) (int, error) {
	gidInt, err := strconv.Atoi(group)
	if err == nil {
		if gidInt < 0 {
			// chown(uid, -1) would silently leave the group unchanged —
			// surprising for an explicitly configured group.
			return 0, fmt.Errorf("invalid gid %d for group %s", gidInt, group)
		}
		return gidInt, nil
	}
	g, lookupErr := user.LookupGroup(group)
	if lookupErr != nil {
		return 0, fmt.Errorf("failed to resolve group %s: %w", group, lookupErr)
	}
	gidInt, err = strconv.Atoi(g.Gid)
	if err != nil {
		return 0, fmt.Errorf("failed to parse gid %s for group %s: %w", g.Gid, group, err)
	}
	return gidInt, nil
}

// Ensure builds and applies the directory resource described by opts,
// without registering it.
func Ensure(path string, opts ...opt.DirOption) error {
	d, err := build(path, opts...)
	if err != nil {
		return err
	}
	return d.apply()
}

// ensureWithPlanFacts is Ensure for plan apply: facts (the apply's
// plan.ApplyContext.Facts) reach every templated entry of a synced tree, so
// {{.Gonf.*}} matches what the file handler renders for a standalone file op
// in the same apply (see Dir.planFacts). facts is copied, so the caller's
// value cannot change under a running apply.
func ensureWithPlanFacts(path string, facts plan.Facts, opts ...opt.DirOption) error {
	d, err := build(path, opts...)
	if err != nil {
		return err
	}
	d.planFacts = &facts
	return d.apply()
}

// Present registers a directory resource that ensures path exists with the
// configured mode, ownership, and source content, and records a plan draft
// for remote apply. A build failure (invalid option combination, option
// misuse) is recipe misuse: it is reported as a declaration error
// (resource.Refuse) and nothing is registered.
func Present(path string, opts ...opt.DirOption) resource.Resource {
	d, err := build(path, opts...)
	if err != nil {
		// build's error already names the path.
		return resource.Refuse("Directory", path, err)
	}

	d.resource = resource.Register("Directory", d.path, d, d.DependsOn.IDs...)
	resource.RecordPlanDraft(d.planDraft())
	return d.resource
}

func (d *Dir) planDraft() resource.PlanDraft {
	draft := resource.PlanDraft{
		ID:     d.resource.ID(),
		Path:   d.path,
		Mode:   opt.ModeToWire(d.mode),
		Absent: d.Absent,
		Prune:  d.prune,
		Deps:   d.DependsOn.SortedIDs(),
		// Sensitive is the explicit WithSensitive: the scan does not read
		// a synced tree's blob, so only the recipe can mark it.
		Sensitive: d.Sensitive,
	}
	// Only explicitly configured ownership is recorded: build()'s
	// user.Current() default must not be pushed to remote hosts. Absent
	// directories are removed, so ownership would be dead wire data. The
	// sync_dir kinds carry the dir's owner/group at the op level; destination
	// apply forwards them to every copied file via dir's per-file delegation.
	if !d.Absent {
		if d.userSet {
			draft.Owner = d.user
		}
		if d.groupSet {
			draft.Group = d.group
		}
	}
	switch {
	case d.sourceGlob != "":
		draft.Kind = "sync_dir"
		draft.SourceGlob = d.sourceGlob
		// The declared source directory (the glob pattern's directory)
		// travels on the sync_dir op as source_dir: destination apply
		// renders tree .tmpl files' {{.Param}} from it — stable across plan
		// runs instead of the ephemeral blob path. Blob packaging stays
		// driven by SourceGlob (packageDraft checks it before SourceDir);
		// the flat glob blob restores as a plain tree whose entries sit at
		// the root, so the per-file param is dir(glob) + "/" + basename.
		draft.SourceDir = filepath.ToSlash(filepath.Dir(d.sourceGlob))
		draft.FileMode = opt.ModeToWire(d.fileMode)
	case d.source != "":
		draft.Kind = "sync_dir"
		draft.SourceDir = filepath.ToSlash(d.source)
		draft.FileMode = opt.ModeToWire(d.fileMode)
	default:
		draft.Kind = "dir"
	}
	return draft
}

// Absent registers a directory resource that ensures path does not exist;
// WithPrune makes the removal recursive.
func Absent(path string, opts ...opt.DirOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(path, opts...)
}

// EnsurePlanDraft builds an ensure_dir PlanDraft without registering or applying.
// Used by api.EnsureDir in plan-record mode.
func EnsurePlanDraft(path string, opts ...opt.DirOption) (resource.PlanDraft, error) {
	d, err := build(path, opts...)
	if err != nil {
		return resource.PlanDraft{}, err
	}
	draft := resource.PlanDraft{
		Kind: "ensure_dir",
		Path: d.path,
		Mode: opt.ModeToWire(d.mode),
		ID:   resource.FormatID("EnsureDir", d.path),
		Deps: d.DependsOn.SortedIDs(),
		// ensure_dir carries no payload; an explicit WithSensitive still
		// marks the op, as on every kind that accepts the option.
		Sensitive: d.Sensitive,
	}
	// Same rule as planDraft: only explicitly configured ownership is
	// recorded, never build()'s user.Current() default.
	if d.userSet {
		draft.Owner = d.user
	}
	if d.groupSet {
		draft.Group = d.group
	}
	return draft, nil
}
