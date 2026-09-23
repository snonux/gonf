package api

import (
	"errors"

	"github.com/snonux/gonf/resource/file"
)

// RenderTemplate reads and renders the template file at path on the
// controller, returning the fully resolved text. It exists for recipes that
// must resolve daemon configuration on the controller from typed,
// explicit data structs — controller-only topology inputs such as an ACME
// host list or a cluster's F3S members — and then hand the fully-rendered
// text to WithContent, instead of shipping a template plus data for
// WithTemplateData to render on the destination. This preserves
// controller-side render semantics for recipes that need them while still
// reusing WithTemplateData's underlying template engine: the same helper
// functions (join, lower, upper, trim, replace) and the same strict
// "missingkey=error" handling, so a typo'd field name fails loudly at
// record time instead of silently rendering empty. See
// docs/file-dir-link.md, "Template data and destination facts", for the
// destination-render path this deliberately does not replace, and
// resource/file/render.go for why the two paths do not share every detail
// (no process environment, no {{.Gonf}} destination facts here — there is
// no destination yet when this runs).
//
// data must be JSON-compatible: a top-level JSON object's keys become root
// template variables, and the complete decoded value is also available as
// .Data.
//
// A failed render's error is redacted (RedactSecrets) before it is
// returned: file.RenderTemplateFile's underlying text/template error can
// quote the offending value verbatim, including data built from
// MustSecret/ResolveSecret, and unlike a destination render (a File's
// Sensitive option) there is no resource here to flag as sensitive — see
// resource/file/render.go's package doc comment. Redacting by known secret
// value rather than withholding the whole message wholesale keeps a
// non-sensitive template's error fully useful for debugging (it is
// unaffected) while still closing the leak for a sensitive one, regardless
// of whether the caller ever attaches WithSensitive downstream. Only a
// resolved secret's tracked bytes disappear; the constructed error is a new
// one carrying just the redacted text, deliberately not wrapping the
// original (wrapping it would still let a caller reach the raw, secret-
// bearing text through errors.Unwrap/errors.As).
func RenderTemplate(path string, data any) (string, error) {
	text, err := file.RenderTemplateFile(path, data)
	if err != nil {
		return "", errors.New(RedactSecrets(err.Error()))
	}
	return text, nil
}
