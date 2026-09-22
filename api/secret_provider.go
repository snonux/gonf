package api

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/secret"
)

var secretConfig secretProviders

// secretProviders is the process-wide secret provider configuration. The
// DSL itself is single-goroutine, but ResolveSecret is a public function a
// consumer may call from its own goroutines, so every field is guarded by mu.
// The lock covers only reading/updating the configuration, never a
// resolution itself.
type secretProviders struct {
	mu         sync.Mutex
	provider   secret.Provider // nil: the default secret.FileProvider{}
	configured bool            // SetSecretProvider has been called
	used       bool            // a secret has been resolved
}

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

// ResolveSecret resolves ref through the configured provider and returns its
// exact, non-empty bytes. Failures are typed (see package secret). Decide
// "may this be skipped?" only with secret.IsNotFound(err) on the returned
// error, as OptionalSecret does: errors.Is(err, secret.ErrNotFound) also
// matches a not-found buried in the cause of another failure. ctx bounds the
// resolution; a done context yields an error wrapping ctx.Err(). An empty
// value is refused as secret.ErrInvalid, the rule MustSecret has always
// applied. Unlike MustSecret it returns the error instead of stashing it, so
// it also works outside plan recording. Errors never contain secret bytes;
// the returned slice is the caller's.
func ResolveSecret(ctx context.Context, ref secret.Ref) ([]byte, error) {
	data, err := secret.Resolve(ctx, useSecretProvider(), ref)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, &secret.Error{Kind: secret.ErrInvalid, Ref: ref,
			Msg: fmt.Sprintf("secret %q is empty", string(ref))}
	}
	return data, nil
}

// setSecretProvider is SetSecretProvider's checked core; it returns the
// misuse instead of exiting, so tests can pin every refusal.
func setSecretProvider(p secret.Provider) error {
	secretConfig.mu.Lock()
	defer secretConfig.mu.Unlock()
	switch {
	case secret.IsNilProvider(p):
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

// resetSecretProviderForTest restores the default file provider (see
// ResetForTest).
func resetSecretProviderForTest() {
	secretConfig.mu.Lock()
	defer secretConfig.mu.Unlock()
	secretConfig.provider, secretConfig.configured, secretConfig.used = nil, false, false
}

// useSecretProvider marks the configuration as used (so it can no longer be
// changed) and returns the configured provider or the default file provider.
func useSecretProvider() secret.Provider {
	secretConfig.mu.Lock()
	defer secretConfig.mu.Unlock()
	secretConfig.used = true
	if secretConfig.provider == nil {
		return secret.FileProvider{}
	}
	return secretConfig.provider
}
