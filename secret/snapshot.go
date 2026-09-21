package secret

import (
	"bytes"
	"context"
	"path/filepath"
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
// Returned slices are copies, on a miss and on a hit, so a caller that
// scrubs or edits its bytes cannot change what later callers get.
//
// References are cached under their canonical form — the path-cleaned
// reference without leading slashes, the same normalisation FileProvider
// applies — so "/garage/rpc_secret" and "garage/rpc_secret" share one entry.
// Providers used behind a Snapshot must therefore treat such spellings as
// the same secret. A reference with no canonical form (empty, or escaping
// with "..") is passed to the provider uncached, which refuses it.
//
// Each reference is resolved by one caller at a time without holding a
// global lock: a slow reference does not block others, and a caller waiting
// for another caller's resolution of the same reference gives up as soon as
// its own ctx is done.
//
// The cached bytes live as long as the Snapshot. Installed with
// api.SetSecretProvider, that is the rest of the process: the provider
// cannot be replaced, so the snapshot spans the whole invocation.
//
// The built-in file provider is not wrapped by default, which keeps
// MustSecret/OptionalSecret reading the file on every call as they always
// have.
type Snapshot struct {
	provider Provider
	mu       sync.Mutex // guards entries (not the resolutions themselves)
	entries  map[Ref]*snapshotEntry
}

// snapshotEntry is one reference's resolution. The resolving caller fills
// data, err and kept and then closes done; others read them only after done
// is closed.
type snapshotEntry struct {
	done chan struct{}
	data []byte
	err  error
	kept bool // a fact (success or not-found) served to later callers
}

// NewSnapshot returns a Snapshot over p. A nil p (including a typed nil
// pointer) is a composition-root programmer error and panics immediately
// rather than on the first resolution.
func NewSnapshot(p Provider) *Snapshot {
	if IsNilProvider(p) {
		panic("secret: NewSnapshot with nil provider")
	}
	return &Snapshot{provider: p, entries: map[Ref]*snapshotEntry{}}
}

// Resolve implements Provider.
func (s *Snapshot) Resolve(ctx context.Context, ref Ref) ([]byte, error) {
	key, ok := canonicalRef(ref)
	if !ok {
		return Resolve(ctx, s.provider, ref)
	}
	for {
		e, leader := s.entry(key)
		if leader {
			return s.lead(ctx, key, ref, e)
		}
		select {
		case <-ctx.Done():
			return nil, canceled(ref, ctx.Err())
		case <-e.done:
		}
		if e.kept {
			return bytes.Clone(e.data), withRef(e.err, ref)
		}
		// The resolving caller failed transiently and dropped the entry:
		// loop to resolve (or wait for a new resolution) with our own ctx.
	}
}

// entry returns key's entry, creating it when absent; leader reports that
// the caller created it and must resolve it.
func (s *Snapshot) entry(key Ref) (e *snapshotEntry, leader bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok {
		return e, false
	}
	e = &snapshotEntry{done: make(chan struct{})}
	s.entries[key] = e
	return e, true
}

// lead resolves ref for entry e (keyed key) outside the lock. A transient
// failure — or a panicking provider — drops the entry again so the next
// caller retries; done is closed in every case so waiters never hang.
func (s *Snapshot) lead(ctx context.Context, key, ref Ref, e *snapshotEntry) ([]byte, error) {
	defer func() {
		if !e.kept {
			s.mu.Lock()
			delete(s.entries, key)
			s.mu.Unlock()
		}
		close(e.done)
	}()
	data, err := Resolve(ctx, s.provider, ref)
	if err == nil || IsNotFound(err) {
		e.data, e.err, e.kept = bytes.Clone(data), err, true
	}
	return data, err
}

// canonicalRef returns the cache key of ref: cleaned, without leading
// slashes, with forward slashes. ok is false when ref has no canonical form.
func canonicalRef(ref Ref) (Ref, bool) {
	clean, err := cleanRef(ref)
	if err != nil {
		return "", false
	}
	return Ref(filepath.ToSlash(clean)), true
}

// withRef returns err naming ref: a cached *Error recorded for another
// spelling of the same reference is copied with Ref replaced, so Resolve's
// classification (which requires the error to name the requested ref)
// still passes it through. Its Msg keeps the first spelling.
func withRef(err error, ref Ref) error {
	if e, ok := err.(*Error); ok && e.Ref != ref {
		c := *e
		c.Ref = ref
		return &c
	}
	return err
}
