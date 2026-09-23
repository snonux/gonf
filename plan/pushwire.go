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
// public, so anyone can produce a plan.age that decrypts). 256 MiB
// comfortably covers a legitimate plan's JSONL (docs/plan.md notes plans
// with many blob-backed ops can run to tens of MB) while staying far below
// what would meaningfully threaten a typical machine's RAM. Named here, not
// inlined, so it is easy to find and raise if a legitimate plan ever needs
// more.
const MaxDecompressedPushPlan = 256 << 20 // 256 MiB

// ErrPushPlanTooLarge is returned when decompressing the GONF-PUSH/1 frame's
// plan section would exceed MaxDecompressedPushPlan. The refusal is loud and
// immediate: no partial or truncated plan is ever handed to DecodePlanBytes.
var ErrPushPlanTooLarge = errors.New("plan push: decompressed plan section exceeds size limit")

// PushPayload is the decoded result of a GONF-PUSH/1 stream.
type PushPayload struct {
	Ops     []Op
	PlanDir string // non-empty when blobs were unpacked into Dir
}

// EncodePush writes a GONF-PUSH/1 frame to w: optional gzip+tar blobs from
// mem, then gzip-compressed plan JSONL. mem only needs to satisfy
// BlobReader (a nil BlobReader is treated as "no blobs"): callers pass
// *MemoryStore today, but any read-back implementation works.
func EncodePush(w io.Writer, ops []Op, mem BlobReader) error {
	if _, err := io.WriteString(w, pushMagic+"\n"); err != nil {
		return err
	}
	hasBlobs := mem != nil && mem.HasBlobs()
	if hasBlobs {
		if _, err := io.WriteString(w, "blobs 1\n"); err != nil {
			return err
		}
		if err := writeBlobsGzipTar(w, mem); err != nil {
			return err
		}
	} else {
		if _, err := io.WriteString(w, "blobs 0\n"); err != nil {
			return err
		}
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

// DecodePush reads either a GONF-PUSH/1 frame or bare JSONL from r.
// When blobs are present they are unpacked under planDir (must be an existing
// empty owner-only directory) and the payload reports that path in PlanDir.
// The plan dir's lifecycle belongs to the caller: the apply CLI owns it via
// NewApplyRunDir or the sticky -apply-dir (see staging.go). The plan
// section's gzip decompression is capped at MaxDecompressedPushPlan
// (ErrPushPlanTooLarge past it, see that constant's doc comment); blob
// extraction (readBlobsGzipTar) streams straight to planDir on disk rather
// than buffering in memory, so it is not part of this in-memory cap.
func DecodePush(r io.Reader, planDir string) (*PushPayload, error) {
	br := bufio.NewReader(r)
	peek, err := br.Peek(1)
	if err != nil {
		return nil, fmt.Errorf("plan push: read: %w", err)
	}
	if peek[0] == '{' || peek[0] == '\n' {
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

	magic, err := br.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("plan push: magic: %w", err)
	}
	if strings.TrimSpace(magic) != pushMagic {
		return nil, fmt.Errorf("plan push: bad magic %q", strings.TrimSpace(magic))
	}

	blobsLine, err := br.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("plan push: blobs line: %w", err)
	}
	blobsLine = strings.TrimSpace(blobsLine)
	var out PushPayload
	switch blobsLine {
	case "blobs 0":
		// no blob phase
	case "blobs 1":
		if planDir == "" {
			return nil, fmt.Errorf("plan push: blobs present but no plan dir")
		}
		if err := readBlobsGzipTar(br, planDir); err != nil {
			return nil, err
		}
		out.PlanDir = planDir
	default:
		return nil, fmt.Errorf("plan push: unexpected blobs line %q", blobsLine)
	}

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
	ops, err := DecodePlanBytes(raw)
	if err != nil {
		return nil, err
	}
	out.Ops = ops
	return &out, nil
}

// PushHasBlobs reports whether data — an already fully-read GONF-PUSH/1
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
	lines := bytes.SplitN(data, []byte("\n"), 3)
	if len(lines) < 2 {
		return false
	}
	return strings.TrimSpace(string(lines[1])) == "blobs 1"
}

func readGzipOrRaw(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return maybeGunzip(raw, MaxDecompressedPushPlan)
}

// maybeGunzip decompresses raw when it carries a gzip magic header,
// otherwise returns it unchanged (the bare-JSONL plan section case). max
// caps the DECOMPRESSED output, not just the compressed input — see
// MaxDecompressedPushPlan's doc comment for why the two are not
// interchangeable. It is a parameter rather than a direct read of that
// constant so this package's own tests can exercise the cap mechanism at a
// small scale (a few MB) instead of actually decompressing hundreds of
// megabytes just to prove the check fires.
//
// The limited reader is given max+1: reading one byte past the cap is what
// lets the check below tell "landed exactly on the limit" (max+1 bytes
// requested, fewer came back: genuine EOF, no error) apart from "would have
// kept going" (max+1 bytes came back: the stream had more) without needing
// to read past max+1 to find out. Exceeding it fails loudly with
// ErrPushPlanTooLarge; the plan is never silently truncated and handed to
// DecodePlanBytes as if it were complete.
func maybeGunzip(raw []byte, max int64) ([]byte, error) {
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		return raw, nil
	}
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("plan push: gzip: %w", err)
	}
	defer func() { _ = gr.Close() }()
	out, err := io.ReadAll(io.LimitReader(gr, max+1))
	if err != nil {
		return nil, fmt.Errorf("plan push: gzip: %w", err)
	}
	if int64(len(out)) > max {
		return nil, fmt.Errorf("%w (%d byte limit)", ErrPushPlanTooLarge, max)
	}
	return out, nil
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
	// Blob phase is a gzip stream; after it ends, "plan\n" follows.
	// Use a tee approach: gzip.Reader reads until EOF of the gzip member.
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("plan push: blobs gzip: %w", err)
	}
	gr.Multistream(false)
	defer func() { _ = gr.Close() }()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("plan push: blobs tar: %w", err)
		}
		if err := extractTarHeader(planDir, hdr, tr); err != nil {
			return err
		}
	}
	return nil
}

func extractTarHeader(planDir string, hdr *tar.Header, r io.Reader) error {
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
	return extractTarFile(target, r)
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

func extractTarFile(target string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	// O_NONBLOCK turns a planted FIFO at the target into a loud error instead
	// of allowing OpenFile to block while waiting for a reader.
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, r)
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
