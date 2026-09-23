package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
// refused as recipe misuse (a declaration error, internal/declerr, like other
// invalid File options), see file.PresentSecret.
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
// every payload string of every sensitive op wholesale (content_b64,
// template_data, member contents, argv, environment, lines, cron command
// and environment, guard and validator arguments; see redactOp), and every
// resolved secret value in every payload and identity
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
// and maps may be shared, is never modified). For a sensitive op every
// payload string (content, argv, environment keys and values, lines, cron
// command and environment, guard and validator arguments, ...; see
// opFieldClasses) is withheld wholesale and its template_data replaced as a
// whole, because such an op may carry secret material no resolved value
// matches (an explicit WithSensitive over a derived secret). Identity and
// metadata strings stay readable. Every remaining string then goes through
// RedactSecrets (metadata only for strong secrets). walkOpStrings runs with
// mutate=true: it is the one caller that legitimately rewrites strings, and
// it does so on out, its own copyOp deep copy, never on the caller's op —
// so writing the rewritten Op.Payload back is safe here even though the
// same write-back would race a concurrent reader on a shared op (see
// scanOp and opDisplayName, task 0f2).
func redactOp(op plan.Op) (plan.Op, error) {
	out, err := copyOp(op)
	if err != nil {
		return plan.Op{}, err
	}
	withhold := func(string, string) (string, bool) { return "", false }
	if out.Sensitive {
		// TemplateData moved onto plan.FilePayload (task ae2): only a
		// KindFile op ever carries one (payloadFromWire only builds one for
		// KindFile), so the comma-ok assertion degrades harmlessly to the
		// zero payload for every other kind, mirroring plan/sensitive.go's
		// identical assertion.
		if fp, ok := out.Payload.(plan.FilePayload); ok && len(fp.TemplateData) != 0 {
			fp.TemplateData = redactedJSON
			out.Payload = fp
		}
		withhold = payloadWithholder()
	}
	walkOpStrings(&out, func(path, s string) string {
		if r, ok := withhold(path, s); ok {
			return r
		}
		// Metadata (op kind, owner, mode, ...) is redacted only for strong
		// secrets: a weak one equal to "file" or "root" is a coincidence,
		// and redacting it would only garble the preview.
		if classOf(path) == classMetadata {
			return secretConfig.values.RedactStrong(s)
		}
		return RedactSecrets(s)
	}, true)
	return out, nil
}

// payloadWithholder returns the replacement rule for a sensitive op's
// strings: a non-empty payload, content or template-data string becomes
// secret.Redacted, and a map key "<secret.Redacted>-N" (numbered, so an
// environment keeps one distinct entry per variable). Identity and
// metadata strings are left to the caller (ok false).
func payloadWithholder() func(path, s string) (string, bool) {
	keys := 0
	return func(path, s string) (string, bool) {
		if class := classOf(path); s == "" || class == classIdentity || class == classMetadata {
			return "", false
		}
		if strings.HasSuffix(path, "{key}") {
			keys++
			return fmt.Sprintf("%s-%d", secret.Redacted, keys), true
		}
		return secret.Redacted, true
	}
}
