package file

// Controller-side template rendering: a small, deliberately separate entry
// point for recipes that must fully resolve their content before it ever
// becomes a File resource (see RenderTemplate's doc comment). It reuses the
// exact template engine (funcs, missingkey=error, JSON-compatible data
// encoding) that applyTemplateToContent in template.go uses for a File's
// destination-rendered ".tmpl" source, so the two render paths never drift
// apart, but it deliberately does not reuse applyTemplateToContent itself:
// that method also mixes in process environment variables and
// {{.Gonf.*}} destination facts, neither of which exists yet (or would be
// deterministic) for content rendered on the controller before a
// destination is even selected.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/template"
)

// RenderTemplate renders templateText on the controller and returns the
// fully resolved result. It exists for recipes whose rendered content must
// be resolved before it becomes a resource at all — for example frontend
// daemon configuration assembled from controller-only topology inputs —
// so the caller hands the result to a plain WithContent the same way it
// would hand over any other literal string, instead of shipping a template
// plus data for WithTemplateData to render on the destination. See
// docs/file-dir-link.md, "Template data and destination facts", for that
// separate destination-render path.
//
// data must be JSON-compatible the same way WithTemplateData's data must
// be: it is JSON-encoded once (so a caller mutating or reusing it
// afterwards cannot change an already-rendered result), a top-level JSON
// object's keys become root template variables, and the complete decoded
// value is also available as .Data — the same encoding
// applyTemplateToContent's templateDataMap produces for a destination
// render. Templates use the same stable helper functions (join, lower,
// upper, trim, replace) and the same strict "missingkey=error" handling: a
// missing map key fails the render rather than silently producing an empty
// value. There is no {{.Gonf}} and no process environment here, unlike a
// destination render — see the package doc comment above.
func RenderTemplate(templateText string, data any) (string, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("template data must be JSON-compatible: %w", err)
	}
	root, err := templateDataMap(encoded, nil, true)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New("controller").Funcs(templateFuncMap()).Option("missingkey=error").Parse(templateText)
	if err != nil {
		return "", fmt.Errorf("template parse error: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, root); err != nil {
		return "", fmt.Errorf("template execute error: %w", err)
	}
	return buf.String(), nil
}

// RenderTemplateFile reads the template file at path with the same safety
// guarantees WithSource uses for a recipe-declared source (readForSource:
// symlinks are followed, but a FIFO, socket, device node, or directory is a
// loud error rather than a hang or a silent empty read) and renders it with
// RenderTemplate.
func RenderTemplateFile(path string, data any) (string, error) {
	content, err := readForSource(path)
	if err != nil {
		return "", err
	}
	return RenderTemplate(string(content), data)
}
