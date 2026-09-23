package plan

import "fmt"

// PreviewKind is the header op of a redacted human preview of a plan (`gonf
// plan -redacted`). It is deliberately not a Kind in AllKinds and not
// KindPlan: ValidateHeader refuses any first line other than KindPlan, in
// every gonf version, so a preview can never be applied or pushed — its
// redacted payload would otherwise be written to hosts as content.
const PreviewKind Kind = "plan_preview"

// RequiredVersion is the plan schema a recorded plan's header declares: the
// lowest version whose destinations apply ops faithfully. Schemas 22, 23 and
// 24 only add fields whose absence older destinations would silently
// misinterpret, so each is declared only when a plan uses it:
//   - v22 (sensitive) when an op is marked sensitive;
//   - v23 (keyed_lines) when a file op has a keyed line edit (WithKeyedLine);
//   - v24 (sync_dir glob) when a glob sync_dir op prunes — an older
//     destination would tree-prune it and delete unmanaged subdirectories.
//     A non-pruning glob sync_dir installs the same files under both
//     semantics, so it does not raise the header.
//
// Otherwise the header stays v21 and the plan still applies on a v0.15.0
// destination. Every earlier bump was emitted unconditionally, so v21 is the
// floor. v24 is the highest on-demand schema, so an op that needs it ends
// the scan early; the other two can only raise the version further (never
// past v24), so the loop keeps checking every remaining op for them. A
// future bump must extend this (TestRequiredVersion pins it to
// CurrentVersion).
func RequiredVersion(ops []Op) int {
	version := VersionConfigSet
	for _, op := range ops {
		// Glob moved onto SyncDirPayload (task 9e2); Prune stayed a flat
		// Op field (KindDir shares it — see Op.Prune's own doc comment in
		// types.go), so only Glob needs the payload assertion here. A
		// non-sync_dir op can never carry a SyncDirPayload
		// (payloadFromWire only builds one for KindSyncDir), so the
		// comma-ok degrades harmlessly to the zero payload for every
		// other kind.
		p, _ := op.Payload.(SyncDirPayload)
		if op.Op == KindSyncDir && p.Glob && op.Prune {
			return VersionSyncDirGlob // the highest on-demand schema
		}
		if len(op.KeyedLines) != 0 && version < VersionKeyedLines {
			version = VersionKeyedLines
		}
		if op.Sensitive && version < VersionSensitive {
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
