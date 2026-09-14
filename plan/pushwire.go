package plan

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const pushMagic = "GONF-PUSH/1"

// PushPayload is the decoded result of a GONF-PUSH/1 stream.
type PushPayload struct {
	Ops     []Op
	PlanDir string // non-empty when blobs were unpacked into Dir
	cleanup func()
}

// Close removes the temporary plan dir when present.
func (p *PushPayload) Close() {
	if p != nil && p.cleanup != nil {
		p.cleanup()
		p.cleanup = nil
	}
}

// EncodePush writes a GONF-PUSH/1 frame to w: optional gzip+tar blobs from
// mem, then gzip-compressed plan JSONL.
func EncodePush(w io.Writer, ops []Op, mem *MemoryStore) error {
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
// empty owner-only directory). Caller must Close the payload to remove planDir
// contents if cleanup was registered — prefer SweepApplyRoot helpers in apply
// CLI for lifecycle; here cleanup is optional via returned Close.
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

func decodeMaybeGzipPlan(raw []byte) (*PushPayload, error) {
	data, err := maybeGunzip(raw)
	if err != nil {
		return nil, err
	}
	ops, err := DecodePlanBytes(data)
	if err != nil {
		return nil, err
	}
	return &PushPayload{Ops: ops}, nil
}

func readGzipOrRaw(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return maybeGunzip(raw)
}

func maybeGunzip(raw []byte) ([]byte, error) {
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("plan push: gzip: %w", err)
		}
		defer gr.Close()
		return io.ReadAll(gr)
	}
	return raw, nil
}

func writeBlobsGzipTar(w io.Writer, mem *MemoryStore) error {
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
	defer gr.Close()
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
	name := filepath.Clean(filepath.FromSlash(hdr.Name))
	if name == "." || name == "" {
		return nil
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
		return fmt.Errorf("plan push: zip-slip path %q", hdr.Name)
	}
	target := filepath.Join(planDir, name)
	rel, err := filepath.Rel(planDir, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("plan push: zip-slip path %q", hdr.Name)
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

	switch {
	case hdr.Typeflag == tar.TypeDir || strings.HasSuffix(hdr.Name, "/"):
		return os.MkdirAll(target, 0o700)
	case hdr.Typeflag == tar.TypeSymlink:
		// Recreate the symlink with its raw link target — the exact string
		// the admin's source tree carries (dangling included), matching the
		// disk Store's planDir tree. A pre-existing entry of ANY type at the
		// target is removed first: os.Symlink refuses to replace an existing
		// name, and a stale directory would otherwise block extraction. The
		// header's mode is ignored — symlinks carry no meaningful mode.
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			// Note: a non-empty pre-existing directory at the target also fails
			// here (os.Remove does not remove non-empty dirs) and aborts the
			// stream; planDir is fresh in practice.
			return fmt.Errorf("plan push: clear %s for symlink: %w", target, err)
		}
		if err := os.Symlink(hdr.Linkname, target); err != nil {
			return fmt.Errorf("plan push: symlink %s -> %s: %w", target, hdr.Linkname, err)
		}
		return nil
	default:
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
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
