package cli

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// This file is task 4b2 (w82 phase 2, docs/plan-encryption.md): `gonf plan
// -seal -for host|cluster|fleet`. The central property under test —
// docs/plan-encryption.md's whole reason for recording once per host — is
// TestCLIPlanSealForIsolatesHostSecrets: a host's ForHosts-only secret must
// never reach another host's plan-<host>.age, even though both are recorded
// from the same recipe in the same command.
//
// TestCLIPlanSealForNameSubstringCarriesOtherHostsSecret and
// TestCLIPlanSealForSSHHostSubstringCarriesUnrelatedHostsSecret (task ng2)
// pin the flip side of that property, deliberately: -for's record-time host
// selection is inventory.SelectionForHosts, the same substring-based
// push-alias expansion `gonf push` itself relies on (see
// internal/inventory/destination.go), not an exact single-host match. When
// one registered host's name or SSHHost is a substring of another's, the
// OTHER host's ForHosts body is pulled into the recording too, and its
// secret material physically lands in the target host's sealed artifact —
// see api.RecordPlanForHost's doc comment and docs/plan-encryption.md's
// "Runbook" for the qualified guarantee and the operator-facing warning
// these two tests pin.

// registerSealForInventory registers two hosts (hostA, hostB), each with its
// own generated age1pq recipient/identity pair and its own secret file under
// secrets/<host>/token, a cluster "edge" containing both, a fleet
// "edge-fleet" wrapping a solo cluster containing only hostA, and one task
// (cli_seal_for_task) that writes each visited host's own secret into
// <work>/<host>-out via ForHosts — the shape docs/plan-encryption.md's
// "records once per host" rule exists to protect.
func registerSealForInventory(t *testing.T) (work string, recipient, identity map[string]string) {
	t.Helper()
	api.ResetForTest()
	api.ResetInventory()
	t.Cleanup(func() {
		api.ResetForTest()
		api.ResetInventory()
	})
	work = t.TempDir()
	t.Chdir(work)

	recipient = map[string]string{}
	identity = map[string]string{}
	for _, host := range []string{"hostA", "hostB"} {
		r, id := genSealKeyPair(t)
		recipient[host] = r
		identity[host] = id
		writeHostSecretFile(t, host, "secret-for-"+host)
	}

	hA := api.Host("hostA", api.WithPlanRecipient(recipient["hostA"]), api.WithValue("secretkey", "hostA/token"))
	hB := api.Host("hostB", api.WithPlanRecipient(recipient["hostB"]), api.WithValue("secretkey", "hostB/token"))
	api.Cluster("edge", hA, hB)
	api.Fleet("edge-fleet", api.Cluster("solo", hA))

	api.Task("cli_seal_for_task", "", func() {
		api.ForHosts("secretkey", func(host string, key string) {
			api.File(filepath.Join(work, host+"-out"), options.WithContent(api.MustSecret(key)))
		})
	}, api.WithTaskCluster("edge"))

	return work, recipient, identity
}

// writeHostSecretFile writes content as the fake secrets/<host>/token file
// the default file secret provider reads relative to the current directory
// (see internal/cli/secret_plan_test.go's registerSecretTask, same
// technique).
func writeHostSecretFile(t *testing.T, host, content string) {
	t.Helper()
	dir := filepath.Join("secrets", host)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte(content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// decryptSealedPlanOps opens path with identityLine (an AGE-SECRET-KEY-PQ-1…
// line) and decodes its GONF-PUSH/1 frame, failing the test on any error.
func decryptSealedPlanOps(t *testing.T, path, identityLine string) []plan.Op {
	t.Helper()
	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	identityPath := filepath.Join(t.TempDir(), "identity")
	writeIdentityFile(t, identityPath, identityLine)
	identities, err := seal.LoadIdentities(identityPath)
	if err != nil {
		t.Fatalf("LoadIdentities: %v", err)
	}
	r, err := seal.Open(bytes.NewReader(sealed), identities)
	if err != nil {
		t.Fatalf("Open %s: %v", path, err)
	}
	payload, err := plan.DecodePush(r, "")
	if err != nil {
		t.Fatalf("DecodePush %s: %v", path, err)
	}
	return payload.Ops
}

// fileOpContents returns the decoded content of every "file" op in ops.
func fileOpContents(t *testing.T, ops []plan.Op) []string {
	t.Helper()
	var out []string
	for _, op := range ops {
		if string(op.Op) != "file" {
			continue
		}
		fp, ok := op.Payload.(plan.FilePayload)
		if !ok {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(fp.ContentB64)
		if err != nil {
			t.Fatalf("decode content_b64 of %s: %v", op.Path, err)
		}
		out = append(out, string(raw))
	}
	return out
}

// containsSubstring reports whether any of vals contains want.
func containsSubstring(vals []string, want string) bool {
	for _, v := range vals {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}

// TestCLIPlanSealForIsolatesHostSecrets is the core property task 4b2 adds:
// -for records once per target host, so a per-host sealed artifact carries
// only that host's own ForHosts secret, never a sibling's — even though
// hostA and hostB are recorded from the very same command against the same
// recipe. It also proves the cross-identity negative: hostB's identity does
// not open hostA's artifact.
func TestCLIPlanSealForIsolatesHostSecrets(t *testing.T) {
	isolateXDGConfig(t)
	_, _, identity := registerSealForInventory(t)
	// An operator base recipient (task mg2: -for now refuses with none, the
	// same as plain -seal, so every test that expects success must supply
	// one) — also used below to confirm the operator can open BOTH per-host
	// artifacts, the "host's recipient plus the operator's" property the
	// docs promise.
	operatorRecipient, operatorIdentity := genSealKeyPair(t)
	dir := filepath.Join(t.TempDir(), "out")

	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", operatorRecipient, "-for", "edge", "cli_seal_for_task")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, name := range []string{"plan-hostA.age", "plan-hostB.age"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}

	opsA := decryptSealedPlanOps(t, filepath.Join(dir, "plan-hostA.age"), identity["hostA"])
	opsB := decryptSealedPlanOps(t, filepath.Join(dir, "plan-hostB.age"), identity["hostB"])
	contentsA := fileOpContents(t, opsA)
	contentsB := fileOpContents(t, opsB)

	if !containsSubstring(contentsA, "secret-for-hostA") || containsSubstring(contentsA, "secret-for-hostB") {
		t.Fatalf("plan-hostA.age file contents = %v; want only hostA's own secret", contentsA)
	}
	if !containsSubstring(contentsB, "secret-for-hostB") || containsSubstring(contentsB, "secret-for-hostA") {
		t.Fatalf("plan-hostB.age file contents = %v; want only hostB's own secret", contentsB)
	}

	// Both artifacts also open with the operator's own identity (task mg2's
	// fix): each is sealed to its host's recipient PLUS the operator's base
	// recipient, not the host's alone.
	opsAForOperator := decryptSealedPlanOps(t, filepath.Join(dir, "plan-hostA.age"), operatorIdentity)
	if !containsSubstring(fileOpContents(t, opsAForOperator), "secret-for-hostA") {
		t.Fatalf("plan-hostA.age did not open with the operator's own identity")
	}
	opsBForOperator := decryptSealedPlanOps(t, filepath.Join(dir, "plan-hostB.age"), operatorIdentity)
	if !containsSubstring(fileOpContents(t, opsBForOperator), "secret-for-hostB") {
		t.Fatalf("plan-hostB.age did not open with the operator's own identity")
	}

	// Cross-identity refusal: hostA's artifact does not open with hostB's
	// identity (each is sealed to its own host's recipient, not the other's).
	sealedA, err := os.ReadFile(filepath.Join(dir, "plan-hostA.age"))
	if err != nil {
		t.Fatal(err)
	}
	idPath := filepath.Join(t.TempDir(), "hostB-identity")
	writeIdentityFile(t, idPath, identity["hostB"])
	idsB, err := seal.LoadIdentities(idPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seal.Open(bytes.NewReader(sealedA), idsB); err == nil {
		t.Fatal("plan-hostA.age opened with hostB's identity; want a refusal")
	}
}

// TestCLIPlanSealForNameSubstringCarriesOtherHostsSecret pins a real,
// documented limitation of -for's per-host isolation (task ng2): the
// record-time selection is inventory.SelectionForHosts, which expands a
// target to every registered host whose NAME is a substring of it, not an
// exact single-host match. Here "web" is a substring of "web01", so
// `-for web01` pulls web's ForHosts body into the same recording and web's
// secret physically ends up inside plan-web01.age, alongside web01's own
// secret.
func TestCLIPlanSealForNameSubstringCarriesOtherHostsSecret(t *testing.T) {
	isolateXDGConfig(t)
	api.ResetForTest()
	api.ResetInventory()
	t.Cleanup(func() {
		api.ResetForTest()
		api.ResetInventory()
	})
	work := t.TempDir()
	t.Chdir(work)

	rWeb, _ := genSealKeyPair(t)
	rWeb01, idWeb01 := genSealKeyPair(t)
	// Operator base recipient (task mg2): -for now refuses with none.
	operatorRecipient, _ := genSealKeyPair(t)
	writeHostSecretFile(t, "web", "secret-for-web")
	writeHostSecretFile(t, "web01", "secret-for-web01")

	hWeb := api.Host("web", api.WithPlanRecipient(rWeb), api.WithValue("secretkey", "web/token"))
	hWeb01 := api.Host("web01", api.WithPlanRecipient(rWeb01), api.WithValue("secretkey", "web01/token"))
	api.Cluster("edge", hWeb, hWeb01)
	api.Task("cli_seal_for_substring_name", "", func() {
		api.ForHosts("secretkey", func(host string, key string) {
			api.File(filepath.Join(work, host+"-out"), options.WithContent(api.MustSecret(key)))
		})
	}, api.WithTaskCluster("edge"))

	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", operatorRecipient, "-for", "web01", "cli_seal_for_substring_name")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	// -for web01 resolves to exactly one TARGET host (PlanRecipientTargetHosts
	// does not expand a plain host name), so only plan-web01.age is written —
	// even though the RECORDING selection pulled web in too.
	if _, err := os.Stat(filepath.Join(dir, "plan-web.age")); !os.IsNotExist(err) {
		t.Fatalf("plan-web.age unexpectedly written (err=%v)", err)
	}
	ops := decryptSealedPlanOps(t, filepath.Join(dir, "plan-web01.age"), idWeb01)
	contents := fileOpContents(t, ops)
	if !containsSubstring(contents, "secret-for-web01") {
		t.Fatalf("plan-web01.age contents = %v; missing web01's own secret", contents)
	}
	if !containsSubstring(contents, "secret-for-web") {
		t.Fatalf("plan-web01.age contents = %v; want it to ALSO carry web's secret "+
			`(pins the substring-selection limitation: SelectionForHosts([]string{"web01"}) `+
			`includes "web" because "web" is a substring of "web01")`, contents)
	}
}

// TestCLIPlanSealForSSHHostSubstringCarriesUnrelatedHostsSecret pins the
// second probed shape of the same limitation (task ng2): the substring test
// also runs over a host's SSHHost, not just its inventory name, so an
// otherwise wholly unrelated host can be pulled in. web's SSHHost is
// "web.db.example", which contains "db", so `-for web` pulls db's ForHosts
// body into the recording too — wrapped in a when_begin/hostname_contains
// "db" guard that will never actually match web's real live hostname, but
// db's secret material is still physically present in plan-web.age, so
// web's root could read it directly from the decoded artifact.
func TestCLIPlanSealForSSHHostSubstringCarriesUnrelatedHostsSecret(t *testing.T) {
	isolateXDGConfig(t)
	api.ResetForTest()
	api.ResetInventory()
	t.Cleanup(func() {
		api.ResetForTest()
		api.ResetInventory()
	})
	work := t.TempDir()
	t.Chdir(work)

	rWeb, idWeb := genSealKeyPair(t)
	// Operator base recipient (task mg2): -for now refuses with none.
	operatorRecipient, _ := genSealKeyPair(t)
	writeHostSecretFile(t, "web", "secret-for-web")
	writeHostSecretFile(t, "db", "SECRET-DB-ONLY")

	hWeb := api.Host("web", api.WithSSHHost("web.db.example"), api.WithPlanRecipient(rWeb),
		api.WithValue("secretkey", "web/token"))
	// db is otherwise unrelated to web and is never a -for target itself, so
	// it needs no api.WithPlanRecipient of its own.
	hDb := api.Host("db", api.WithValue("secretkey", "db/token"))
	api.Cluster("edge", hWeb, hDb)
	api.Task("cli_seal_for_substring_sshhost", "", func() {
		api.ForHosts("secretkey", func(host string, key string) {
			api.File(filepath.Join(work, host+"-out"), options.WithContent(api.MustSecret(key)))
		})
	}, api.WithTaskCluster("edge"))

	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", operatorRecipient, "-for", "web", "cli_seal_for_substring_sshhost")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	ops := decryptSealedPlanOps(t, filepath.Join(dir, "plan-web.age"), idWeb)
	contents := fileOpContents(t, ops)
	if !containsSubstring(contents, "secret-for-web") {
		t.Fatalf("plan-web.age contents = %v; missing web's own secret", contents)
	}
	if !containsSubstring(contents, "SECRET-DB-ONLY") {
		t.Fatalf("plan-web.age contents = %v; want it to ALSO carry db's secret "+
			`(pins the substring-selection limitation: web's SSHHost "web.db.example" `+
			`contains "db", so SelectionForHosts([]string{"web"}) includes db even `+
			`though db is otherwise unrelated to web)`, contents)
	}
}

// TestCLIPlanSealForFleetTargetSingleHost: -for accepts a fleet name too,
// and -stdout is allowed when it resolves to exactly one host (edge-fleet
// here wraps a solo cluster containing only hostA).
func TestCLIPlanSealForFleetTargetSingleHost(t *testing.T) {
	isolateXDGConfig(t)
	_, _, identity := registerSealForInventory(t)
	// Operator base recipient (task mg2): -for now refuses with none.
	operatorRecipient, _ := genSealKeyPair(t)
	var code int
	var stderr string
	out := captureStdout(t, func() {
		code, stderr = runGonf(t, "plan", "-seal", "-stdout", "-recipient", operatorRecipient, "-for", "edge-fleet", "cli_seal_for_task")
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.HasPrefix(out, "age-encryption.org/v1") {
		t.Fatalf("stdout does not start with the age magic; got %q", out[:min(len(out), 40)])
	}
	identityPath := filepath.Join(t.TempDir(), "identity")
	writeIdentityFile(t, identityPath, identity["hostA"])
	identities, err := seal.LoadIdentities(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	r, err := seal.Open(strings.NewReader(out), identities)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	payload, err := plan.DecodePush(r, "")
	if err != nil {
		t.Fatalf("DecodePush: %v", err)
	}
	contents := fileOpContents(t, payload.Ops)
	if !containsSubstring(contents, "secret-for-hostA") {
		t.Fatalf("contents = %v, want hostA's secret", contents)
	}
}

// TestCLIPlanSealForStdoutRefusesMultipleHosts: -for -stdout is refused,
// naming the host count, when the target resolves to more than one host.
func TestCLIPlanSealForStdoutRefusesMultipleHosts(t *testing.T) {
	isolateXDGConfig(t)
	registerSealForInventory(t)
	var code int
	var stderr string
	out := captureStdout(t, func() {
		code, stderr = runGonf(t, "plan", "-seal", "-stdout", "-for", "edge", "cli_seal_for_task")
	})
	if code == 0 || out != "" {
		t.Fatalf("exit %d, stdout %q; want a refusal and no output", code, out)
	}
	if !strings.Contains(stderr, "2 hosts") {
		t.Fatalf("stderr %q, want it to name the 2 resolved hosts", stderr)
	}
}

// TestCLIPlanSealForRefusesHostWithoutRecipient: a target host missing
// api.WithPlanRecipient is refused BY NAME before anything is written —
// docs/plan-encryption.md's "-for" row's own rule.
func TestCLIPlanSealForRefusesHostWithoutRecipient(t *testing.T) {
	isolateXDGConfig(t)
	api.ResetForTest()
	api.ResetInventory()
	t.Cleanup(func() {
		api.ResetForTest()
		api.ResetInventory()
	})
	work := t.TempDir()
	t.Chdir(work)
	recipient, _ := genSealKeyPair(t)
	hA := api.Host("hostA", api.WithPlanRecipient(recipient))
	hB := api.Host("hostB") // no WithPlanRecipient
	api.Cluster("edge", hA, hB)
	api.Task("cli_seal_for_norecipient", "", func() {})

	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-for", "edge", "cli_seal_for_norecipient")
	if code == 0 {
		t.Fatalf("exit 0, want a refusal; stderr %q", stderr)
	}
	if !strings.Contains(stderr, "hostB") || !strings.Contains(stderr, "a plan recipient") {
		t.Fatalf("stderr %q, want it to name hostB and say it lacks a plan recipient", stderr)
	}
	if strings.Contains(stderr, "hostA lacks") {
		t.Fatalf("stderr %q names hostA, which HAS a recipient", stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("output directory %s exists (err=%v); nothing should have been written", dir, err)
	}
}

// TestCLIPlanSealForRefusesZeroRecipients is task mg2's own regression
// probe: reproduces the exact reported gap. Before the fix,
// `gonf plan -o out -seal -for host …` with no ~/.config/gonf/recipients
// file (isolateXDGConfig, an empty XDG_CONFIG_HOME) and no -recipient flags
// exited 0 and wrote out/plan-<host>.age sealed to EXACTLY ONE recipient —
// the destination host's own — so the operator's own identity could never
// open it ("plan/seal: open: identity did not match any of the
// recipients"). The fix makes -for refuse the same way plain -seal already
// does (TestCLIPlanSealRefusesZeroRecipients, plan_seal_test.go), before
// anything is written, rather than silently producing an artifact nobody,
// not even the operator, can open (plan/seal.ErrNoRecipients' own doc
// comment).
func TestCLIPlanSealForRefusesZeroRecipients(t *testing.T) {
	isolateXDGConfig(t)
	api.ResetForTest()
	api.ResetInventory()
	t.Cleanup(func() {
		api.ResetForTest()
		api.ResetInventory()
	})
	work := t.TempDir()
	t.Chdir(work)
	recipient, _ := genSealKeyPair(t)
	api.Host("hostA", api.WithPlanRecipient(recipient))
	api.Task("cli_seal_for_zero_base", "", func() {})

	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-for", "hostA", "cli_seal_for_zero_base")
	if code == 0 {
		t.Fatalf("exit 0, want a refusal; stderr %q", stderr)
	}
	if !strings.Contains(stderr, "no recipients") {
		t.Fatalf("stderr %q, want it to say there are no recipients (matching planSealed's own message)", stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("output directory %s exists (err=%v); nothing should have been written", dir, err)
	}
}

// TestCLIPlanSealForNoDefaultRecipientsRefusesZeroRecipients: the probe's
// second reported path. -no-default-recipients (task ce2) with no
// -recipient flags reaches the identical zero-base-recipients refusal
// deterministically, even when an ambient recipients file DOES exist (it is
// just not consulted) — so -no-default-recipients can never be used to
// silently reach the host-only, operator-excluded state either.
func TestCLIPlanSealForNoDefaultRecipientsRefusesZeroRecipients(t *testing.T) {
	xdgDir := isolateXDGConfig(t)
	api.ResetForTest()
	api.ResetInventory()
	t.Cleanup(func() {
		api.ResetForTest()
		api.ResetInventory()
	})
	work := t.TempDir()
	t.Chdir(work)
	recipient, _ := genSealKeyPair(t)
	api.Host("hostA", api.WithPlanRecipient(recipient))
	api.Task("cli_seal_for_no_default_recipients", "", func() {})

	// An ambient default recipients file DOES exist here (unlike the sibling
	// test above), to prove -no-default-recipients is what makes this
	// refuse, not merely an absent file.
	ambientRecipient, _ := genSealKeyPair(t)
	recipientsDir := filepath.Join(xdgDir, "gonf")
	if err := os.MkdirAll(recipientsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recipientsDir, "recipients"), []byte(ambientRecipient+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-no-default-recipients", "-for", "hostA", "cli_seal_for_no_default_recipients")
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

// TestCLIPlanSealForRequiresSeal: -for without -seal is a static usage
// error (exit 2), not a runtime refusal.
func TestCLIPlanSealForRequiresSeal(t *testing.T) {
	isolateXDGConfig(t)
	registerSealForInventory(t)
	code, stderr := runGonf(t, "plan", "-for", "edge", "cli_seal_for_task")
	if code != 2 || !strings.Contains(stderr, "-for only applies to -seal") {
		t.Fatalf("exit %d, stderr %q; want a usage refusal", code, stderr)
	}
}

// TestCLIPlanSealForUnknownTarget: an unregistered -for name is refused by
// name, exit 1.
func TestCLIPlanSealForUnknownTarget(t *testing.T) {
	isolateXDGConfig(t)
	registerSealForInventory(t)
	code, stderr := runGonf(t, "plan", "-seal", "-for", "ghost", "cli_seal_for_task")
	if code == 0 || !strings.Contains(stderr, `"ghost" is not a registered host, cluster or fleet`) {
		t.Fatalf("exit %d, stderr %q; want the unknown-target refusal", code, stderr)
	}
}

// TestCLIPlanSealForPrintsDeclarationLocation is task og2's own regression
// probe: a per-host task body failure (here api.MustSecret on a
// deliberately missing secret, inside sealPerHostPlans' RecordPlanForHost
// call) must print the recipe's declared-at location on stderr, exactly
// like plain -seal already does (planToSealedDir's eprintErr) -- before the
// fix, planSealedFor used eprintf and silently dropped that second line.
func TestCLIPlanSealForPrintsDeclarationLocation(t *testing.T) {
	isolateXDGConfig(t)
	api.ResetForTest()
	api.ResetInventory()
	t.Cleanup(func() {
		api.ResetForTest()
		api.ResetInventory()
	})
	work := t.TempDir()
	t.Chdir(work)
	recipient, _ := genSealKeyPair(t)
	// Operator base recipient (task mg2): -for now refuses with none, before
	// this test's actual target (the missing-secret record failure) is ever
	// reached.
	operatorRecipient, _ := genSealKeyPair(t)
	api.Host("hostA", api.WithPlanRecipient(recipient))
	api.Task("cli_seal_for_missing_secret", "", func() {
		api.MustSecret("nope/missing")
	})

	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", operatorRecipient, "-for", "hostA", "cli_seal_for_missing_secret")
	if code == 0 {
		t.Fatalf("exit 0, want a refusal; stderr %q", stderr)
	}
	if !strings.Contains(stderr, `"nope/missing"`) {
		t.Fatalf("stderr %q, want it to name the missing secret", stderr)
	}
	if !strings.Contains(stderr, "declared at ") || !strings.Contains(stderr, "plan_seal_for_test.go:") {
		t.Fatalf("stderr %q, want the recipe's declared-at location (the gap task og2 fixes)", stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("output directory %s exists (err=%v); nothing should have been written on a record failure", dir, err)
	}
}

// TestCLIPlanSealForFilenameCollisionRefused: two hosts whose names
// sanitize to the same dir/plan-<name>.age fragment are refused before
// anything is written, rather than one silently overwriting the other's
// artifact.
func TestCLIPlanSealForFilenameCollisionRefused(t *testing.T) {
	isolateXDGConfig(t)
	api.ResetForTest()
	api.ResetInventory()
	t.Cleanup(func() {
		api.ResetForTest()
		api.ResetInventory()
	})
	work := t.TempDir()
	t.Chdir(work)
	r1, _ := genSealKeyPair(t)
	r2, _ := genSealKeyPair(t)
	// Operator base recipient (task mg2): -for now refuses with none, before
	// this test's actual target (the filename collision) is ever reached.
	operatorRecipient, _ := genSealKeyPair(t)
	h1 := api.Host("h/a", api.WithPlanRecipient(r1))
	h2 := api.Host("h*a", api.WithPlanRecipient(r2))
	api.Cluster("collide", h1, h2)
	api.Task("cli_seal_for_collide", "", func() {})

	dir := filepath.Join(t.TempDir(), "out")
	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", operatorRecipient, "-for", "collide", "cli_seal_for_collide")
	if code == 0 {
		t.Fatalf("exit 0, want a refusal; stderr %q", stderr)
	}
	if !strings.Contains(stderr, "plan-h_a.age") {
		t.Fatalf("stderr %q, want it to name the colliding filename plan-h_a.age", stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("output directory %s exists (err=%v); nothing should have been written", dir, err)
	}
}
