// Package file implements the file resource: regular files from literal
// content or a source file, with optional template rendering.
package file

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/template"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
)

// File reconciles a regular file's existence, content (literal, source
// file, or rendered template), mode, ownership, and optional line edits.
type File struct {
	embed.DependsOn
	embed.Absence
	resource   resource.Resource
	path       string
	content    string
	source     string // bare path, no "source://" prefix
	user       string
	group      string
	userSet    bool // WithOwner was called explicitly (build()'s default does not count)
	groupSet   bool // WithGroup was called explicitly (build()'s default does not count)
	addLine    string
	removeLine string
	mode       os.FileMode
}

// SetContent implements opt.Contented. Setting literal content clears any
// previously configured source, as the two are mutually exclusive.
func (f *File) SetContent(content string) {
	f.content = content
	f.source = ""
}

// SetSource implements opt.Sourced. Setting a source clears any previously
// configured literal content, as the two are mutually exclusive.
func (f *File) SetSource(source string) {
	f.source = source
	f.content = ""
}

// SetAddLine implements opt.LineAddable.
func (f *File) SetAddLine(line string) {
	f.addLine = line
}

// SetRemoveLine implements opt.LineRemovable.
func (f *File) SetRemoveLine(line string) {
	f.removeLine = line
}

// SetOwner implements opt.Owner. It marks ownership as explicitly configured
// so plan recording carries it to the destination (build()'s user.Current()
// default stays unrecorded to avoid churning remote hosts to the ssh user).
func (f *File) SetOwner(user string) {
	f.user = user
	f.userSet = true
}

// SetGroup implements opt.Grouped. It marks group ownership as explicitly
// configured so plan recording carries it to the destination.
func (f *File) SetGroup(group string) {
	f.group = group
	f.groupSet = true
}

// SetMode implements opt.Moded.
func (f *File) SetMode(mode os.FileMode) { f.mode = mode }

var (
	_ opt.LineAddable   = (*File)(nil)
	_ opt.LineRemovable = (*File)(nil)
)

func build(path string, opts ...opt.Option) (*File, error) {
	curr, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("failed to get current user for default: %w", err)
	}

	f := &File{
		path:  path,
		mode:  0o640,
		user:  curr.Username,
		group: curr.Gid,
	}

	for _, o := range opts {
		o(f)
	}

	if f.lineEdit() && (f.content != "" || f.source != "") {
		return nil, fmt.Errorf("file %s: WithLine/WithoutLine cannot be combined with WithContent/WithSource", path)
	}

	return f, nil
}

func (f *File) lineEdit() bool {
	return f.addLine != "" || f.removeLine != ""
}

// apply performs the idempotent OS work for f without registering a
// resource.
func (f *File) apply() error {
	if f.Absent {
		return ensureAbsent(f.targetPath())
	}

	if f.lineEdit() {
		finalPath, content, noop, err := f.resolveLine()
		if err != nil {
			return fmt.Errorf("failed to resolve line edits for %s: %w", f.path, err)
		}
		if noop {
			logger.Debug("no line edits needed for missing file %s", finalPath)
			resource.Note(fmt.Sprintf("File[%s]", finalPath), resource.StatusSkipped)
			return nil
		}
		return f.ensureFile(finalPath, content)
	}

	finalPath, content, err := f.resolveFromSourceOrContent()
	if err != nil {
		return fmt.Errorf("failed to resolve content for %s: %w", f.path, err)
	}

	return f.ensureFile(finalPath, content)
}

// targetPath returns the actual on-disk path f writes to. It strips a
// trailing ".tmpl" suffix from the caller-given path whenever templating was
// triggered by a ".tmpl"-suffixed source, so a source foo.conf.tmpl never
// leaves a foo.conf.tmpl behind on disk — whether that source is written via
// a direct Have/Ensure call or delegated to from a directory source-tree
// copy, since both routes go through this same function.
func (f *File) targetPath() string {
	if strings.HasSuffix(f.source, ".tmpl") {
		return strings.TrimSuffix(f.path, ".tmpl")
	}
	return f.path
}

// shouldRenderTemplate reports whether content should be rendered through
// text/template: either the destination path or the source path ends in
// ".tmpl".
func (f *File) shouldRenderTemplate() bool {
	return strings.HasSuffix(f.path, ".tmpl") || strings.HasSuffix(f.source, ".tmpl")
}

// resolveLine applies WithoutLine then WithLine to the on-disk file.
// noop is true when the file is missing and only removal was requested
// (already absent — nothing to write).
func (f *File) resolveLine() (path string, content []byte, noop bool, err error) {
	path = f.path
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return "", nil, false, fmt.Errorf("failed to read file %s: %w", path, readErr)
		}
		if f.addLine == "" {
			return path, nil, true, nil
		}
		return path, []byte(f.addLine + "\n"), false, nil
	}

	var kept []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		if f.removeLine != "" && line == f.removeLine {
			continue
		}
		kept = append(kept, line)
	}
	if err := scanner.Err(); err != nil {
		return "", nil, false, fmt.Errorf("failed to scan file %s: %w", path, err)
	}

	if f.addLine != "" {
		found := false
		for _, line := range kept {
			if line == f.addLine {
				found = true
				break
			}
		}
		if !found {
			kept = append(kept, f.addLine)
		}
	}

	if len(kept) == 0 {
		return path, []byte{}, false, nil
	}
	return path, []byte(strings.Join(kept, "\n") + "\n"), false, nil
}

// resolveFromSourceOrContent reads f's content (from source or literal content), renders it as
// a template if applicable, and returns the final on-disk path alongside the
// resulting bytes. Param is always the bare source path when source-based
// (never a "source://"-prefixed string), or the literal content when
// content-based — one definition used by both the single-file path and by
// dir's per-file delegation.
func (f *File) resolveFromSourceOrContent() (string, []byte, error) {
	var content []byte
	param := f.content

	if f.source != "" {
		bytes, err := os.ReadFile(f.source)
		if err != nil {
			return "", nil, fmt.Errorf("failed to read source file %s: %w", f.source, err)
		}
		content = bytes
		param = f.source
	} else {
		content = []byte(f.content)
	}

	if f.shouldRenderTemplate() {
		rendered, err := f.applyTemplateToContent(content, param)
		if err != nil {
			return "", nil, err
		}
		content = rendered
	}

	// targetPath, not f.path: it strips a ".tmpl" suffix from the
	// caller-given path when the source triggered templating (dir's
	// copySourceTree passes the source's ".tmpl" suffix through); see
	// targetPath.
	return f.targetPath(), content, nil
}

func (f *File) applyTemplateToContent(content []byte, param string) ([]byte, error) {
	data := make(map[string]string)
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if len(pair) == 2 {
			data[pair[0]] = pair[1]
		}
	}
	data["Param"] = param

	tmpl, err := template.New("resource").Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("template parse error: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("template execute error: %w", err)
	}
	return buf.Bytes(), nil
}

// applyAttributesTo sets f's mode and ownership on the regular file at
// path. It opens the path with O_NOFOLLOW (plus O_NONBLOCK so a planted
// FIFO cannot hang the open) and applies the changes to the opened file
// descriptor, so the kernel refuses — ELOOP — to open a symlink planted at
// path instead of following it: a planted symlink is never followed, and a
// swap into the window between the caller's atomic rename and this open
// cannot escalate either (ELOOP, or chmod of an inode inside the already
// attacker-controlled directory). A symlink at the target path is expected
// to have been replaced by the managed regular file via the atomic write
// path.
//
// The owner is resolved via user.Lookup (name) and the group via a numeric
// parse first and user.LookupGroup (name) second, so both WithGroup("1")
// and WithGroup("daemon") work; an unresolvable group is an error.
//
// Chown runs BEFORE chmod deliberately: Linux clears the setuid/setgid bits
// on every unprivileged chown of a non-directory (even when the owner/group
// values are unchanged, which is the common case because build() defaults to
// the current user), so a chmod followed by chown would silently strip the
// special bits it had just set. Ending on the chmod means the requested
// ModeSetuid/ModeSetgid/ModeSticky flags are the final state on disk.
func (f *File) applyAttributesTo(path string) error {
	fd, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		// POSIX denies the owner O_RDONLY on a file whose mode lacks
		// owner-read (e.g. WithMode(0o000)), so a non-root run cannot open
		// its own unreadable file for the fd-based application above.
		// That is the one case where the old path-based os.Chmod worked and
		// this open does not, so fall back to the path (still refusing
		// symlinks; see applyAttributesViaPath).
		if errors.Is(err, fs.ErrPermission) {
			return f.applyAttributesViaPath(path, err)
		}
		return fmt.Errorf("failed to open %s for attribute changes: %w", path, err)
	}
	defer func() { _ = fd.Close() }()

	uid, gid, err := f.ownerIDs()
	if err != nil {
		return err
	}

	if err := fd.Chown(uid, gid); err != nil {
		return fmt.Errorf("failed to chown %s to %s:%s: %w", path, f.user, f.group, err)
	}
	logger.Debug("set owner %s:%s for %s", f.user, f.group, path)

	// Chmod last: Go maps ModeSetuid/ModeSetgid/ModeSticky to the raw
	// S_ISUID/S_ISGID/S_ISVTX syscall bits, so the full mode (including any
	// special bits normalized into f.mode) lands as the final state.
	if err := fd.Chmod(f.mode); err != nil {
		return fmt.Errorf("failed to chmod %s to %v: %w", path, f.mode, err)
	}
	logger.Debug("set mode %v for %s", f.mode, path)

	return nil
}

// ownerIDs resolves f's configured user/group into the numeric ids for the
// chown calls: -1 for unset values leaves the respective owner unchanged.
// Shared by the fd-based applyAttributesTo and its path-based fallback.
func (f *File) ownerIDs() (uid, gid int, err error) {
	uid, gid = -1, -1

	if f.user != "" {
		u, err := user.Lookup(f.user)
		if err != nil {
			return -1, -1, fmt.Errorf("failed to lookup user %s: %w", f.user, err)
		}
		uid, _ = strconv.Atoi(u.Uid)
	}

	if f.group != "" {
		if gid, err = resolveGroupID(f.group); err != nil {
			return -1, -1, err
		}
	}

	return uid, gid, nil
}

// applyAttributesViaPath applies f's ownership and mode to the regular file
// at path with path-based os.Chown/os.Chmod calls. It is the fallback for
// the permission-denied case of applyAttributesTo's O_NOFOLLOW open, which
// POSIX restricts to callers with owner-read access on the target mode
// (non-root cannot open its own 0o000 file, root's CAP_DAC_OVERRIDE makes
// the open succeed — so the fallback is reachable only by non-root runs).
//
// The fallback cannot widen the symlink guarantee: an Lstat first refuses
// (by returning the original open error) when a symlink sits at path, so
// the path-based calls — which would follow a final symlink — never run on
// one, and a non-regular entry is refused the same way. The residual race
// between Lstat and chmod is bounded for a non-root caller: unprivileged
// chown/chmod only succeed on entries the caller already owns, so a swap
// into that window cannot touch anything the caller does not own (the same
// exposure the pre-O_NOFOLLOW path-based implementation had on every apply).
//
// Chown runs before chmod, mirroring the fd-based path: the special bits
// must survive the chown, and the chmod is the final state.
func (f *File) applyAttributesViaPath(path string, openErr error) error {
	info, err := os.Lstat(path)
	if err != nil {
		// The entry is gone or unstatable since the open failed: surface
		// the original open error.
		return fmt.Errorf("failed to open %s for attribute changes: %w", path, openErr)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		// A symlink at the target is never followed, and a non-regular
		// entry is not what the caller verified either: surface the
		// original open error instead of path-based chown/chmod.
		return fmt.Errorf("failed to open %s for attribute changes: %w", path, openErr)
	}

	uid, gid, err := f.ownerIDs()
	if err != nil {
		return err
	}

	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("failed to chown %s to %s:%s: %w", path, f.user, f.group, err)
	}
	logger.Debug("set owner %s:%s for %s (path fallback)", f.user, f.group, path)

	if err := os.Chmod(path, f.mode); err != nil {
		return fmt.Errorf("failed to chmod %s to %v: %w", path, f.mode, err)
	}
	logger.Debug("set mode %v for %s (path fallback)", f.mode, path)

	return nil
}

// resolveGroupID resolves a configured group to a numeric gid: numeric
// strings pass through strconv.Atoi, anything else is looked up by name via
// os/user (works with and without cgo on the supported unix targets). Both
// paths wrap failures with the offending group name.
func resolveGroupID(group string) (int, error) {
	gidInt, err := strconv.Atoi(group)
	if err == nil {
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

func ensureAbsent(path string) error {
	id := fmt.Sprintf("File[%s]", path)
	logger.Debug("ensuring absent: %s", path)

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

// Ensure builds and applies the file resource described by opts, without
// registering it. Used by other resource packages (e.g. dir) to write an
// individual file without it becoming its own top-level resource.
func Ensure(path string, opts ...opt.Option) error {
	f, err := build(path, opts...)
	if err != nil {
		return err
	}
	return f.apply()
}

// Present registers a file resource that ensures path exists with the
// configured content, mode, and ownership, and records a plan draft for
// remote apply. A build failure (invalid option combination) is recipe
// misuse and fails fast via logger.Fatal at record time.
func Present(path string, opts ...opt.Option) resource.Resource {
	f, err := build(path, opts...)
	if err != nil {
		// build's error already names the path.
		logger.Fatal("%v", err)
	}

	f.resource = resource.Register("File", f.targetPath(),
		resource.ApplierFunc(func() error { return f.apply() }), f.DependsOn.IDs...)
	resource.RecordPlanDraft(f.planDraft())
	return f.resource
}

func (f *File) planDraft() resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:       "file",
		ID:         f.resource.ID(),
		Path:       f.targetPath(),
		Mode:       opt.ModeToWire(f.mode),
		Absent:     f.Absent,
		AddLine:    f.addLine,
		RemoveLine: f.removeLine,
		Deps:       f.DependsOn.SortedIDs(),
	}
	// Only explicitly configured ownership is recorded: build()'s
	// user.Current() default must not be pushed to remote hosts. Absent files
	// are removed, so ownership would be dead wire data.
	if !f.Absent {
		if f.userSet {
			d.Owner = f.user
		}
		if f.groupSet {
			d.Group = f.group
		}
	}
	switch {
	case f.content != "":
		d.ContentB64 = base64.StdEncoding.EncodeToString([]byte(f.content))
	case f.source != "":
		d.SourcePath = f.source
	}
	return d
}

// Absent registers a file resource that ensures path does not exist.
func Absent(path string, opts ...opt.Option) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(path, opts...)
}
