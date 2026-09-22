package file

// Content resolution and template rendering: reading the literal content or
// source file, deciding whether it is a text/template, and rendering it with
// the environment, WithTemplateData values, {{.Param}} and the {{.Gonf.*}}
// destination facts (localTemplateFacts here for direct runs; plan apply
// overrides them, see EnsureWithPlanFacts in planwire.go).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"text/template"
)

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
	templateData, err := templateDataMap(f.templateData, f.templateDataErr, f.templateDataSet)
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
		return nil, f.templateError("parse", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, f.templateError("execute", err)
	}
	return buf.Bytes(), nil
}

// templateError reports a failed template step. text/template errors quote
// the offending template text (a parse error names the unexpected token),
// so for sensitive content (File.sensitive) the details are withheld and
// only the step is reported.
func (f *File) templateError(step string, err error) error {
	if f.sensitive {
		return fmt.Errorf("template %s error (details withheld: the file holds secret material)", step)
	}
	return fmt.Errorf("template %s error: %w", step, err)
}

// templateDataMap decodes the WithTemplateData encoding (raw, or the
// encoding error encErr) into the template's root data: the flattened map
// keys plus .Data.
func templateDataMap(raw []byte, encErr error, set bool) (map[string]any, error) {
	data := make(map[string]any)
	if !set {
		return data, nil
	}
	if encErr != nil {
		return nil, fmt.Errorf("template data must be JSON-compatible: %w", encErr)
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
