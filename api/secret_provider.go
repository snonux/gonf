package api

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/secret"
)

// secretProviders is the process-wide secret provider configuration. The
// DSL is single-goroutine (see ResetForTest), so it is not locked.
type secretProviders struct {
	provider   secret.Provider // nil: the default secret.FileProvider{}
	configured bool            // SetSecretProvider has been called
	used       bool            // a secret has been resolved
}

var secretConfig secretProviders

// SetSecretProvider configures the provider that MustSecret, OptionalSecret
// and ResolveSecret resolve through. Without it they use
// secret.FileProvider{} (the controller-local secrets/ directory), exactly as
// before providers existed.
//
// Call it once, at the consumer's composition root (typically main, before
// cli.CLI or any Run/RecordPlan), so every task, host and chunk of one
// invocation resolves through the same provider. Calling it twice, with nil,
// from a task body, or after a secret has already been resolved is
// registration-time misuse and fails fast via logger.Fatal: silently
// switching providers mid-run could make one plan mix secret sources. Wrap
// the provider in secret.NewSnapshot to also pin each secret's value for the
// whole invocation.
func SetSecretProvider(p secret.Provider) {
	if err := setSecretProvider(p); err != nil {
		logger.Fatal("SetSecretProvider: %v", err)
	}
}

// setSecretProvider is SetSecretProvider's checked core; it returns the
// misuse instead of exiting, so tests can pin every refusal.
func setSecretProvider(p secret.Provider) error {
	switch {
	case providerIsNil(p):
		return errors.New("provider must not be nil")
	case secretConfig.configured:
		return errors.New("provider already configured; call it once at the composition root")
	case plan.Recording():
		return errors.New("must be called at the composition root, not inside a task body")
	case secretConfig.used:
		return errors.New("must be called before the first secret is resolved")
	}
	secretConfig.provider = p
	secretConfig.configured = true
	return nil
}

// providerIsNil reports a nil provider, including one held in a typed nil
// pointer (e.g. (*adapter)(nil)), which the p == nil interface check misses;
// such a provider would panic only at the first resolution.
func providerIsNil(p secret.Provider) bool {
	if p == nil {
		return true
	}
	switch v := reflect.ValueOf(p); v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return v.IsNil()
	}
	return false
}

// resetSecretProviderForTest restores the default file provider (see
// ResetForTest).
func resetSecretProviderForTest() { secretConfig = secretProviders{} }

// currentSecretProvider returns the configured provider or the default file
// provider.
func currentSecretProvider() secret.Provider {
	if secretConfig.provider == nil {
		return secret.FileProvider{}
	}
	return secretConfig.provider
}

// ResolveSecret resolves ref through the configured provider and returns its
// exact, non-empty bytes. Failures are typed (see package secret): match
// them with errors.Is(err, secret.ErrNotFound) and friends, or
// secret.IsNotFound for the one kind an optional lookup may ignore. ctx
// bounds the resolution; a done context yields an error wrapping ctx.Err().
// An empty value is refused as secret.ErrInvalid, the rule MustSecret has
// always applied. Unlike MustSecret it returns the error instead of stashing
// it, so it also works outside plan recording. Errors never contain secret
// bytes; the returned slice is the caller's.
func ResolveSecret(ctx context.Context, ref secret.Ref) ([]byte, error) {
	secretConfig.used = true
	data, err := secret.Resolve(ctx, currentSecretProvider(), ref)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, &secret.Error{Kind: secret.ErrInvalid, Ref: ref,
			Msg: fmt.Sprintf("secret %q is empty", string(ref))}
	}
	return data, nil
}
