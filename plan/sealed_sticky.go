package plan

import (
	"fmt"
	"io"
	"sort"
)

// Sealed sticky-dir refs (w82 phase 4, task zf2; docs/plan-encryption.md,
// "Phase 4 design: sealed multi-chunk sticky-dir blobs").
//
// A multi-chunk push stages every blob in one sticky directory owned by the
// SSH login user (internal/remote's uploadSticky). A blob that a Sensitive
// op of an elevated chunk reads (the selection SensitiveElevatedBlobs
// describes) must not sit there as plaintext, so the controller seals each
// such ref, as one opaque age stream per ref, to a per-push ephemeral key
// and uploads it at SealedBlobPath(ref) in place of the ref's own tar
// members. The plaintext each stream seals is WriteRefArchive's output: the
// gzip+tar of exactly that ref's members, the same bytes the blobs section
// of a push frame would carry for it, so the destination can unpack a
// decrypted ref with the same bounded, zip-slip-checked extractor
// (readBlobsGzipTar) it already uses for a push frame's blobs section, for
// a file ref and a sync_dir tree ref alike.
//
// The elevated chunk that reads a sealed ref receives the ephemeral key on
// its own stdin, in a GONF-PUSH/2 frame (pushwire_key.go). Which chunks
// need it is decided from the plan alone (ChunkNeedsStickyKey), so the
// frame lists no refs: a destination that holds the key derives the same
// selection from the ops it applies (KeyedChunkSealedOps), decrypts each
// ref and unpacks it into its private run dir with SealedRefExtractor
// (sealed_sticky_extract.go, task 0g2).

// sealedStickyDir is the sticky-dir subdirectory that holds sealed refs.
// Every real blob ref starts with "blobs/" (validateBlobRef), so nothing
// under "sealed/" can collide with an unsealed ref's own members.
const sealedStickyDir = "sealed"

// SealedBlobPath is the sticky-dir path (relative, slash-separated) of the
// sealed stream for blob ref: "sealed/<ref>.age".
func SealedBlobPath(ref string) string {
	return sealedStickyDir + "/" + ref + ".age"
}

// sealsStickyBlob reports whether op, in chunk c, reads a blob that a
// multi-chunk push must seal: a Sensitive, blob-backed op of an elevated
// chunk. It is the one selection shared by SensitiveElevatedBlobs (the
// refusal's description), SealedStickyRefs and ChunkNeedsStickyKey.
func sealsStickyBlob(c Chunk, op Op) bool {
	return c.Elevate && op.Sensitive && op.Blob != ""
}

// ChunkNeedsStickyKey reports whether ch reads a sealed sticky ref, i.e.
// whether it is an elevated chunk holding a Sensitive, blob-backed op. Only
// such a chunk may receive the per-push ephemeral key; every other chunk
// keeps its GONF-PUSH/1 frame.
func ChunkNeedsStickyKey(ch Chunk) bool {
	for _, op := range ch.Ops {
		if sealsStickyBlob(ch, op) {
			return true
		}
	}
	return false
}

// KeyedChunkSealedOps returns the positions (indexes into ops) of the ops
// whose blob is a sealed sticky ref, for ops that arrived as one chunk in a
// GONF-PUSH/2 frame. The destination does not see the chunk's Elevate flag,
// but only an elevated chunk ever receives the key (ChunkNeedsStickyKey),
// so it applies sealsStickyBlob as if the chunk were elevated: exactly the
// ops the controller's SealedStickyRefs selected in this chunk.
func KeyedChunkSealedOps(ops []Op) []int {
	keyed := Chunk{Elevate: true, Ops: ops}
	var idx []int
	for i, op := range ops {
		if sealsStickyBlob(keyed, op) {
			idx = append(idx, i)
		}
	}
	return idx
}

// SealedStickyRefs returns, sorted and without duplicates, every blob ref a
// multi-chunk push must seal: the refs SensitiveElevatedBlobs' ops read.
//
// It refuses a plan in which such a ref is also read by an op outside that
// selection (a non-sensitive op, or an op of an unprivileged chunk): that
// op would need the ref as plaintext in the login user's directory, which
// is exactly the exposure sealing removes. The refusal names the ops by
// position, like SensitiveElevatedBlobs, never by ID or ref: plan cannot
// redact, and either may be derived from a secret-bearing name.
func SealedStickyRefs(chunks []Chunk) ([]string, error) {
	sealed := map[string]bool{}
	for _, c := range chunks {
		for _, op := range c.Ops {
			if sealsStickyBlob(c, op) {
				sealed[op.Blob] = true
			}
		}
	}
	for i, c := range chunks {
		for j, op := range c.Ops {
			if op.Blob != "" && sealed[op.Blob] && !sealsStickyBlob(c, op) {
				return nil, fmt.Errorf("plan: %s op %d of chunk %d reads a blob that a sensitive elevated op also reads; "+
					"a sealed sticky-dir blob can only be read by sensitive elevated ops", op.Op, j, i+1)
			}
		}
	}
	refs := make([]string, 0, len(sealed))
	for ref := range sealed {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs, nil
}

// WriteRefArchive writes to w the plaintext a sealed sticky ref carries:
// the gzip-compressed tar of exactly ref's members from mem (one file
// entry for a file blob; the tree root and its entries for a tree blob),
// the same bytes a push frame's blobs section would hold for that ref
// alone. A ref mem does not hold is an error, so a sealed ref can never
// silently become an empty archive.
func WriteRefArchive(w io.Writer, mem BlobReader, ref string) error {
	if mem == nil || !hasRef(mem, ref) {
		return fmt.Errorf("plan: sealed blob ref is not in the blob store")
	}
	return writeBlobsGzipTar(w, onlyRef{BlobReader: mem, ref: ref})
}

// hasRef reports whether mem holds ref as a file or a tree blob.
func hasRef(mem BlobReader, ref string) bool {
	if _, ok := mem.FileBlob(ref); ok {
		return true
	}
	_, ok := mem.TreeBlob(ref)
	return ok
}

// onlyRef narrows a BlobReader to one ref, so writeBlobsGzipTar emits
// exactly that ref's members.
type onlyRef struct {
	BlobReader
	ref string
}

func (o onlyRef) HasBlobs() bool { return true }
func (o onlyRef) Refs() []string { return []string{o.ref} }

// WithSealedRefs returns the BlobReader a multi-chunk push uploads to its
// sticky dir: mem's blobs, except that every ref in sealed is replaced by
// one file blob at SealedBlobPath(ref) holding sealed[ref] (the ref's
// sealed WriteRefArchive stream). A sealed ref's own plaintext members are
// never served, so writeBlobsGzipTar cannot emit them. With an empty
// sealed it serves exactly mem's blobs.
//
// Refs() then includes "sealed/..." paths as well as "blobs/..." refs: it
// is only meant for the upload frame's tar member names, never for
// resolving an op's Blob field.
func WithSealedRefs(mem BlobReader, sealed map[string][]byte) BlobReader {
	byPath := make(map[string][]byte, len(sealed))
	for ref, data := range sealed {
		byPath[SealedBlobPath(ref)] = data
	}
	return sealedUpload{mem: mem, sealed: sealed, byPath: byPath}
}

// sealedUpload is WithSealedRefs' BlobReader.
type sealedUpload struct {
	mem    BlobReader
	sealed map[string][]byte // original ref -> sealed stream
	byPath map[string][]byte // SealedBlobPath(ref) -> sealed stream
}

func (s sealedUpload) HasBlobs() bool { return len(s.Refs()) > 0 }

// Refs returns mem's unsealed refs plus every sealed path, sorted.
func (s sealedUpload) Refs() []string {
	var out []string
	if s.mem != nil {
		for _, ref := range s.mem.Refs() {
			if _, isSealed := s.sealed[ref]; !isSealed {
				out = append(out, ref)
			}
		}
	}
	for path := range s.byPath {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// FileBlob serves a sealed stream at its sealed path and an unsealed file
// ref from mem; a sealed ref's own plaintext is never returned.
func (s sealedUpload) FileBlob(ref string) ([]byte, bool) {
	if data, ok := s.byPath[ref]; ok {
		return data, true
	}
	if _, isSealed := s.sealed[ref]; isSealed || s.mem == nil {
		return nil, false
	}
	return s.mem.FileBlob(ref)
}

// TreeBlob serves an unsealed tree ref from mem; a sealed ref's own
// manifest is never returned.
func (s sealedUpload) TreeBlob(ref string) ([]BlobEntry, bool) {
	if _, isSealed := s.sealed[ref]; isSealed || s.mem == nil {
		return nil, false
	}
	return s.mem.TreeBlob(ref)
}
