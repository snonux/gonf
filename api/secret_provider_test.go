package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/secret"
)

// These tests pin the api side of the secret-provider contract: one provider
// configured at the composition root, MustSecret/OptionalSecret/ResolveSecret
// routed through it, and OptionalSecret suppressing only secret.ErrNotFound.
// Every provider here is a fake with synthetic bytes.

const syntheticSecret = "synthetic-provider-value\n"

// fakeSecrets is an in-memory provider: values by reference, or fail for
// every lookup when set.
type fakeSecrets struct {
	values map[secret.Ref]string
	fail   error
	calls  int
}

func (f *fakeSecrets) Resolve(_ context.Context, ref secret.Ref) ([]byte, error) {
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	v, ok := f.values[ref]
	if !ok {
		return nil, &secret.Error{Kind: secret.ErrNotFound, Ref: ref}
	}
	return []byte(v), nil
}

// useFakeSecrets resets api state and configures a fake provider.
func useFakeSecrets(t *testing.T, f *fakeSecrets) {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
	if err := setSecretProvider(f); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredProviderFeedsMustSecret(t *testing.T) {
	useSecretWorkDir(t) // no secrets/ at all: the file provider is not consulted
	useFakeSecrets(t, &fakeSecrets{values: map[secret.Ref]string{"garage/rpc_secret": syntheticSecret}})
	Task("t", "", func() {
		File("/tmp/secret", options.WithContent(MustSecret("garage/rpc_secret")))
	})
	ops, err := RecordPlan("secrets", "", "t")
	if err != nil {
		t.Fatal(err)
	}
	got, err := base64.StdEncoding.DecodeString(ops[len(ops)-1].ContentB64)
	if err != nil || string(got) != syntheticSecret {
		t.Fatalf("recorded content = (%q, %v), want the provider bytes", got, err)
	}
}

func TestOptionalSecretSuppressesOnlyNotFound(t *testing.T) {
	store := &fakeSecrets{values: map[secret.Ref]string{"present": syntheticSecret}}
	useFakeSecrets(t, store)
	if v, ok := OptionalSecret("present"); !ok || v != syntheticSecret {
		t.Fatalf("OptionalSecret(present) = (%q, %v)", v, ok)
	}
	if v, ok := OptionalSecret("absent"); ok || v != "" {
		t.Fatalf("OptionalSecret(absent) = (%q, %v), want omitted", v, ok)
	}
}

// Negative: every failure other than not-found fails recording through
// OptionalSecret, with no plan ops and without the value in the error.
func TestOptionalSecretRefusesEveryOtherFailure(t *testing.T) {
	for name, fail := range map[string]error{
		"unavailable":  &secret.Error{Kind: secret.ErrUnavailable, Ref: "k", Msg: "store locked"},
		"unreadable":   &secret.Error{Kind: secret.ErrUnreadable, Ref: "k", Msg: "io failed"},
		"invalid":      &secret.Error{Kind: secret.ErrInvalid, Ref: "k", Msg: "bad reference"},
		"unclassified": errors.New("adapter crashed"),
		"cancelled":    context.Canceled,
	} {
		t.Run(name, func(t *testing.T) {
			useFakeSecrets(t, &fakeSecrets{fail: fail})
			Task("t", "", func() {
				if v, ok := OptionalSecret("k"); ok {
					File("/tmp/k", options.WithContent(v))
				}
			})
			ops, err := RecordPlan("secrets", "", "t")
			if err == nil || !strings.Contains(err.Error(), fail.Error()) {
				t.Fatalf("RecordPlan error = %v, want it to carry %q", err, fail)
			}
			if len(ops) != 0 {
				t.Fatalf("failed secret produced ops: %#v", ops)
			}
		})
	}
}

func TestResolveSecretRefusesEmptyValues(t *testing.T) {
	useFakeSecrets(t, &fakeSecrets{values: map[secret.Ref]string{"empty": ""}})
	data, err := ResolveSecret(context.Background(), "empty")
	if data != nil || secret.KindOf(err) != secret.ErrInvalid || err.Error() != `secret "empty" is empty` {
		t.Fatalf("ResolveSecret(empty) = (%q, %v), want ErrInvalid", data, err)
	}
}

func TestResolveSecretHonoursContext(t *testing.T) {
	store := &fakeSecrets{values: map[secret.Ref]string{"k": syntheticSecret}}
	useFakeSecrets(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, err := ResolveSecret(ctx, "k")
	if !errors.Is(err, context.Canceled) || data != nil || store.calls != 0 {
		t.Fatalf("ResolveSecret(cancelled) = (%q, %v, calls %d)", data, err, store.calls)
	}
	if data, err := ResolveSecret(context.Background(), "k"); err != nil || string(data) != syntheticSecret {
		t.Fatalf("ResolveSecret = (%q, %v)", data, err)
	}
}

// The provider is configured once, at the composition root.
func TestSetSecretProviderIsConfiguredOnce(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	if err := setSecretProvider(nil); err == nil {
		t.Fatal("nil provider accepted")
	}
	if err := setSecretProvider((*fakeSecrets)(nil)); err == nil {
		t.Fatal("typed nil provider accepted")
	}
	if err := setSecretProvider(&fakeSecrets{}); err != nil {
		t.Fatal(err)
	}
	if err := setSecretProvider(&fakeSecrets{}); err == nil || !strings.Contains(err.Error(), "already configured") {
		t.Fatalf("second call: %v", err)
	}

	ResetForTest()
	useSecretWorkDir(t)
	// The default file provider has been used (and failed: no secrets/).
	if _, err := ResolveSecret(context.Background(), "anything"); err == nil {
		t.Fatal("ResolveSecret without secrets/ succeeded")
	}
	if err := setSecretProvider(&fakeSecrets{}); err == nil || !strings.Contains(err.Error(), "before the first secret") {
		t.Fatalf("after use: %v", err)
	}
}

func TestSetSecretProviderRefusedInsideTaskBody(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	var inBody error
	Task("t", "", func() { inBody = setSecretProvider(&fakeSecrets{}) })
	if _, err := RecordPlan("secrets", "", "t"); err != nil {
		t.Fatal(err)
	}
	if inBody == nil || !strings.Contains(inBody.Error(), "composition root") {
		t.Fatalf("setSecretProvider in a task body = %v, want refusal", inBody)
	}
}

// Resolving a secret adds nothing to the plan by itself: only what a recipe
// explicitly places into a resource (e.g. WithContent) is recorded, exactly
// as before the provider contract.
func TestResolvedSecretIsNotRecordedUnlessUsed(t *testing.T) {
	useFakeSecrets(t, &fakeSecrets{values: map[secret.Ref]string{"k": syntheticSecret}})
	Task("t", "", func() {
		_ = MustSecret("k")
		_, _ = OptionalSecret("k")
		File("/tmp/public", options.WithContent("public\n"))
	})
	ops, err := RecordPlan("secrets", "", "t")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(syntheticSecret))
	if strings.Contains(string(raw), "synthetic-provider-value") || strings.Contains(string(raw), encoded) {
		t.Fatalf("plan contains an unused secret: %s", raw)
	}
}
