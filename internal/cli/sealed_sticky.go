package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"github.com/snonux/gonf/internal/sealeddir"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// Sealed sticky-dir refs, destination side (w82 phase 4 step 3, task 0g2;
// docs/plan-encryption.md, "Phase 4 design: sealed multi-chunk sticky-dir
// blobs").
//
// A multi-chunk push uploads its blobs to a sticky dir owned by the SSH
// login user. A ref that a sensitive op of an elevated chunk reads arrives
// there only sealed, at plan.SealedBlobPath(ref), to a per-push ephemeral
// key (internal/remote, task zf2). That chunk's own stdin frame is a
// GONF-PUSH/2 frame carrying the key. Before anything of the chunk applies,
// stageSealedStickyRefs decrypts every sealed ref its ops read into a fresh
// 0700 plan.NewSealedApplyRunDir() and returns a context under which those
// refs, and only those, resolve to that private dir (internal/sealeddir);
// the chunk's other refs keep resolving to the sticky dir. The plaintext
// therefore lands only in the private dir, never in the sticky dir, and the
// private dir is removed when the chunk's apply returns, on every path.
//
// Refusals, all before any op applies, and none naming a ref, a path below
// the private dir, or the key (a ref name may derive from a secret-bearing
// resource name; ops are named by their position in the chunk instead):
//   - a /2 frame whose ops read no sealed ref (errKeyWithoutSealedOp): the
//     controller sends the key only to a chunk that needs it;
//   - a /2 frame that embeds blobs (errKeyedFrameWithBlobs, cliApplyStdin):
//     sealed refs travel in the sticky dir, never in the chunk frame;
//   - a malformed key (errMalformedStickyKey);
//   - a sealed ref that is missing, not a regular file, does not decrypt
//     with this push's key (tampered, truncated, or from another push), or
//     whose archive holds anything but that ref's own members (another
//     ref's archive swapped in, extra members, trailing data);
//   - a /1 frame (no key) whose op reads a ref the sticky dir holds sealed
//     (errSealedRefWithoutKey).
//
// The key itself only ever lives in this call stack's memory: it arrives on
// stdin, is parsed by seal.ParseEphemeral, and is never logged, written to
// disk, placed in an error, or passed on argv or in the environment.

var (
	errKeyWithoutSealedOp   = errors.New("GONF-PUSH/2 frame carries an ephemeral key, but none of its ops reads a sealed sticky ref")
	errKeyedFrameWithBlobs  = errors.New("GONF-PUSH/2 frame embeds blobs; sealed refs must come from the sticky -apply-dir")
	errKeyWithoutApplyDir   = errors.New("GONF-PUSH/2 frame needs the sticky -apply-dir its sealed refs were uploaded to")
	errMalformedStickyKey   = errors.New("GONF-PUSH/2 frame carries a malformed ephemeral key")
	errSealedRefWithoutKey  = errors.New("reads a sealed sticky ref, but the frame carries no ephemeral key")
	errSealedRefMissing     = errors.New("sealed blob is missing from the sticky dir")
	errSealedRefNotRegular  = errors.New("sealed blob is not a regular file")
	errSealedRefUnreadable  = errors.New("sealed blob cannot be opened")
	errSealedRefDecrypt     = errors.New("sealed blob does not decrypt with this push's key (tampered, or not sealed for this push)")
	errSealedRefExtractFail = errors.New("sealed blob could not be extracted")
)

// stageSealedStickyRefs prepares the sealed sticky refs of one decoded
// -apply-dir chunk (see this file's header comment). It returns the context
// the chunk must apply under and a cleanup that removes the private run
// dir; cleanup is always safe to call and the caller defers it
// unconditionally. With no key in payload it only refuses an op that reads
// a sealed ref. payload.Key is cleared, so the key is referenced only here.
func stageSealedStickyRefs(ctx context.Context, payload *plan.PushPayload, applyDir string) (context.Context, func(), error) {
	noop := func() {}
	key := payload.Key
	payload.Key = nil
	if key == nil {
		return ctx, noop, refuseUnkeyedSealedRefs(payload.Ops, applyDir)
	}
	if applyDir == "" {
		return ctx, noop, errKeyWithoutApplyDir
	}
	sealedOps := plan.KeyedChunkSealedOps(payload.Ops)
	if len(sealedOps) == 0 {
		return ctx, noop, errKeyWithoutSealedOp
	}
	id, err := seal.ParseEphemeral(key.Line())
	if err != nil {
		return ctx, noop, errMalformedStickyKey // never wraps err: keep key-derived text out
	}
	dir, cleanup, err := plan.NewSealedApplyRunDir()
	if err != nil {
		return ctx, noop, fmt.Errorf("sealed sticky refs: run dir: %w", err)
	}
	refs, err := decryptSealedRefs(applyDir, dir, payload.Ops, sealedOps, id)
	if err != nil {
		cleanup()
		return ctx, noop, err
	}
	return sealeddir.With(ctx, dir, refs), cleanup, nil
}

// decryptSealedRefs opens, decrypts and extracts into dir the sealed ref of
// every op at the positions sealedOps names (each distinct ref once),
// reading only below applyDir (os.Root: no symlink can lead outside it). It
// returns the refs it extracted. Errors name the op by position only.
func decryptSealedRefs(applyDir, dir string, ops []plan.Op, sealedOps []int, id seal.Identity) ([]string, error) {
	root, err := os.OpenRoot(applyDir)
	if err != nil {
		return nil, fmt.Errorf("sealed sticky refs: open apply-dir: %w", err)
	}
	defer func() { _ = root.Close() }()
	x := plan.NewSealedRefExtractor(dir)
	seen := map[string]bool{}
	var refs []string
	for _, i := range sealedOps {
		ref := ops[i].Blob
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if err := decryptSealedRef(root, x, ref, id); err != nil {
			return nil, fmt.Errorf("sealed sticky ref of op %d: %w", i, redactSealedRefErr(err))
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// decryptSealedRef opens ref's sealed stream below root, decrypts it with
// id and extracts the plaintext archive through x. The decrypted bytes go
// straight from the age reader into the bounded extractor: nothing is
// buffered whole in memory, and x reads the stream to its authenticated
// end before reporting success.
func decryptSealedRef(root *os.Root, x *plan.SealedRefExtractor, ref string, id seal.Identity) error {
	if err := plan.CheckSealedRef(ref); err != nil {
		return err
	}
	f, err := openSealedFile(root, plan.SealedBlobPath(ref))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	r, err := seal.Open(f, []seal.Identity{id})
	if err != nil {
		return errSealedRefDecrypt
	}
	return x.Extract(r, ref)
}

// openSealedFile opens name below root for reading, refusing a symlink
// (the Lstat, then O_NOFOLLOW for one swapped in after it), a FIFO that
// would block (O_NONBLOCK, then the regular-file check on the open file)
// and anything else that is not a regular file. The sticky dir belongs to
// the login user, who may have replaced a sealed stream with anything;
// os.Root keeps every lookup, including an intermediate symlink, inside it.
func openSealedFile(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, sealedOpenErr(err)
	}
	if !info.Mode().IsRegular() {
		return nil, errSealedRefNotRegular
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, sealedOpenErr(err)
	}
	if info, err = f.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, errSealedRefNotRegular
	}
	return f, nil
}

// sealedOpenErr classifies a failed lookup or open of a sealed stream
// without its path: missing, a symlink (ELOOP from O_NOFOLLOW), or
// otherwise unreadable with only the errno kept.
func sealedOpenErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errSealedRefMissing
	case errors.Is(err, syscall.ELOOP):
		return errSealedRefNotRegular
	default:
		return fmt.Errorf("%w: %w", errSealedRefUnreadable, errnoOf(err))
	}
}

// refuseUnkeyedSealedRefs refuses a keyless -apply-dir chunk with an op
// whose blob the sticky dir holds sealed: such an op cannot read the
// plaintext, so the frame should have been a keyed one. Without an
// -apply-dir there is no sticky dir and nothing to check.
func refuseUnkeyedSealedRefs(ops []plan.Op, applyDir string) error {
	if applyDir == "" {
		return nil
	}
	root, err := os.OpenRoot(applyDir)
	if err != nil {
		return fmt.Errorf("sealed sticky refs: open apply-dir: %w", err)
	}
	defer func() { _ = root.Close() }()
	for i, op := range ops {
		if op.Blob == "" || plan.CheckSealedRef(op.Blob) != nil {
			continue
		}
		if _, err := root.Lstat(plan.SealedBlobPath(op.Blob)); err == nil {
			return fmt.Errorf("op %d %w", i, errSealedRefWithoutKey)
		}
	}
	return nil
}

// redactSealedRefErr keeps a sealed-ref failure's class and drops anything
// that could name a ref, a member or a path below the private run dir:
// errors built in this file and plan's sealed-archive and size errors are
// already name-free and pass through; a file-system error keeps only its
// operation and errno; anything else becomes errSealedRefExtractFail.
func redactSealedRefErr(err error) error {
	switch {
	case errors.Is(err, errSealedRefMissing), errors.Is(err, errSealedRefNotRegular),
		errors.Is(err, errSealedRefUnreadable), errors.Is(err, errSealedRefDecrypt),
		errors.Is(err, plan.ErrSealedRefArchive), errors.Is(err, plan.ErrPushBlobsTooLarge):
		return err
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%w: %s: %w", errSealedRefExtractFail, pe.Op, pe.Err)
	}
	return errSealedRefExtractFail
}

// errnoOf returns err's syscall.Errno when it carries one (a path-free
// cause), otherwise a fixed, path-free stand-in (e.g. for os.Root refusing
// a lookup that would escape the sticky dir through a symlink).
func errnoOf(err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return errors.New("not reachable inside the sticky dir")
}
