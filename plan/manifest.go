package plan

import (
	"archive/tar"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BlobEntryKind classifies one entry of a packaged source tree.
type BlobEntryKind int

const (
	// BlobFile is a regular file packaged by content.
	BlobFile BlobEntryKind = iota
	// BlobDir is a directory entry; empty directories survive packaging.
	BlobDir
	// BlobSymlink is a symlink preserved with its raw target string —
	// exactly what os.Readlink returned, dangling symlinks included.
	// Packaging never reads through a symlink.
	BlobSymlink
)

// BlobEntry is one neutral entry of a packaged source tree. Both blob
// stores (disk and memory) package source trees through scanTree into
// this manifest, and both transports (the planDir blob tree and the
// GONF-PUSH tar) materialize the same entries — so a tree applied
// locally cannot diverge from the same tree pushed to a remote host.
type BlobEntry struct {
	// Rel is the slash-separated path relative to the tree root.
	Rel string
	// Kind selects which payload field carries the entry's content.
	Kind BlobEntryKind
	// Target is the raw symlink target (BlobSymlink entries only).
	Target string
	// Data is the file content (BlobFile entries only).
	Data []byte
}

// scanTree walks srcDir and returns the neutral tree manifest: one entry
// per directory (empty ones included), regular file (by content), and
// symlink (raw target, dangling included — never read through). Entries
// are sorted by Rel for deterministic transport. FIFOs, sockets, and
// devices fail loudly naming the path: packaging must never silently drop
// source content. Both blob stores package through this one function.
func scanTree(srcDir string) ([]BlobEntry, error) {
	info, err := os.Stat(srcDir)
	if err != nil {
		return nil, fmt.Errorf("plan: package tree %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("plan: package tree %s: not a directory", srcDir)
	}
	var out []BlobEntry
	walkErr := filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out = append(out, BlobEntry{Rel: filepath.ToSlash(rel), Kind: BlobSymlink, Target: target})
		case entry.IsDir():
			out = append(out, BlobEntry{Rel: filepath.ToSlash(rel), Kind: BlobDir})
		case entry.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out = append(out, BlobEntry{Rel: filepath.ToSlash(rel), Kind: BlobFile, Data: data})
		default:
			return fmt.Errorf("plan: package tree %s: unsupported file type at %s", srcDir, path)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sortEntries(out)
	return out, nil
}

// scanGlob classifies the basename matches of pattern into flat manifest
// entries (Rel = basename) through GlobMatchCounts — the one definition of
// the glob-match rule, shared with resource/dir's direct WithSourceGlob copy
// path and prune keep-set (resource/dir.GlobMatchCounts delegates here):
// counting matches (regular files and symlinks resolving to regular files,
// read through into content) are packaged by content; directories, dangling
// links and other non-regular entries are skipped, exactly like the direct
// copySourceGlob path, so an empty tree is a valid outcome.
func scanGlob(pattern string) ([]BlobEntry, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("plan: package glob %q: %w", pattern, err)
	}
	var out []BlobEntry
	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil {
			return nil, fmt.Errorf("plan: package glob match %s: %w", match, err)
		}
		if !GlobMatchCounts(match, info) {
			continue
		}
		// Glob blobs are flat regular-file pickers (their destination apply
		// runs with tree semantics, so preserved symlinks would either dangle
		// or change the entry from the direct WithSourceGlob behavior):
		// counting matches are read through into content.
		data, err := os.ReadFile(match)
		if err != nil {
			return nil, fmt.Errorf("plan: package glob match %s: %w", match, err)
		}
		out = append(out, BlobEntry{Rel: filepath.Base(match), Kind: BlobFile, Data: data})
	}
	sortEntries(out)
	return out, nil
}

// GlobMatchCounts is the single definition of the WithSourceGlob match rule,
// consumed by plan's own glob blob packaging (scanGlob above) and by
// resource/dir's copy path (copySourceGlob) and prune keep-set (pruneGlob)
// via resource/dir.GlobMatchCounts, a thin delegating wrapper — dir cannot
// define the canonical rule itself without plan importing resource/dir back,
// which is exactly the coupling this package no longer carries. A glob match
// counts when it is a regular file or a symlink that resolves to a regular
// file (read through into content); directories, dangling links, and other
// non-regular entries are skipped.
//
// info must be the os.Lstat result for match (the entry's own type, never
// following the link); resolving a symlink happens here via os.Stat. Every
// consumer of a source glob must classify its matches through this
// predicate so install, prune, and blob packaging cannot diverge — a
// divergence would make destination files flap between install and prune on
// each run.
func GlobMatchCounts(match string, info os.FileInfo) bool {
	switch {
	case info.IsDir():
		return false
	case info.Mode()&os.ModeSymlink != 0:
		// Follow a symlink to a regular file; skip symlink-to-dir, dangling
		// links, and every other non-regular target.
		target, err := os.Stat(match)
		return err == nil && target.Mode().IsRegular()
	case info.Mode().IsRegular():
		return true
	default:
		return false
	}
}

// sortEntries orders entries by Rel so packaging is deterministic.
func sortEntries(entries []BlobEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Rel < entries[j].Rel })
}

// materializeEntries writes manifest entries under dstRoot: files by
// content (0600), directories 0700, symlinks raw (exactly what the source
// tree carries, dangling included). Parent directories are created as
// needed, though sorted manifests list parents before children.
func materializeEntries(dstRoot string, entries []BlobEntry) error {
	for _, e := range entries {
		dst := filepath.Join(dstRoot, filepath.FromSlash(e.Rel))
		switch e.Kind {
		case BlobDir:
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return err
			}
		case BlobSymlink:
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return err
			}
			if err := os.Symlink(e.Target, dst); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(dst, e.Data, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

// slashParent returns the slash-separated parent path of rel, or "." for
// top-level entries (used to synthesize parent dir headers on the wire).
func slashParent(rel string) string {
	return filepath.ToSlash(filepath.Dir(rel))
}

// ensureDirHeaders emits tar.TypeDir headers for rel's parent chain under
// ref that have not been emitted yet (ref itself must already be emitted
// and marked in emitted). Explicit BlobDir entries go through the same
// helper, so no directory header is ever duplicated.
func ensureDirHeaders(tw *tar.Writer, ref, rel string, emitted map[string]bool) error {
	if rel == "." {
		return nil
	}
	cur := ref
	for _, p := range strings.Split(rel, "/") {
		cur += "/" + p
		name := cur + "/"
		if emitted[name] {
			continue
		}
		emitted[name] = true
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o700,
			Typeflag: tar.TypeDir,
		}); err != nil {
			return err
		}
	}
	return nil
}
