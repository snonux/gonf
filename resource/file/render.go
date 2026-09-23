package file

// Controller-side template rendering: a small, deliberately separate entry
// point for recipes that must fully resolve their content before it ever
// becomes a File resource (see RenderTemplate's doc comment). It reuses the
// exact template engine (funcs, missingkey=error, JSON-compatible data
// encoding) that applyTemplateToContent in template.go uses for a File's
// destination-rendered ".tmpl" source, so the two render paths never drift
// apart in what they render, but it deliberately does not reuse
// applyTemplateToContent itself: that method also mixes in process
// environment variables and {{.Gonf.*}} destination facts, neither of which
// exists yet (or would be deterministic) for content rendered on the
// controller before a destination is even selected.
//
// The two paths DO deliberately drift in error handling. A destination
// render (template.go's applyTemplateToContent) has a File resource at hand
// and can check its Sensitive field, so a failure there withholds the
// text/template error's details (which may quote the offending value
// verbatim) whenever the file is marked sensitive (see templateError).
// RenderTemplate has no resource, no Sensitive flag and — sitting below
// package api — no access to the secret registry that would let it
// recognise a resolved secret in the error text on its own; instead the two
// text/template error sites below route through the package-level
// ErrorRedactor SetErrorRedactor installs. api wires the secret registry
// into it at init (api/secret_provider.go), so api.RenderTemplate's caller
// never sees a raw secret. A caller that links this package without also
// linking api — or, within this module, a test that never calls
// SetErrorRedactor itself — still sees the raw, unredacted error: see the
// safety note in file.go's package doc comment, and the warning repeated on
// RenderTemplateFile's own doc comment below, the actual entry point a
// direct caller reaches.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"text/template"
)

// ErrorRedactor rewrites an error's text so RenderTemplate/RenderTemplateFile
// can withhold a secret value their two text/template error sites might
// otherwise quote verbatim — see SetErrorRedactor.
type ErrorRedactor interface {
	// Redact returns s with every secret it knows replaced.
	Redact(s string) string
}

var (
	errorRedactorMu sync.Mutex
	errorRedactor   ErrorRedactor
)

// SetErrorRedactor installs r to rewrite RenderTemplate/RenderTemplateFile's
// parse- and execute-error text before either function returns it, so a
// resolved secret one of those two errors may quote never leaves this
// package — regardless of caller (mirroring internal/logger.SetRedactor).
// api installs the secret registry with it at init, which is also why
// api.RenderTemplate's own RedactSecrets pass has nothing left to redact:
// this package has already done it. Passing nil (the default, and what a
// test restores in its own t.Cleanup) removes it, and both functions return
// the raw text/template error exactly as before this function existed — the
// state a caller that links this package without api is still in. r must be
// safe for concurrent use.
func SetErrorRedactor(r ErrorRedactor) {
	errorRedactorMu.Lock()
	defer errorRedactorMu.Unlock()
	errorRedactor = r
}

// redactRenderError applies the installed ErrorRedactor (if any) to err's
// text and returns a new error carrying the redacted text, but ONLY when
// redaction actually changed something: an error redaction left untouched
// (nothing it recognised, e.g. a non-secret parse error) is returned
// unchanged so errors.Is/errors.As keep matching it, the same rule
// api.RenderTemplate's own RedactSecrets pass follows. With no redactor
// installed, err is returned unchanged.
func redactRenderError(err error) error {
	if err == nil {
		return nil
	}
	errorRedactorMu.Lock()
	r := errorRedactor
	errorRedactorMu.Unlock()
	if r == nil {
		return err
	}
	if red := r.Redact(err.Error()); red != err.Error() {
		return errors.New(red)
	}
	return err
}

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
// destination render — see this file's leading comment above (not this
// package's real doc comment, which lives in file.go).
//
// A parse or execute error may quote templateText or data verbatim,
// including a resolved secret value: this package cannot recognise or
// withhold one on its own (see the package doc comment in file.go and this
// file's own leading comment), so both error sites route through the
// ErrorRedactor SetErrorRedactor installs before returning. With api
// linked (its init wires in the secret registry), that redactor is already
// in place, so a resolved secret never reaches the caller raw; without it —
// or from a test that never calls SetErrorRedactor — the error stays raw.
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
		return "", redactRenderError(fmt.Errorf("template parse error: %w", err))
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, root); err != nil {
		return "", redactRenderError(fmt.Errorf("template execute error: %w", err))
	}
	return buf.String(), nil
}

// RenderTemplateFile reads the template file at path with the same safety
// guarantees WithSource uses for a recipe-declared source (readForSource:
// symlinks are followed, but a FIFO, socket, device node, or directory is a
// loud error rather than a hang or a silent empty read) and renders it with
// RenderTemplate.
//
// WARNING: this is the package's actual risky entry point for a secret
// leak. A render failure's error may quote path's content or the supplied
// data verbatim, including a resolved secret value (see RenderTemplate's
// doc comment). It is safe when the process also links api: api's init
// installs the secret registry as this package's ErrorRedactor
// (SetErrorRedactor), and api.RenderTemplate is this package's one
// production caller. Calling RenderTemplateFile directly — bypassing
// api.RenderTemplate, or from a binary that links this package without api
// and never calls SetErrorRedactor itself — returns the raw, unredacted
// error.
func RenderTemplateFile(path string, data any) (string, error) {
	content, err := readForSource(path)
	if err != nil {
		return "", err
	}
	return RenderTemplate(string(content), data)
}
