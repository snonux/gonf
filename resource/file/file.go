// Package file implements the file resource: regular files from literal
// content or a source file, with optional template rendering.
//
// Layout: file.go holds the File type, its option setters, build, identity,
// the apply dispatch and the Present/Ensure constructors. The rest is split
// by responsibility: template.go (content resolution and rendering),
// lineedit.go (WithLine(s)/WithoutLine(s)), read.go (non-blocking reads of
// existing and source content), checksum.go (atomic content writes),
// validation.go (WithValidation candidates), attributes.go (mode and
// ownership), ensure.go (absence and EnsureFile), planwire.go (plan draft,
// ToOp and plan apply) and target.go (unregistered Target for composites).
package file

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"

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
	// Register takes the value as a resource.Applier; asserting it here reports a
	// renamed or re-signed Apply at the declaration, not at the Register call.
	_ resource.Applier     = (*File)(nil)
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

func (f *File) reportID(path string) string {
	if f.name != "" {
		if f.preserveContent {
			return resource.FormatID("EnsureFile", f.name)
		}
		return resource.FormatID("File", f.name)
	}
	if f.preserveContent {
		return resource.FormatID("EnsureFile", path)
	}
	return resource.FormatID("File", path)
}

func (f *File) resourceName() string {
	if f.name != "" {
		return f.name
	}
	return f.targetPath()
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

// Absent registers a file resource that ensures path does not exist.
func Absent(path string, opts ...opt.FileOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(path, opts...)
}
