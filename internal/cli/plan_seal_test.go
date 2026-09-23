package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// registerSealTask registers "cli_seal_task": one plain file resource,
// carrying no secret material. Most of this file's tests only need
// something cheap to record — -seal's own refusals (zero recipients,
// -redacted/-with-secrets) and the plan.age vs. plan.jsonl/blobs/ split
// do not depend on sensitivity. It resets task/resource state and changes
// into a fresh temp working directory, like registerSecretTask
// (secret_plan_test.go).
func registerSealTask(t *testing.T) string {
	t.Helper()
	api.ResetForTest()
	t.Cleanup(api.ResetForTest)
	work := t.TempDir()
	t.Chdir(work)
	api.Task("cli_seal_task", "", func() {
		api.File(filepath.Join(work, "plain"), options.WithContent("plain\n"))
	})
	return work
}

// isolateXDGConfig points XDG_CONFIG_HOME at a fresh, empty temp directory
// for the duration of one test, so resolvePlanRecipients's default
// recipients file lookup never reads the real host's
// ~/.config/gonf/recipients: every -seal test must control exactly which
// recipients are in play, never depend on ambient host state.
func isolateXDGConfig(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

// genSealKeyPair returns a fresh age1pq hybrid recipient line and the
// matching identity's own line (AGE-SECRET-KEY-PQ-1...), generated through
// filippo.io/age directly — the same technique plan/seal's own tests use
// (plan/seal/main_test.go's genKeyPair) — since plan/seal exposes no
// key-generation API of its own (its doc comment: "no CLI surface", tasks
// 2b2/3b2 only consume Seal/Open/ParseRecipients/LoadIdentities).
func genSealKeyPair(t *testing.T) (recipientLine, identityLine string) {
	t.Helper()
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatalf("generate hybrid identity: %v", err)
	}
	return id.Recipient().String(), id.String()
}

// writeIdentityFile writes identityLine as an age identity file LoadIdentities
// accepts: a regular file, owned by the current user, mode 0600 (no group
// or other bits).
func writeIdentityFile(t *testing.T, path, identityLine string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(identityLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCLIPlanSealWritesOnlyPlanAge: -seal writes dir/plan.age and nothing
// else — no plan.jsonl, no blobs/ — even for a task that packages a blob
// (registerOutDirProbe, plan_outdir_test.go: a SyncDir source, which the
// plaintext -o path would write into dir/blobs/). Sealing packages that same
// blob straight into the in-memory GONF-PUSH/1 frame instead
// (docs/plan-encryption.md "Artifact": recording goes to a plan.MemoryStore,
// so no plaintext blob ever lands in dir).
func TestCLIPlanSealWritesOnlyPlanAge(t *testing.T) {
	isolateXDGConfig(t)
	registerOutDirProbe(t)
	recipient, _ := genSealKeyPair(t)
	dir := filepath.Join(t.TempDir(), "out")
	var code int
	var stderr string
	out := captureStdout(t, func() { code, stderr = runGonf(t, "plan", "-o", dir, "-seal", "-recipient", recipient, "cli_outdir") })
	if code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if !strings.Contains(out, filepath.Join(dir, "plan.age")) || !strings.Contains(out, "1 recipients") {
		t.Fatalf("stdout %q, want a wrote-plan.age line naming the file and 1 recipients", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.age")); err != nil {
		t.Fatalf("plan.age missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("plan.jsonl exists (err=%v); -seal must never write it", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs")); !os.IsNotExist(err) {
		t.Fatalf("blobs/ exists (err=%v); -seal must never write it", err)
	}
	if info, err := os.Stat(filepath.Join(dir, "plan.age")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("plan.age mode = %v, %v; want 0600", info, err)
	}
}

// TestCLIPlanSealRoundTrips proves the written plan.age is not just present
// but genuinely openable: plan/seal.Open with the matching identity decrypts
// it, and plan.DecodePush recovers the same ops the plaintext path would
// have recorded (same op count, same file path/content op).
func TestCLIPlanSealRoundTrips(t *testing.T) {
	isolateXDGConfig(t)
	work := registerSealTask(t)
	recipient, identityLine := genSealKeyPair(t)
	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", recipient, "cli_seal_task")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	sealed, err := os.ReadFile(filepath.Join(dir, "plan.age"))
	if err != nil {
		t.Fatalf("read plan.age: %v", err)
	}

	identityPath := filepath.Join(work, "identity")
	writeIdentityFile(t, identityPath, identityLine)
	identities, err := seal.LoadIdentities(identityPath)
	if err != nil {
		t.Fatalf("LoadIdentities: %v", err)
	}
	r, err := seal.Open(bytes.NewReader(sealed), identities)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	payload, err := plan.DecodePush(r, "")
	if err != nil {
		t.Fatalf("DecodePush: %v", err)
	}
	// api.File also ensures its parent directory, so the recorded plan has
	// an ensure_dir op alongside the file op; find the file op by kind
	// rather than assuming its index.
	fileOp := findOpByKind(t, payload.Ops, "file")
	if got := fileOp.Path; got != filepath.Join(work, "plain") {
		t.Fatalf("op path = %q, want %q", got, filepath.Join(work, "plain"))
	}
	if got := fileOp.ContentB64; got == "" {
		t.Fatalf("op content is empty")
	}
}

// findOpByKind returns the one op of ops whose Op field equals kind,
// failing the test when there is not exactly one.
func findOpByKind(t *testing.T, ops []plan.Op, kind string) plan.Op {
	t.Helper()
	var found []plan.Op
	for _, op := range ops {
		if string(op.Op) == kind {
			found = append(found, op)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d ops of kind %q in %v, want exactly 1", len(found), kind, ops)
	}
	return found[0]
}

// TestCLIPlanSealRoundTripWrongIdentityFails is the negative half of the
// round trip: a genuine, differently-keyed identity does not open a plan.age
// sealed to someone else's recipient (age's "no identity matched" case,
// reached through plan/seal.Open).
func TestCLIPlanSealRoundTripWrongIdentityFails(t *testing.T) {
	isolateXDGConfig(t)
	work := registerSealTask(t)
	recipient, _ := genSealKeyPair(t)
	_, wrongIdentityLine := genSealKeyPair(t)
	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", recipient, "cli_seal_task")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	sealed, err := os.ReadFile(filepath.Join(dir, "plan.age"))
	if err != nil {
		t.Fatalf("read plan.age: %v", err)
	}
	identityPath := filepath.Join(work, "wrong-identity")
	writeIdentityFile(t, identityPath, wrongIdentityLine)
	identities, err := seal.LoadIdentities(identityPath)
	if err != nil {
		t.Fatalf("LoadIdentities: %v", err)
	}
	if _, err := seal.Open(bytes.NewReader(sealed), identities); err == nil {
		t.Fatal("Open succeeded with the wrong identity; want a refusal")
	}
}

// TestCLIPlanSealRefusesZeroRecipients: -seal with no -recipient flags and
// no (or empty) default recipients file is refused, exit non-zero, and
// nothing is written — never a silent plaintext fallback
// (docs/plan-encryption.md "Keys": "A sealed write with zero recipients is
// refused, never degraded to plaintext").
func TestCLIPlanSealRefusesZeroRecipients(t *testing.T) {
	isolateXDGConfig(t)
	registerSealTask(t)
	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "cli_seal_task")
	if code == 0 {
		t.Fatalf("exit 0, want a refusal; stderr %q", stderr)
	}
	if !strings.Contains(stderr, "no recipients") {
		t.Fatalf("stderr %q, want it to say there are no recipients", stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("output directory %s exists (err=%v); nothing should have been written", dir, err)
	}
}

// TestCLIPlanSealStdoutWritesSealedBytesNoDisk: -seal -stdout writes the
// sealed (binary age, no armor) bytes to stdout and touches no file at all
// — the pipe form docs/plan-encryption.md "Operator UX" documents
// (`gonf plan -seal -stdout | ssh host gonf apply -identity ... -`).
func TestCLIPlanSealStdoutWritesSealedBytesNoDisk(t *testing.T) {
	isolateXDGConfig(t)
	work := registerSealTask(t)
	recipient, identityLine := genSealKeyPair(t)
	var code int
	var stderr string
	out := captureStdout(t, func() {
		code, stderr = runGonf(t, "plan", "-seal", "-stdout", "-recipient", recipient, "cli_seal_task")
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.HasPrefix(out, "age-encryption.org/v1") {
		t.Fatalf("stdout does not start with the age magic; got %q", out[:min(len(out), 40)])
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Fatalf("unexpected file %s written to disk by -seal -stdout", e.Name())
	}
	if len(entries) != 0 {
		t.Fatalf("work dir not empty: %v", entries)
	}

	// The stdout bytes really are the sealed plan, openable the same way as
	// the file form.
	identityPath := filepath.Join(t.TempDir(), "identity")
	writeIdentityFile(t, identityPath, identityLine)
	identities, err := seal.LoadIdentities(identityPath)
	if err != nil {
		t.Fatalf("LoadIdentities: %v", err)
	}
	r, err := seal.Open(strings.NewReader(out), identities)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := plan.DecodePush(r, ""); err != nil {
		t.Fatalf("DecodePush: %v", err)
	}
}

// TestCLIPlanSealRejectsRedactedAndWithSecrets: -seal combined with
// -redacted or -with-secrets is a usage error (exit 2), never a silent
// choice between them — sealing and redaction/secret-revealing are
// mutually exclusive concepts (2b2's REVISED scope annotation).
func TestCLIPlanSealRejectsRedactedAndWithSecrets(t *testing.T) {
	isolateXDGConfig(t)
	registerSealTask(t)
	recipient, _ := genSealKeyPair(t)
	cases := [][]string{
		{"plan", "-seal", "-redacted", "-recipient", recipient, "cli_seal_task"},
		{"plan", "-seal", "-with-secrets", "-recipient", recipient, "cli_seal_task"},
		{"plan", "-seal", "-stdout", "-with-secrets", "-recipient", recipient, "cli_seal_task"},
	}
	for _, args := range cases {
		var code int
		var stderr string
		out := captureStdout(t, func() { code, stderr = runGonf(t, args...) })
		if code != 2 || out != "" || !strings.HasPrefix(stderr, "plan: -") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want a usage refusal (exit 2)", args, code, out, stderr)
		}
	}
}

// TestCLIPlanSealWarnsAboutPreexistingPlaintext: sealing into a directory
// that still holds a plaintext plan.jsonl (and/or blobs/) from an earlier,
// unsealed run warns about it on stderr and leaves it exactly as it was —
// never deletes it (docs/plan-encryption.md "Operator UX" table).
func TestCLIPlanSealWarnsAboutPreexistingPlaintext(t *testing.T) {
	isolateXDGConfig(t)
	registerSealTask(t)
	recipient, _ := genSealKeyPair(t)
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	leftover := []byte(`{"op":"file"}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "plan.jsonl"), leftover, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", recipient, "cli_seal_task")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "plan.jsonl") || !strings.Contains(stderr, "blobs/") ||
		!strings.Contains(stderr, "earlier unsealed run") {
		t.Fatalf("stderr %q, want a warning naming plan.jsonl and blobs/ from an earlier unsealed run", stderr)
	}
	got, err := os.ReadFile(filepath.Join(dir, "plan.jsonl"))
	if err != nil || !bytes.Equal(got, leftover) {
		t.Fatalf("plan.jsonl changed: got %q, %v; want it left exactly as it was", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.age")); err != nil {
		t.Fatalf("plan.age missing: %v", err)
	}
}

// TestCLIPlanSealUsesDefaultRecipientsFile: with no -recipient flags, -seal
// falls back to the default recipients file
// ($XDG_CONFIG_HOME/gonf/recipients), "#" comments and blank lines ignored,
// exactly as plan/seal.ParseRecipients documents.
func TestCLIPlanSealUsesDefaultRecipientsFile(t *testing.T) {
	xdg := isolateXDGConfig(t)
	registerSealTask(t)
	recipient, _ := genSealKeyPair(t)
	recipientsDir := filepath.Join(xdg, "gonf")
	if err := os.MkdirAll(recipientsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "# operator recipients\n\n" + recipient + "\n"
	if err := os.WriteFile(filepath.Join(recipientsDir, "recipients"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "out")
	var code int
	var stderr string
	out := captureStdout(t, func() { code, stderr = runGonf(t, "plan", "-o", dir, "-seal", "cli_seal_task") })
	if code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if !strings.Contains(out, "1 recipients") {
		t.Fatalf("stdout %q, want 1 recipients (from the default file, no -recipient flag given)", out)
	}
}

// TestCLIPlanSealRefusesSymlinkedRecipientsFile reproduces task ce2's probe
// (A): the default recipients file planted as a SYMLINK to a mode-666 file
// carrying an attacker's recipient line is refused (exit non-zero), and
// nothing is written — before this fix, `gonf plan -seal` accepted it and
// the injected key genuinely decrypted the sealed frame.
func TestCLIPlanSealRefusesSymlinkedRecipientsFile(t *testing.T) {
	xdg := isolateXDGConfig(t)
	registerSealTask(t)
	operatorRecipient, _ := genSealKeyPair(t)
	attackerRecipient, _ := genSealKeyPair(t)

	recipientsDir := filepath.Join(xdg, "gonf")
	if err := os.MkdirAll(recipientsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The attacker's world-writable file, planted anywhere, and a symlink
	// from the default recipients path to it — exactly probe (A).
	attackerFile := filepath.Join(t.TempDir(), "attacker-recipients")
	if err := os.WriteFile(attackerFile, []byte(attackerRecipient+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attackerFile, filepath.Join(recipientsDir, "recipients")); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", operatorRecipient, "cli_seal_task")
	if code == 0 {
		t.Fatalf("exit 0, want a refusal for a symlinked recipients file; stderr %q", stderr)
	}
	if !strings.Contains(stderr, "symlink") {
		t.Fatalf("stderr %q, want it to name the symlink refusal", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.age")); !os.IsNotExist(err) {
		t.Fatalf("plan.age exists (err=%v); a refused recipients file must never seal", err)
	}
}

// TestCLIPlanSealRefusesWorldWritableRecipientsFile reproduces task ce2's
// probe (B): a plain mode-666 (world-writable) regular default recipients
// file is refused the same way, no warning-only fallback.
func TestCLIPlanSealRefusesWorldWritableRecipientsFile(t *testing.T) {
	xdg := isolateXDGConfig(t)
	registerSealTask(t)
	operatorRecipient, _ := genSealKeyPair(t)
	attackerRecipient, _ := genSealKeyPair(t)

	recipientsDir := filepath.Join(xdg, "gonf")
	if err := os.MkdirAll(recipientsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recipientsDir, "recipients"), []byte(attackerRecipient+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile's mode is narrowed by umask; force it exactly, since this
	// test is about the mode gonf sees.
	if err := os.Chmod(filepath.Join(recipientsDir, "recipients"), 0o666); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", operatorRecipient, "cli_seal_task")
	if code == 0 {
		t.Fatalf("exit 0, want a refusal for a world-writable recipients file; stderr %q", stderr)
	}
	if !strings.Contains(stderr, "writable") {
		t.Fatalf("stderr %q, want it to name the writable-by-group-or-other refusal", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.age")); !os.IsNotExist(err) {
		t.Fatalf("plan.age exists (err=%v); a refused recipients file must never seal", err)
	}
}

// TestCLIPlanSealNoDefaultRecipientsExcludesDefaultFile reproduces task
// ce2's probe (C) as a genuine round trip, not just a count: with
// -no-default-recipients, a legitimate (properly-permissioned) default
// recipients file's own key does NOT decrypt the sealed plan, while the
// explicitly-passed -recipient key still does — before this fix, an
// operator passing exactly one -recipient still got the default file's
// entries silently unioned in.
func TestCLIPlanSealNoDefaultRecipientsExcludesDefaultFile(t *testing.T) {
	xdg := isolateXDGConfig(t)
	work := registerSealTask(t)
	explicitRecipient, explicitIdentity := genSealKeyPair(t)
	defaultRecipient, defaultIdentity := genSealKeyPair(t)

	recipientsDir := filepath.Join(xdg, "gonf")
	if err := os.MkdirAll(recipientsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recipientsDir, "recipients"), []byte(defaultRecipient+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "out")
	var code int
	var stderr string
	out := captureStdout(t, func() {
		code, stderr = runGonf(t, "plan", "-o", dir, "-seal", "-recipient", explicitRecipient,
			"-no-default-recipients", "cli_seal_task")
	})
	if code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if !strings.Contains(out, "1 recipients") {
		t.Fatalf("stdout %q, want exactly 1 recipient (the default file must be excluded)", out)
	}
	sealed, err := os.ReadFile(filepath.Join(dir, "plan.age"))
	if err != nil {
		t.Fatalf("read plan.age: %v", err)
	}

	// The explicit -recipient identity still decrypts.
	explicitIdentityPath := filepath.Join(work, "explicit-identity")
	writeIdentityFile(t, explicitIdentityPath, explicitIdentity)
	explicitIdentities, err := seal.LoadIdentities(explicitIdentityPath)
	if err != nil {
		t.Fatalf("LoadIdentities(explicit): %v", err)
	}
	if _, err := seal.Open(bytes.NewReader(sealed), explicitIdentities); err != nil {
		t.Fatalf("Open with the explicit -recipient identity failed: %v", err)
	}

	// The default-file-only identity does NOT: it was excluded.
	defaultIdentityPath := filepath.Join(work, "default-identity")
	writeIdentityFile(t, defaultIdentityPath, defaultIdentity)
	defaultIdentities, err := seal.LoadIdentities(defaultIdentityPath)
	if err != nil {
		t.Fatalf("LoadIdentities(default): %v", err)
	}
	if _, err := seal.Open(bytes.NewReader(sealed), defaultIdentities); err == nil {
		t.Fatal("Open succeeded with the excluded default-file identity; -no-default-recipients did not exclude it")
	}
}

// TestCLIPlanSealPrintsResolvedRecipientKeys: the "wrote ..." output names
// the actual resolved recipient public keys, not only a count (task ce2),
// so an operator reviewing output has a real chance of noticing an
// unexpected extra recipient.
func TestCLIPlanSealPrintsResolvedRecipientKeys(t *testing.T) {
	isolateXDGConfig(t)
	registerSealTask(t)
	r1, _ := genSealKeyPair(t)
	r2, _ := genSealKeyPair(t)
	dir := filepath.Join(t.TempDir(), "out")
	var code int
	var stderr string
	out := captureStdout(t, func() {
		code, stderr = runGonf(t, "plan", "-o", dir, "-seal", "-recipient", r1, "-recipient", r2, "cli_seal_task")
	})
	if code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if !strings.Contains(out, r1) || !strings.Contains(out, r2) {
		t.Fatalf("stdout %q, want both resolved recipient public keys printed, not only a count", out)
	}
}

// TestCLIPlanSealRecipientsFileFlagReadsExplicitPath: -recipients-file
// reads the named file instead of the ambient default, still unioned with
// -recipient flags.
func TestCLIPlanSealRecipientsFileFlagReadsExplicitPath(t *testing.T) {
	isolateXDGConfig(t) // no ambient default file exists
	registerSealTask(t)
	explicit, _ := genSealKeyPair(t)
	recipientsFile := filepath.Join(t.TempDir(), "my-recipients")
	if err := os.WriteFile(recipientsFile, []byte(explicit+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "out")
	var code int
	var stderr string
	out := captureStdout(t, func() {
		code, stderr = runGonf(t, "plan", "-o", dir, "-seal", "-recipients-file", recipientsFile, "cli_seal_task")
	})
	if code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if !strings.Contains(out, "1 recipients") || !strings.Contains(out, explicit) {
		t.Fatalf("stdout %q, want 1 recipients naming %s", out, explicit)
	}
}

// TestCLIPlanSealRecipientsFileFlagMissingIsError: unlike the ambient
// default, an explicitly named -recipients-file that does not exist is a
// hard error, not silently empty — a typo'd path must not fail open into
// "zero recipients from this source".
func TestCLIPlanSealRecipientsFileFlagMissingIsError(t *testing.T) {
	isolateXDGConfig(t)
	registerSealTask(t)
	recipient, _ := genSealKeyPair(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", recipient,
		"-recipients-file", missing, "cli_seal_task")
	if code == 0 {
		t.Fatalf("exit 0, want an error for a missing explicit -recipients-file; stderr %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.age")); !os.IsNotExist(err) {
		t.Fatalf("plan.age exists (err=%v); a missing explicit -recipients-file must refuse before sealing", err)
	}
}

// TestCLIPlanWithoutSealUnchanged is a narrow regression pin, alongside the
// pre-existing plan_outdir_test.go/secret_flags_test.go suites (which this
// task must not alter the behaviour of): a plain `gonf plan -o dir` with no
// -seal still writes plan.jsonl (never plan.age), byte-for-byte the same
// plan.EncodePlan output the pre-2b2 planToDir always wrote.
func TestCLIPlanWithoutSealUnchanged(t *testing.T) {
	isolateXDGConfig(t)
	registerSealTask(t)
	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "cli_seal_task")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.jsonl")); err != nil {
		t.Fatalf("plan.jsonl missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.age")); !os.IsNotExist(err) {
		t.Fatalf("plan.age exists (err=%v); a non-seal run must never write it", err)
	}
}
