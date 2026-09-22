package api

import (
	"context"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/secret"
)

// MustSecret resolves the non-empty secret at path through the configured
// secret provider (SetSecretProvider; by default secret.FileProvider, which
// reads the controller-local file secrets/path). It preserves every byte
// exactly, including leading/trailing whitespace and newlines. Any failure —
// missing, unreadable, empty, unsafe path, unavailable provider — fails plan
// recording before a local apply or remote push can begin. Secret contents
// are never included in the error.
//
// A leading slash is accepted for compatibility with the Rex convention:
// MustSecret("/var/nsd/key") reads secrets/var/nsd/key, not /var/nsd/key on
// the controller. Paths may not escape the secrets directory.
//
// The value is an ordinary string: once placed into an op (file content,
// template data, argv, ...) it is recorded into the plan in the clear, but
// the op carrying it is marked sensitive (ResolveSecret remembers the
// value), so it is kept off stdout, out of previews and out of validator
// and command errors; a strong secret in an op's identity refuses the
// record (see docs/secrets.md for the rules and limits).
func MustSecret(path string) string {
	data, err := ResolveSecret(context.Background(), secret.Ref(path))
	if err != nil {
		stashSecretError(err)
		return ""
	}
	return string(data)
}

// OptionalSecret is MustSecret for a secret that may legitimately not exist.
// It returns ("", false) only when the provider reports secret.ErrNotFound,
// so callers can omit a host-specific plan fragment. Every other failure —
// including an empty value and a missing or unusable secrets directory
// itself — fails plan recording like MustSecret does: an optional lookup
// never hides a broken store.
//
// A leading slash is accepted for compatibility with the Rex convention; the
// path remains rooted below the recipe's secrets directory.
func OptionalSecret(path string) (string, bool) {
	data, err := ResolveSecret(context.Background(), secret.Ref(path))
	if secret.IsNotFound(err) {
		return "", false
	}
	if err != nil {
		stashSecretError(err)
		return "", false
	}
	return string(data), true
}

func stashSecretError(err error) {
	// Secrets are controller inputs, so their failures are record-time runtime
	// errors—not registration-time DSL misuse. Stashing lets RecordPlan/Run
	// return normally through their deferred cleanup and keeps PushTo from
	// opening an SSH connection.
	if plan.Recording() {
		stashBodyError(err)
		return
	}
	panic(err)
}
