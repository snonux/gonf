package file

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"
	"os/user"
	"strconv"
	"strings"
	"text/template"

	opt "codeberg.org/snonux/gonf/api/options"
	"codeberg.org/snonux/gonf/internal/resource"
	"codeberg.org/snonux/gonf/internal/resource/embed"
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

// TODO: Implement to the end
func (f *File) SetAddLine(line string) {
	f.addLine = line
}

// TODO: Implement to the end.
func (f *File) SetRemoveline(line string) {
	f.removeLine = line
}

// SetOwner implements opt.Owner.
func (f *File) SetOwner(user string) { f.user = user }

// SetGroup implements opt.Grouped.
func (f *File) SetGroup(group string) { f.group = group }

// SetMode implements opt.Moded.
func (f *File) SetMode(mode os.FileMode) { f.mode = mode }

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

	return f, nil
}

// apply performs the idempotent OS work for f without registering a
// resource.
func (f *File) apply() error {
	if f.Absent {
		return ensureAbsent(f.targetPath())
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

func (f *File) resolveLine() (string, []byte, error) {
	file, err := os.Open(f.path)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read file %s: %w", f.path, err)
	}
	defer file.Close()

	var sb strings.Builder
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == f.removeLine {
			continue
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return f.path, []byte(sb.String()), nil
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
	log.Printf("set mode %v for %s", f.mode, path)

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
	log.Printf("set owner %s:%s for %s", f.user, f.group, path)

	return nil
}

func ensureAbsent(path string) error {
	log.Printf("ensuring absent: %s", path)

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
		log.Fatalf("failed to apply file resource %s: %v", path, err)
	}

	f.resource = resource.Register("File", f.targetPath(),
		resource.ApplierFunc(func() error { return f.apply() }), f.DependsOn.IDs...)

	return f.resource
}

func Absent(path string, opts ...opt.Option) resource.Resource {
	opts = append(opts, opt.IsAbsent)
	return Present(path, opts...)
}
