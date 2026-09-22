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
// op, v23 with a keyed line edit (sensitive or not). It also pins
// CurrentVersion, so a later bump must revisit RequiredVersion instead of
// silently emitting too old a header.
func TestRequiredVersion(t *testing.T) {
	t.Parallel()
	if CurrentVersion != VersionKeyedLines {
		t.Fatalf("CurrentVersion %d: extend RequiredVersion for the new schema", CurrentVersion)
	}
	plain := []Op{{Op: KindFile, Path: "/a"}}
	if got := RequiredVersion(plain); got != VersionConfigSet {
		t.Fatalf("RequiredVersion(plain) = %d, want %d", got, VersionConfigSet)
	}
	if got := RequiredVersion(append(plain, Op{Op: KindFile, Path: "/k", Sensitive: true})); got != VersionSensitive {
		t.Fatalf("RequiredVersion(sensitive) = %d, want %d", got, VersionSensitive)
	}
	keyed := Op{Op: KindFile, Path: "/p", KeyedLines: []KeyedLine{{Key: "k=", Line: "k=v"}}}
	for _, ops := range [][]Op{
		append(slices.Clone(plain), keyed),
		{{Op: KindFile, Path: "/k", Sensitive: true}, keyed},
		{keyed, {Op: KindFile, Path: "/k", Sensitive: true}},
	} {
		if got := RequiredVersion(ops); got != VersionKeyedLines {
			t.Fatalf("RequiredVersion(%v) = %d, want %d", ops, got, VersionKeyedLines)
		}
	}
}

// TestSensitiveElevatedBlobs pins which ops a multi-chunk push must refuse:
// only sensitive blob-backed ops in elevated chunks.
func TestSensitiveElevatedBlobs(t *testing.T) {
	t.Parallel()
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, ID: "File[/user-blob]", Blob: "blobs/u", Sensitive: true},
		{Op: KindFile, ID: "File[/root-inline]", ContentB64: "eA==", Sensitive: true, Elevate: true},
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
		{Op: KindFile, ID: "File[/k]", Path: "/k", ContentB64: "eA==", HasContent: true, Sensitive: true, Elevate: true},
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
