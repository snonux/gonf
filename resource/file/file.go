// Package file implements the file resource: regular files from literal
// content or a source file, with optional template rendering.
package file

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"text/template"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
)

type File struct {
	embed.DependsOn
	embed.Absence
	resource   resource.Resource
	path       string
	content    string
	source     string // bare path, no "source://" prefix
	user       string
	group      string
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

// SetOwner implements opt.Owner.
func (f *File) SetOwner(user string) { f.user = user }

// SetGroup implements opt.Grouped.
func (f *File) SetGroup(group string) { f.group = group }

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
		logger.Fatal("file %s: WithLine/WithoutLine cannot be combined with WithContent/WithSource", path)
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

	return f.targetPath(), content, nil // TODO: Why do we need targetPath()? cant we just return f.path?
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

func (f *File) applyAttributesTo(path string) error {
	if err := os.Chmod(path, f.mode); err != nil {
		return fmt.Errorf("failed to chmod %s to %v: %w", path, f.mode, err)
	}
	logger.Debug("set mode %v for %s", f.mode, path)

	uid, gid := -1, -1

	if f.user != "" {
		u, err := user.Lookup(f.user)
		if err != nil {
			return fmt.Errorf("failed to lookup user %s: %w", f.user, err)
		}
		uid, _ = strconv.Atoi(u.Uid)
	}

	if f.group != "" {
		gidInt, err := strconv.Atoi(f.group)
		if err != nil {
			return fmt.Errorf("group must be numeric for now: %s", f.group)
		}
		gid = gidInt
	}

	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("failed to chown %s to %s:%s: %w", path, f.user, f.group, err)
	}
	logger.Debug("set owner %s:%s for %s", f.user, f.group, path)

	return nil
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

func Present(path string, opts ...opt.Option) resource.Resource {
	f, err := build(path, opts...)
	if err != nil {
		logger.Fatal("failed to apply file resource %s: %v", path, err)
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
		Mode:       fmt.Sprintf("%#o", f.mode&os.ModePerm),
		Absent:     f.Absent,
		AddLine:    f.addLine,
		RemoveLine: f.removeLine,
	}
	switch {
	case f.content != "":
		d.ContentB64 = base64.StdEncoding.EncodeToString([]byte(f.content))
	case f.source != "":
		// Blob packaging is a later task; path ref is enough for record mode.
		d.Blob = f.source
	}
	return d
}

func Absent(path string, opts ...opt.Option) resource.Resource {
	opts = append(opts, opt.IsAbsent)
	return Present(path, opts...)
}
