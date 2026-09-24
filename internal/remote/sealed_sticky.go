package remote

import (
	"bytes"
	"fmt"
	"io"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// Sealed sticky-dir blobs, controller side (w82 phase 4 step 2, task zf2;
// docs/plan-encryption.md, "Phase 4 design: sealed multi-chunk sticky-dir
// blobs").
//
// A multi-chunk push with blobs uploads them once to a sticky dir owned by
// the SSH login user (uploadSticky). When an elevated chunk's Sensitive op
// reads one of those blobs (plan.SealedStickyRefs), Delivery.ToHost
// generates one fresh ephemeral age1pq key pair for this push to this host
// (seal.GenerateEphemeral, in memory only), seals each such ref to it
// (plan.WriteRefArchive's archive, one age stream per ref) and uploads the
// sealed stream at plan.SealedBlobPath(ref) instead of the ref's plaintext.
// Every other ref uploads unchanged. The identity then travels only on the
// stdin of the elevated chunk(s) that read a sealed ref, as the key line of
// a GONF-PUSH/2 frame (plan.EncodePushWithKey); every other chunk, and the
// blob upload itself, keeps its GONF-PUSH/1 frame, so a push that seals
// nothing is byte-identical to one before this change.
//
// Where the key may appear, audited for task zf2:
//   - argv / environment: never. remoteApplyCmd builds the same command for
//     a keyed chunk as for any other; the key is only in the stdin bytes.
//   - logs: nothing here logs the key or the frame; the frame buffer
//     (encodeChunk's bytes.Buffer in streamChunks) is only handed to
//     SSHRunner as stdin, and SSHRunner never logs its stdin. What SSHRunner
//     relays is the REMOTE's stdout/stderr: a pre-/2 remote refuses the
//     frame on its magic line ("bad magic \"GONF-PUSH/2\"") before reading
//     the key line, and a current one's DecodePush refuses with
//     plan.ErrPushKeyNotAccepted, also before reading it, so neither can
//     echo the key back.
//   - errors: every error on this path is built from positions, plan IDs,
//     sentinels and I/O errors, never from the key or the frame bytes
//     (plan.EncodePushWithKey and plan/seal guarantee the same for theirs).
//   - disk: never on the controller. On the destination only the sealed
//     streams land in the sticky dir; the elevated chunk decrypts them into
//     its own private run dir before it applies (internal/cli's
//     stageSealedStickyRefs, task 0g2), which replaced task 062's refusal
//     of such plans.
//
// The path is gated on the remote release (RequireRemoteSealedSticky,
// sealedStickyMinRelease): a remote that cannot decrypt is refused before
// any blob is uploaded, after EnsureRemoteGonf had its chance to upgrade it.

// stickySeal is one push's sealed sticky-dir state: the ephemeral identity
// the keyed chunks receive, and the blob set to upload in place of the
// plaintext one. It exists only for the duration of one Delivery.ToHost
// call and is never logged, stored or copied into an error. A nil
// *stickySeal means "nothing to seal": its methods then fall back to the
// unchanged GONF-PUSH/1 behaviour.
type stickySeal struct {
	key    plan.PushKey // the ephemeral identity's EncodeEphemeral line
	upload plan.BlobReader
}

// newStickySeal seals, to a fresh ephemeral key, every blob ref of mem that
// chunks' sensitive elevated ops read. It returns nil (and no error) when
// there is no such ref, so no key is generated for a push that does not
// need one. It runs entirely on the controller, before any SSH traffic.
func newStickySeal(chunks []plan.Chunk, mem plan.BlobReader) (*stickySeal, error) {
	refs, err := plan.SealedStickyRefs(chunks)
	if err != nil || len(refs) == 0 {
		return nil, err
	}
	key, recipient, err := newEphemeralPushKey()
	if err != nil {
		return nil, err
	}
	sealed := make(map[string][]byte, len(refs))
	for i, ref := range refs {
		data, err := sealRef(mem, ref, recipient)
		if err != nil {
			// The position, not the ref: a ref name may derive from a
			// secret-bearing resource name (see plan.SealedStickyRefs).
			return nil, fmt.Errorf("seal sticky blob %d of %d: %w", i+1, len(refs), err)
		}
		sealed[ref] = data
	}
	return &stickySeal{key: key, upload: plan.WithSealedRefs(mem, sealed)}, nil
}

// newEphemeralPushKey generates this push's key pair in memory
// (seal.GenerateEphemeral) and returns the identity already in its wire
// form, a plan.PushKey, with the recipient that seals to it.
func newEphemeralPushKey() (plan.PushKey, seal.Recipient, error) {
	id, recipient, err := seal.GenerateEphemeral()
	if err != nil {
		return plan.PushKey{}, seal.Recipient{}, err
	}
	line, err := seal.EncodeEphemeral(id)
	if err != nil {
		return plan.PushKey{}, seal.Recipient{}, err
	}
	key, err := plan.NewPushKey(line)
	if err != nil {
		return plan.PushKey{}, seal.Recipient{}, err
	}
	return key, recipient, nil
}

// sealRef returns ref's archive (plan.WriteRefArchive) sealed to recipient
// as one age stream.
func sealRef(mem plan.BlobReader, ref string, recipient seal.Recipient) ([]byte, error) {
	var out bytes.Buffer
	w, err := seal.Seal(&out, []seal.Recipient{recipient})
	if err != nil {
		return nil, err
	}
	if err := plan.WriteRefArchive(w, mem, ref); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// uploadBlobs is the blob set uploadSticky sends: mem itself when s is nil,
// otherwise mem with every sealed ref replaced by its sealed stream.
func (s *stickySeal) uploadBlobs(mem plan.BlobReader) plan.BlobReader {
	if s == nil {
		return mem
	}
	return s.upload
}

// encodeChunk writes ch's push frame to w: a GONF-PUSH/2 frame carrying the
// ephemeral key when s is set and ch reads a sealed ref
// (plan.ChunkNeedsStickyKey), otherwise the unchanged GONF-PUSH/1 frame.
// chunkMem is the frame's embedded blob set (nil for a sticky-dir push,
// whose chunks are plan-only).
func (s *stickySeal) encodeChunk(w io.Writer, ch plan.Chunk, chunkMem plan.BlobReader) error {
	if s == nil || !plan.ChunkNeedsStickyKey(ch) {
		return plan.EncodePush(w, ch.Ops, chunkMem)
	}
	return plan.EncodePushWithKey(w, ch.Ops, chunkMem, s.key)
}
