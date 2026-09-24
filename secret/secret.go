// Package secret is gonf's controller-side secret-provider contract.
//
// A Provider turns a logical reference (Ref) into the exact secret bytes,
// honouring a context for cancellation and deadlines, and reports failures as
// typed errors (*Error with one of the Err* kinds). The distinction between
// the kinds is the point of the contract: a caller that treats a secret as
// optional may suppress ErrNotFound — "this secret does not exist" — and
// nothing else. A store that is locked, unreachable, corrupt, misconfigured or
// failing I/O is never mistaken for an absent secret.
//
// FileProvider is the built-in provider: it reads regular files below the
// controller-local secrets/ directory and is what api.MustSecret and
// api.OptionalSecret have always used. Other providers (such as the
// argv-invoked foostore adapter in package secret/foostore) live outside
// gonf's recipe and resource packages and are configured once, at the
// consumer's composition root, with api.SetSecretProvider.
//
// The contract only resolves bytes. Plans become secret-aware through
// Values: api.ResolveSecret remembers every value it returns, and plan
// recording marks the ops that still carry one as sensitive (see
// docs/design/secrets.md). The value itself stays in the plan in clear text.
package secret

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
)

// The error kinds. KindOf/IsNotFound read the kind of a top-level *Error and
// are what decisions (such as an optional lookup) must use; errors.Is also
// finds kinds in causes (an *Error unwraps to its kind and its cause), which
// is fine for matching in logs and tests but not for deciding.
var (
	// ErrNotFound: the reference is well-formed and the store is usable, but
	// holds no such secret. The only kind an optional lookup may suppress.
	ErrNotFound = errors.New("secret not found")
	// ErrInvalid: the reference or what it names is unacceptable — an empty
	// or escaping path, a symlink or non-regular file where a secret is
	// expected, or an empty value where one is required.
	ErrInvalid = errors.New("invalid secret")
	// ErrUnreadable: the secret exists but could not be read (permission
	// denied, I/O error).
	ErrUnreadable = errors.New("secret unreadable")
	// ErrUnavailable: the provider or store itself is unusable — its root is
	// missing, it is locked or unauthenticated, corrupt, misconfigured, or
	// failed in a way the provider did not classify.
	ErrUnavailable = errors.New("secret provider unavailable")
)

// kinds lists the valid Error.Kind values.
var kinds = []error{ErrNotFound, ErrInvalid, ErrUnreadable, ErrUnavailable}

// Ref is a provider-neutral logical secret reference, e.g.
// "garage/rpc_secret". Its meaning belongs to the provider: FileProvider
// reads the path below its directory. A Ref names a secret; it never holds
// secret bytes, so it may appear in errors and logs.
type Ref string

// Provider resolves a reference to the exact secret bytes (no trimming, no
// re-encoding). Implementations must
//   - return promptly once ctx is done, with an error wrapping ctx.Err();
//   - report failures as an unwrapped *Error with one of the Err* kinds and
//     Ref set to the requested ref (anything else — an unclassified error, a
//     wrapped *Error, one about another reference — is treated as
//     ErrUnavailable by Resolve, never as not-found);
//   - be safe for concurrent use when api.ResolveSecret or a Snapshot may
//     call it from several goroutines;
//   - never put secret bytes into an error, log line or panic value.
//
// Empty values are returned as they are; the policy that a secret must not be
// empty belongs to the caller (the api helpers refuse empty secrets).
type Provider interface {
	Resolve(ctx context.Context, ref Ref) ([]byte, error)
}

// ProviderFunc adapts a function to Provider, mainly for tests and small
// adapters.
type ProviderFunc func(ctx context.Context, ref Ref) ([]byte, error)

// Error is a typed secret failure. It names the reference, never the value.
type Error struct {
	Kind error  // one of ErrNotFound, ErrInvalid, ErrUnreadable, ErrUnavailable
	Ref  Ref    // the reference that failed
	Msg  string // optional full message; replaces the generated one
	Err  error  // optional cause (e.g. unix.EACCES); must not carry secret bytes
}

// Resolve calls f.
func (f ProviderFunc) Resolve(ctx context.Context, ref Ref) ([]byte, error) { return f(ctx, ref) }

// IsNilProvider reports a nil provider, including one held in a typed nil
// pointer, map, channel, slice or function (e.g. (*adapter)(nil)), which the
// plain p == nil interface check misses; such a provider would panic only
// at the first resolution. A Snapshot that wraps no provider (the zero
// &Snapshot{} instead of NewSnapshot) counts as nil too: it cannot resolve
// anything. A Fallback whose primary or secondary is nil (including a typed
// nil) counts as nil too, for the same reason — it cannot resolve any
// reference either way — so a broken staged-cutover composition
// (secret.NewFallback with one operand accidentally nil) is refused exactly
// like a bare nil provider. Composition roots use it to refuse a nil
// provider: api.SetSecretProvider with a declaration error, NewSnapshot by
// returning a providerless Snapshot.
func IsNilProvider(p Provider) bool {
	if p == nil {
		return true
	}
	if s, ok := p.(*Snapshot); ok && s != nil {
		return IsNilProvider(s.provider)
	}
	if f, ok := p.(*Fallback); ok && f != nil {
		return IsNilProvider(f.primary) || IsNilProvider(f.secondary)
	}
	switch v := reflect.ValueOf(p); v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return v.IsNil()
	}
	return false
}

// Error returns Msg when set, otherwise `secret "<ref>": <kind>[: <cause>]`.
// Msg exists so the file provider keeps its historical messages exactly. A
// nil *Error (a provider's nil-receiver mistake) describes itself instead of
// panicking.
func (e *Error) Error() string {
	if e == nil {
		return "secret: nil *secret.Error"
	}
	if e.Msg != "" {
		return e.Msg
	}
	msg := fmt.Sprintf("secret %q: %v", string(e.Ref), e.Kind)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes both the kind and the cause to errors.Is/As; a nil *Error
// has neither.
func (e *Error) Unwrap() []error {
	if e == nil {
		return nil
	}
	var out []error
	for _, err := range []error{e.Kind, e.Err} {
		if err != nil {
			out = append(out, err)
		}
	}
	return out
}

// KindOf returns the Kind of err when err itself — not a cause further down
// its chain — is an *Error, and nil otherwise. Wrapping a typed error in
// another error therefore hides its kind on purpose: an unreadable store
// whose cause happens to wrap a not-found, or a caller that added context
// with fmt.Errorf, must never look like an absent secret. Resolve and
// api.ResolveSecret return the *Error unwrapped, so apply KindOf and
// IsNotFound directly to their result. A nil *Error held in a non-nil error
// has no kind either.
func KindOf(err error) error {
	if e, ok := err.(*Error); ok && e != nil {
		return e.Kind
	}
	return nil
}

// IsNotFound reports whether err is an absent secret, the only failure an
// optional lookup may suppress. It is the one test to base that decision on:
// errors.Is(err, ErrNotFound) also matches a not-found buried in the cause of
// another failure (see KindOf).
func IsNotFound(err error) bool { return KindOf(err) == ErrNotFound }

// Resolve resolves ref with p and enforces the contract around it:
//   - a context that is already done, or becomes done while p runs, yields
//     an error wrapping ctx.Err() and no bytes, even if p returned some
//     (or a typed error);
//   - only a top-level *Error with a known kind that names ref itself is
//     passed through; anything else — an unclassified error, a typed error
//     wrapped in another one or naming a different reference (e.g. the
//     store's unlock file), or a context error of the provider's own while
//     the caller's ctx is still live — becomes ErrUnavailable, so a provider
//     can never make a broken store read as "this secret is not found".
//
// Callers should use Resolve rather than calling p.Resolve directly.
func Resolve(ctx context.Context, p Provider, ref Ref) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, canceled(ref, err)
	}
	data, err := p.Resolve(ctx, ref)
	if cerr := ctx.Err(); cerr != nil {
		return nil, canceled(ref, cerr)
	}
	if err != nil {
		return nil, classify(ref, err)
	}
	return data, nil
}

// canceled wraps a context error of the caller; it is deliberately not an
// *Error, so KindOf reports no kind and an optional lookup does not suppress
// it.
func canceled(ref Ref, err error) error {
	return fmt.Errorf("resolve secret %q: %w", string(ref), err)
}

// classify returns err unchanged when it is a top-level *Error about ref with
// a known kind, and wraps everything else as ErrUnavailable (keeping it as
// the cause). A nil *Error in a non-nil error — a provider returning its
// nil *Error variable — is unclassified too; it is described in Msg rather
// than kept as the cause, so nothing downstream calls methods on it.
// Resolve has already handled the caller's own cancellation.
func classify(ref Ref, err error) error {
	e, ok := err.(*Error)
	if ok && e == nil {
		return &Error{Kind: ErrUnavailable, Ref: ref,
			Msg: fmt.Sprintf("secret %q: %v: provider returned a nil *secret.Error", string(ref), ErrUnavailable)}
	}
	if ok && e.Ref == ref && slices.Contains(kinds, e.Kind) {
		return err
	}
	return &Error{Kind: ErrUnavailable, Ref: ref, Err: err}
}
