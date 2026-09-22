package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/secret"
)

// redactedJSON is secret.Redacted as a JSON value, the stand-in for a
// sensitive op's template_data in a redacted preview.
var redactedJSON = json.RawMessage(`"` + secret.Redacted + `"`)

// SecretFile manages the file at path with the exact bytes of the secret
// ref as its content, resolved through the configured provider
// (SetSecretProvider). The recorded op is sensitive (see plan.Op.Sensitive),
// and ref — not the value — is what appears in errors.
//
// Its mode defaults to 0600 instead of File's 0640; an explicit WithMode
// overrides it. opts may set mode, ownership, a name, dependencies,
// validation and change watches, but not the content: WithContent,
// WithSource, WithTemplate, WithTemplateData, line edits and IsAbsent are
// refused as recipe misuse (logger.Fatal, like other invalid File options),
// see file.PresentSecret.
//
// A failed resolution fails plan recording exactly like MustSecret and
// registers nothing: the returned empty Multi keeps a DependsOn on it safe
// until the stashed error fails the record.
func SecretFile(path string, ref secret.Ref, opts ...options.FileOption) Resource {
	data, err := ResolveSecret(context.Background(), ref)
	if err != nil {
		stashSecretError(err)
		return resource.Multi(nil)
	}
	return file.PresentSecret(path, data, opts...)
}

// RedactSecrets returns s with every value ResolveSecret returned in this
// process replaced by secret.Redacted (see secret.Values.Redact for the
// forms and the short-secret rule). Callers use it on text that may quote a
// secret before printing it, e.g. op identities in CLI messages.
func RedactSecrets(s string) string {
	return secretConfig.values.Redact(s)
}

// EncodeRedactedPreview encodes ops as a human preview that is safe to print:
// JSONL like a plan, but headed by a plan.PreviewKind line (which no gonf
// version applies) and with secret material replaced by secret.Redacted —
// the content_b64, template_data and member contents of every sensitive op
// wholesale, and every resolved secret value in every payload and identity
// string of every op (fields, list elements, map keys and values, template
// data leaves); metadata strings only for strong secrets. The
// strings are redacted as decoded values and the op is re-encoded, so every
// line stays valid JSON. It is for reading only: do not label or keep it as
// a replayable plan.
func EncodeRedactedPreview(ops []plan.Op) ([]byte, error) {
	if len(ops) == 0 || ops[0].Op != plan.KindPlan {
		return nil, fmt.Errorf("preview: plan must start with its %q header", plan.KindPlan)
	}
	var buf bytes.Buffer
	for i, op := range ops {
		redacted, err := redactOp(op)
		if err != nil {
			return nil, fmt.Errorf("preview: line %d: %w", i+1, err)
		}
		if i == 0 {
			redacted.Op = plan.PreviewKind
		}
		line, err := plan.EncodeOp(redacted)
		if err != nil {
			return nil, fmt.Errorf("preview: line %d: %w", i+1, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// redactOp returns a redacted deep copy of op (the caller's op, whose slices
// and maps may be shared, is never modified): the encoded content of a
// sensitive op as a whole, since it cannot be redacted piecewise, then every
// string value through RedactSecrets.
func redactOp(op plan.Op) (plan.Op, error) {
	out, err := copyOp(op)
	if err != nil {
		return plan.Op{}, err
	}
	if out.Sensitive {
		if out.ContentB64 != "" {
			out.ContentB64 = secret.Redacted
		}
		if len(out.TemplateData) != 0 {
			out.TemplateData = redactedJSON
		}
		for i := range out.Members {
			if out.Members[i].ContentB64 != "" {
				out.Members[i].ContentB64 = secret.Redacted
			}
		}
	}
	walkOpStrings(&out, func(path, s string) string {
		// Metadata (op kind, owner, mode, ...) is redacted only for strong
		// secrets: a weak one equal to "file" or "root" is a coincidence,
		// and redacting it would only garble the preview.
		if classOf(path) == classMetadata {
			return secretConfig.values.RedactStrong(s)
		}
		return RedactSecrets(s)
	})
	return out, nil
}
