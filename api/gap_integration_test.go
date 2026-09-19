package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
)

// TestGapFeatureSetRecordsAndAppliesTogether is a consumer-shaped regression
// fixture for the features that unblock the remaining conf Rex migrations.
// It deliberately records package and user operations but applies only the
// filesystem/command projection: applying an account or package fixture on a
// developer or CI host would not be safe. The projection still exercises the
// real JSONL codec and the change-report path twice.
func TestGapFeatureSetRecordsAndAppliesTogether(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	useSecretWorkDir(t)

	const secret = "integration-token\n"
	writeSecret(t, "tokens/api", secret)

	base := t.TempDir()
	config := filepath.Join(base, "service.conf")
	lines := filepath.Join(base, "daily.local")
	preserved := filepath.Join(base, "preserved.conf")
	reloads := filepath.Join(base, "reloads")
	if err := os.WriteFile(lines, []byte("keep\nobsolete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preserved, []byte("retain these bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	Task("gap_feature_set", "combined migration fixture", func() {
		packageRes := Package("dtail",
			options.IsLatest,
			options.WithEnv(map[string]string{"PKG_PATH": "https://pkgrepo.example/openbsd/"}),
		)
		User("_gonf_integration",
			options.WithPrimaryGroup("_gonf_integration"),
			options.WithSupplementaryGroups("wheel", "audio"),
			options.WithHome("/var/empty/gonf-integration"),
			options.WithShell("/sbin/nologin"),
			options.DependsOn(packageRes),
		)

		managed := File(config,
			options.WithContent("token={{.Data.Token}}host={{.Gonf.Hostname}}\n"),
			options.WithTemplateData(map[string]any{"Token": MustSecret("tokens/api")}),
		)
		edited := File(lines,
			options.WithoutLines("obsolete"),
			options.WithLines("enabled=1", "enabled=1"),
		)
		EnsureFile(preserved, options.WithMode(0o644))
		Command("sh", []string{"-c", `printf 'reload\n' >> "$1"`, "gonf", reloads},
			options.WithName("reload-after-config"),
			options.OnChange(managed, edited),
		)
	})

	ops, err := RecordPlan("gap-feature-set", base, "gap_feature_set")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}
	if !reflect.DeepEqual(decoded, ops) {
		t.Fatalf("plan codec changed combined fixture:\n got: %#v\nwant: %#v", decoded, ops)
	}

	packageOp := findGapOp(t, decoded, plan.KindPackage)
	if !packageOp.Latest || packageOp.Env["PKG_PATH"] != "https://pkgrepo.example/openbsd/" {
		t.Fatalf("package op lost IsLatest or WithEnv: %#v", packageOp)
	}
	userOp := findGapOp(t, decoded, plan.KindUser)
	if userOp.Name != "_gonf_integration" || userOp.PrimaryGroup != "_gonf_integration" ||
		!reflect.DeepEqual(userOp.SupplementaryGroups, []string{"wheel", "audio"}) ||
		!reflect.DeepEqual(userOp.Deps, []string{packageOp.ID}) {
		t.Fatalf("user op lost creation data or package dependency: %#v", userOp)
	}
	configOp := findGapOp(t, decoded, plan.KindFile, "File["+config+"]")
	var templateData map[string]string
	if err := json.Unmarshal(configOp.TemplateData, &templateData); err != nil {
		t.Fatalf("decode template data: %v", err)
	}
	if templateData["Token"] != secret {
		t.Fatalf("template data did not preserve the secret bytes")
	}
	lineOp := findGapOp(t, decoded, plan.KindFile, "File["+lines+"]")
	if !reflect.DeepEqual(lineOp.RemoveLines, []string{"obsolete"}) ||
		!reflect.DeepEqual(lineOp.AddLines, []string{"enabled=1"}) {
		t.Fatalf("line edit op = %#v", lineOp)
	}
	ensureOp := findGapOp(t, decoded, plan.KindEnsureFile)
	if ensureOp.Path != preserved || ensureOp.Mode != "0644" {
		t.Fatalf("ensure_file op = %#v", ensureOp)
	}
	commandOp := findGapOp(t, decoded, plan.KindCommand)
	wantWatches := []string{configOp.ID, lineOp.ID}
	wantDeps := slices.Clone(wantWatches)
	slices.Sort(wantDeps)
	if !commandOp.IfChanged || !reflect.DeepEqual(commandOp.Watch, wantWatches) ||
		!reflect.DeepEqual(commandOp.Deps, wantDeps) {
		t.Fatalf("change-gated command = %#v, want watches %v and deps %v", commandOp, wantWatches, wantDeps)
	}

	// Keep account/package changes out of this end-to-end fixture while
	// retaining every operation that can change the temporary filesystem.
	safeOps := []plan.Op{decoded[0], configOp, lineOp, ensureOp, commandOp}
	facts := plan.Facts{GOOS: "openbsd", Hostname: "frontend-1", Profile: "production"}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := plan.Apply(safeOps, facts, base); err != nil {
			t.Fatalf("Apply attempt %d: %v", attempt, err)
		}
	}

	if got, err := os.ReadFile(config); err != nil || string(got) != "token="+secret+"host=frontend-1\n" {
		t.Fatalf("rendered config = %q, %v", got, err)
	}
	if got, err := os.ReadFile(lines); err != nil || string(got) != "keep\nenabled=1\n" {
		t.Fatalf("edited lines = %q, %v", got, err)
	}
	if got, err := os.ReadFile(preserved); err != nil || string(got) != "retain these bytes\n" {
		t.Fatalf("EnsureFile content = %q, %v", got, err)
	}
	info, err := os.Stat(preserved)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("EnsureFile mode = %o, want 0644", info.Mode().Perm())
	}
	if got, err := os.ReadFile(reloads); err != nil || string(got) != "reload\n" {
		t.Fatalf("change-gated command ran %q, %v; want once", got, err)
	}
}

func TestNamedFileLineEditsRemainDistinctAcrossTasksAndGateChanges(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	base := t.TempDir()
	rcLocal := filepath.Join(base, "rc.conf.local")
	reloads := filepath.Join(base, "reloads")
	var packageScripts Resource

	Task("rc_package_scripts", "set package startup scripts", func() {
		packageScripts = File(rcLocal,
			options.WithName("rc-conf-package-scripts"),
			options.WithLine(`pkg_scripts="dtail"`),
		)
	})
	Task("rc_httpd_flags", "set httpd startup flags", func() {
		httpdFlags := File(rcLocal,
			options.WithName("rc-conf-httpd-flags"),
			options.WithLine(`httpd_flags=""`),
			options.DependsOn(packageScripts),
		)
		Command("sh", []string{"-c", `printf 'reload\n' >> "$1"`, "gonf", reloads},
			options.WithName("reload-httpd-after-rc-flags"),
			options.OnChange(httpdFlags),
		)
	})

	ops, err := RecordPlan("named-rc-lines", base, "rc_package_scripts", "rc_httpd_flags")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	ops, err = plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("DecodePlan: %v", err)
	}
	packageID := "File[rc-conf-package-scripts]"
	httpdID := "File[rc-conf-httpd-flags]"
	packageOp := findGapOp(t, ops, plan.KindFile, packageID)
	if packageOp.Path != rcLocal || packageOp.Name != "rc-conf-package-scripts" {
		t.Fatalf("package line op = %#v", packageOp)
	}
	httpdOp := findGapOp(t, ops, plan.KindFile, httpdID)
	if httpdOp.Path != rcLocal || httpdOp.Name != "rc-conf-httpd-flags" ||
		!reflect.DeepEqual(httpdOp.Deps, []string{packageID}) {
		t.Fatalf("httpd line op = %#v", httpdOp)
	}
	commandOp := findGapOp(t, ops, plan.KindCommand)
	if !commandOp.IfChanged ||
		!reflect.DeepEqual(commandOp.Watch, []string{httpdID}) ||
		!reflect.DeepEqual(commandOp.Deps, []string{httpdID}) {
		t.Fatalf("change-gated command = %#v", commandOp)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := plan.Apply(ops, plan.Facts{}, base); err != nil {
			t.Fatalf("Apply attempt %d: %v", attempt, err)
		}
	}
	got, err := os.ReadFile(rcLocal)
	if err != nil {
		t.Fatal(err)
	}
	if want := "pkg_scripts=\"dtail\"\nhttpd_flags=\"\"\n"; string(got) != want {
		t.Fatalf("rc.conf.local = %q, want %q", got, want)
	}
	if got, err := os.ReadFile(reloads); err != nil || string(got) != "reload\n" {
		t.Fatalf("change gate reloads = %q, %v; want exactly one run", got, err)
	}
}

// findGapOp returns the unique operation of kind. With id supplied, it also
// pins which of the two file operations the combined fixture is asserting.
func findGapOp(t *testing.T, ops []plan.Op, kind plan.Kind, ids ...string) plan.Op {
	t.Helper()
	var found []plan.Op
	for _, op := range ops {
		if op.Op != kind || (len(ids) == 1 && op.ID != ids[0]) {
			continue
		}
		found = append(found, op)
	}
	if len(found) != 1 {
		t.Fatalf("%s operations matching %q = %#v, want one", kind, ids, found)
	}
	return found[0]
}
