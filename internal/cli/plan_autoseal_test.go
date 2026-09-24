package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// Task 5b2: `gonf plan -o dir` seals a sensitive plan by default when an
// operator recipients file exists; -plaintext opts out, -plaintext with
// -seal is a usage error, and an existing but unusable recipients file
// refuses a sensitive plan instead of falling back to plaintext.

// recipientsState is how the default recipients file looks for one
// autoSealCase.
type recipientsState int

const (
	recipientsAbsent  recipientsState = iota // no file at the default path
	recipientsValid                          // one valid age1pq line, mode 0600
	recipientsInvalid                        // a valid line, but world-writable (refused by the loader)
)

// autoSealCase is one cell of the sensitivity x recipients file x flag
// matrix, and what `gonf plan -o dir` must do in it.
type autoSealCase struct {
	name       string
	sensitive  bool
	recipients recipientsState
	flag       string // "", "-plaintext" or "-seal"
	wantCode   int
	wantFile   string // "plan.jsonl", "plan.age" or "" (nothing written)
	wantStderr string // a substring stderr must hold ("" for none)
	denyStderr string // a substring stderr must not hold ("" for none)
}

// autoSealMatrix is every combination of sensitive/non-sensitive,
// recipients file absent/valid/invalid and no flag/-plaintext/-seal.
func autoSealMatrix() []autoSealCase {
	const note, clear = "sealed by default", "carries secret material in clear text"
	return []autoSealCase{
		{"plain/absent/none", false, recipientsAbsent, "", 0, "plan.jsonl", "", note},
		{"plain/absent/plaintext", false, recipientsAbsent, "-plaintext", 0, "plan.jsonl", "", note},
		{"plain/absent/seal", false, recipientsAbsent, "-seal", 1, "", "no recipients", ""},
		{"plain/valid/none", false, recipientsValid, "", 0, "plan.jsonl", "", note},
		{"plain/valid/plaintext", false, recipientsValid, "-plaintext", 0, "plan.jsonl", "", note},
		{"plain/valid/seal", false, recipientsValid, "-seal", 0, "plan.age", "", note},
		{"plain/invalid/none", false, recipientsInvalid, "", 0, "plan.jsonl", "a secret-bearing plan would be refused", note},
		{"plain/invalid/plaintext", false, recipientsInvalid, "-plaintext", 0, "plan.jsonl", "", "warning"},
		{"plain/invalid/seal", false, recipientsInvalid, "-seal", 1, "", "writable", ""},
		{"secret/absent/none", true, recipientsAbsent, "", 0, "plan.jsonl", clear, note},
		{"secret/absent/plaintext", true, recipientsAbsent, "-plaintext", 0, "plan.jsonl", clear, note},
		{"secret/absent/seal", true, recipientsAbsent, "-seal", 1, "", "no recipients", ""},
		{"secret/valid/none", true, recipientsValid, "", 0, "plan.age", note, clear},
		{"secret/valid/plaintext", true, recipientsValid, "-plaintext", 0, "plan.jsonl", clear, note},
		{"secret/valid/seal", true, recipientsValid, "-seal", 0, "plan.age", "", note},
		{"secret/invalid/none", true, recipientsInvalid, "", 1, "", "sealing by default refused", clear},
		{"secret/invalid/plaintext", true, recipientsInvalid, "-plaintext", 0, "plan.jsonl", clear, note},
		{"secret/invalid/seal", true, recipientsInvalid, "-seal", 1, "", "writable", ""},
	}
}

// writeDefaultRecipients writes content as the default recipients file
// below xdg with exactly mode, and returns its path.
func writeDefaultRecipients(t *testing.T, xdg, content string, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(xdg, "gonf")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "recipients")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile's mode is narrowed by the umask; set it exactly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// setUpRecipients puts the default recipients file into state and returns
// the recipient and identity lines of its key.
func setUpRecipients(t *testing.T, xdg string, state recipientsState) (recipient, identity string) {
	t.Helper()
	recipient, identity = genSealKeyPair(t)
	switch state {
	case recipientsValid:
		writeDefaultRecipients(t, xdg, recipient+"\n", 0o600)
	case recipientsInvalid:
		writeDefaultRecipients(t, xdg, recipient+"\n", 0o666)
	}
	return recipient, identity
}

// registerAutoSealTask registers the sensitive (registerSecretTask) or the
// non-sensitive (registerSealTask) task and returns its name.
func registerAutoSealTask(t *testing.T, sensitive bool) string {
	t.Helper()
	if sensitive {
		registerSecretTask(t)
		return "cli_secret"
	}
	registerSealTask(t)
	return "cli_seal_task"
}

// TestCLIPlanAutoSealMatrix runs every cell of autoSealMatrix and checks
// the exit code, which of plan.jsonl/plan.age was written (never both) and
// the stderr it must and must not hold.
func TestCLIPlanAutoSealMatrix(t *testing.T) {
	for _, tc := range autoSealMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			xdg := isolateXDGConfig(t)
			task := registerAutoSealTask(t, tc.sensitive)
			setUpRecipients(t, xdg, tc.recipients)
			dir := filepath.Join(t.TempDir(), "out")
			args := []string{"plan", "-o", dir}
			if tc.flag != "" {
				args = append(args, tc.flag)
			}
			var code int
			var stderr string
			out := captureStdout(t, func() { code, stderr = runGonf(t, append(args, task)...) })
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d; stdout %q, stderr %q", code, tc.wantCode, out, stderr)
			}
			requireOnlyArtifact(t, dir, tc.wantFile)
			if tc.wantStderr != "" && !strings.Contains(stderr, tc.wantStderr) {
				t.Fatalf("stderr %q, want it to contain %q", stderr, tc.wantStderr)
			}
			if tc.denyStderr != "" && strings.Contains(stderr, tc.denyStderr) {
				t.Fatalf("stderr %q, must not contain %q", stderr, tc.denyStderr)
			}
			if leaksCLISecret(out) || leaksCLISecret(stderr) {
				t.Fatalf("output leaks the secret: stdout %q, stderr %q", out, stderr)
			}
		})
	}
}

// requireOnlyArtifact checks that dir holds want ("plan.jsonl" or
// "plan.age") and not the other one, or neither when want is "".
func requireOnlyArtifact(t *testing.T, dir, want string) {
	t.Helper()
	for _, name := range []string{"plan.jsonl", "plan.age"} {
		_, err := os.Stat(filepath.Join(dir, name))
		switch {
		case name == want && err != nil:
			t.Fatalf("%s missing: %v", name, err)
		case name != want && !os.IsNotExist(err):
			t.Fatalf("%s exists (err=%v); want only %q", name, err, want)
		}
	}
}

// TestCLIPlanAutoSealExactWording pins the exact stderr line of a default
// seal and that the sealed plan.age opens with the recipients file's key
// and holds the sensitive op.
func TestCLIPlanAutoSealExactWording(t *testing.T) {
	xdg := isolateXDGConfig(t)
	work := registerSecretTask(t)
	_, identity := setUpRecipients(t, xdg, recipientsValid)
	dir := filepath.Join(t.TempDir(), "out")
	var code int
	var stderr string
	out := captureStdout(t, func() { code, stderr = runGonf(t, "plan", "-o", dir, "cli_secret") })
	if code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	planAge := filepath.Join(dir, "plan.age")
	wantNote := "plan: sealed by default: the plan carries secret material in File[" + filepath.Join(work, "token") +
		"] and the recipients file " + filepath.Join(xdg, "gonf", "recipients") + " exists, so " + planAge +
		" was written instead of a plaintext plan.jsonl; decrypt it with gonf apply -identity <file>, " +
		"or pass -plaintext to write plaintext instead\n"
	if stderr != wantNote {
		t.Fatalf("stderr\n%q\nwant\n%q", stderr, wantNote)
	}
	if !strings.HasPrefix(out, "wrote "+planAge+" (") {
		t.Fatalf("stdout %q, want the usual wrote-plan.age report", out)
	}
	ops := openSealedPlan(t, planAge, filepath.Join(work, "identity"), identity)
	if len(plan.SensitiveIDs(ops)) != 1 {
		t.Fatalf("sealed plan sensitive ops = %v, want 1", plan.SensitiveIDs(ops))
	}
}

// openSealedPlan decrypts the plan.age at path with identityLine (written
// to identityPath first) and returns its ops.
func openSealedPlan(t *testing.T, path, identityPath, identityLine string) []plan.Op {
	t.Helper()
	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeIdentityFile(t, identityPath, identityLine)
	identities, err := seal.LoadIdentities(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	r, err := seal.Open(bytes.NewReader(sealed), identities)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	payload, err := plan.DecodePush(r, "")
	if err != nil {
		t.Fatalf("DecodePush: %v", err)
	}
	return payload.Ops
}

// TestCLIPlanAutoSealRefusalExactWording pins the exact refusal of a
// sensitive plan when the recipients file exists but is unusable
// (world-writable), and that nothing, not even the directory, is written.
func TestCLIPlanAutoSealRefusalExactWording(t *testing.T) {
	xdg := isolateXDGConfig(t)
	work := registerSecretTask(t)
	setUpRecipients(t, xdg, recipientsInvalid)
	path := filepath.Join(xdg, "gonf", "recipients")
	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "cli_secret")
	if code != 1 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	_, loadErr := seal.LoadRecipientsFile(path)
	if loadErr == nil {
		t.Fatal("LoadRecipientsFile accepted a world-writable file")
	}
	want := "plan: sealing by default refused (the plan carries secret material in File[" +
		filepath.Join(work, "token") + "]): " + loadErr.Error() +
		"; not written in plaintext by default — fix the recipients, pass -seal -recipient age1pq..., " +
		"or pass -plaintext to write a plaintext plan.jsonl anyway; nothing written\n"
	if stderr != want {
		t.Fatalf("stderr\n%q\nwant\n%q", stderr, want)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("output directory exists (err=%v); nothing must be written", err)
	}
}

// TestCLIPlanAutoSealRefusesEmptyRecipientsFile: a recipients file of
// comments only exists, so it opts in, but holds no recipient: a sensitive
// plan is refused (a sealed write needs a recipient, and plaintext is not
// the fallback), naming the file.
func TestCLIPlanAutoSealRefusesEmptyRecipientsFile(t *testing.T) {
	xdg := isolateXDGConfig(t)
	registerSecretTask(t)
	path := writeDefaultRecipients(t, xdg, "# nobody yet\n", 0o600)
	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "cli_secret")
	if code != 1 || !strings.Contains(stderr, "no recipients: "+path+" holds no age1pq recipient") {
		t.Fatalf("exit %d, stderr %q; want the empty-recipients refusal", code, stderr)
	}
	requireOnlyArtifact(t, dir, "")
}

// TestCLIPlanAutoSealNonSensitiveByteIdentical: with a valid recipients
// file, a non-sensitive plan that packages a blob tree takes the deferred
// (in-memory) recording path, and must write exactly the plan.jsonl and
// blobs/ the unchanged plaintext path writes with no recipients file.
func TestCLIPlanAutoSealNonSensitiveByteIdentical(t *testing.T) {
	xdg := isolateXDGConfig(t)
	registerOutDirProbe(t)
	plainDir := filepath.Join(t.TempDir(), "plain")
	if code, stderr := runGonf(t, "plan", "-o", plainDir, "cli_outdir"); code != 0 {
		t.Fatalf("plaintext run: exit %d, stderr %q", code, stderr)
	}
	setUpRecipients(t, xdg, recipientsValid)
	deferredDir := filepath.Join(t.TempDir(), "deferred")
	code, stderr := runGonf(t, "plan", "-o", deferredDir, "cli_outdir")
	if code != 0 || stderr != "" {
		t.Fatalf("deferred run: exit %d, stderr %q; want a silent plaintext write", code, stderr)
	}
	if a, b := treeSnapshot(t, plainDir), treeSnapshot(t, deferredDir); a != b {
		t.Fatalf("output differs:\nplaintext path:\n%s\ndeferred path:\n%s", a, b)
	}
}

// treeSnapshot renders every entry below root (relative path, mode and,
// for a file, its content) in walk order, for comparing two outputs.
func treeSnapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		b.WriteString(rel + " " + info.Mode().String())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b.WriteString(" " + string(data))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestCLIPlanAutoSealRecipientSources: -no-default-recipients stops the
// ambient file from opting in (plaintext, with the clear-text warning), and
// an explicit -recipients-file opts in on its own and is named in the note.
func TestCLIPlanAutoSealRecipientSources(t *testing.T) {
	xdg := isolateXDGConfig(t)
	work := registerSecretTask(t)
	recipient, _ := setUpRecipients(t, xdg, recipientsValid)

	noDefault := filepath.Join(t.TempDir(), "nodefault")
	code, stderr := runGonf(t, "plan", "-o", noDefault, "-no-default-recipients", "cli_secret")
	if code != 0 || !strings.Contains(stderr, "carries secret material in clear text") {
		t.Fatalf("-no-default-recipients: exit %d, stderr %q", code, stderr)
	}
	requireOnlyArtifact(t, noDefault, "plan.jsonl")

	explicit := filepath.Join(work, "my-recipients")
	if err := os.WriteFile(explicit, []byte(recipient+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	named := filepath.Join(t.TempDir(), "named")
	code, stderr = runGonf(t, "plan", "-o", named, "-no-default-recipients", "-recipients-file", explicit, "cli_secret")
	if code != 0 || !strings.Contains(stderr, "the recipients file "+explicit+" exists") {
		t.Fatalf("-recipients-file: exit %d, stderr %q", code, stderr)
	}
	requireOnlyArtifact(t, named, "plan.age")
}

// TestCLIPlanPlaintextFlagConflicts: -plaintext with -seal, -stdout or
// -redacted is a usage error (exit 2) before anything is recorded.
func TestCLIPlanPlaintextFlagConflicts(t *testing.T) {
	isolateXDGConfig(t)
	registerSealTask(t)
	cases := map[string][]string{
		"-plaintext cannot combine with -seal":  {"-plaintext", "-seal"},
		"-plaintext only applies to -o dir":     {"-plaintext", "-stdout"},
		"-plaintext only applies to -o dir: -s": {"-redacted", "-plaintext"},
	}
	for want, flags := range cases {
		args := append(append([]string{"plan"}, flags...), "cli_seal_task")
		if code, stderr := runGonf(t, args...); code != 2 || !strings.Contains(stderr, want) {
			t.Fatalf("%v: exit %d, stderr %q; want exit 2 and %q", flags, code, stderr, want)
		}
	}
}

// TestCLIPlanAutoSealLeavesStdoutForAndSignAlone: the default seal is an
// -o behaviour only. -stdout still refuses a sensitive plan with a
// recipients file present (no silent switch to binary age), and -for and
// -sign still need an explicit -seal (exit 2).
func TestCLIPlanAutoSealLeavesStdoutForAndSignAlone(t *testing.T) {
	xdg := isolateXDGConfig(t)
	registerSecretTask(t)
	setUpRecipients(t, xdg, recipientsValid)
	var code int
	var stderr string
	out := captureStdout(t, func() { code, stderr = runGonf(t, "plan", "-stdout", "cli_secret") })
	if code != 1 || out != "" || !strings.Contains(stderr, "-stdout refused") {
		t.Fatalf("-stdout: exit %d, stdout %q, stderr %q; want the unchanged refusal", code, out, stderr)
	}
	for flag, want := range map[string]string{"-for": "-for only applies to -seal", "-sign": "-sign only applies to -seal"} {
		if code, stderr := runGonf(t, "plan", flag, "x", "cli_secret"); code != 2 || !strings.Contains(stderr, want) {
			t.Fatalf("%s without -seal: exit %d, stderr %q", flag, code, stderr)
		}
	}
}
