package remote

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// fakeBlobReader is a minimal plan.BlobReader implementation that is
// deliberately NOT *plan.MemoryStore. It exists to prove that PushChunks
// and Fanout depend only on the plan.BlobReader interface (DIP): any type
// satisfying it — not just the concrete in-memory store recording writes
// through — works as the mem argument, including a hypothetical disk-backed
// or streaming store for large sync trees.
type fakeBlobReader struct {
	files map[string][]byte
}

func (f *fakeBlobReader) HasBlobs() bool { return len(f.files) > 0 }

func (f *fakeBlobReader) Refs() []string {
	refs := make([]string, 0, len(f.files))
	for ref := range f.files {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func (f *fakeBlobReader) FileBlob(ref string) ([]byte, bool) {
	data, ok := f.files[ref]
	return data, ok
}

func (f *fakeBlobReader) TreeBlob(ref string) ([]plan.BlobEntry, bool) {
	return nil, false
}

var _ plan.BlobReader = (*fakeBlobReader)(nil)

// TestPushChunksWithFakeBlobReader pushes a single (non-elevated, no
// sticky-dir) chunk through PushChunks using fakeBlobReader instead of
// *plan.MemoryStore, then decodes the captured SSH payload with
// plan.DecodePush to confirm the fake's blob content really made the round
// trip through EncodePush. Had PushChunks still required the concrete
// *plan.MemoryStore, fakeBlobReader would not satisfy its parameter type
// and this test would fail to compile.
func TestPushChunksWithFakeBlobReader(t *testing.T) {
	restoreProbe := AssumeRemotePlanCurrent()
	t.Cleanup(restoreProbe)

	old := SSHRunner
	t.Cleanup(func() { SSHRunner = old })

	var captured []byte
	calls := 0
	SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		calls++
		data, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		captured = data
		return nil
	}

	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "demo"},
		{Op: plan.KindFile, Path: "/tmp/out", Mode: "0600", ContentB64: "aGVsbG8K"},
	}
	mem := &fakeBlobReader{files: map[string][]byte{
		"blobs/demo.txt": []byte("fake-blob-content"),
	}}

	err := PushChunks(context.Background(),
		PushTarget{Host: "h.example", Privilege: privilege.None}, "demo", ops, mem)
	if err != nil {
		t.Fatalf("PushChunks: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (single chunk, no sticky dir)", calls)
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := plan.DecodePush(bytes.NewReader(captured), dir)
	if err != nil {
		t.Fatalf("DecodePush: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash("blobs/demo.txt")))
	if err != nil {
		t.Fatalf("read decoded blob: %v", err)
	}
	if string(data) != "fake-blob-content" {
		t.Fatalf("blob content = %q, want %q", data, "fake-blob-content")
	}
	if got.PlanDir != dir {
		t.Fatalf("PlanDir = %q, want %q", got.PlanDir, dir)
	}
}

// TestFanoutWithFakeBlobReader covers the Fanout (fleet) entry point with
// the same fake, proving the decoupling holds through the fan-out path too:
// Fanout just forwards mem (now plan.BlobReader, not *plan.MemoryStore) to
// PushChunks per target.
func TestFanoutWithFakeBlobReader(t *testing.T) {
	restoreProbe := AssumeRemotePlanCurrent()
	t.Cleanup(restoreProbe)

	old := SSHRunner
	t.Cleanup(func() { SSHRunner = old })

	// Fanout runs one PushChunks per target concurrently (errgroup), so the
	// fake SSHRunner is called from multiple goroutines: guard the counter.
	var mu sync.Mutex
	calls := 0
	SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		mu.Lock()
		calls++
		mu.Unlock()
		_, err := io.ReadAll(stdin)
		return err
	}

	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "demo"},
		{Op: plan.KindFile, Path: "/tmp/out", Mode: "0600", ContentB64: "aGVsbG8K"},
	}
	mem := &fakeBlobReader{files: map[string][]byte{
		"blobs/demo.txt": []byte("fake-blob-content"),
	}}
	targets := []PushTarget{
		{Host: "h1.example", Privilege: privilege.None},
		{Host: "h2.example", Privilege: privilege.None},
	}
	labels := []string{"h1", "h2"}

	err := Fanout(context.Background(), "demo-cluster", "demo", ops, mem, targets, labels, 2, 0)
	if err != nil {
		t.Fatalf("Fanout: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one chunk per target)", calls)
	}
}
