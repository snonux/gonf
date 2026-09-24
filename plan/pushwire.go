package plan

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const pushMagic = "GONF-PUSH/1"

// MaxDecompressedPushPlan bounds how many decompressed bytes maybeGunzip
// will produce from the GONF-PUSH/1 frame's gzip-compressed plan section:
// io.ReadAll-ing a gzip.Reader has no size limit of its own, and bounding
// only the compressed INPUT (as an earlier version of this code did, via
// readGzipOrRaw's own io.ReadAll) does nothing to stop a small, high-ratio
// gzip stream ("gzip bomb") from expanding far past it. Without a cap on the
// decompressed OUTPUT, a crafted or corrupted frame can inflate to gigabytes
// in memory before DecodePlanBytes ever gets a chance to reject it as
// malformed JSON — task be2 measured ~10.5 GB peak RSS from a 3 MB plan.age
// built this way (docs/plan-encryption.md, threat T10: recipients are
// public, so anyone can produce a plan.age that decrypts).
//
// be2 originally set this to 256 MiB. Task 3g2 lowered it to 64 MiB: this
// constant's own comment already said a legitimate plan's JSONL runs "to
// tens of MB" (docs/plan.md), so 256 MiB was 4x more headroom than any real
// plan needs — and every extra MiB of headroom is also extra MiB an
// attacker's gzip bomb gets to inflate to inside the "policy-compliant"
// zone, since io.ReadAll's own doubling growth (see readCapped, which
// replaced it) multiplies whatever size IS accepted. 64 MiB still
// comfortably covers every real recorded plan checked when this was chosen
// (a live conf/gonf plan.jsonl was 24 KB; this project's own test fixtures
// are all a few KB) while cutting the amplification ceiling by 4x. It is a
// var, not a const, so an embedder with a genuinely large legitimate plan
// can raise it — e.g. `plan.MaxDecompressedPushPlan = 256 << 20` before
// calling DecodePush — instead of hitting a hard, unfixable wall; do this
// before any concurrent DecodePush call, since the var itself is not
// synchronized.
var MaxDecompressedPushPlan int64 = 64 << 20 // 64 MiB

// ErrPushPlanTooLarge is returned when decompressing the GONF-PUSH/1 frame's
// plan section would exceed MaxDecompressedPushPlan. The refusal is loud and
// immediate: no partial or truncated plan is ever handed to DecodePlanBytes.
var ErrPushPlanTooLarge = errors.New("plan push: decompressed plan section exceeds size limit")

// MaxExtractedPushBlobs bounds the total bytes readBlobsGzipTar will write to
// planDir across every file in the GONF-PUSH/1 frame's blobs tar+gzip
// section. Unlike the plan section (gzip-decompressed into memory and capped
// by MaxDecompressedPushPlan above), the blobs section is streamed straight
// to disk — so neither that cap nor maxSealedFrameBytes (internal/cli),
// which only bounds the frame's still-compressed, on-the-wire size, does
// anything to stop a small, highly compressible tar entry (e.g. one header
// declaring a multi-gigabyte file of repeated bytes) from exhausting disk
// space instead of RAM. Task 2g2 measured this exactly: a 6,264,885 byte
// (~6.3 MB) sealed plan.age whose blobs section was such a bomb drove disk
// usage to 2154 MB at t=1s, 3677 MB at t=2s and 5481 MB at t=3s (~1.8 GB/s),
// then cleanup ran and the apply still REPORTED SUCCESS with no refusal at
// any point — a measured ~1000x disk-amplification DoS, with a theoretical
// ceiling of ~512 GiB given maxSealedFrameBytes' own 512 MiB cap (see that
// constant's doc comment, corrected by task 2g2 to no longer wave this case
// off). The threat is the same class be2 already named for the plan section:
// docs/plan-encryption.md, T2/T10 — anyone who can write the operator's
// plan.age or push stream (backup restore, CI artifact store, a shared
// directory) can fill "/" or $TMPDIR on the controller or the target
// mid-apply. 1 GiB comfortably covers a legitimate blob set (docs/plan.md:
// only files over plan.MaxInlineContent, 512 KiB, become blobs at all, and a
// realistic config tree runs to tens of MB) while staying far below what
// would meaningfully threaten a typical disk. Named here, not inlined, so it
// is easy to find and raise if a legitimate blob set ever needs more.
const MaxExtractedPushBlobs = 1 << 30 // 1 GiB

// ErrPushBlobsTooLarge is returned when extracting the GONF-PUSH/1 frame's
// blobs tar+gzip section would write more than MaxExtractedPushBlobs bytes
// to planDir. The refusal is loud and immediate: extractTarFile checks each
// entry's own declared hdr.Size against the REMAINING budget before writing
// any of that entry's bytes, so an oversized entry is refused up front
// rather than only caught after it has already been written; a running,
// cumulative counter (threaded across every extractTarHeader call in one
// readBlobsGzipTar run) also catches many smaller entries that together
// exceed the cap. No partial file is left behind by the entry that trips
// the cap: its target is never opened before the size check runs.
var ErrPushBlobsTooLarge = errors.New("plan push: extracted blobs exceed size limit")

// PushPayload is the decoded result of a GONF-PUSH/1 or GONF-PUSH/2 stream.
type PushPayload struct {
	Ops     []Op
	PlanDir string // non-empty when blobs were unpacked into Dir
	// Key is the per-push ephemeral identity a GONF-PUSH/2 frame carries
	// (see pushwire_key.go), nil for a GONF-PUSH/1 frame or bare JSONL.
	// Only DecodePushWithKey ever sets it: DecodePush refuses a /2 frame.
	// It is private key material: PushKey prints a placeholder for every
	// fmt verb, and a stray %v of the payload prints only this pointer.
	Key *PushKey
}

// EncodePush writes a GONF-PUSH/1 frame to w: optional gzip+tar blobs from
// mem, then gzip-compressed plan JSONL. mem only needs to satisfy
// BlobReader (a nil BlobReader is treated as "no blobs"): callers pass
// *MemoryStore today, but any read-back implementation works. Its bytes
// are unchanged by the GONF-PUSH/2 extension: a push that needs no
// ephemeral key keeps sending exactly this frame (EncodePushWithKey in
// pushwire_key.go is the /2 form).
func EncodePush(w io.Writer, ops []Op, mem BlobReader) error {
	return encodePushFrame(w, pushMagic, "", ops, mem)
}

// encodePushFrame writes one push frame: the magic line, the key line when
// keyLine is non-empty (GONF-PUSH/2 only, see EncodePushWithKey), the blobs
// line and optional blobs section, then the plan marker and the
// gzip-compressed plan JSONL. Errors come only from w, the tar/gzip
// writers and EncodePlan, never from keyLine's content, so none of them
// can carry the key.
func encodePushFrame(w io.Writer, magic, keyLine string, ops []Op, mem BlobReader) error {
	if _, err := io.WriteString(w, magic+"\n"); err != nil {
		return err
	}
	if keyLine != "" {
		if _, err := io.WriteString(w, pushKeyPrefix+keyLine+"\n"); err != nil {
			return err
		}
	}
	if err := writePushBlobs(w, mem); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "plan\n"); err != nil {
		return err
	}
	raw, err := EncodePlan(ops)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(w)
	if _, err := gz.Write(raw); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

// writePushBlobs writes the frame's blobs line ("blobs 0" or "blobs 1") and,
// for "blobs 1", the gzip+tar blobs section from mem (a nil BlobReader, or
// one without blobs, is "blobs 0").
func writePushBlobs(w io.Writer, mem BlobReader) error {
	if mem == nil || !mem.HasBlobs() {
		_, err := io.WriteString(w, "blobs 0\n")
		return err
	}
	if _, err := io.WriteString(w, "blobs 1\n"); err != nil {
		return err
	}
	return writeBlobsGzipTar(w, mem)
}

// DecodePush reads either a GONF-PUSH/1 frame or bare JSONL from r.
// When blobs are present they are unpacked under planDir (must be an existing
// empty owner-only directory) and the payload reports that path in PlanDir.
// The plan dir's lifecycle belongs to the caller: the apply CLI owns it via
// NewApplyRunDir or the sticky -apply-dir (see staging.go). The plan
// section's gzip decompression is capped at MaxDecompressedPushPlan
// (ErrPushPlanTooLarge past it, see that constant's doc comment); blob
// extraction (readBlobsGzipTar) streams straight to planDir on disk rather
// than buffering in memory, so it is not part of this in-memory cap — it has
// its own, separate disk-bytes-written cap, MaxExtractedPushBlobs
// (ErrPushBlobsTooLarge past it; see that constant's doc comment for why an
// in-memory-only cap does nothing to stop a disk-exhaustion DoS here).
//
// A GONF-PUSH/2 frame (one carrying a per-push ephemeral key, see
// pushwire_key.go) is refused with ErrPushKeyNotAccepted right after its
// magic line: before the key line is read and before any blob is
// extracted. Only a caller that can honour the key, and says so by calling
// DecodePushWithKey, ever receives one: today only internal/cli's sticky
// "-apply-dir" chunk path (task 0g2), which decrypts the sealed refs
// before applying. Every other decode path (plain apply without
// -apply-dir, strict preview, the sealed plan.age apply) therefore stays
// fail-closed against a keyed frame instead of applying ops that would read
// sealed bytes as if they were plaintext content.
func DecodePush(r io.Reader, planDir string) (*PushPayload, error) {
	return decodePush(r, planDir, false)
}

// decodePush implements DecodePush (acceptKey false) and DecodePushWithKey
// (acceptKey true): the header (the magic plus, for /2, the key line), the
// blobs phase, then the plan section.
func decodePush(r io.Reader, planDir string, acceptKey bool) (*PushPayload, error) {
	br := bufio.NewReader(r)
	peek, err := br.Peek(1)
	if err != nil {
		return nil, fmt.Errorf("plan push: read: %w", err)
	}
	if peek[0] == '{' || peek[0] == '\n' {
		return decodeBareJSONL(br)
	}
	var out PushPayload
	if err := readPushHeader(br, acceptKey, &out); err != nil {
		return nil, err
	}
	if err := readPushBlobsPhase(br, planDir, &out); err != nil {
		return nil, err
	}
	ops, err := readPushPlanSection(br)
	if err != nil {
		return nil, err
	}
	out.Ops = ops
	return &out, nil
}

// decodeBareJSONL decodes a push stream that is plain plan JSONL (no frame).
func decodeBareJSONL(br *bufio.Reader) (*PushPayload, error) {
	raw, err := io.ReadAll(br)
	if err != nil {
		return nil, err
	}
	ops, err := DecodePlanBytes(raw)
	if err != nil {
		return nil, err
	}
	return &PushPayload{Ops: ops}, nil
}

// readPushHeader reads the magic line and, for GONF-PUSH/2, the key line
// into out.Key. A /2 frame is refused before its key line is read unless
// acceptKey (see DecodePush). The bad-magic error quotes the magic line:
// it is always the frame's first line, which never holds key material.
func readPushHeader(br *bufio.Reader, acceptKey bool, out *PushPayload) error {
	magic, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("plan push: magic: %w", err)
	}
	switch strings.TrimSpace(magic) {
	case pushMagic:
		return nil
	case pushMagicV2:
		if !acceptKey {
			return ErrPushKeyNotAccepted
		}
		key, err := readPushKeyLine(br)
		if err != nil {
			return err
		}
		out.Key = &key
		return nil
	default:
		return fmt.Errorf("plan push: bad magic %q", strings.TrimSpace(magic))
	}
}

// readPushBlobsPhase reads the blobs line and, for "blobs 1", extracts the
// blobs section under planDir (recorded in out.PlanDir).
func readPushBlobsPhase(br *bufio.Reader, planDir string, out *PushPayload) error {
	blobsLine, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("plan push: blobs line: %w", err)
	}
	blobsLine = strings.TrimSpace(blobsLine)
	switch blobsLine {
	case "blobs 0":
		return nil // no blob phase
	case "blobs 1":
		if planDir == "" {
			return fmt.Errorf("plan push: blobs present but no plan dir")
		}
		if err := readBlobsGzipTar(br, planDir); err != nil {
			return err
		}
		out.PlanDir = planDir
		return nil
	default:
		return fmt.Errorf("plan push: unexpected blobs line %q", blobsLine)
	}
}

// readPushPlanSection reads the plan marker and decodes the (normally
// gzip-compressed) plan JSONL that follows it.
func readPushPlanSection(br *bufio.Reader) ([]Op, error) {
	planLine, err := br.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("plan push: plan marker: %w", err)
	}
	if strings.TrimSpace(planLine) != "plan" {
		return nil, fmt.Errorf("plan push: expected plan marker, got %q", strings.TrimSpace(planLine))
	}
	raw, err := readGzipOrRaw(br)
	if err != nil {
		return nil, err
	}
	return DecodePlanBytes(raw)
}

// PushHasBlobs reports whether data — an already fully-read GONF-PUSH/1 or /2
// frame, or bare JSONL, exactly as DecodePush itself would receive it as its
// r argument — declares a blobs phase, using the same first-byte and
// blobs-line checks DecodePush performs while streaming, without mutating or
// copying data. It exists for a sealed apply (plan/seal, task 3b2), which
// must hold the whole decrypted frame in memory before trusting any of it
// (age authenticates only its final segment at EOF) and so cannot let
// DecodePush read straight from the wire the way an ordinary push does: this
// lets it decide, from the bytes already in hand, whether it needs a run
// directory for DecodePush's blob-unpacking side effect before calling
// DecodePush for real. DecodePush's own parsing stays the single authority
// on whether the frame is actually well-formed; a "true" here that turns out
// wrong (a frame corrupted in a way that does not touch this early prefix)
// simply means DecodePush goes on to report that corruption itself.
func PushHasBlobs(data []byte) bool {
	if len(data) == 0 || data[0] == '{' || data[0] == '\n' {
		return false // bare JSONL (or empty): DecodePush never unpacks blobs for it
	}
	lines := bytes.SplitN(data, []byte("\n"), 4)
	blobsIdx := 1
	if strings.TrimSpace(string(lines[0])) == pushMagicV2 {
		blobsIdx = 2 // a GONF-PUSH/2 frame's key line precedes its blobs line
	}
	if len(lines) <= blobsIdx {
		return false
	}
	return strings.TrimSpace(string(lines[blobsIdx])) == "blobs 1"
}

func readGzipOrRaw(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return maybeGunzip(raw, MaxDecompressedPushPlan)
}

// pushPlanReadChunk is how much readCapped reads between size checks: a
// small, fixed amount unrelated to max (which the caller may set to tens of
// MB), so that a stream which turns out to exceed max is refused after at
// most one chunk's worth of bytes crossed the cap — not after the buffer
// has grown anywhere near max. Matches secret.readChunk's convention
// (secret/file.go) for a bounded chunked read.
const pushPlanReadChunk = 32 << 10 // 32 KiB

// maybeGunzip decompresses raw when it carries a gzip magic header,
// otherwise returns it unchanged (the bare-JSONL plan section case). max
// caps the DECOMPRESSED output, not just the compressed input — see
// MaxDecompressedPushPlan's doc comment for why the two are not
// interchangeable. It is a parameter rather than a direct read of that var
// so this package's own tests can exercise the cap mechanism at a small
// scale (a few MB) instead of actually decompressing hundreds of megabytes
// just to prove the check fires.
func maybeGunzip(raw []byte, max int64) ([]byte, error) {
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		return raw, nil
	}
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("plan push: gzip: %w", err)
	}
	defer func() { _ = gr.Close() }()
	out, err := readCapped(gr, max)
	if err != nil {
		if errors.Is(err, ErrPushPlanTooLarge) {
			// Already fully formatted (readCapped's own message); wrapping
			// it again here would double up the "plan push: gzip:" prefix
			// ErrPushPlanTooLarge's own message never had.
			return nil, err
		}
		return nil, fmt.Errorf("plan push: gzip: %w", err)
	}
	return out, nil
}

// readCapped reads r to EOF in pushPlanReadChunk-sized chunks into a
// bytes.Buffer pre-grown to just ONE chunk — not to max, which would
// reintroduce, for a call about to be refused anyway, the same oversized
// up-front allocation this function exists to avoid. It fails loudly with
// ErrPushPlanTooLarge the moment the running byte count would exceed max,
// before ever writing the bytes that crossed it into the buffer: the plan
// is never silently truncated and handed to DecodePlanBytes as if it were
// complete, and the refusal path never materializes anywhere near max
// bytes, let alone past it.
//
// This replaces an earlier io.ReadAll(io.LimitReader(r, max+1)) (task be2):
// io.ReadAll's internal buffer grows by repeated doubling, so by the time
// its own post-hoc length check could run, it could already have
// over-allocated up to ~2x the bytes it actually needed — for a stream well
// past max, peak memory during that single ReadAll call could approach 2x
// max before the caller ever learned the stream was too large. Checking the
// running total after every small, fixed-size chunk instead means an
// oversized stream is caught within pushPlanReadChunk bytes of crossing
// max, independent of the buffer's own growth strategy.
func readCapped(r io.Reader, max int64) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(pushPlanReadChunk)
	chunk := make([]byte, pushPlanReadChunk)
	var total int64
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			total += int64(n)
			if total > max {
				return nil, fmt.Errorf("%w (%d byte limit)", ErrPushPlanTooLarge, max)
			}
			buf.Write(chunk[:n])
		}
		if errors.Is(err, io.EOF) {
			return buf.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func writeBlobsGzipTar(w io.Writer, mem BlobReader) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, ref := range mem.Refs() {
		if data, ok := mem.FileBlob(ref); ok {
			hdr := &tar.Header{
				Name: ref,
				Mode: 0o600,
				Size: int64(len(data)),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				_ = tw.Close()
				_ = gz.Close()
				return err
			}
			if _, err := tw.Write(data); err != nil {
				_ = tw.Close()
				_ = gz.Close()
				return err
			}
			continue
		}
		tree, ok := mem.TreeBlob(ref)
		if !ok {
			continue
		}
		if err := writeTreeTar(tw, ref, tree); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

// writeTreeTar emits one tree blob as tar entries: the tree root dir, then
// every manifest entry — BlobDir as tar.TypeDir headers, BlobSymlink as
// tar.TypeSymlink headers carrying the raw target (Linkname; the tar just
// transports the link, the destination recreates it), BlobFile by content
// (0600). Entries are sorted by Rel, so parent directories precede their
// children; ensureDirHeaders still synthesizes any missing ancestor header
// and dedupes against explicit dir entries, so no directory header is
// emitted twice.
func writeTreeTar(tw *tar.Writer, ref string, tree []BlobEntry) error {
	if err := tw.WriteHeader(&tar.Header{
		Name:     ref + "/",
		Mode:     0o700,
		Typeflag: tar.TypeDir,
	}); err != nil {
		return err
	}
	emitted := map[string]bool{ref + "/": true}
	ensureDirs := func(rel string) error { return ensureDirHeaders(tw, ref, slashParent(rel), emitted) }
	for _, e := range tree {
		switch e.Kind {
		case BlobDir:
			if err := ensureDirHeaders(tw, ref, e.Rel, emitted); err != nil {
				return err
			}
		case BlobSymlink:
			if err := ensureDirs(e.Rel); err != nil {
				return err
			}
			if err := tw.WriteHeader(&tar.Header{
				Name:     ref + "/" + e.Rel,
				Mode:     0o777,
				Typeflag: tar.TypeSymlink,
				Linkname: e.Target,
			}); err != nil {
				return err
			}
		default:
			if err := ensureDirs(e.Rel); err != nil {
				return err
			}
			if err := tw.WriteHeader(&tar.Header{
				Name: ref + "/" + e.Rel,
				Mode: 0o600,
				Size: int64(len(e.Data)),
			}); err != nil {
				return err
			}
			if _, err := tw.Write(e.Data); err != nil {
				return err
			}
		}
	}
	return nil
}

func readBlobsGzipTar(r *bufio.Reader, planDir string) error {
	return readBlobsGzipTarCapped(r, planDir, MaxExtractedPushBlobs)
}

// readBlobsGzipTarCapped is readBlobsGzipTar's implementation, taking max as
// a parameter (not a direct read of MaxExtractedPushBlobs) so this package's
// own tests can exercise the cap mechanism against a small, fast bomb
// instead of writing a genuine gigabyte to a test's temp dir — mirroring how
// maybeGunzip takes its own max parameter for the same reason (task be2).
// written is a single counter shared, by pointer, across every tar entry in
// this one archive: the cap bounds the CUMULATIVE bytes extracted from the
// whole blobs section, not any one file in isolation, so many smaller
// entries that together exceed max are refused exactly as a single huge one
// would be.
func readBlobsGzipTarCapped(r *bufio.Reader, planDir string, max int64) error {
	// Blob phase is a gzip stream; after it ends, "plan\n" follows.
	// Use a tee approach: gzip.Reader reads until EOF of the gzip member.
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("plan push: blobs gzip: %w", err)
	}
	gr.Multistream(false)
	defer func() { _ = gr.Close() }()
	tr := tar.NewReader(gr)
	var written int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("plan push: blobs tar: %w", err)
		}
		if err := extractTarHeader(planDir, hdr, tr, &written, max); err != nil {
			return err
		}
	}
	return nil
}

func extractTarHeader(planDir string, hdr *tar.Header, r io.Reader, written *int64, max int64) error {
	target, err := tarTarget(planDir, hdr.Name)
	if err != nil {
		return err
	}
	if target == "" {
		return nil
	}
	// Defense-in-depth against planted ancestor symlinks (e.g. a crafted
	// TypeSymlink "blobs" -> /etc followed by a regular "blobs/x"): no path
	// component between planDir and the target may be a symlink, or the
	// MkdirAll/OpenFile below would write through it and escape planDir.
	// The push stream originates from the trusted controller, and the disk
	// Store packages admin source trees as-is — this only closes an
	// extraction-time tampering window.
	if err := ensureNoAncestorSymlink(planDir, target); err != nil {
		return err
	}

	if hdr.Typeflag == tar.TypeDir || strings.HasSuffix(hdr.Name, "/") {
		return os.MkdirAll(target, 0o700)
	}
	if hdr.Typeflag == tar.TypeSymlink {
		return extractTarSymlink(target, hdr.Linkname)
	}
	return extractTarFile(target, r, hdr.Size, written, max)
}

func tarTarget(planDir, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == "" {
		return "", nil
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("plan push: zip-slip path %q", name)
	}
	target := filepath.Join(planDir, clean)
	rel, err := filepath.Rel(planDir, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("plan push: zip-slip path %q", name)
	}
	return target, nil
}

func extractTarSymlink(target, linkname string) error {
	// Recreate the symlink with its raw link target — the exact string
	// the admin's source tree carries (dangling included), matching the
	// disk Store's planDir tree. A pre-existing entry of ANY type at the
	// target is removed first: os.Symlink refuses to replace an existing
	// name, and a stale directory would otherwise block extraction.
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("plan push: clear %s for symlink: %w", target, err)
	}
	if err := os.Symlink(linkname, target); err != nil {
		return fmt.Errorf("plan push: symlink %s -> %s: %w", target, linkname, err)
	}
	return nil
}

// extractTarFile writes one tar entry's data to target, refusing before it
// opens the target at all when size (the entry's own declared hdr.Size)
// alone would push *written past max — the immediate half of
// MaxExtractedPushBlobs' enforcement (see that constant's doc comment): no
// bytes of an oversized entry are ever written, not even a partial file.
// Once past that check, io.CopyN(f, r, size) — not the unbounded io.Copy
// this replaced — is the per-file bounded read the same doc comment
// describes: defense-in-depth so a single entry can never write more than
// its own declared size even if archive/tar's own per-entry accounting were
// ever bypassed. written is advanced by exactly what CopyN actually wrote,
// so a later entry's remaining-budget check always reflects real disk usage.
func extractTarFile(target string, r io.Reader, size int64, written *int64, max int64) error {
	remaining := max - *written
	if size > remaining {
		return fmt.Errorf("%w (%d byte limit)", ErrPushBlobsTooLarge, max)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	// O_NONBLOCK turns a planted FIFO at the target into a loud error instead
	// of allowing OpenFile to block while waiting for a reader.
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return err
	}
	n, copyErr := io.CopyN(f, r, size)
	*written += n
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// ensureNoAncestorSymlink refuses extraction when any existing path component
// between planDir and target is a symlink: MkdirAll and OpenFile would follow
// it and write outside planDir. planDir is freshly created by the push flow,
// so an ancestor symlink there means a crafted stream, not the admin's tree.
func ensureNoAncestorSymlink(planDir, target string) error {
	rel, err := filepath.Rel(planDir, target)
	if err != nil {
		return err
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	cur := planDir
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("plan push: stat %s: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("plan push: refusing extraction under symlink %s", cur)
		}
	}
	return nil
}
