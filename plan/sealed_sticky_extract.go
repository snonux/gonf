package plan

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Destination side of sealed sticky-dir refs (w82 phase 4 step 3, task
// 0g2; docs/design/plan-encryption.md, "Phase 4 design: sealed multi-chunk
// sticky-dir blobs"). The controller seals each ref's WriteRefArchive
// output as one age stream (sealed_sticky.go); the elevated chunk that
// reads it decrypts the stream (plan/seal, in internal/cli, since plan
// stays free of cryptography) and hands the plaintext reader to
// SealedRefExtractor.Extract, which unpacks it into the chunk's private
// run dir with the same bounded, zip-slip-checked extractor the push
// frame's blobs section uses (extractTarHeader).

// ErrSealedRefArchive marks a decrypted sealed ref whose archive is not
// exactly that ref's own members: not gzip+tar, empty, a member outside the
// ref (e.g. another ref's archive swapped in under this ref's sealed path,
// which the ephemeral key cannot tell apart since every ref of one push is
// sealed to it), an unsupported member type, or trailing data after the
// archive. A read error of the underlying (decrypting) reader, i.e. a
// truncated or tampered stream, is reported under it too. Its message, and
// every error Extract wraps it in, never names the ref or a member: ref
// names may derive from a secret-bearing resource name.
var ErrSealedRefArchive = errors.New("plan: sealed sticky ref archive is malformed, tampered, or holds members outside its ref")

// errSealedRefName marks a ref that is not a valid blob ref. It carries no
// ref text for the same reason as ErrSealedRefArchive.
var errSealedRefName = fmt.Errorf("%w: invalid blob ref", ErrSealedRefArchive)

// maxSealedRefTrailer bounds the decompressed bytes Extract accepts after
// the tar end marker (archive/tar's own zero-block padding) before it
// insists the gzip stream ends: anything beyond is refused rather than
// decompressed without limit.
const maxSealedRefTrailer = 64 << 10

// CheckSealedRef reports whether ref is a valid blob ref, failing with an
// ErrSealedRefArchive-wrapped error that does not echo it. The destination
// calls it before building ref's sealed path (SealedBlobPath).
func CheckSealedRef(ref string) error {
	if validateBlobRef(ref) != nil {
		return errSealedRefName
	}
	return nil
}

// SealedRefExtractor unpacks the decrypted archives of one chunk's sealed
// refs into one directory (the chunk's private run dir). Its extraction
// budget (MaxExtractedPushBlobs) is shared across every Extract call, so
// many refs together are bounded exactly like one push frame's blobs
// section.
type SealedRefExtractor struct {
	dir     string
	written int64
	max     int64
}

// NewSealedRefExtractor returns an extractor writing under dir, which must
// be an existing, private, empty directory (plan.NewSealedApplyRunDir).
func NewSealedRefExtractor(dir string) *SealedRefExtractor {
	return &SealedRefExtractor{dir: dir, max: MaxExtractedPushBlobs}
}

// Extract unpacks r, the plaintext of ref's sealed stream, under the
// extractor's directory. It refuses (ErrSealedRefArchive) an invalid ref,
// an archive holding no member or any member outside ref, a member type
// other than a regular file (the only type of a file ref), a directory or
// a symlink (a tree ref's), and anything after the gzip stream: r must end
// exactly there, so that the decrypting reader is read to its authenticated
// end. Size overruns fail with ErrPushBlobsTooLarge. A failed Extract may
// leave partial files behind; the caller removes the whole directory.
func (x *SealedRefExtractor) Extract(r io.Reader, ref string) error {
	if err := CheckSealedRef(ref); err != nil {
		return err
	}
	br := bufio.NewReader(r)
	gr, err := gzip.NewReader(br)
	if err != nil {
		return fmt.Errorf("%w: gzip: %w", ErrSealedRefArchive, err)
	}
	gr.Multistream(false)
	defer func() { _ = gr.Close() }()
	if err := x.extractMembers(tar.NewReader(gr), ref); err != nil {
		return err
	}
	return requireArchiveEnd(gr, br)
}

// extractMembers extracts every tar member, each of which must belong to
// ref, and refuses an archive with none.
func (x *SealedRefExtractor) extractMembers(tr *tar.Reader, ref string) error {
	members := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: tar: %w", ErrSealedRefArchive, err)
		}
		if !memberOfRef(ref, hdr) {
			return fmt.Errorf("%w: member %d is outside its ref or of an unsupported type", ErrSealedRefArchive, members+1)
		}
		if err := extractTarHeader(x.dir, hdr, tr, &x.written, x.max); err != nil {
			return err
		}
		members++
	}
	if members == 0 {
		return fmt.Errorf("%w: empty archive", ErrSealedRefArchive)
	}
	return nil
}

// memberOfRef reports whether hdr is one of ref's own members as
// WriteRefArchive emits them: the regular file named ref (a file ref), or
// the directory "ref/" and directories, symlinks and regular files below
// it (a tree ref). The name is compared after path.Clean, the same
// normalisation the extractor's tarTarget applies, so a name such as
// "ref/../other" that would land outside ref is refused here even though
// it stays inside the extraction directory.
func memberOfRef(ref string, hdr *tar.Header) bool {
	name := path.Clean(hdr.Name)
	below := strings.HasPrefix(name, ref+"/")
	if hdr.Typeflag != tar.TypeReg && hdr.Size != 0 {
		// A directory or symlink carries no data; a size would only make
		// the tar reader decompress and discard it without any budget.
		return false
	}
	switch hdr.Typeflag {
	case tar.TypeReg:
		return name == ref || below
	case tar.TypeDir:
		return (name == ref && strings.HasSuffix(hdr.Name, "/")) || below
	case tar.TypeSymlink:
		return below
	default:
		return false
	}
}

// requireArchiveEnd checks that nothing but tar padding follows the tar end
// marker, that the gzip stream then ends (verifying its checksum), and
// that the underlying reader br is at EOF. Reading br to EOF is what makes
// the decrypting reader authenticate its final segment: a truncated or
// tampered stream fails here even when the archive itself parsed.
func requireArchiveEnd(gr *gzip.Reader, br *bufio.Reader) error {
	n, err := io.Copy(io.Discard, io.LimitReader(gr, maxSealedRefTrailer+1))
	if err != nil {
		return fmt.Errorf("%w: gzip: %w", ErrSealedRefArchive, err)
	}
	if n > maxSealedRefTrailer {
		return fmt.Errorf("%w: data after the archive", ErrSealedRefArchive)
	}
	switch _, err := br.ReadByte(); {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return fmt.Errorf("%w: %w", ErrSealedRefArchive, err)
	default:
		return fmt.Errorf("%w: data after the gzip stream", ErrSealedRefArchive)
	}
}
