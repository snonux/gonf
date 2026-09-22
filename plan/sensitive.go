package plan

import "fmt"

// PreviewKind is the header op of a redacted human preview of a plan (`gonf
// plan -redacted`). It is deliberately not a Kind in AllKinds and not
// KindPlan: ValidateHeader refuses any first line other than KindPlan, in
// every gonf version, so a preview can never be applied or pushed — its
// redacted payload would otherwise be written to hosts as content.
const PreviewKind Kind = "plan_preview"

// RequiredVersion is the plan schema a recorded plan's header declares: the
// lowest version whose destinations apply ops faithfully. Schemas 22 and 23
// are declared on demand: v22 only adds the sensitive field and v23 only the
// file keyed_lines field, so a plan with neither is emitted as v21 and still
// applies on a v0.15.0 destination; one with a keyed line edit needs v23 and
// one with a sensitive op (but no keyed edit) v22, and an older destination
// refuses it at its header gate. Every earlier bump was emitted
// unconditionally, so v21 is the floor. A future bump must extend this
// (TestRequiredVersion pins it to CurrentVersion).
func RequiredVersion(ops []Op) int {
	version := VersionSensitive - 1
	for _, op := range ops {
		if len(op.KeyedLines) != 0 {
			return VersionKeyedLines
		}
		if op.Sensitive {
			version = VersionSensitive
		}
	}
	return version
}

// SensitiveIDs returns the identity of every op marked Sensitive, in plan
// order: its ID, or "<kind> <path>" for an op without one. Callers use it to
// refuse or warn about secret-bearing output without printing the payload.
// The IDs are not redacted (plan knows no secrets): print
// api.SensitiveOpNames instead wherever a person reads them.
func SensitiveIDs(ops []Op) []string {
	var ids []string
	for _, op := range ops {
		if !op.Sensitive {
			continue
		}
		id := op.ID
		if id == "" {
			id = string(op.Op) + " " + op.Path
		}
		ids = append(ids, id)
	}
	return ids
}

// SensitiveElevatedBlobs describes every Sensitive op in an elevated chunk
// whose content travels as a blob, as "<kind> op <n> of chunk <m>" (1-based,
// the chunk header being op 0). A multi-chunk push stages all blobs in one
// directory owned by the SSH login user (see remote.Delivery.ToHost), so
// such a blob — secret material destined for a privileged file — would be
// readable by that less privileged user. Positions rather than IDs are
// returned because plan cannot redact: an ID may equal a short secret.
func SensitiveElevatedBlobs(chunks []Chunk) []string {
	var ops []string
	for i, c := range chunks {
		if !c.Elevate {
			continue
		}
		for j, op := range c.Ops {
			if op.Sensitive && op.Blob != "" {
				ops = append(ops, fmt.Sprintf("%s op %d of chunk %d", op.Op, j, i+1))
			}
		}
	}
	return ops
}
