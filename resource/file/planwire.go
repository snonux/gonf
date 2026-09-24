package file

import (
	"encoding/base64"
	"fmt"
	"slices"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// planHandler is the file kind's plan.Handler: see resource/pkg/planwire.go
// for why record-time ToOp and apply-time Apply live together in the
// resource package instead of api/packager.go's draftToOp and plan/apply.go's
// applyFile.
type planHandler struct{}
type ensureFileHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindFile, planHandler{})
	plan.RegisterHandler(plan.KindEnsureFile, ensureFileHandler{})
}

// ToOp lowers a "file" resource draft to a plan.Op. The file-exclusive
// fields come from d.Payload (Payload, task w62 Layer 1) and are built into
// a plan.FilePayload (task ae2 Layer 2) set once on the returned Op; a
// "file" draft without a file.Payload is a record-time bug (planDraft
// always sets it), reported like any other handler error rather than
// panicking.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	p, ok := d.Payload.(Payload)
	if !ok {
		return plan.Op{}, fmt.Errorf("file: draft missing file.Payload (got %T)", d.Payload)
	}
	fp := plan.FilePayload{
		ContentB64:     p.ContentB64,
		HasContent:     p.HasContent,
		Template:       p.Template,
		TemplateParam:  p.TemplateParam,
		ValidationBin:  p.ValidationBin,
		ValidationArgs: slices.Clone(p.ValidationArgs),
		AddLines:       slices.Clone(p.AddLines),
		RemoveLines:    slices.Clone(p.RemoveLines),
		KeyedLines:     wireKeyedLines(p.KeyedLines),
	}
	if p.TemplateDataSet {
		if p.TemplateDataErr != nil {
			return plan.Op{}, fmt.Errorf("file: template data must be JSON-compatible: %w", p.TemplateDataErr)
		}
		fp.TemplateData = slices.Clone(p.TemplateData)
	}
	return plan.Op{
		Op:      plan.KindFile,
		ID:      d.ID,
		Name:    d.Name,
		Path:    d.Path,
		Mode:    d.Mode,
		Owner:   d.Owner,
		Group:   d.Group,
		Blob:    d.Blob,
		Absent:  d.Absent,
		Deps:    slices.Clone(d.Deps),
		Payload: fp,
	}, nil
}

// ToOp lowers an ensure_file resource draft to a plan.Op.
func (ensureFileHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:    plan.KindEnsureFile,
		ID:    d.ID,
		Name:  d.Name,
		Path:  d.Path,
		Mode:  d.Mode,
		Owner: d.Owner,
		Group: d.Group,
		Deps:  slices.Clone(d.Deps),
	}, nil
}

// Apply writes, edits, or removes the destination file, mirroring the
// resource's own content/template/line-edit/mode/ownership handling exactly
// (it calls the same Ensure entry point a direct, non-plan use would). The
// file-exclusive fields come from op.Payload (plan.FilePayload, task ae2),
// read through plan.PayloadOf like every other migrated kind's Apply (task
// eg2); see PayloadOf's doc comment for why a missing or mistyped payload
// reads as the zero FilePayload instead of an error.
func (planHandler) Apply(op plan.Op, ctx plan.ApplyContext) error {
	path, err := plan.ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("file: missing path")
	}
	p := plan.PayloadOf[plan.FilePayload](op)
	if err := validatePlanValidation(path, op, p); err != nil {
		return err
	}
	if op.Absent {
		opts := []opt.FileOption{opt.IsAbsent}
		if op.Name != "" {
			opts = append(opts, opt.WithName(op.Name))
		}
		return Ensure(path, opts...)
	}

	// Empty owner/group means "not recorded": leaving them unset keeps the
	// build() defaults (apply-side user) identical to direct resource use.
	ownership := plan.OwnerGroupOptions(op)

	if len(p.AddLines) != 0 || len(p.RemoveLines) != 0 || len(p.KeyedLines) != 0 || p.AddLine != "" || p.RemoveLine != "" {
		return applyFileLines(path, op, p, ownership)
	}
	return applyFileContent(path, op, p, ownership, ctx)
}

// validatePlanValidation rejects malformed validation-bearing file ops before
// the absent or line-edit branches could mutate while ignoring their validator
// fields. Normal File recording cannot produce these combinations because
// build validates them first; the explicit check protects hand-authored plans.
func validatePlanValidation(path string, op plan.Op, p plan.FilePayload) error {
	if p.ValidationBin == "" && len(p.ValidationArgs) == 0 {
		return nil
	}
	addLines, removeLines := planLines(p)
	f := File{
		path:           path,
		contentSet:     p.HasContent,
		validationBin:  p.ValidationBin,
		validationArgs: slices.Clone(p.ValidationArgs),
		validationSet:  true,
		addLines:       addLines,
		removeLines:    removeLines,
		keyedLines:     draftKeyedLines(p.KeyedLines),
	}
	f.Absent = op.Absent
	return f.validateConfiguration(path)
}

// Apply creates an empty regular file only when it is absent. Existing files
// retain their contents while explicitly recorded attributes converge.
func (ensureFileHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	path, err := plan.ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("ensure_file: missing path")
	}
	opts := make([]opt.FileOption, 0, 3)
	if op.Name != "" {
		opts = append(opts, opt.WithName(op.Name))
	}
	if op.Mode != "" {
		mode, err := plan.ParseMode(op.Mode)
		if err != nil {
			return fmt.Errorf("ensure_file: %w", err)
		}
		opts = append(opts, opt.WithMode(mode))
	}
	for _, ownerOpt := range plan.OwnerGroupOptions(op) {
		opts = append(opts, ownerOpt)
	}
	return EnsurePresent(path, opts...)
}

// planLines returns a file op's line edits in apply order: the v14+ arrays,
// then the singular add_line/remove_line wire fields that pre-v14 plans carry
// (current recording never sets them). An unset singular field is skipped
// rather than appended as "", so a v14+ op with no removals yields an empty
// removeLines and the len() checks in applyFileLines mean what they say.
func planLines(p plan.FilePayload) (addLines, removeLines []string) {
	addLines = slices.Clone(p.AddLines)
	if p.AddLine != "" {
		addLines = append(addLines, p.AddLine)
	}
	removeLines = slices.Clone(p.RemoveLines)
	if p.RemoveLine != "" {
		removeLines = append(removeLines, p.RemoveLine)
	}
	return addLines, removeLines
}

func applyFileLines(path string, op plan.Op, p plan.FilePayload, ownership []opt.FileDirOption) error {
	if p.ContentB64 != "" || op.Blob != "" {
		return fmt.Errorf("file: add_line/remove_line/keyed_lines cannot combine with content_b64/blob")
	}
	var opts []opt.FileOption
	if op.Name != "" {
		opts = append(opts, opt.WithName(op.Name))
	}
	addLines, removeLines := planLines(p)
	if len(removeLines) != 0 {
		opts = append(opts, opt.WithoutLines(removeLines...))
	}
	for _, edit := range p.KeyedLines {
		opts = append(opts, opt.WithKeyedLine(edit.Key, edit.Line))
	}
	if len(addLines) != 0 {
		opts = append(opts, opt.WithLines(addLines...))
	}
	if op.Mode != "" {
		mode, err := plan.ParseMode(op.Mode)
		if err != nil {
			return fmt.Errorf("file: %w", err)
		}
		opts = append(opts, opt.WithMode(mode))
	}
	for _, ownerOpt := range ownership {
		opts = append(opts, ownerOpt)
	}
	return Ensure(path, opts...)
}

func applyFileContent(path string, op plan.Op, p plan.FilePayload, ownership []opt.FileDirOption, ctx plan.ApplyContext) error {
	content, err := fileContent(p, op.Blob, ctx.PlanDir)
	if err != nil {
		return err
	}
	opts, err := fileContentOptions(op, p, content, ownership)
	if err != nil {
		return err
	}
	return ensureWithFacts(path, templateFacts(ctx.Facts), opts...)
}

// EnsureWithPlanFacts is Ensure for plan apply: {{.Gonf.GOOS/.Profile/
// .Hostname}} render from facts (plan.ApplyContext.Facts — what the
// destination's plan.Apply caller detected, honoring api.SetProfileOverride
// and the CLI -profile flag) instead of Ensure's own local re-detection,
// which knows nothing about that override. Every plan handler that writes a
// possibly-templated file must render with the plan's facts — the file
// handler above (through ensureWithFacts directly) and dir's sync_dir
// handler for each entry of a synced tree, through here — so identical
// template text renders identically within one apply. The entries of a
// sensitive sync_dir op arrive with WithSensitive among opts, like the
// content of a sensitive file op.
func EnsureWithPlanFacts(path string, facts plan.Facts, opts ...opt.FileOption) error {
	return ensureWithFacts(path, templateFacts(facts), opts...)
}

func fileContent(p plan.FilePayload, blob, planDir string) ([]byte, error) {
	switch {
	case p.ContentB64 != "":
		data, err := plan.DecodeContentB64(p.ContentB64)
		if err != nil {
			return nil, err
		}
		return data, nil
	case blob != "":
		data, err := plan.ReadFile(planDir, blob)
		if err != nil {
			return nil, err
		}
		return data, nil
	case p.HasContent:
		// WithContent("") or a zero-byte WithSource file: content_b64
		// legitimately encodes as "" for zero bytes. HasContent (recorded
		// whenever WithContent/WithSource was configured at all) is what
		// tells this apart from an op that never got content data.
		return nil, nil
	default:
		return nil, fmt.Errorf("file: missing content_b64 and blob")
	}
}

func fileContentOptions(op plan.Op, p plan.FilePayload, content []byte, ownership []opt.FileDirOption) ([]opt.FileOption, error) {
	opts := []opt.FileOption{opt.WithContent(string(content))}
	if op.Name != "" {
		opts = append(opts, opt.WithName(op.Name))
	}
	if p.Template {
		// The wire content is raw template text (packageDraft/RecordPlan
		// reads a .tmpl source's bytes verbatim), and by now neither path
		// nor source still carries the ".tmpl" suffix File.shouldRenderTemplate
		// would otherwise key off (Path was already stripped at record time,
		// and there is no source here — content came from content_b64/blob).
		// WithTemplate forces rendering; WithParam reproduces the {{.Param}}
		// a direct (non-plan) run with the same declared source would use.
		opts = append(opts, opt.WithTemplate)
		if p.TemplateParam != "" {
			opts = append(opts, opt.WithParam(p.TemplateParam))
		}
	}
	if p.ValidationBin != "" || len(p.ValidationArgs) != 0 {
		opts = append(opts, opt.WithValidation(p.ValidationBin, slices.Clone(p.ValidationArgs)))
	}
	if len(p.TemplateData) != 0 {
		data, err := decodeTemplateData(p.TemplateData)
		if err != nil {
			return nil, fmt.Errorf("file: decode template_data: %w", err)
		}
		opts = append(opts, opt.WithTemplateData(data))
	}
	if op.Mode != "" {
		mode, err := plan.ParseMode(op.Mode)
		if err != nil {
			return nil, fmt.Errorf("file: %w", err)
		}
		opts = append(opts, opt.WithMode(mode))
	}
	for _, ownerOpt := range ownership {
		opts = append(opts, ownerOpt)
	}
	// A sensitive op (scan-detected or WithSensitive at record time) rebuilds
	// a sensitive File, which withholds validator and template details.
	if op.Sensitive {
		opts = append(opts, opt.WithSensitive)
	}
	return opts, nil
}

// wireKeyedLines and draftKeyedLines convert keyed line edits between the
// draft and wire types (plan imports resource, so neither can be the other).
// Both keep nil for no edits, so an op without keyed lines encodes as before.
func wireKeyedLines(edits []resource.KeyedLine) []plan.KeyedLine {
	if len(edits) == 0 {
		return nil
	}
	out := make([]plan.KeyedLine, len(edits))
	for i, edit := range edits {
		out[i] = plan.KeyedLine{Key: edit.Key, Line: edit.Line}
	}
	return out
}

func draftKeyedLines(edits []plan.KeyedLine) []resource.KeyedLine {
	if len(edits) == 0 {
		return nil
	}
	out := make([]resource.KeyedLine, len(edits))
	for i, edit := range edits {
		out[i] = resource.KeyedLine{Key: edit.Key, Line: edit.Line}
	}
	return out
}

// planDraft records f as a "file" (or, for EnsureFile, "ensure_file") plan
// draft: identity and attributes here, the file-exclusive fields (line
// edits, validation, content, template intent and data — see Payload, task
// w62 Layer 1) through contentDraft and templateDraft into the Payload this
// builds and assigns once at the end. Payload is filled the same way for
// both kinds, matching the previous flat-field behaviour: ensureFileHandler
// simply never reads it.
func (f *File) planDraft() resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:      "file",
		ID:        f.resource.ID(),
		Name:      f.name,
		Path:      f.targetPath(),
		Mode:      opt.ModeToWire(f.mode),
		Absent:    f.Absent,
		Deps:      f.DependsOn.SortedIDs(),
		Sensitive: f.Sensitive,
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
	p := Payload{
		AddLines:       slices.Clone(f.addLines),
		RemoveLines:    slices.Clone(f.removeLines),
		KeyedLines:     slices.Clone(f.keyedLines),
		ValidationBin:  f.validationBin,
		ValidationArgs: slices.Clone(f.validationArgs),
	}
	f.contentDraft(&p)
	f.templateDraft(&p)
	d.Payload = p
	return d
}

// contentDraft records f's content half on p: the literal content, or the
// source path for packageDraft to package. HasContent flags that
// WithContent/WithSource was configured at all, so packageDraft/applyFile
// can tell a legitimately empty file (content or source resolving to zero
// bytes, which base64-encodes as "") apart from an op with no content data
// recorded (a bug, not a valid empty file).
func (f *File) contentDraft(p *Payload) {
	switch {
	case f.source != "":
		p.SourcePath = f.source
		p.HasContent = true
	case f.contentSet:
		p.ContentB64 = base64.StdEncoding.EncodeToString([]byte(f.content))
		p.HasContent = true
	}
}

// templateDraft records f's template intent and data on p. Template intent
// must travel on the wire explicitly: packageDraft reads f.source's RAW
// bytes into content_b64/blob, and targetPath already stripped ".tmpl" from
// the recorded Path, so neither field plan apply sees still carries the
// suffix shouldRenderTemplate would otherwise key off. Without
// Template/TemplateParam, plan apply (Run/push/cluster/fleet) would write
// the literal unrendered template text to the destination.
func (f *File) templateDraft(p *Payload) {
	if f.shouldRenderTemplate() {
		p.Template = true
		p.TemplateParam = f.templateParam()
	}
	if f.templateDataSet {
		p.TemplateData = slices.Clone(f.templateData)
		p.TemplateDataErr = f.templateDataErr
		p.TemplateDataSet = true
	}
}
