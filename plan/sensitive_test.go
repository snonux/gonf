package plan

import (
	"slices"
	"strings"
	"testing"
)

func TestSensitiveIDs(t *testing.T) {
	t.Parallel()
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, ID: "File[/a]", Path: "/a", Sensitive: true},
		{Op: KindFile, ID: "File[/b]", Path: "/b"},
		{Op: KindFile, Path: "/c", Sensitive: true},
	}
	if got, want := SensitiveIDs(ops), []string{"File[/a]", "file /c"}; !slices.Equal(got, want) {
		t.Fatalf("SensitiveIDs = %q, want %q", got, want)
	}
	if got := SensitiveIDs(ops[2:3]); got != nil {
		t.Fatalf("SensitiveIDs(no sensitive op) = %q, want nil", got)
	}
}

// TestRequiredVersion pins the on-demand header: v22 only with a sensitive
// op, v23 with a keyed line edit (sensitive or not), v24 only with a
// pruning glob sync_dir op, v25 with a flags-managing service op or a noop
// op (the highest wins). It also pins CurrentVersion,
// so a later bump must revisit RequiredVersion instead of silently emitting
// too old a header.
func TestRequiredVersion(t *testing.T) {
	t.Parallel()
	if CurrentVersion != VersionServiceFlags {
		t.Fatalf("CurrentVersion %d: extend RequiredVersion for the new schema", CurrentVersion)
	}
	plain := Op{Op: KindFile, Path: "/a"}
	sensitive := Op{Op: KindFile, Path: "/k", Sensitive: true}
	globPrune := Op{Op: KindSyncDir, Path: "/g", Blob: "blobs/g", Prune: true, Payload: SyncDirPayload{Glob: true}}
	// "glob on dir" (task 9e2, superseded by task 2f2): once Glob moved onto
	// SyncDirPayload, a "dir" op literal can no longer even express a stray
	// glob flag — Op has no Glob field any more outside SyncDirPayload, so
	// the type system enforces what this case used to prove at runtime. The
	// only way left to reconstruct the shape (an old or forged plan.jsonl
	// line) was decoding raw wire bytes; task 9e2's version of this case
	// pinned that payloadFromWire silently dropped a "dir" op's wire-level
	// glob:true (never copied anywhere) instead of refusing it — exactly
	// the encode/decode fidelity bug task 2f2's checkForeignPayload
	// (op_payload.go) closed. Decoding this fixture is now refused instead
	// of silently succeeding, so "glob on dir" can no longer reach
	// RequiredVersion via the decode path; TestDecodeRefusesForeignKindFields
	// (op_payload_test.go) is task 2f2's replacement pin for that shape.
	if _, err := DecodeOp([]byte(`{"op":"dir","path":"/d","glob":true,"prune":true}`)); err == nil {
		t.Fatal("decode glob-on-dir fixture: want a foreign-payload refusal, got nil error")
	}
	// "glob on non-sync_dir op" (task pf2): the decode path above can never
	// reach RequiredVersion with a mismatched Kind/Payload any more, but an
	// IN-PROCESS Op is not decoded and nothing on the record path rules the
	// shape out by construction — a mutation probe found RequiredVersion's
	// own Kind guard could be deleted from the SyncDirPayload check with
	// every test (including the decode-refusal one above) still green,
	// because this was the only case that ever reached it. It compiles
	// (unlike a "dir" op literal setting a Glob field directly, which no
	// longer exists on Op) precisely because Payload is untyped per-Kind at
	// the Go level; RequiredVersion's Kind guard is what keeps it from
	// bumping the header for a kind that cannot legitimately carry glob
	// semantics at all.
	globOnDir := Op{Op: KindDir, Path: "/d", Prune: true, Payload: SyncDirPayload{Glob: true}}
	cases := []struct {
		name string
		ops  []Op
		want int
	}{
		{"plain", []Op{plain}, VersionConfigSet},
		{"sensitive", []Op{plain, sensitive}, VersionSensitive},
		{"glob without prune", []Op{{Op: KindSyncDir, Path: "/g", Blob: "blobs/g", Payload: SyncDirPayload{Glob: true}}}, VersionConfigSet},
		{"tree prune", []Op{{Op: KindSyncDir, Path: "/t", Blob: "blobs/t", Prune: true}}, VersionConfigSet},
		{"glob on non-sync_dir op", []Op{globOnDir}, VersionConfigSet},
		{"glob prune", []Op{plain, globPrune}, VersionSyncDirGlob},
		{"glob prune before sensitive", []Op{globPrune, sensitive}, VersionSyncDirGlob},
		{"sensitive before glob prune", []Op{sensitive, globPrune}, VersionSyncDirGlob},
		{"service without flags", []Op{{Op: KindService, Name: "s", Payload: ServicePayload{}}}, VersionConfigSet},
		{"service flags", []Op{plain, {Op: KindService, Name: "s", Payload: ServicePayload{HasFlags: true}}}, VersionServiceFlags},
		{"service flags after glob prune", []Op{globPrune, {Op: KindService, Name: "s", Payload: ServicePayload{HasFlags: true}}}, VersionServiceFlags},
		{"flags payload on non-service op", []Op{{Op: KindTimer, Name: "t", Payload: ServicePayload{HasFlags: true}}}, VersionConfigSet},
		{"noop", []Op{sensitive, {Op: KindNoop, Name: "ping", ID: "Noop[ping]"}}, VersionServiceFlags},
	}
	for _, tc := range cases {
		if got := RequiredVersion(tc.ops); got != tc.want {
			t.Errorf("RequiredVersion(%s) = %d, want %d", tc.name, got, tc.want)
		}
	}
	keyed := Op{Op: KindFile, Path: "/p", Payload: FilePayload{KeyedLines: []KeyedLine{{Key: "k=", Line: "k=v"}}}}
	for _, ops := range [][]Op{
		{plain, keyed},
		{{Op: KindFile, Path: "/k", Sensitive: true}, keyed},
		{keyed, {Op: KindFile, Path: "/k", Sensitive: true}},
	} {
		if got := RequiredVersion(ops); got != VersionKeyedLines {
			t.Fatalf("RequiredVersion(%v) = %d, want %d", ops, got, VersionKeyedLines)
		}
	}
	// "keyed lines on non-file op" (task rf2): the ALREADY-WRONG comma-ok
	// copy this task fixed. Unlike the SyncDirPayload arm above, the
	// FilePayload arm's comma-ok assertion had no matching Kind guard
	// before this fix — an in-process Op{Op: KindDir, Payload:
	// FilePayload{KeyedLines: ...}} genuinely bumped the header to
	// VersionKeyedLines, asymmetric with its neighbor and wrong: a "dir" op
	// cannot legitimately carry keyed-line semantics at all, the same
	// reasoning that already protected the SyncDirPayload arm. This proves
	// the fix changes real output, not just test coverage: reverting
	// plan.go's "if op.Op == KindFile" guard alone (keeping this test case)
	// makes this assertion fail with got=23 (VersionKeyedLines), want=21
	// (VersionConfigSet) — see this task's self-review patch revert.
	keyedOnDir := Op{Op: KindDir, Path: "/d", Payload: FilePayload{KeyedLines: []KeyedLine{{Key: "k=", Line: "k=v"}}}}
	if got := RequiredVersion([]Op{keyedOnDir}); got != VersionConfigSet {
		t.Fatalf("RequiredVersion(keyed lines on non-file op) = %d, want %d", got, VersionConfigSet)
	}
}

// TestSensitiveElevatedBlobs pins which ops a multi-chunk push must refuse:
// only sensitive blob-backed ops in elevated chunks.
func TestSensitiveElevatedBlobs(t *testing.T) {
	t.Parallel()
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, ID: "File[/user-blob]", Blob: "blobs/u", Sensitive: true},
		{Op: KindFile, ID: "File[/root-inline]", Payload: FilePayload{ContentB64: "eA=="}, Sensitive: true, Elevate: true},
		{Op: KindFile, ID: "File[/root-plain-blob]", Blob: "blobs/p", Elevate: true},
		{Op: KindFile, ID: "File[/root-blob]", Blob: "blobs/r", Sensitive: true, Elevate: true},
	}
	got := SensitiveElevatedBlobs(SplitPrivilegeChunks(ops))
	if want := []string{"file op 3 of chunk 2"}; !slices.Equal(got, want) {
		t.Fatalf("SensitiveElevatedBlobs = %q, want %q", got, want)
	}
}

// TestPreviewHeaderIsNeverAPlan pins that a redacted preview cannot be
// decoded as a plan, whatever version it claims.
func TestPreviewHeaderIsNeverAPlan(t *testing.T) {
	t.Parallel()
	if IsKnownKind(PreviewKind) || slices.Contains(AllKinds(), PreviewKind) {
		t.Fatal("PreviewKind must not be an applicable kind")
	}
	for _, v := range []int{1, VersionConfigSet, CurrentVersion} {
		err := ValidateHeader(Op{Op: PreviewKind, Version: v})
		if err == nil || !strings.Contains(err.Error(), "first op must be") {
			t.Fatalf("ValidateHeader(preview v%d) = %v, want a refusal", v, err)
		}
	}
}

// TestSensitiveSurvivesCodecAndSplit round-trips the flag through the codec
// and the privilege split; older schemas without it stay supported.
func TestSensitiveSurvivesCodecAndSplit(t *testing.T) {
	t.Parallel()
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, ID: "File[/k]", Path: "/k", Payload: FilePayload{ContentB64: "eA==", HasContent: true}, Sensitive: true, Elevate: true},
	}
	raw, err := EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	chunks := SplitPrivilegeChunks(decoded)
	last := chunks[len(chunks)-1].Ops
	if !last[len(last)-1].Sensitive {
		t.Fatalf("sensitive lost through codec/split: %s", raw)
	}
	if !SupportsVersion(VersionSensitive-1) || !SupportsVersion(VersionSensitive) {
		t.Fatal("v21 and v22 must both stay supported")
	}
}
