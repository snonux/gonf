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
	"sort"
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
		dirHdr := &tar.Header{
			Name:     ref + "/",
			Mode:     0o700,
			Typeflag: tar.TypeDir,
		}
		if err := tw.WriteHeader(dirHdr); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
		paths := make([]string, 0, len(tree))
		for p := range tree {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, rel := range paths {
			data := tree[rel]
			name := ref + "/" + rel
			// ensure parent dir headers
			dir := filepath.ToSlash(filepath.Dir(rel))
			if dir != "." {
				parts := strings.Split(dir, "/")
				cur := ref
				for _, p := range parts {
					cur += "/" + p
					_ = tw.WriteHeader(&tar.Header{
						Name:     cur + "/",
						Mode:     0o700,
						Typeflag: tar.TypeDir,
					})
				}
			}
			hdr := &tar.Header{
				Name: name,
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
		}
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
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

	isDir := hdr.Typeflag == tar.TypeDir || strings.HasSuffix(hdr.Name, "/")
	if isDir {
		return os.MkdirAll(target, 0o700)
	}
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
