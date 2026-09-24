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
		// Glob moved onto SyncDirPayload (task 9e2); Prune stayed a flat Op
		// field (KindDir shares it — see Op.Prune's own doc comment in
		// types.go), so only Glob needs the payload read here. The Kind
		// check is NOT redundant with PayloadOf's own zero-value degrade: it
		// held for every DECODED op (payloadFromWire only ever builds a
		// SyncDirPayload for KindSyncDir), but not for an in-process
		// Op{Op: KindDir, Payload: SyncDirPayload{Glob: true}} — RequiredVersion
		// runs on the raw, freshly recorded []Op (plan.FinishRecord calls it
		// to build the header) strictly before any op in that slice is ever
		// encoded, so it cannot lean on toWire's own ownership check
		// (op_payload.go, task cg2) to rule this shape out for it; it needs
		// its own guard regardless of that later, independent safety net. A
		// mutation probe found this Kind check could be deleted with every
		// test still green, because the only regression case then in
		// TestRequiredVersion had been rewritten (task 9e2) into a decoded
		// line, which task 2f2's checkForeignPayload now refuses before
		// RequiredVersion ever sees it; task pf2 restored a Go-literal case
		// that reaches this exact guard. Task cg2 closed the matching
		// encode-side gap (toWire now refuses to merge a mismatched payload
		// onto the wire at all), so this shape can no longer reach a written
		// plan.jsonl even if this Kind check were ever removed — but that
		// protection fires downstream of RequiredVersion, at encode, not in
		// place of this guard.
		if op.Op == KindSyncDir {
			if p := PayloadOf[SyncDirPayload](op); p.Glob && op.Prune {
				return VersionSyncDirGlob // the highest on-demand schema
			}
		}
		// KeyedLines moved onto FilePayload (task ae2). Before task rf2 this
		// arm had NO Kind guard at all — unlike its SyncDirPayload sibling
		// above, which already checked op.Op == KindSyncDir — so an
		// in-process Op{Op: KindDir, Payload: FilePayload{KeyedLines: ...}}
		// genuinely bumped the header to VersionKeyedLines for a "dir" op,
		// which can never legitimately carry keyed-line semantics: a real
		// output bug, not merely a test-coverage gap (see
		// TestRequiredVersion's "keyed lines on non-file op" case, which
		// fails against the pre-rf2 code with got=23 want=21). The Kind
		// check here closes that the same way the SyncDirPayload arm's
		// already did — and, like that arm's own guard, still runs before
		// task cg2's encode-side ownership check (toWire, op_payload.go)
		// ever sees this same Op, so it stays necessary rather than made
		// redundant by it: an EncodeOp of the exact Op literal in this
		// comment is refused, since task cg2, but RequiredVersion runs on
		// it first and still needs its own correct answer regardless.
		if op.Op == KindFile {
			if fp := PayloadOf[FilePayload](op); len(fp.KeyedLines) != 0 && version < VersionKeyedLines {
				version = VersionKeyedLines
			}
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
// readable by that less privileged user as plaintext; the push therefore
// seals it instead (tasks zf2/0g2, which replaced task 062's refusal of
// these plans). Positions rather than IDs are returned because plan cannot
// redact: an ID may equal a short secret.
//
// The selection is sealsStickyBlob (sealed_sticky.go), shared with
// SealedStickyRefs, ChunkNeedsStickyKey and KeyedChunkSealedOps: the ops
// described here are exactly the ones whose refs the sealed sticky-dir
// path seals.
func SensitiveElevatedBlobs(chunks []Chunk) []string {
	var ops []string
	for i, c := range chunks {
		for j, op := range c.Ops {
			if sealsStickyBlob(c, op) {
				ops = append(ops, fmt.Sprintf("%s op %d of chunk %d", op.Op, j, i+1))
			}
		}
	}
	return ops
}
