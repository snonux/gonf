package api

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
)

// fillValue sets every string, slice, map and pointer reachable from v to a
// non-empty value, so a walk over it meets every field path. A kind the
// walker does not know fails the test, like it would panic the walker.
//
// The struct case skips a field literally named "Payload": Op.Payload
// (task yd2) is polymorphic — one Op value holds at most one concrete
// OpPayload at a time — so filling every kind's exclusive fields takes one
// pass per kind instead of one shared pass here; see
// TestOpFieldClassesAreExhaustive, which drives those passes itself using
// plan.OpPayloadExamples.
func fillValue(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	switch {
	case v.Type() == rawMessageType:
		v.SetBytes([]byte(`{"k":"v"}`))
	case v.Kind() == reflect.String:
		v.SetString("x")
	case v.Kind() == reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillValue(t, v.Elem(), path)
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillValue(t, v.Index(0), path+"[]")
	case isStringMap(v.Type()):
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(reflect.ValueOf("k").Convert(v.Type().Key()), reflect.ValueOf("v").Convert(v.Type().Elem()))
		v.Set(m)
	case v.Kind() == reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() || f.Name == "Payload" {
				continue
			}
			fillValue(t, v.Field(i), path+"."+f.Name)
		}
	case isScalar(v.Kind()):
	default:
		t.Fatalf("plan.Op%s has type %s, which walkOpStrings does not handle", path, v.Type())
	}
}

// TestOpFieldClassesAreExhaustive is the fitness test of opFieldClasses: a
// fully populated plan.Op must yield exactly the classified paths, so a new
// string-bearing field fails here until it is classified as payload,
// identity or metadata, and a removed one fails as stale.
//
// Since task yd2, Op.Payload is polymorphic (one Op value holds at most one
// concrete OpPayload), so a single fillValue pass can no longer reach every
// kind's exclusive fields at once: the base pass below fills every core
// field with Payload left nil (fillValue skips it by name), and one further
// pass per plan.OpPayloadExamples entry fills that kind's own payload and
// walks an Op carrying it, so every kind-exclusive field is reached exactly
// once across the union of passes.
func TestOpFieldClassesAreExhaustive(t *testing.T) {
	seen := map[string]bool{}
	record := func(op plan.Op) {
		t.Helper()
		walkOpStrings(&op, func(path, s string) string {
			seen[path] = true
			return s
		})
	}

	var base plan.Op
	fillValue(t, reflect.ValueOf(&base).Elem(), "")
	record(base)

	for kind, example := range plan.OpPayloadExamples() {
		pv := reflect.New(reflect.TypeOf(example)).Elem()
		fillValue(t, pv, "")
		op := plan.Op{Op: kind}
		var ok bool
		op.Payload, ok = pv.Interface().(plan.OpPayload)
		if !ok {
			t.Fatalf("plan.OpPayloadExamples()[%q] = %T does not implement plan.OpPayload", kind, example)
		}
		record(op)
	}

	for path := range seen {
		if _, ok := opFieldClasses[path]; !ok {
			t.Errorf("plan.Op field %q is not classified in opFieldClasses", path)
		}
	}
	for path := range opFieldClasses {
		if !seen[path] {
			t.Errorf("opFieldClasses entry %q matches no plan.Op field", path)
		}
	}
}

// The walker refuses kinds it cannot see strings in instead of skipping
// them silently.
func TestWalkerPanicsOnUnhandledKinds(t *testing.T) {
	for _, v := range []any{&struct{ X any }{X: "s"}, &struct{ X [1]string }{}, &struct{ X map[string]int }{}, &struct{ X []byte }{}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("walkValue(%T) did not panic", v)
				}
			}()
			walkValue(reflect.ValueOf(v).Elem(), "", func(_, s string) string { return s })
		}()
	}
}

// TestRedactedPreviewIsValidJSONL pins that redaction works on decoded
// values: a secret that contains JSON syntax cannot break a preview line.
func TestRedactedPreviewIsValidJSONL(t *testing.T) {
	const tricky = `fake","x":"y-secret\"` // quotes and a backslash
	ops, err := recordWithSecret(t, tricky, func() {
		key := MustSecret("svc/key")
		Command("/bin/echo", List(key), options.WithName("echo"), options.WithEnv(map[string]string{"K": key}))
		File("/etc/t", options.WithContent("{{.K}}"), options.WithTemplateData(map[string]any{"K": []string{key}}))
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := EncodeRedactedPreview(ops)
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(preview)), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("preview line %d is not JSON (%v): %s", i+1, err, line)
		}
		if strings.Contains(line, "y-secret") {
			t.Fatalf("preview line %d leaks the secret: %s", i+1, line)
		}
	}
}

// coverageCase is one recipe placing a strong secret into one plan.Op field.
type coverageCase struct {
	name   string
	field  string // "" when the op is only marked, never refused
	record func(key string)
}

// coverageCases places a strong secret into every kind of field the round-2
// review found unscanned.
var coverageCases = []coverageCase{
	{"command dir", "dir", func(k string) { Command("/bin/true", nil, options.WithName("c"), options.WithDir("/srv/"+k)) }},
	{"command creates", "creates", func(k string) { Command("/bin/true", nil, options.WithName("c"), options.Creates("/srv/"+k)) }},
	{"guard bin", "unless.bin", func(k string) { Command("/bin/true", nil, options.WithName("c"), options.Unless("/opt/"+k, nil)) }},
	{"env key", "", func(k string) {
		Command("/bin/true", nil, options.WithName("c"), options.WithEnv(map[string]string{"K_" + k: "v"}))
	}},
	{"user home", "home", func(k string) { User("svc", options.WithHome("/home/"+k)) }},
	// A when block's id embeds its predicate, so the id is refused first.
	{"when path", "id", func(k string) { WhenPathExists("/etc/"+k, func() {}) }},
	{"validation bin", "validation_bin", func(k string) {
		File("/etc/v", options.WithContent("x"), options.WithValidation("/opt/"+k, []string{options.CandidatePath}))
	}},
	{"command bin", "bin", func(k string) { Command("/opt/"+k, nil, options.WithName("c")) }},
	{"cron schedule", "", func(k string) {
		Cron("job", options.WithCommand("/bin/true"), options.WithMinute(k))
	}},
	{"timer after", "after[]", func(k string) {
		SystemdTimer("t", options.WithCommand("/bin/true"), options.WithOnCalendar("daily"), options.WithAfter(k+".service"))
	}},
	// Groups are metadata: a strong secret there marks the op, never refuses.
	{"user group", "", func(k string) { User("svc", options.WithSupplementaryGroups(k)) }},
}

// TestEveryFieldKindIsCovered records each coverage case with a strong
// secret: identity fields refuse the record naming the field, metadata and
// payload fields mark the op.
func TestEveryFieldKindIsCovered(t *testing.T) {
	for _, tc := range coverageCases {
		ops, err := recordWithSecret(t, fakePlanSecret, func() { tc.record(MustSecret("svc/key")) })
		if tc.field != "" {
			if err == nil || !strings.Contains(err.Error(), "its "+tc.field+" holds a resolved secret value") {
				t.Errorf("%s: err = %v, want the %s refusal", tc.name, err, tc.field)
			} else {
				requireNoSecret(t, tc.name, []byte(err.Error()))
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if len(plan.SensitiveIDs(ops)) == 0 {
			t.Errorf("%s: no op marked sensitive", tc.name)
		}
	}
}

// Low-entropy secrets (below secret.MinStrongLen) never refuse an identity:
// the op is marked sensitive and its printed name redacted instead.
func TestShortSecretInIdentityMarksInsteadOfRefusing(t *testing.T) {
	ops, err := recordWithSecret(t, "paul\n", func() {
		_ = MustSecret("svc/key")
		File("/home/paul/.bashrc", options.WithContent("x"))
		User("git", options.WithHome("/home/git"))
	})
	if err != nil {
		t.Fatalf("a short secret in a path must not refuse: %v", err)
	}
	if !opByPath(t, ops, "/home/paul/.bashrc").Sensitive {
		t.Fatal("the path holding the short secret must mark the op")
	}
	names := SensitiveOpNames(ops)
	if !slices.Equal(names, []string{"File[/home/[redacted]/.bashrc]"}) {
		t.Fatalf("SensitiveOpNames = %q", names)
	}

	ops, err = recordWithSecret(t, "git", func() {
		_ = MustSecret("svc/key")
		User("git", options.WithHome("/home/git"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if names := SensitiveOpNames(ops); !slices.Equal(names, []string{"User[[redacted]]"}) {
		t.Fatalf("an exact short secret must be redacted inside the name: %q", names)
	}
}

// A word-like secret ("postgres", 8 letters) is weak: resolving it does not
// break User("postgres") or WithOwner("postgres"), and metadata never
// refuses even a strong secret — it marks the op instead.
func TestWordLikeAndMetadataSecretsNeverRefuse(t *testing.T) {
	ops, err := recordWithSecret(t, "postgres\n", func() {
		_ = MustSecret("svc/key")
		User("postgres", options.WithHome("/var/lib/postgres"))
		File("/var/lib/postgres/x", options.WithContent("x"), options.WithOwner("postgres"))
	})
	if err != nil {
		t.Fatalf("a word-like secret must not refuse identities: %v", err)
	}
	if !opByPath(t, ops, "/var/lib/postgres/x").Sensitive {
		t.Fatal("the path holding the weak secret must still mark the op")
	}
	ops, err = recordWithSecret(t, fakePlanSecret, func() {
		File("/etc/owned", options.WithContent("x"), options.WithOwner(MustSecret("svc/key")))
	})
	if err != nil {
		t.Fatalf("a strong secret in metadata must not refuse: %v", err)
	}
	if !opByPath(t, ops, "/etc/owned").Sensitive {
		t.Fatal("a strong secret in metadata must mark the op")
	}
	preview, err := EncodeRedactedPreview(ops)
	if err != nil {
		t.Fatal(err)
	}
	requireNoSecret(t, "metadata preview", preview)
}

// In the preview a weak secret equal to metadata (the op kind "file", an
// owner) is left alone, while payload and identity values are redacted.
func TestPreviewLeavesWeakMetadataMatches(t *testing.T) {
	ops, err := recordWithSecret(t, "file", func() {
		Command("/bin/echo", List(MustSecret("svc/key")), options.WithName("echo"))
		File("/etc/x", options.WithContent("x"), options.WithOwner("file"))
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := EncodeRedactedPreview(ops)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"op":"file"`, `"owner":"file"`, `"args":["[redacted]"]`} {
		if !strings.Contains(string(preview), want) {
			t.Errorf("preview lacks %s:\n%s", want, preview)
		}
	}
}

// Every controller-side log line passes through the secret registry: the
// -verbose registration line of an unnamed Command quotes its argv, and a
// resolved secret there is redacted before it is written.
func TestLogLinesAreRedacted(t *testing.T) {
	output := testutil.CaptureLog(t, logger.LevelDebug)
	_, err := recordWithSecret(t, fakePlanSecret, func() {
		Command("/usr/bin/curl", List("-H", "Authorization: "+MustSecret("svc/key")))
	})
	if err == nil {
		t.Fatal("test premise: the unnamed secret command is refused")
	}
	if !strings.Contains(output(), "Registered resource") {
		t.Fatalf("test premise: the registration debug line was logged:\n%s", output())
	}
	requireNoSecret(t, "debug log", []byte(output()))
}

// The review probe: an unnamed Command with a weak (6-byte) secret argument
// is not refused, so its ID holds the secret; running it locally must not
// print the secret in the log lines or the apply summary.
func TestRunSummaryAndLogsRedactWeakSecretInID(t *testing.T) {
	output := testutil.CaptureLog(t, logger.LevelDebug)
	ResetForTest()
	t.Cleanup(ResetForTest)
	useSecretWorkDir(t)
	writeSecret(t, "svc/pin", "tok123\n")
	Task("t", "", func() { Command("/bin/true", List(strings.TrimSpace(MustSecret("svc/pin")))) })
	var runErr error
	stderr := testutil.CaptureStderr(t, func() { runErr = Run("t") })
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if !strings.Contains(stderr, "changed Command[") {
		t.Fatalf("test premise: the summary names the command:\n%s", stderr)
	}
	for what, text := range map[string]string{"summary": stderr, "log": output()} {
		if strings.Contains(text, "tok123") {
			t.Fatalf("%s leaks the secret:\n%s", what, text)
		}
	}
}

// Control ops (when blocks) are recorded directly and scanned too.
func TestControlOpsAreScanned(t *testing.T) {
	_, err := recordWithSecret(t, fakePlanSecret, func() {
		WhenHostname(MustSecret("svc/key"), func() { File("/etc/x", options.WithContent("x")) })
	})
	if err == nil || !strings.Contains(err.Error(), "plan line") || !strings.Contains(err.Error(), "holds a resolved secret value") {
		t.Fatalf("err = %v, want a control-op refusal", err)
	}
	requireNoSecret(t, "control-op refusal", []byte(err.Error()))
}
