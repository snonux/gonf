package file

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// planHandler is the file kind's plan.Handler: see resource/pkg/planwire.go
// for why record-time ToOp and apply-time Apply live together in the
// resource package instead of api/plan.go's draftToOp and plan/apply.go's
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
		Op:            plan.KindFile,
		ID:            d.ID,
		Path:          d.Path,
		Mode:          d.Mode,
		Owner:         d.Owner,
		Group:         d.Group,
		ContentB64:    d.ContentB64,
		Blob:          d.Blob,
		HasContent:    d.HasContent,
		Template:      d.Template,
		TemplateParam: d.TemplateParam,
		AddLines:      d.AddLines,
		RemoveLines:   d.RemoveLines,
		AddLine:       d.AddLine,
		RemoveLine:    d.RemoveLine,
		Absent:        d.Absent,
		Deps:          d.Deps,
	}
	if d.TemplateDataSet {
		raw, err := json.Marshal(d.TemplateData)
		if err != nil {
			return plan.Op{}, fmt.Errorf("file: template data must be JSON-compatible: %w", err)
		}
		op.TemplateData = raw
	}
	return op, nil
}

// ToOp lowers an ensure_file resource draft to a plan.Op.
func (ensureFileHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:    plan.KindEnsureFile,
		ID:    d.ID,
		Path:  d.Path,
		Mode:  d.Mode,
		Owner: d.Owner,
		Group: d.Group,
		Deps:  d.Deps,
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
	if op.Absent {
		return Ensure(path, opt.IsAbsent)
	}

	// Empty owner/group means "not recorded": leaving them unset keeps the
	// build() defaults (apply-side user) identical to direct resource use.
	ownership := plan.OwnerGroupOptions(op)

	if len(op.AddLines) != 0 || len(op.RemoveLines) != 0 || op.AddLine != "" || op.RemoveLine != "" {
		return applyFileLines(path, op, ownership)
	}
	return applyFileContent(path, op, ownership, ctx)
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

func applyFileLines(path string, op plan.Op, ownership []opt.FileDirOption) error {
	if op.ContentB64 != "" || op.Blob != "" {
		return fmt.Errorf("file: add_line/remove_line cannot combine with content_b64/blob")
	}
	var opts []opt.FileOption
	removeLines := append(slices.Clone(op.RemoveLines), op.RemoveLine)
	addLines := append(slices.Clone(op.AddLines), op.AddLine)
	if len(removeLines) != 0 {
		opts = append(opts, opt.WithoutLines(removeLines...))
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
	return opts, nil
}
