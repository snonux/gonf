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

// ToOp lowers a "file" resource draft to a plan.Op.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	op := plan.Op{
		Op:             plan.KindFile,
		ID:             d.ID,
		Name:           d.Name,
		Path:           d.Path,
		Mode:           d.Mode,
		Owner:          d.Owner,
		Group:          d.Group,
		ContentB64:     d.ContentB64,
		Blob:           d.Blob,
		HasContent:     d.HasContent,
		Template:       d.Template,
		TemplateParam:  d.TemplateParam,
		ValidationBin:  d.ValidationBin,
		ValidationArgs: slices.Clone(d.ValidationArgs),
		AddLines:       slices.Clone(d.AddLines),
		RemoveLines:    slices.Clone(d.RemoveLines),
		KeyedLines:     wireKeyedLines(d.KeyedLines),
		Absent:         d.Absent,
		Deps:           slices.Clone(d.Deps),
	}
	if d.TemplateDataSet {
		if d.TemplateDataErr != nil {
			return plan.Op{}, fmt.Errorf("file: template data must be JSON-compatible: %w", d.TemplateDataErr)
		}
		op.TemplateData = slices.Clone(d.TemplateData)
	}
	return op, nil
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
// (it calls the same Ensure entry point a direct, non-plan use would).
func (planHandler) Apply(op plan.Op, ctx plan.ApplyContext) error {
	path, err := plan.ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("file: missing path")
	}
	if err := validatePlanValidation(path, op); err != nil {
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

	if len(op.AddLines) != 0 || len(op.RemoveLines) != 0 || len(op.KeyedLines) != 0 || op.AddLine != "" || op.RemoveLine != "" {
		return applyFileLines(path, op, ownership)
	}
	return applyFileContent(path, op, ownership, ctx)
}

// validatePlanValidation rejects malformed validation-bearing file ops before
// the absent or line-edit branches could mutate while ignoring their validator
// fields. Normal File recording cannot produce these combinations because
// build validates them first; the explicit check protects hand-authored plans.
func validatePlanValidation(path string, op plan.Op) error {
	if op.ValidationBin == "" && len(op.ValidationArgs) == 0 {
		return nil
	}
	addLines, removeLines := planLines(op)
	f := File{
		path:           path,
		contentSet:     op.HasContent,
		validationBin:  op.ValidationBin,
		validationArgs: slices.Clone(op.ValidationArgs),
		validationSet:  true,
		addLines:       addLines,
		removeLines:    removeLines,
		keyedLines:     draftKeyedLines(op.KeyedLines),
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
func planLines(op plan.Op) (addLines, removeLines []string) {
	addLines = slices.Clone(op.AddLines)
	if op.AddLine != "" {
		addLines = append(addLines, op.AddLine)
	}
	removeLines = slices.Clone(op.RemoveLines)
	if op.RemoveLine != "" {
		removeLines = append(removeLines, op.RemoveLine)
	}
	return addLines, removeLines
}

func applyFileLines(path string, op plan.Op, ownership []opt.FileDirOption) error {
	if op.ContentB64 != "" || op.Blob != "" {
		return fmt.Errorf("file: add_line/remove_line/keyed_lines cannot combine with content_b64/blob")
	}
	var opts []opt.FileOption
	if op.Name != "" {
		opts = append(opts, opt.WithName(op.Name))
	}
	addLines, removeLines := planLines(op)
	if len(removeLines) != 0 {
		opts = append(opts, opt.WithoutLines(removeLines...))
	}
	for _, edit := range op.KeyedLines {
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

func applyFileContent(path string, op plan.Op, ownership []opt.FileDirOption, ctx plan.ApplyContext) error {
	content, err := fileContent(op, ctx.PlanDir)
	if err != nil {
		return err
	}
	opts, err := fileContentOptions(op, content, ownership)
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

func fileContent(op plan.Op, planDir string) ([]byte, error) {
	switch {
	case op.ContentB64 != "":
		data, err := plan.DecodeContentB64(op.ContentB64)
		if err != nil {
			return nil, err
		}
		return data, nil
	case op.Blob != "":
		data, err := plan.ReadFile(planDir, op.Blob)
		if err != nil {
			return nil, err
		}
		return data, nil
	case op.HasContent:
		// WithContent("") or a zero-byte WithSource file: content_b64
		// legitimately encodes as "" for zero bytes. HasContent (recorded
		// whenever WithContent/WithSource was configured at all) is what
		// tells this apart from an op that never got content data.
		return nil, nil
	default:
		return nil, fmt.Errorf("file: missing content_b64 and blob")
	}
}

func fileContentOptions(op plan.Op, content []byte, ownership []opt.FileDirOption) ([]opt.FileOption, error) {
	opts := []opt.FileOption{opt.WithContent(string(content))}
	if op.Name != "" {
		opts = append(opts, opt.WithName(op.Name))
	}
	if op.Template {
		// The wire content is raw template text (packageDraft/RecordPlan
		// reads a .tmpl source's bytes verbatim), and by now neither path
		// nor source still carries the ".tmpl" suffix File.shouldRenderTemplate
		// would otherwise key off (Path was already stripped at record time,
		// and there is no source here — content came from content_b64/blob).
		// WithTemplate forces rendering; WithParam reproduces the {{.Param}}
		// a direct (non-plan) run with the same declared source would use.
		opts = append(opts, opt.WithTemplate)
		if op.TemplateParam != "" {
			opts = append(opts, opt.WithParam(op.TemplateParam))
		}
	}
	if op.ValidationBin != "" || len(op.ValidationArgs) != 0 {
		opts = append(opts, opt.WithValidation(op.ValidationBin, slices.Clone(op.ValidationArgs)))
	}
	if len(op.TemplateData) != 0 {
		data, err := decodeTemplateData(op.TemplateData)
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
// draft: identity, attributes, line edits, validation, dependencies and the
// explicit sensitivity here, the content through draftContent.
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
		KeyedLines:     slices.Clone(f.keyedLines),
		ValidationBin:  f.validationBin,
		ValidationArgs: slices.Clone(f.validationArgs),
		Deps:           f.DependsOn.SortedIDs(),
		Sensitive:      f.Sensitive,
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
	f.draftContent(&d)
	return d
}

// draftContent records f's content half on d: the literal content or the
// source to package, and the template intent and data.
func (f *File) draftContent(d *resource.PlanDraft) {
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
		d.TemplateData = slices.Clone(f.templateData)
		d.TemplateDataErr = f.templateDataErr
		d.TemplateDataSet = true
	}
}
