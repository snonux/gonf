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
// api.OptionalSecret have always used. Other providers (for example a
// foostore adapter executed via argv) live outside gonf's recipe and resource
// packages and are configured once, at the consumer's composition root, with
// api.SetSecretProvider.
//
// The contract only resolves bytes. It does not make plans secret-aware: a
// value a recipe places into file content or template data is recorded in
// the plan exactly as before (see docs/secrets.md).
package secret

import (
	"context"
	"errors"
	"fmt"
)

// Ref is a provider-neutral logical secret reference, e.g.
// "garage/rpc_secret". Its meaning belongs to the provider: FileProvider
// reads the path below its directory. A Ref names a secret; it never holds
// secret bytes, so it may appear in errors and logs.
type Ref string

// Provider resolves a reference to the exact secret bytes (no trimming, no
// re-encoding). Implementations must
//   - return promptly once ctx is done, with an error wrapping ctx.Err();
//   - report failures as *Error with one of the Err* kinds (an unclassified
//     error is treated as ErrUnavailable by Resolve, never as not-found);
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

// Resolve calls f.
func (f ProviderFunc) Resolve(ctx context.Context, ref Ref) ([]byte, error) { return f(ctx, ref) }

// The error kinds. Match them with errors.Is (an *Error unwraps to its kind)
// or read Error.Kind; KindOf returns the kind of the outermost *Error.
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

// Error is a typed secret failure. It names the reference, never the value.
type Error struct {
	Kind error  // one of ErrNotFound, ErrInvalid, ErrUnreadable, ErrUnavailable
	Ref  Ref    // the reference that failed
	Msg  string // optional full message; replaces the generated one
	Err  error  // optional cause (e.g. unix.EACCES); must not carry secret bytes
}

// Error returns Msg when set, otherwise `secret "<ref>": <kind>[: <cause>]`.
// Msg exists so the file provider keeps its historical messages exactly.
func (e *Error) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	msg := fmt.Sprintf("secret %q: %v", string(e.Ref), e.Kind)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes both the kind and the cause to errors.Is/As.
func (e *Error) Unwrap() []error {
	var out []error
	for _, err := range []error{e.Kind, e.Err} {
		if err != nil {
			out = append(out, err)
		}
	}
	return out
}

// KindOf returns the Kind of the outermost *Error in err's chain, or nil when
// there is none. Unlike errors.Is(err, ErrNotFound) it cannot be fooled by a
// cause further down the chain (an unreadable store whose cause happens to
// wrap a not-found), so it is what optional lookups use.
func KindOf(err error) error {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return nil
}

// IsNotFound reports whether err is an absent secret, the only failure an
// optional lookup may suppress.
func IsNotFound(err error) bool { return KindOf(err) == ErrNotFound }

// Resolve resolves ref with p and enforces the contract around it:
//   - a context that is already done, or becomes done while p runs, yields
//     an error wrapping ctx.Err() and no bytes, even if p returned some
//     (or a typed error);
//   - an error that is not a correctly typed *Error becomes ErrUnavailable,
//     so a provider bug can never read as "not found".
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

// canceled wraps a context error; it is deliberately not an *Error, so
// KindOf reports no kind and an optional lookup does not suppress it.
func canceled(ref Ref, err error) error {
	return fmt.Errorf("resolve secret %q: %w", string(ref), err)
}

// classify returns err unchanged when it is a context error or an *Error with
// a known kind, and wraps everything else as ErrUnavailable.
func classify(ref Ref, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var e *Error
	if errors.As(err, &e) {
		for _, k := range kinds {
			if e.Kind == k {
				return err
			}
		}
	}
	return &Error{Kind: ErrUnavailable, Ref: ref, Err: err}
}
