package api

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/secret"
)

// fakePlanSecret is synthetic secret material for the sensitivity tests.
const fakePlanSecret = "fake-rpc-secret-0f1e2d3c4b5a"

// recordSecretTask resets the DSL, writes the fake secret below a fresh
// secrets/ directory, registers body as task "t" and records it.
func recordSecretTask(t *testing.T, body func()) []plan.Op {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
	useSecretWorkDir(t)
	writeSecret(t, "svc/key", fakePlanSecret+"\n")
	Task("t", "", body)
	ops, err := RecordPlanTo("sensitive", plan.NewMemoryStore(), "t")
	if err != nil {
		t.Fatalf("RecordPlanTo: %v", err)
	}
	return ops
}

// opByPath returns the recorded op managing path.
func opByPath(t *testing.T, ops []plan.Op, path string) plan.Op {
	t.Helper()
	for _, op := range ops {
		if op.Path == path {
			return op
		}
	}
	t.Fatalf("no op for %s in %#v", path, ops)
	return plan.Op{}
}

// requireNoSecret fails when out holds the fake secret in any encoding the
// plan wire uses.
func requireNoSecret(t *testing.T, what string, out []byte) {
	t.Helper()
	for _, form := range []string{fakePlanSecret, base64.StdEncoding.EncodeToString([]byte(fakePlanSecret + "\n")),
		base64.StdEncoding.EncodeToString([]byte(fakePlanSecret))} {
		if bytes.Contains(out, []byte(form)) {
			t.Fatalf("%s leaks the secret (%q):\n%s", what, form, out)
		}
	}
}

// TestSecretsMarkEveryCarryingOp pins that sensitivity survives the string
// a recipe turned the secret into: file content (verbatim, trimmed and
// embedded), template data, a command argument and a config set member are
// all marked, while ops without the secret are not.
func TestSecretsMarkEveryCarryingOp(t *testing.T) {
	ops := recordSecretTask(t, func() {
		key := MustSecret("svc/key")
		File("/etc/verbatim", options.WithContent(key))
		File("/etc/embedded", options.WithContent("key \""+strings.TrimSpace(key)+"\";\n"))
		File("/etc/templated", options.WithContent("rpc = {{.Secret}}\n"),
			options.WithTemplateData(map[string]string{"Secret": strings.TrimSuffix(key, "\n")}))
		File("/etc/plain", options.WithContent("no secret here\n"))
		Command("/bin/true", List("--token", strings.TrimSpace(key)), options.WithName("uses-token"))
		ConfigSet("svc", options.ConfigFile("conf", "/etc/svc/conf",
			options.WithContent("secret "+strings.TrimSpace(key)+"\n")),
			options.WithSetValidation("/bin/true", []string{options.MemberPath("conf")}))
	})
	for _, path := range []string{"/etc/verbatim", "/etc/embedded", "/etc/templated"} {
		if !opByPath(t, ops, path).Sensitive {
			t.Errorf("%s: not marked sensitive", path)
		}
	}
	if opByPath(t, ops, "/etc/plain").Sensitive {
		t.Error("/etc/plain: marked sensitive without secret material")
	}
	var command, set bool
	for _, op := range ops {
		command = command || (op.Op == plan.KindCommand && op.Sensitive)
		set = set || (op.Op == plan.KindConfigSet && op.Sensitive)
		if op.Op == plan.KindConfigSetMember && op.Sensitive {
			t.Error("a config set member handle carries no payload and must not be sensitive")
		}
	}
	if !command || !set {
		t.Fatalf("command sensitive=%v, config set sensitive=%v; want both", command, set)
	}
}

// TestSensitivityScanAcceptsEmptyConfigMember is the client-gate regression
// (conf frontends_smtpd): an empty config set member is legitimate and must
// not fail the scan once a secret has been resolved.
func TestSensitivityScanAcceptsEmptyConfigMember(t *testing.T) {
	ops := recordSecretTask(t, func() {
		_ = MustSecret("svc/key")
		ConfigSet("svc", options.ConfigFile("empty", "/etc/svc/empty", options.WithContent("")),
			options.WithSetValidation("/bin/true", []string{options.MemberPath("empty")}))
	})
	for _, op := range ops {
		if op.Op == plan.KindConfigSet && op.Sensitive {
			t.Fatal("an empty member set must not be sensitive")
		}
	}
}

// TestSecretFileRecordsSensitiveExactContent pins the typed-reference entry
// point: exact bytes, sensitive op, and a failed reference that fails the
// record naming the ref only.
func TestSecretFileRecordsSensitiveExactContent(t *testing.T) {
	ops := recordSecretTask(t, func() {
		SecretFile("/etc/svc.key", secret.Ref("svc/key"), options.WithMode(0o600))
	})
	op := opByPath(t, ops, "/etc/svc.key")
	opPayload, _ := op.Payload.(plan.FilePayload)
	content, err := plan.DecodeContentB64(opPayload.ContentB64)
	if err != nil {
		t.Fatal(err)
	}
	if !op.Sensitive || string(content) != fakePlanSecret+"\n" || op.Mode != "0600" {
		t.Fatalf("op = sensitive %v mode %s content %q", op.Sensitive, op.Mode, content)
	}

	Task("missing", "", func() { SecretFile("/etc/other.key", "svc/missing") })
	_, err = RecordPlanTo("missing", plan.NewMemoryStore(), "missing")
	if err == nil || !strings.Contains(err.Error(), "svc/missing") {
		t.Fatalf("missing ref: err = %v, want a record error naming svc/missing", err)
	}
	requireNoSecret(t, "missing-ref error", []byte(err.Error()))
}

// TestShortSecretIsMarkedOnlyAsWholeContent pins the MinContainedLen rule:
// a 3-byte secret marks the op whose content is exactly it, but not every op
// that happens to contain those bytes.
func TestShortSecretIsMarkedOnlyAsWholeContent(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	useSecretWorkDir(t)
	writeSecret(t, "pin", "k9z")
	Task("t", "", func() {
		SecretFile("/etc/pin", "pin")
		File("/etc/other", options.WithContent("mentions k9z in passing\n"))
	})
	ops, err := RecordPlanTo("short", plan.NewMemoryStore(), "t")
	if err != nil {
		t.Fatal(err)
	}
	if !opByPath(t, ops, "/etc/pin").Sensitive || opByPath(t, ops, "/etc/other").Sensitive {
		t.Fatalf("pin sensitive=%v other sensitive=%v, want true/false",
			opByPath(t, ops, "/etc/pin").Sensitive, opByPath(t, ops, "/etc/other").Sensitive)
	}
}

// TestSensitiveBlobSourceIsScanned pins that a file source packaged as a
// blob (above plan.MaxInlineContent) is scanned from its bytes, although the
// op itself then only carries the blob reference.
func TestSensitiveBlobSourceIsScanned(t *testing.T) {
	var src string
	ops := recordSecretTask(t, func() {
		key := strings.TrimSpace(MustSecret("svc/key"))
		src = filepath.Join(t.TempDir(), "big.conf")
		big := append(bytes.Repeat([]byte("# padding\n"), plan.MaxInlineContent/10+1), []byte("key "+key+"\n")...)
		if err := os.WriteFile(src, big, 0o600); err != nil {
			t.Fatal(err)
		}
		InstallFile("/etc/big.conf", src)
	})
	op := opByPath(t, ops, "/etc/big.conf")
	opPayload, _ := op.Payload.(plan.FilePayload)
	if op.Blob == "" || opPayload.ContentB64 != "" || !op.Sensitive {
		t.Fatalf("blob op = blob %q inline %d bytes sensitive %v; want a sensitive blob", op.Blob, len(opPayload.ContentB64), op.Sensitive)
	}
}

// TestRedactedPreviewHidesSecretsAndCannotApply checks every payload form
// of the preview and that no decoder accepts it as a plan.
func TestRedactedPreviewHidesSecretsAndCannotApply(t *testing.T) {
	ops := recordSecretTask(t, func() {
		key := strings.TrimSpace(MustSecret("svc/key"))
		File("/etc/k", options.WithContent(key+"\n"))
		File("/etc/t", options.WithContent("{{.K}}"), options.WithTemplateData(map[string]string{"K": key}))
		File("/etc/plain", options.WithContent("visible\n"))
		Command("/bin/echo", List(key), options.WithName("echo-token"))
		ConfigSet("svc", options.ConfigFile("conf", "/etc/svc/conf", options.WithContent(key)),
			options.WithSetValidation("/bin/true", []string{options.MemberPath("conf")}))
	})
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(fakePlanSecret)) {
		t.Fatal("test premise: the executable plan carries the secret")
	}
	preview, err := EncodeRedactedPreview(ops)
	if err != nil {
		t.Fatal(err)
	}
	requireNoSecret(t, "redacted preview", preview)
	if !bytes.HasPrefix(preview, []byte(`{"op":"plan_preview"`)) || !bytes.Contains(preview, []byte(secret.Redacted)) {
		t.Fatalf("preview header or markers missing:\n%s", preview)
	}
	if !bytes.Contains(preview, []byte(base64.StdEncoding.EncodeToString([]byte("visible\n")))) {
		t.Fatalf("non-sensitive content must stay visible:\n%s", preview)
	}
	if _, err := plan.DecodePlanBytes(preview); err == nil {
		t.Fatal("a redacted preview decoded as a plan")
	}
	if _, err := plan.DecodePush(bytes.NewReader(preview), ""); err == nil {
		t.Fatal("a redacted preview decoded as a push payload")
	}
	// The caller's ops are untouched.
	if again, _ := plan.EncodePlan(ops); !bytes.Equal(again, raw) {
		t.Fatal("EncodeRedactedPreview modified the recorded ops")
	}
}

// TestEncodeRedactedPreviewRefusesForeignKindPayload pins task cg2's
// encode-side fix (plan.Op.toWire's ownership check, plan/op_payload.go) as
// seen through EncodeRedactedPreview, the one other exported encoder that
// calls plan.EncodeOp directly (redactOp/copyOp only deep-copy op.Payload —
// they never validate its kind, so the fix must live below them, in
// EncodeOp itself, to protect this caller too). Before task cg2,
// EncodeRedactedPreview happily emitted a redacted preview line carrying a
// foreign kind's exclusive fields for the same reason plain EncodeOp did
// (see TestEncodeRefusesForeignKindPayload, plan/op_payload_test.go); now it
// must refuse instead, with no line written for the forged op.
func TestEncodeRedactedPreviewRefusesForeignKindPayload(t *testing.T) {
	forged := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion},
		{Op: plan.KindDir, ID: "Directory[/d]", Path: "/d",
			Payload: plan.FilePayload{KeyedLines: []plan.KeyedLine{{Key: "k", Line: "k=v"}}}},
	}
	if _, err := EncodeRedactedPreview(forged); err == nil {
		t.Fatal("EncodeRedactedPreview accepted an op holding a foreign-kind payload")
	} else if !strings.Contains(err.Error(), "foreign-kind payload") {
		t.Fatalf("EncodeRedactedPreview error = %q, want it to name a foreign-kind payload", err.Error())
	}
}

// TestRedactOpCollapsesSensitiveTemplateDataWholesale pins redactOp's
// wholesale template_data collapse (its own doc comment: "such an op may
// carry secret material no resolved value matches ... its template_data
// replaced as a whole") now that TemplateData lives on plan.FilePayload
// (task ae2) instead of a flat Op field redactOp used to mutate directly.
// FilePayload is a value type, so redactOp must copy it out of out.Payload,
// overwrite the copy's TemplateData, and write the copy BACK onto
// out.Payload; forgetting that last write-back would compile fine (Go
// happily discards an unused local mutation) and still pass every
// pre-existing secrets test — redactOpStrings' payloadWithholder redacts each
// JSON leaf string in place regardless, so the secret bytes would still be
// gone and TestRedactedPreviewHidesSecretsAndCannotApply's substring check
// would not notice — while silently leaving the ORIGINAL template_data
// object shape (a JSON object with a redacted leaf) on the wire instead of
// collapsing it to one opaque secret.Redacted marker. This test asserts the
// stronger, shape-collapsing contract directly against redactOp's own
// output, not just the absence of the secret's bytes.
func TestRedactOpCollapsesSensitiveTemplateDataWholesale(t *testing.T) {
	ops := recordSecretTask(t, func() {
		key := strings.TrimSpace(MustSecret("svc/key"))
		File("/etc/t", options.WithContent("{{.K}}"), options.WithTemplateData(map[string]string{"K": key}))
	})
	op := opByPath(t, ops, "/etc/t")
	if !op.Sensitive {
		t.Fatal("test premise: /etc/t must be recorded sensitive")
	}
	redacted, err := redactOp(op)
	if err != nil {
		t.Fatal(err)
	}
	fp, ok := redacted.Payload.(plan.FilePayload)
	if !ok {
		t.Fatalf("redactOp(op).Payload = %#v, want plan.FilePayload", redacted.Payload)
	}
	// The wholesale collapse replaces the WHOLE field with one JSON string
	// marker; a leaf-only redaction (the regression this test guards
	// against) would instead leave a JSON OBJECT like {"K":"[redacted]"} —
	// still secret-free, but the wrong shape.
	if want := `"` + secret.Redacted + `"`; string(fp.TemplateData) != want {
		t.Fatalf("redacted template_data = %s, want the wholesale marker %s (not a leaf-redacted object)", fp.TemplateData, want)
	}
}

// TestSensitiveRoundTripAndOldSchemas pins the wire: the flag survives
// encode/decode, a sensitive op still applies, and a v21 plan (no field)
// decodes and applies as before.
func TestSensitiveRoundTripAndOldSchemas(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "key")
	ops := recordSecretTask(t, func() {
		SecretFile(target, "svc/key", options.WithMode(0o600))
	})
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"sensitive":true`)) || !bytes.Contains(raw, []byte(`"version":22`)) {
		t.Fatalf("plan lacks the v22 sensitive field:\n%s", raw)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !opByPath(t, decoded, target).Sensitive {
		t.Fatal("sensitive flag lost in the round trip")
	}
	if err := plan.Apply(decoded, plan.Facts{}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	requireFileContent(t, target, fakePlanSecret+"\n")

	old := filepath.Join(dir, "old")
	v21 := `{"op":"plan","version":21,"id":"old"}` + "\n" +
		`{"op":"file","id":"File[` + old + `]","path":"` + old + `","has_content":true,"content_b64":"b2xkCg=="}` + "\n"
	oldOps, err := plan.DecodePlanBytes([]byte(v21))
	if err != nil {
		t.Fatalf("v21 plan refused: %v", err)
	}
	if err := plan.Apply(oldOps, plan.Facts{}, ""); err != nil {
		t.Fatalf("Apply v21: %v", err)
	}
	requireFileContent(t, old, "old\n")
}

// TestSensitiveValidatorOutputIsWithheld runs a failing validator that
// prints the whole candidate, for a file and for a config set: the apply
// error reports the failure and the output size but never the secret, while
// the same validator on non-sensitive content still shows its output.
func TestSensitiveValidatorOutputIsWithheld(t *testing.T) {
	dir := t.TempDir()
	echoAndFail := []string{"-c", `cat "$1"; exit 3`, "v", options.CandidatePath}
	ops := recordSecretTask(t, func() {
		key := strings.TrimSpace(MustSecret("svc/key"))
		File(filepath.Join(dir, "secret.conf"), options.WithContent("key "+key+"\n"),
			options.WithValidation("sh", echoAndFail))
	})
	err := plan.Apply(roundTrip(t, ops), plan.Facts{}, "")
	if err == nil || !strings.Contains(err.Error(), "validator output withheld") {
		t.Fatalf("file: err = %v, want a withheld-output validation failure", err)
	}
	requireNoSecret(t, "file validation error", []byte(err.Error()))

	ops = recordSecretTask(t, func() {
		key := strings.TrimSpace(MustSecret("svc/key"))
		ConfigSet("svc", options.ConfigFile("conf", filepath.Join(dir, "set.conf"), options.WithContent("key "+key+"\n")),
			options.WithSetValidation("sh", []string{"-c", `cat "$1"; exit 3`, "v", options.MemberPath("conf")}))
	})
	err = plan.Apply(roundTrip(t, ops), plan.Facts{}, "")
	if err == nil || !strings.Contains(err.Error(), "validator output withheld") {
		t.Fatalf("config set: err = %v, want a withheld-output validation failure", err)
	}
	requireNoSecret(t, "config set validation error", []byte(err.Error()))

	ops = recordSecretTask(t, func() {
		File(filepath.Join(dir, "plain.conf"), options.WithContent("visible-line\n"), options.WithValidation("sh", echoAndFail))
	})
	err = plan.Apply(roundTrip(t, ops), plan.Facts{}, "")
	if err == nil || !strings.Contains(err.Error(), "visible-line") {
		t.Fatalf("plain file: err = %v, want the validator output", err)
	}
}

// TestSensitiveTemplateErrorIsWithheld pins that a template that fails to
// parse on the destination does not quote its secret-bearing text.
func TestSensitiveTemplateErrorIsWithheld(t *testing.T) {
	dir := t.TempDir()
	ops := recordSecretTask(t, func() {
		key := strings.TrimSpace(MustSecret("svc/key"))
		File(filepath.Join(dir, "bad"), options.WithContent("{{"+key+"}}"), options.WithTemplate)
	})
	err := plan.Apply(roundTrip(t, ops), plan.Facts{}, "")
	if err == nil || !strings.Contains(err.Error(), "details withheld") {
		t.Fatalf("err = %v, want a withheld template error", err)
	}
	requireNoSecret(t, "template error", []byte(err.Error()))
}

// roundTrip encodes and decodes ops, as a push or plan file would.
func roundTrip(t *testing.T, ops []plan.Op) []plan.Op {
	t.Helper()
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resource.ResetForTest)
	return decoded
}

func requireFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
