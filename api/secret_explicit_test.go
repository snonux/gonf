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
)

// payloadKindsBody declares one resource of every kind WithSensitive
// accepts, each carrying a base64 form of the resolved secret, which the
// scan cannot recognise (docs/secrets.md, "Limits of the scan"). With mark
// each gets WithSensitive (through marked); without, the body is the plain
// recipe. tree is a controller-local source directory for the synced tree.
func payloadKindsBody(tree string, mark bool) func() {
	return func() {
		derived := base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(MustSecret("svc/key"))))
		File("/etc/derived", marked[options.FileOption](mark, options.WithContent("auth "+derived+"\n"))...)
		SyncDir("/etc/tree", filepath.Join(tree, "*"), marked[options.DirOption](mark)...)
		Command("/bin/true", List("--token", derived), marked[options.CommandOption](mark, options.WithName("upload"))...)
		Package("vendor-tool", marked[options.PackageOption](mark,
			options.WithEnv(map[string]string{"PKG_PATH": "https://u:" + derived + "@repo"}))...)
		Cron("upload", marked[options.CronOption](mark, options.WithCommand("/bin/up "+derived), options.WithMinute("5"))...)
		SystemdTimer("upload", marked[options.SystemdTimerOption](mark, options.WithCommand("/bin/up "+derived),
			options.WithOnCalendar("daily"))...)
		ConfigSet("whole", marked[options.ConfigSetOption](mark,
			options.ConfigFile("conf", "/etc/whole/conf", options.WithContent(derived)),
			options.WithSetValidation("/bin/true", []string{options.MemberPath("conf")}))...)
		ConfigSet("member", options.ConfigFile("conf", "/etc/member/conf",
			marked[options.FileOption](mark, options.WithContent(derived))...),
			options.WithSetValidation("/bin/true", []string{options.MemberPath("conf")}))
		File("/etc/plain", options.WithContent("nothing secret\n"))
	}
}

// marked returns opts, plus WithSensitive when mark is set. The one
// WithSensitive value converts to every payload family T, which is the
// compile-time contract of options.SensitiveOption.
func marked[T any](mark bool, opts ...T) []T {
	if mark {
		var sensitive options.SensitiveOption = options.WithSensitive
		opts = append(opts, any(sensitive).(T))
	}
	return opts
}

// payloadTree returns a fresh one-file source tree for the synced
// directory of payloadKindsBody.
func payloadTree(t *testing.T) string {
	t.Helper()
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "token"), []byte("tree-held material\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return tree
}

// recordPayloadKinds records payloadKindsBody over tree below a fresh
// secrets/ directory.
func recordPayloadKinds(t *testing.T, tree string, mark bool) []plan.Op {
	t.Helper()
	return recordSecretTask(t, payloadKindsBody(tree, mark))
}

// TestWithSensitiveMarksEveryPayloadKind pins the option end to end: every
// kind that accepts it records a sensitive op (a config set also when only
// a member is marked), the plan header declares schema 22, and
// SensitiveOpNames — what `gonf plan -stdout` refuses with — names them.
// The unmarked plain file and the config-set member handles stay unmarked.
func TestWithSensitiveMarksEveryPayloadKind(t *testing.T) {
	ops := recordPayloadKinds(t, payloadTree(t), true)
	if ops[0].Version != plan.VersionSensitive {
		t.Fatalf("header version = %d, want %d", ops[0].Version, plan.VersionSensitive)
	}
	want := map[plan.Kind]int{plan.KindFile: 1, plan.KindSyncDir: 1, plan.KindCommand: 1, plan.KindPackage: 1,
		plan.KindCron: 1, plan.KindSystemdTimer: 1, plan.KindConfigSet: 2}
	got := map[plan.Kind]int{}
	for _, op := range ops[1:] {
		if op.Sensitive {
			got[op.Op]++
		}
	}
	for kind, n := range want {
		if got[kind] != n {
			t.Errorf("%s: %d sensitive ops, want %d", kind, got[kind], n)
		}
	}
	if opByPath(t, ops, "/etc/plain").Sensitive || got[plan.KindConfigSetMember] != 0 {
		t.Errorf("unmarked ops became sensitive: %v", got)
	}
	if names := SensitiveOpNames(ops); len(names) != 8 {
		t.Fatalf("SensitiveOpNames = %v, want the 8 marked ops", names)
	}
}

// TestSensitivityScanMissesDerivedSecrets is the gap WithSensitive closes:
// without it, none of the base64-derived payloads is recognised, and the
// plan keeps its v21 header.
func TestSensitivityScanMissesDerivedSecrets(t *testing.T) {
	ops := recordPayloadKinds(t, payloadTree(t), false)
	if names := SensitiveOpNames(ops); len(names) != 0 {
		t.Fatalf("derived payloads unexpectedly detected: %v", names)
	}
	if ops[0].Version != plan.VersionSensitive-1 {
		t.Fatalf("header version = %d, want %d", ops[0].Version, plan.VersionSensitive-1)
	}
}

// TestWithSensitiveChangesOnlyTheSensitiveFlag pins that the option adds
// nothing but the flag: the marked plan with every sensitive flag cleared
// and the header set back to v21 encodes byte for byte like the plan
// recorded without the option.
func TestWithSensitiveChangesOnlyTheSensitiveFlag(t *testing.T) {
	tree := payloadTree(t) // one tree: its path is recorded as source_dir
	plain := recordPayloadKinds(t, tree, false)
	marked := recordPayloadKinds(t, tree, true)
	for i := range marked {
		marked[i].Sensitive = false
	}
	marked[0].Version = plan.RequiredVersion(marked[1:])
	a, err := plan.EncodePlan(plain)
	if err != nil {
		t.Fatal(err)
	}
	b, err := plan.EncodePlan(marked)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("plans differ beyond the sensitive flag:\nwithout: %s\nwith:    %s", a, b)
	}
	if bytes.Contains(a, []byte(`"sensitive":`)) {
		t.Fatalf("a plan without WithSensitive or secrets carries a sensitive field:\n%s", a)
	}
}

// TestWithSensitiveWithoutResolvedSecrets pins that the explicit marker
// does not depend on the secret registry: a recipe that never called
// ResolveSecret (its secret came from elsewhere) still gets a sensitive op
// and a v22 header, and the -redacted preview withholds its content.
func TestWithSensitiveWithoutResolvedSecrets(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	const material = "externally-sourced-material"
	Task("t", "", func() {
		File("/etc/external", options.WithContent(material), options.WithSensitive)
	})
	ops, err := RecordPlanTo("explicit", plan.NewMemoryStore(), "t")
	if err != nil {
		t.Fatalf("RecordPlanTo: %v", err)
	}
	if !opByPath(t, ops, "/etc/external").Sensitive || ops[0].Version != plan.VersionSensitive {
		t.Fatalf("op not sensitive or header not v22: %#v", ops)
	}
	preview, err := EncodeRedactedPreview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(preview, []byte(base64.StdEncoding.EncodeToString([]byte(material)))) {
		t.Fatalf("redacted preview carries the marked content:\n%s", preview)
	}
}

// TestRedactedPreviewWithholdsExplicitPayloads is the review repro: a
// WithSensitive-only op carries material no resolved value matches, so the
// preview must withhold every payload string of the op (argv, cron command
// and environment, package environment keys and values, lines, guards),
// keep its identities, and still be valid JSONL.
func TestRedactedPreviewWithholdsExplicitPayloads(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("t", "", func() {
		Command("/bin/upload", List("--token", "DERIVED-argv"), options.WithName("upload"),
			options.Unless("/bin/check", []string{"DERIVED-guard"}), options.WithSensitive)
		Cron("job", options.WithCommand("/bin/up DERIVED-cron"), options.WithCronEnv("TOKEN=DERIVED-env"),
			options.WithSensitive)
		Package("tool", options.WithEnv(map[string]string{"DERIVED_KEY": "DERIVED-value", "B": "DERIVED-b"}),
			options.WithSensitive)
		File("/etc/lines", options.WithLines("key DERIVED-line"), options.WithSensitive)
	})
	ops, err := RecordPlanTo("explicit", plan.NewMemoryStore(), "t")
	if err != nil {
		t.Fatalf("RecordPlanTo: %v", err)
	}
	preview, err := EncodeRedactedPreview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(preview, []byte("DERIVED")) {
		t.Fatalf("preview carries a derived payload:\n%s", preview)
	}
	for i, line := range bytes.Split(bytes.TrimSpace(preview), []byte("\n")) {
		op, err := plan.DecodeOp(line)
		if err != nil {
			t.Fatalf("preview line %d is not a valid op line: %v: %s", i+1, err, line)
		}
		if op.Op == plan.KindPackage && len(op.Env) != 2 {
			t.Errorf("package env should keep one withheld entry per variable: %s", line)
		}
	}
	for _, keep := range []string{`"id":"Command[upload]"`, `"bin":"/bin/upload"`, `"id":"Package[tool]"`, `"path":"/etc/lines"`} {
		if !bytes.Contains(preview, []byte(keep)) {
			t.Errorf("preview lost the identity %s:\n%s", keep, preview)
		}
	}
}
