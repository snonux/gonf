package api

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/secret"
)

// SensitiveOpNames returns a printable name for every sensitive op of ops,
// for messages that must say which ops carry secret material: its ID (or
// "<kind> <path>") with every resolved secret redacted, including a short
// one that an identity field equals exactly and that RedactSecrets alone
// would leave inside the longer ID (opDisplayName).
func SensitiveOpNames(ops []plan.Op) []string {
	var names []string
	for i := range ops {
		if ops[i].Sensitive {
			names = append(names, opDisplayName(&ops[i]))
		}
	}
	return names
}

// markSensitive scans an op lowered from a resource draft (scanOp) and sets
// op.Sensitive when it carries a resolved secret, or refuses the op when a
// strong secret sits in one of its identity fields. source is
// the raw bytes of a packaged file source (nil otherwise): a blob-backed op
// no longer carries them itself. It never clears the flag. Synced directory
// trees (sync_dir blobs) are not scanned.
func markSensitive(op *plan.Op, source []byte, task string) error {
	if secretConfig.values.Empty() {
		return nil
	}
	sensitive, field := scanOp(op)
	if field != "" {
		return secretFieldError(recordingPrefix(task), string(op.Op), field)
	}
	if sensitive || secretConfig.values.Contains(source) {
		op.Sensitive = true
	}
	return nil
}

// markRecordedControlOps runs the same scan over the control ops of a
// finished plan (when_begin predicates and requirements, when_end), which
// are recorded directly rather than lowered from a resource draft, and then
// recomputes the header version (plan.RequiredVersion) because marking may
// have changed it. The refusal names the plan line.
func markRecordedControlOps(ops []plan.Op) error {
	if secretConfig.values.Empty() {
		return nil
	}
	for i := 1; i < len(ops); i++ {
		if !plan.IsControlKind(ops[i].Op) {
			continue
		}
		sensitive, field := scanOp(&ops[i])
		if field != "" {
			return secretFieldError(fmt.Sprintf("RecordPlan: plan line %d: ", i+1), string(ops[i].Op), field)
		}
		ops[i].Sensitive = ops[i].Sensitive || sensitive
	}
	if len(ops) != 0 && ops[0].Op == plan.KindPlan {
		ops[0].Version = plan.RequiredVersion(ops[1:])
	}
	return nil
}

// scanOp walks every string of op (walkOpStrings, classified by
// opFieldClasses) and reports whether one holds a resolved secret, and the
// JSON path of the first identity field holding a strong one
// (secret.Values.ContainsStrong), which the caller refuses. op is not
// modified.
func scanOp(op *plan.Op) (sensitive bool, refuse string) {
	values := &secretConfig.values
	walkOpStrings(op, func(path, s string) string {
		if refuse != "" || s == "" {
			return s
		}
		switch classOf(path) {
		case classIdentity:
			if values.ContainsStrong([]byte(s)) {
				refuse = path
			}
			sensitive = sensitive || values.Contains([]byte(s))
		case classMetadata:
			sensitive = sensitive || values.ContainsStrong([]byte(s))
		case classPayload:
			sensitive = sensitive || values.Contains([]byte(s))
		case classContent:
			sensitive = sensitive || values.Contains(decodedBase64(s))
		case classTemplateData:
			sensitive = sensitive || values.Contains([]byte(s)) || values.Contains(decodedBase64(s))
		}
		return s
	})
	return sensitive, refuse
}

// decodedBase64 is the standard base64 decoding of s, or nil.
func decodedBase64(s string) []byte {
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return decoded
}

// recordingPrefix is the "RecordPlan: [task %q: ]" prefix of a refusal
// raised while a draft of task is packaged ("" outside a task body, e.g.
// api.Apply).
func recordingPrefix(task string) string {
	if task == "" {
		return "RecordPlan: "
	}
	return fmt.Sprintf("RecordPlan: task %q: ", task)
}

// secretFieldError is the identity refusal (only identity fields refuse).
// It names the op kind and the field, never the op's ID or the value: the
// ID may itself be the leak (an unnamed Command's ID is its argv) or quote
// a weak secret.
func secretFieldError(prefix, kind, field string) error {
	return fmt.Errorf("%s%s op: its %s holds a resolved secret value; this field is an identity (an id, name, path "+
		"or binary), logged and reported unredacted on every host, so keep the secret out of it (e.g. WithName "+
		"for a Command, a managed 0600 file for a path argument)", prefix, kind, field)
}

// opDisplayName is one SensitiveOpNames entry.
func opDisplayName(op *plan.Op) string {
	name := op.ID
	if name == "" {
		name = string(op.Op) + " " + op.Path
	}
	name = RedactSecrets(name)
	walkOpStrings(op, func(path, s string) string {
		if s != "" && classOf(path) == classIdentity && secretConfig.values.Contains([]byte(s)) {
			name = strings.ReplaceAll(name, s, secret.Redacted)
		}
		return s
	})
	return name
}
