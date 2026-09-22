package api

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
)

// recordWithSecret is recordSecretTask for a caller-chosen secret value;
// it returns the ops and the record error instead of failing on it.
func recordWithSecret(t *testing.T, value string, body func()) ([]plan.Op, error) {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
	useSecretWorkDir(t)
	writeSecret(t, "svc/key", value)
	Task("t", "", body)
	return RecordPlanTo("scan", plan.NewMemoryStore(), "t")
}

// An unnamed Command's ID is its whole argv, which destinations log and
// report everywhere: a secret there fails the record, and the refusal
// itself carries the secret redacted.
func TestSecretInOpIdentityIsRefused(t *testing.T) {
	_, err := recordWithSecret(t, fakePlanSecret+"\n", func() {
		key := strings.TrimSpace(MustSecret("svc/key"))
		Command("/usr/bin/curl", List("-H", "Authorization: "+key))
	})
	if err == nil || !strings.Contains(err.Error(), "holds a resolved secret value") {
		t.Fatalf("err = %v, want the identity refusal", err)
	}
	requireNoSecret(t, "identity refusal", []byte(err.Error()))
	if !strings.Contains(err.Error(), `task "t": command op: its id`) {
		t.Fatalf("refusal should name the task, kind and field: %v", err)
	}

	_, err = recordWithSecret(t, fakePlanSecret+"\n", func() {
		File("/etc/"+strings.TrimSpace(MustSecret("svc/key")), options.WithContent("x"))
	})
	if err == nil || strings.Contains(err.Error(), fakePlanSecret) {
		t.Fatalf("secret path: err = %v, want a redacted identity refusal", err)
	}
}

// A secret that equals common metadata ("root", "true", "0644", "file")
// does not mark ops whose owner, flags, mode or kind merely look like it:
// only payload values are scanned, not field names or metadata.
func TestSecretMatchingMetadataDoesNotMarkOps(t *testing.T) {
	for _, value := range []string{"root", "true", "0644", "file"} {
		ops, err := recordWithSecret(t, value, func() {
			_ = MustSecret("svc/key")
			File("/etc/owned", options.WithContent("plain\n"), options.WithOwner("root"), options.WithMode(0o644))
		})
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if opByPath(t, ops, "/etc/owned").Sensitive {
			t.Errorf("secret %q marked an op that only matches its metadata", value)
		}
	}
}

// A []byte in template data is recorded as base64 (json.Marshal), and the
// destination renders exactly that text; the scan decodes it, so the op is
// still sensitive.
func TestByteSliceTemplateDataIsDetected(t *testing.T) {
	ops, err := recordWithSecret(t, fakePlanSecret, func() {
		key := []byte(MustSecret("svc/key"))
		File("/etc/b64", options.WithContent("{{.K}}"), options.WithTemplateData(map[string]any{"K": key}))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !opByPath(t, ops, "/etc/b64").Sensitive {
		t.Fatal("a []byte secret in template data was not detected")
	}
}

// A short secret (below secret.MinContainedLen) that is a whole argv value
// marks the command, whose argv the preview then withholds wholesale (every
// payload string of a sensitive op, v82).
func TestShortSecretArgvIsRedactedInPreview(t *testing.T) {
	ops, err := recordWithSecret(t, "k9z", func() {
		Command("/usr/bin/tool", List("--pin", MustSecret("svc/key")), options.WithName("pin-tool"))
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := EncodeRedactedPreview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(preview), `"k9z"`) || !strings.Contains(string(preview), `"args":["[redacted]","[redacted]"]`) {
		t.Fatalf("short argv secret not redacted:\n%s", preview)
	}
}

// SecretFile defaults to 0600 without WithMode.
func TestSecretFileDefaultsToOwnerOnlyMode(t *testing.T) {
	ops, err := recordWithSecret(t, fakePlanSecret, func() {
		SecretFile("/etc/default.key", "svc/key")
	})
	if err != nil {
		t.Fatal(err)
	}
	if op := opByPath(t, ops, "/etc/default.key"); op.Mode != "0600" || !op.Sensitive {
		t.Fatalf("mode %q sensitive %v, want 0600 and sensitive", op.Mode, op.Sensitive)
	}
}

// The header declares v22 only when an op is sensitive; a plan without
// secret material stays v21 and applies on a v0.15.0 destination.
func TestHeaderVersionFollowsSensitivity(t *testing.T) {
	ops, err := recordWithSecret(t, fakePlanSecret, func() {
		_ = MustSecret("svc/key")
		File("/etc/plain", options.WithContent("plain\n"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if ops[0].Version != plan.VersionSensitive-1 {
		t.Fatalf("plain plan header v%d, want v%d", ops[0].Version, plan.VersionSensitive-1)
	}
	ops, err = recordWithSecret(t, fakePlanSecret, func() {
		SecretFile("/etc/k", "svc/key")
	})
	if err != nil {
		t.Fatal(err)
	}
	if ops[0].Version != plan.VersionSensitive {
		t.Fatalf("sensitive plan header v%d, want v%d", ops[0].Version, plan.VersionSensitive)
	}
}
