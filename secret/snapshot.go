package secret

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
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
// reference without leading slashes or backslashes, the same normalisation
// FileProvider applies — so "/garage/rpc_secret", `\garage/rpc_secret` and
// "garage/rpc_secret" share one entry. A backslash that is not leading is
// part of the name, so `garage\rpc_secret` is a different reference.
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
// cannot be replaced, so the snapshot spans the whole invocation. They are
// never printed: formatting a Snapshot with any fmt verb yields only its
// provider type and entry count (see Format).
//
// Create a Snapshot with NewSnapshot. The zero Snapshot{} wraps no provider:
// IsNilProvider reports it (so api.SetSecretProvider refuses it), and its
// Resolve fails every call with ErrUnavailable instead of panicking.
//
// The built-in file provider is not wrapped by default, which keeps
// MustSecret/OptionalSecret reading the file on every call as they always
// have.
type Snapshot struct {
	provider Provider
	mu       sync.Mutex // guards entries (not the resolutions themselves)
	entries  map[Ref]*snapshotEntry

	// onWait, when set (tests only), is called by a caller that found
	// another caller's in-flight entry and is about to wait for it. It lets
	// tests park waiters deterministically before releasing the leader.
	onWait func(key Ref)
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
	if IsNilProvider(s.provider) {
		return nil, &Error{Kind: ErrUnavailable, Ref: ref,
			Msg: fmt.Sprintf("secret %q: snapshot has no provider (create it with secret.NewSnapshot)", string(ref))}
	}
	key, ok := canonicalRef(ref)
	if !ok {
		return Resolve(ctx, s.provider, ref)
	}
	for {
		e, leader := s.entry(key)
		if leader {
			return s.lead(ctx, key, ref, e)
		}
		if s.onWait != nil {
			s.onWait(key)
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

// Format implements fmt.Formatter so a Snapshot never prints its cached
// bytes. Without it fmt walks the struct, and for a verb that does not suit
// a pointer (%s, %q) it reprints each nested *snapshotEntry at depth 0 —
// secret bytes included. Every verb therefore yields the same description:
// the wrapped provider's type and the number of entries (cached or still
// being resolved); neither holds secret bytes. %T and %p are answered by
// fmt itself before Format is consulted.
func (s *Snapshot) Format(f fmt.State, _ rune) {
	if s == nil {
		_, _ = io.WriteString(f, "secret.Snapshot(nil)")
		return
	}
	s.mu.Lock()
	n := len(s.entries)
	s.mu.Unlock()
	_, _ = fmt.Fprintf(f, "secret.Snapshot{provider: %T, entries: %d}", s.provider, n)
}

// Format implements fmt.Formatter so an entry reached on its own (a map of
// entries, a debugger-style dump) never prints its data either; see
// Snapshot.Format.
func (e *snapshotEntry) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "secret.snapshotEntry(redacted)")
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
	if err != nil {
		return nil, err
	}
	// A copy on the miss too, so this caller cannot reach the provider's
	// own buffer (which the provider may keep and hand out again).
	return bytes.Clone(data), nil
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
// still passes it through. Where its Msg quotes the first spelling (as the
// file provider's `secret "/a" is missing` does), that quote is replaced by
// the requested one, so the message is the one a direct lookup of ref would
// give and never names a reference other than Ref.
func withRef(err error, ref Ref) error {
	if e, ok := err.(*Error); ok && e.Ref != ref {
		c := *e
		c.Ref = ref
		c.Msg = strings.ReplaceAll(e.Msg, strconv.Quote(string(e.Ref)), strconv.Quote(string(ref)))
		return &c
	}
	return err
}
