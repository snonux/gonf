package secret

import (
	"bytes"
	"context"
	"sync"
)

// Snapshot is a Provider that resolves each reference at most once and then
// answers from memory, so every consumer within one snapshot — every task,
// host and privilege chunk recorded by one gonf invocation — sees the same
// bytes even if the backing store rotates the secret meanwhile. Wrap a
// provider in it at the composition root:
//
//	api.SetSecretProvider(secret.NewSnapshot(myProvider))
//
// Only a successful resolution and ErrNotFound are remembered: both are facts
// about the store's content. Every other failure (a locked store, an I/O
// error, a cancelled context) is transient and retried on the next call.
// Returned slices are copies, so a caller that scrubs or edits its bytes
// cannot change what later callers get. The cached bytes live as long as the
// Snapshot; drop it to end the snapshot.
//
// The built-in file provider is not wrapped by default, which keeps
// MustSecret/OptionalSecret reading the file on every call as they always
// have.
type Snapshot struct {
	provider Provider
	mu       sync.Mutex
	entries  map[Ref]snapshotEntry
}

type snapshotEntry struct {
	data []byte
	err  error
}

// NewSnapshot returns a Snapshot over p. A nil p is a composition-root
// programmer error and panics immediately rather than on the first
// resolution.
func NewSnapshot(p Provider) *Snapshot {
	if p == nil {
		panic("secret: NewSnapshot with nil provider")
	}
	return &Snapshot{provider: p, entries: map[Ref]snapshotEntry{}}
}

// Resolve implements Provider. Resolutions are serialised, so concurrent
// callers of one reference trigger one provider call.
func (s *Snapshot) Resolve(ctx context.Context, ref Ref) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[ref]; ok {
		return bytes.Clone(e.data), e.err
	}
	data, err := Resolve(ctx, s.provider, ref)
	if err == nil || IsNotFound(err) {
		s.entries[ref] = snapshotEntry{data: bytes.Clone(data), err: err}
	}
	return data, err
}
