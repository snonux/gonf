// Package file implements the file resource: regular files from literal
// content or a source file, with optional template rendering.
package file

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/template"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

// File reconciles a regular file's existence, content (literal, source
// file, or rendered template), mode, ownership, and optional line edits.
type File struct {
	embed.DependsOn
	embed.Absence
	resource resource.Resource
	name     string
	path     string
	content  string
	source   string // bare path, no "source://" prefix
	// contentSet marks that WithContent or WithSource was called explicitly,
	// even with an empty value (WithContent("") or a source resolving to
	// zero bytes). It distinguishes legitimately empty content from a File
	// with neither configured, mirroring userSet/groupSet below.
	contentSet bool
	// param, when set, overrides the {{.Param}} value rendered into
	// template content (opt.WithParam). Callers that know a more stable
	// identity than this resource's mechanical source path — dir's tree
	// copies on the plan path, which would otherwise derive the ephemeral
	// blob-extraction path — use it; unset, Param stays the derived default.
	param string
	// template forces shouldRenderTemplate to report true regardless of any
	// ".tmpl" suffix on path/source (opt.WithTemplate). Plan apply sets it on
	// the destination-side File built from a file op recorded with
	// template:true: by then the wire content_b64/blob is raw template text
	// with no source set and a path already stripped of ".tmpl" (planDraft
	// records the stripped targetPath), so neither suffix check would fire
	// on its own.
	template        bool
	templateData    any
	templateDataSet bool
	// templateFacts feeds {{.Gonf.*}}: build() seeds it from
	// localTemplateFacts, and plan apply overrides it with the plan's
	// facts (ensureWithFacts) so -profile is honored.
	templateFacts   templateFacts
	user            string
	group           string
	userSet         bool // WithOwner was called explicitly (build()'s default does not count)
	groupSet        bool // WithGroup was called explicitly (build()'s default does not count)
	addLines        []string
	removeLines     []string
	mode            os.FileMode
	modeSet         bool
	preserveContent bool
	validationBin   string
	validationArgs  []string
	validationSet   bool
}

// SetName implements opt.Named. It overrides this resource's identity but
// never its on-disk target path.
func (f *File) SetName(name string) { f.name = name }

// SetContent implements opt.Contented. Setting literal content clears any
// previously configured source, as the two are mutually exclusive.
func (f *File) SetContent(content string) {
	f.content = content
	f.source = ""
	f.contentSet = true
}

// SetSource implements opt.Sourced. Setting a source clears any previously
// configured literal content, as the two are mutually exclusive.
func (f *File) SetSource(source string) {
	f.source = source
	f.content = ""
	f.contentSet = true
}

// SetParam implements opt.Paramable. It overrides the {{.Param}} value used
// when rendering template content; the default remains the derived source
// path (or literal content).
func (f *File) SetParam(param string) { f.param = param }

// SetTemplate implements opt.Templateable. It forces shouldRenderTemplate to
// report true even when neither path nor source ends in ".tmpl" — see the
// template field comment for why plan apply needs this.
func (f *File) SetTemplate() { f.template = true }

// SetTemplateData implements opt.TemplateDataable. Template data always
// implies template rendering, including for literal content without .tmpl.
func (f *File) SetTemplateData(data any) {
	f.templateData = data
	f.templateDataSet = true
	f.template = true
}

// SetValidation implements opt.Validatable. The arguments are copied because
// callers commonly reuse List values for command declarations.
func (f *File) SetValidation(bin string, args []string) {
	f.validationBin = bin
	f.validationArgs = slices.Clone(args)
	f.validationSet = true
}

// SetAddLine implements opt.LineAddable.
func (f *File) SetAddLine(line string) {
	f.AddLines(line)
}

// SetRemoveLine implements opt.LineRemovable.
func (f *File) SetRemoveLine(line string) {
	f.RemoveLines(line)
}

// AddLines implements opt.LinesAddable.
func (f *File) AddLines(lines ...string) { f.addLines = appendUniqueLines(f.addLines, lines...) }

// RemoveLines implements opt.LinesRemovable.
func (f *File) RemoveLines(lines ...string) {
	f.removeLines = appendUniqueLines(f.removeLines, lines...)
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
func (f *File) SetMode(mode os.FileMode) {
	f.mode = mode
	f.modeSet = true
}

var (
	_ opt.LineAddable      = (*File)(nil)
	_ opt.LineRemovable    = (*File)(nil)
	_ opt.LinesAddable     = (*File)(nil)
	_ opt.LinesRemovable   = (*File)(nil)
	_ opt.Named            = (*File)(nil)
	_ opt.Paramable        = (*File)(nil)
	_ opt.Templateable     = (*File)(nil)
	_ opt.TemplateDataable = (*File)(nil)
	_ opt.Validatable      = (*File)(nil)
)

func build(path string, opts ...opt.FileOption) (*File, error) {
	curr, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("failed to get current user for default: %w", err)
	}

	f := &File{
		path:          path,
		mode:          0o640,
		user:          curr.Username,
		group:         curr.Gid,
		templateFacts: localTemplateFacts(),
	}

	for _, o := range opts {
		o.Apply(f)
	}

	if f.lineEdit() && (f.contentSet || f.source != "") {
		return nil, fmt.Errorf("file %s: WithLine(s)/WithoutLine(s) cannot be combined with WithContent/WithSource", path)
	}
	if err := f.validateConfiguration(path); err != nil {
		return nil, err
	}

	return f, nil
}

func (f *File) validateConfiguration(path string) error {
	if !f.validationSet {
		return nil
	}
	if f.validationBin == "" {
		return fmt.Errorf("file %s: WithValidation requires a validator binary", path)
	}
	if f.Absent {
		return fmt.Errorf("file %s: WithValidation cannot combine with IsAbsent", path)
	}
	if f.lineEdit() {
		return fmt.Errorf("file %s: WithValidation cannot combine with WithLine(s)/WithoutLine(s)", path)
	}
	if !f.contentSet {
		return fmt.Errorf("file %s: WithValidation requires WithContent or WithSource", path)
	}
	if !filepath.IsAbs(f.targetPath()) {
		return fmt.Errorf("file %s: WithValidation requires an absolute target path", path)
	}
	if _, err := validationPathComponents(f.targetPath()); err != nil {
		return fmt.Errorf("file %s: invalid validation target path: %w", path, err)
	}
	count := 0
	for _, arg := range f.validationArgs {
		if arg == opt.CandidatePath {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("file %s: WithValidation arguments must contain CandidatePath exactly once", path)
	}
	return nil
}

func (f *File) lineEdit() bool {
	return len(f.addLines) != 0 || len(f.removeLines) != 0
}

func (f *File) reportID(path string) string {
	if f.name != "" {
		if f.preserveContent {
			return fmt.Sprintf("EnsureFile[%s]", f.name)
		}
		return fmt.Sprintf("File[%s]", f.name)
	}
	if f.preserveContent {
		return fmt.Sprintf("EnsureFile[%s]", path)
	}
	return fmt.Sprintf("File[%s]", path)
}

func (f *File) resourceName() string {
	if f.name != "" {
		return f.name
	}
	return f.targetPath()
}

func appendUniqueLines(dst []string, lines ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(lines))
	for _, line := range dst {
		seen[line] = struct{}{}
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		dst = append(dst, line)
	}
	return dst
}

// apply performs the idempotent OS work for f without registering a
// resource.
// Apply runs the file reconciliation directly for the legacy resource path.
func (f *File) Apply() error { return f.apply() }

func (f *File) apply() error {
	if f.Absent {
		return ensureAbsentWithID(f.targetPath(), f.reportID(f.targetPath()))
	}
	if f.preserveContent {
		if f.validationSet {
			return fmt.Errorf("file %s: WithValidation cannot combine with EnsureFile", f.path)
		}
		return f.ensurePresent()
	}

	if f.lineEdit() {
		finalPath, content, noop, err := f.resolveLine()
		if err != nil {
			return fmt.Errorf("failed to resolve line edits for %s: %w", f.path, err)
		}
		if noop {
			logger.Debug("no line edits needed for missing file %s", finalPath)
			resource.Note(f.reportID(finalPath), resource.StatusSkipped)
			return nil
		}
		return f.ensureValidatedFile(finalPath, content)
	}

	finalPath, content, err := f.resolveFromSourceOrContent()
	if err != nil {
		return fmt.Errorf("failed to resolve content for %s: %w", f.path, err)
	}

	return f.ensureValidatedFile(finalPath, content)
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
// ".tmpl", or rendering was forced explicitly (opt.WithTemplate — plan
// apply's way of carrying template intent recorded at RecordPlan time, since
// by apply time neither suffix survives on the wire; see the template field).
func (f *File) shouldRenderTemplate() bool {
	return f.template || strings.HasSuffix(f.path, ".tmpl") || strings.HasSuffix(f.source, ".tmpl")
}

// templateParam returns the {{.Param}} value used when rendering f as a
// template: the explicit WithParam override when set, else the bare source
// path when source-based, or the literal content otherwise. Shared by
// resolveFromSourceOrContent (direct/local apply, and dir's delegated
// per-file copies) and planDraft, which carries the same value onto the
// wire's template_param field so plan apply renders the identical
// {{.Param}} a direct run would.
func (f *File) templateParam() string {
	if f.param != "" {
		return f.param
	}
	if f.source != "" {
		return f.source
	}
	return f.content
}

// resolveLine applies WithoutLine then WithLine to the on-disk file.
// noop is true when the file is missing and only removal was requested
// (already absent — nothing to write). A non-regular, non-symlink entry at
// the target (planted FIFO, socket, device node, directory) is a loud user
// error naming the path and the entry type — line edits manage regular
// files, and unlike the content path there is no "replace with new
// content" semantics to fall back on; see readForLineEdit.
func (f *File) resolveLine() (path string, content []byte, noop bool, err error) {
	path = f.path
	raw, readErr := readForLineEdit(path)
	if readErr != nil {
		// errors.Is, not os.IsNotExist: the raw os error is wrapped on the
		// way out of readForLineEdit, and os.IsNotExist does not unwrap %w
		// chains.
		if !errors.Is(readErr, os.ErrNotExist) {
			return "", nil, false, readErr
		}
		if len(f.addLines) == 0 {
			return path, nil, true, nil
		}
		return path, []byte(strings.Join(f.addLines, "\n") + "\n"), false, nil
	}

	var kept []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		if slices.Contains(f.removeLines, line) {
			continue
		}
		kept = append(kept, line)
	}
	if err := scanner.Err(); err != nil {
		return "", nil, false, fmt.Errorf("failed to scan file %s: %w", path, err)
	}

	for _, add := range f.addLines {
		if !slices.Contains(kept, add) {
			kept = append(kept, add)
		}
	}

	if len(kept) == 0 {
		return path, []byte{}, false, nil
	}
	return path, []byte(strings.Join(kept, "\n") + "\n"), false, nil
}

// entryTypeName renders mode's type bits as a short human name for error
// messages: a policy refusal naming "FIFO" or "directory" reads better
// than os.FileMode's terse type rune ("p", "d").
func entryTypeName(mode os.FileMode) string {
	switch {
	case mode&os.ModeNamedPipe != 0:
		return "FIFO"
	case mode&os.ModeSocket != 0:
		return "unix socket"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode&os.ModeDevice != 0 && mode&os.ModeCharDevice != 0:
		return "character device"
	case mode&os.ModeDevice != 0:
		return "block device"
	case mode&os.ModeDir != 0:
		return "directory"
	case mode&os.ModeIrregular != 0:
		return "irregular file"
	default:
		return fmt.Sprintf("entry of type %v", mode.Type())
	}
}

// readFollowNonBlocking reads all bytes from the file at path, following a
// symlink at the final component exactly like the os.ReadFile calls it
// replaces, but so that neither the open nor the read can ever block on or
// stream from a non-regular entry:
//
//   - the open carries O_NONBLOCK, so it returns immediately even when a
//     FIFO sits at path — possible inside the caller's Lstat-to-open
//     window, or when path is a symlink to one;
//   - an fstat of the OPENED entry refuses anything that is not a regular
//     file before a single byte is read: a followed symlink may point at a
//     FIFO (whose read would error EAGAIN with a writer present and, worse,
//     misread as an empty file without one) or at a device node such as
//     /dev/zero, whose reads never end. fstat inspects the opened inode
//     itself, so an entry swapped in between the caller's Lstat and this
//     open cannot slip past the check either;
//   - a regular file reads identically with O_NONBLOCK set.
func readFollowNonBlocking(path string) ([]byte, error) {
	fd, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	defer func() { _ = fd.Close() }()

	if fi, statErr := fd.Stat(); statErr == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("file %s: cannot read from a %s at that path", path, entryTypeName(fi.Mode()))
	}
	raw, err := io.ReadAll(fd)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	return raw, nil
}

// readForLineEdit reads the current content at path for a line edit,
// preserving the former os.ReadFile semantics for everything the line-edit
// path manages:
//
//   - a missing path surfaces its os.ErrNotExist-able error (resolveLine
//     maps it to the noop/create behaviors);
//   - a symlink at the target is followed for the read — the line edit
//     applies to the target's content, and ensureFile then replaces the
//     symlink with the regular managed file, matching its replace rule;
//   - a regular file is read as before.
//
// Any other entry type at the target — planted FIFO, unix socket, device
// node, directory — is a loud user error naming the path and the entry
// type: line edits manage regular files, and unlike the content path there
// is no "replace with new content" semantics to fall back on. The read
// itself never blocks on a planted FIFO (see readFollowNonBlocking).
func readForLineEdit(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return nil, fmt.Errorf("file %s: cannot apply line edits to a %s", path, entryTypeName(info.Mode()))
	}
	return readFollowNonBlocking(path)
}

// readForSource reads the recipe-declared source file at path, preserving
// the former os.ReadFile semantics for regular files and symlinks (a
// symlink source's target is read through). A missing path and any other
// non-regular entry type — planted FIFO, unix socket, device node,
// directory — are loud errors naming the path: the source is content the
// recipe declared, never something gonf may replace, classify as changed,
// or read as empty. The read itself never blocks on a planted FIFO (see
// readFollowNonBlocking). This guards both the single-file WithSource path
// and dir's source-tree copies, which delegate every file with WithSource
// to Ensure (direct path) or EnsureWithPlanFacts (plan path); both go
// through build()/apply() and therefore through this read.
func readForSource(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read source file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return nil, fmt.Errorf("cannot read %s: not a regular file (found %s)", path, entryTypeName(info.Mode()))
	}
	return readFollowNonBlocking(path)
}

// resolveFromSourceOrContent reads f's content (from source or literal
// content), renders it as a template if applicable, and returns the final
// on-disk path alongside the resulting bytes. Param — the {{.Param}}
// template value — is the explicit WithParam override when set, else the
// bare source path when source-based (never a "source://"-prefixed string),
// or the literal content when content-based. One definition used by both
// the single-file path and by dir's per-file delegation: dir's plan-path
// tree copies pass the override so synced .tmpl files do not embed the
// ephemeral blob-extraction path.
func (f *File) resolveFromSourceOrContent() (string, []byte, error) {
	var content []byte
	if f.source != "" {
		read, err := readForSource(f.source)
		if err != nil {
			return "", nil, err
		}
		content = read
	} else {
		content = []byte(f.content)
	}

	if f.shouldRenderTemplate() {
		rendered, err := f.applyTemplateToContent(content, f.templateParam())
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

type templateFacts struct {
	GOOS     string
	Profile  string
	Hostname string
}

// templateContext is the stable destination template API. Gonf is reserved;
// user data cannot replace live destination facts.
type templateContext struct {
	GOOS     string
	Profile  string
	Hostname string
}

func (f *File) applyTemplateToContent(content []byte, param string) ([]byte, error) {
	data := make(map[string]any)
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if len(pair) == 2 {
			data[pair[0]] = pair[1]
		}
	}
	templateData, err := templateDataMap(f.templateData, f.templateDataSet)
	if err != nil {
		return nil, err
	}
	for key, value := range templateData {
		data[key] = value
	}
	data["Param"] = param
	data["Gonf"] = templateContext(f.templateFacts)

	tmpl, err := template.New("resource").Funcs(templateFuncMap()).Option("missingkey=error").Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("template parse error: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("template execute error: %w", err)
	}
	return buf.Bytes(), nil
}

func templateDataMap(value any, set bool) (map[string]any, error) {
	data := make(map[string]any)
	if !set {
		return data, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("template data must be JSON-compatible: %w", err)
	}
	normalized, err := decodeTemplateData(raw)
	if err != nil {
		return nil, err
	}
	if values, ok := normalized.(map[string]any); ok {
		for key, item := range values {
			data[key] = item
		}
	}
	// Data is the complete normalized value, even when a map input also has
	// a user field named "Data". The root-level flattened map is convenient,
	// but it must not replace the stable .Data escape hatch.
	data["Data"] = normalized
	return data, nil
}

// decodeTemplateData uses json.Number so JSON normalization does not round a
// large integer through float64 before text/template renders it.
func decodeTemplateData(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var data any
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("template data must be JSON-compatible: %w", err)
	}
	return data, nil
}

// localTemplateFacts is the fallback {{.Gonf.*}} source for the direct
// (non-plan) Ensure/Present path only, e.g. the deprecated resource.Apply
// repository path and unit tests. It mirrors api.DetectFacts but cannot
// honor api.SetProfileOverride: this package sits below api (api imports
// it), so it cannot call api.DetectFacts without an import cycle. Plan apply
// therefore never renders from it: every plan handler that writes template
// content (file, and sync_dir per synced entry) replaces these facts with
// plan.ApplyContext.Facts via EnsureWithPlanFacts (see
// resource/file/planwire.go), which api.ApplyPlan detected on the
// destination through api.DetectFacts, override included.
func localTemplateFacts() templateFacts {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	return templateFacts{
		GOOS:     runtime.GOOS,
		Profile:  localTemplateProfile(hostname),
		Hostname: hostname,
	}
}

func localTemplateProfile(hostname string) string {
	if strings.Contains(strings.ToLower(hostname), "rocky") {
		return "rocky"
	}
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "unknown"
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "ID=") {
			continue
		}
		id := strings.Trim(strings.TrimPrefix(scanner.Text(), "ID="), `"`)
		switch id {
		case "rocky", "centos", "rhel", "almalinux":
			return "rocky"
		case "":
			return "unknown"
		default:
			return id
		}
	}
	return "unknown"
}

func templateFuncMap() template.FuncMap {
	return template.FuncMap{
		"join":    templateJoin,
		"lower":   strings.ToLower,
		"replace": strings.ReplaceAll,
		"trim":    strings.TrimSpace,
		"upper":   strings.ToUpper,
	}
}

func templateJoin(values any, separator string) string {
	value := reflect.ValueOf(values)
	if !value.IsValid() || (value.Kind() != reflect.Array && value.Kind() != reflect.Slice) {
		return ""
	}
	parts := make([]string, value.Len())
	for i := range value.Len() {
		parts[i] = fmt.Sprint(value.Index(i).Interface())
	}
	return strings.Join(parts, separator)
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
		parsedUID, err := strconv.Atoi(u.Uid)
		if err != nil {
			return -1, -1, fmt.Errorf("failed to parse uid %s for user %s: %w", u.Uid, f.user, err)
		}
		uid = parsedUID
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

func ensureAbsent(path string) error {
	return ensureAbsentWithID(path, fmt.Sprintf("File[%s]", path))
}

func ensureAbsentWithID(path, id string) error {
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

// ensurePresent creates an empty regular file when path is absent. Existing
// regular files retain their content; only explicitly requested attributes
// are reconciled. This is the apply-side behavior of api.EnsureFile.
func (f *File) ensurePresent() error {
	path := f.targetPath()
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return f.ensureFile(path, []byte{})
	}
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("file %s: cannot ensure a %s while preserving content", path, entryTypeName(info.Mode()))
	}

	id := f.reportID(path)
	changed, err := f.explicitMetadataChanged(info)
	if err != nil {
		return err
	}
	if !changed {
		resource.Note(id, resource.StatusOK)
		return nil
	}
	if resource.DryRun() {
		resource.Note(id, resource.StatusWouldChange)
		return nil
	}

	attrs := *f
	if !attrs.modeSet {
		attrs.mode = info.Mode()
	}
	if !attrs.userSet {
		attrs.user = ""
	}
	if !attrs.groupSet {
		attrs.group = ""
	}
	if err := attrs.applyAttributesTo(path); err != nil {
		return err
	}
	resource.Note(id, resource.StatusChanged)
	return nil
}

// explicitMetadataChanged reports whether an explicitly configured mode,
// owner, or group differs from the existing regular file. Defaults selected
// by build() deliberately do not count: EnsureFile preserves pre-existing
// metadata unless the recipe requested a value.
func (f *File) explicitMetadataChanged(info fs.FileInfo) (bool, error) {
	if f.modeSet {
		const modeBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
		if info.Mode()&modeBits != f.mode&modeBits {
			return true, nil
		}
	}
	if !f.userSet && !f.groupSet {
		return false, nil
	}

	attrs := *f
	if !attrs.userSet {
		attrs.user = ""
	}
	if !attrs.groupSet {
		attrs.group = ""
	}
	uid, gid, err := attrs.ownerIDs()
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("failed to inspect ownership of %s", f.targetPath())
	}
	if uid != -1 && int(stat.Uid) != uid {
		return true, nil
	}
	if gid != -1 && int(stat.Gid) != gid {
		return true, nil
	}
	return false, nil
}

// Ensure builds and applies the file resource described by opts, without
// registering it. Used by other resource packages (e.g. dir) to write an
// individual file without it becoming its own top-level resource.
func Ensure(path string, opts ...opt.FileOption) error {
	f, err := build(path, opts...)
	if err != nil {
		return err
	}
	return f.apply()
}

// EnsurePresent applies EnsureFile semantics without registering a resource.
// It is used by the ensure_file plan handler.
func EnsurePresent(path string, opts ...opt.FileOption) error {
	f, err := build(path, opts...)
	if err != nil {
		return err
	}
	if f.contentSet || f.lineEdit() || f.Absent || f.validationSet {
		return fmt.Errorf("file %s: EnsureFile cannot combine WithContent/WithSource, WithLine(s)/WithoutLine(s), or IsAbsent", path)
	}
	f.preserveContent = true
	return f.apply()
}

// ensureWithFacts is Ensure with build()'s locally detected template facts
// replaced by facts; EnsureWithPlanFacts is its exported plan-apply wrapper.
func ensureWithFacts(path string, facts templateFacts, opts ...opt.FileOption) error {
	f, err := build(path, opts...)
	if err != nil {
		return err
	}
	f.templateFacts = facts
	return f.apply()
}

// Present registers a file resource that ensures path exists with the
// configured content, mode, and ownership, and records a plan draft for
// remote apply. A build failure (invalid option combination) is recipe
// misuse and fails fast via logger.Fatal at record time.
func Present(path string, opts ...opt.FileOption) resource.Resource {
	f, err := build(path, opts...)
	if err != nil {
		// build's error already names the path.
		logger.Fatal("%v", err)
	}

	f.resource = resource.Register("File", f.resourceName(), f, f.DependsOn.IDs...)
	resource.RecordPlanDraft(f.planDraft())
	return f.resource
}

// PresentEnsure registers an EnsureFile resource. It creates an empty file
// only when absent and otherwise preserves file content.
func PresentEnsure(path string, opts ...opt.FileOption) resource.Resource {
	f, err := build(path, opts...)
	if err != nil {
		logger.Fatal("%v", err)
	}
	if f.contentSet || f.lineEdit() || f.Absent || f.validationSet {
		logger.Fatal("file %s: EnsureFile cannot combine WithContent/WithSource, WithLine(s)/WithoutLine(s), or IsAbsent", path)
	}
	f.preserveContent = true
	f.resource = resource.Register("EnsureFile", f.resourceName(), f, f.DependsOn.IDs...)
	resource.RecordPlanDraft(f.planDraft())
	return f.resource
}

func (f *File) planDraft() resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:           "file",
		ID:             f.resource.ID(),
		Name:           f.name,
		Path:           f.targetPath(),
		Mode:           opt.ModeToWire(f.mode),
		Absent:         f.Absent,
		AddLines:       slices.Clone(f.addLines),
		RemoveLines:    slices.Clone(f.removeLines),
		ValidationBin:  f.validationBin,
		ValidationArgs: slices.Clone(f.validationArgs),
		Deps:           f.DependsOn.SortedIDs(),
	}
	if f.preserveContent {
		d.Kind = "ensure_file"
		d.Absent = false
		if !f.modeSet {
			d.Mode = ""
		}
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
	// HasContent flags that WithContent/WithSource was configured at all, so
	// packageDraft/applyFile can tell a legitimately empty file (content or
	// source resolving to zero bytes, which base64-encodes as "") apart from
	// an op with no content data recorded (a bug, not a valid empty file).
	switch {
	case f.source != "":
		d.SourcePath = f.source
		d.HasContent = true
	case f.contentSet:
		d.ContentB64 = base64.StdEncoding.EncodeToString([]byte(f.content))
		d.HasContent = true
	}
	// Template intent must travel on the wire explicitly: packageDraft
	// reads f.source's RAW bytes into content_b64/blob (below), and
	// targetPath above already stripped ".tmpl" from the recorded Path, so
	// neither field plan apply sees still carries the suffix
	// shouldRenderTemplate would otherwise key off. Without Template/
	// TemplateParam, plan apply (Run/push/cluster/fleet) would write the
	// literal unrendered template text to the destination.
	if f.shouldRenderTemplate() {
		d.Template = true
		d.TemplateParam = f.templateParam()
	}
	if f.templateDataSet {
		d.TemplateData = f.templateData
		d.TemplateDataSet = true
	}
	return d
}

// Absent registers a file resource that ensures path does not exist.
func Absent(path string, opts ...opt.FileOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(path, opts...)
}
