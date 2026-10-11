package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestWithShellVarsPlanMatchesSingles(t *testing.T) {
	requireSameOps(t,
		func() { File("/tmp/s/rc.conf", WithShellVar("lodge", `a "b" $c\d`+"`"), WithShellVar("kern.x", "YES")) },
		func() { File("/tmp/s/rc.conf", WithShellVars("lodge", `a "b" $c\d`+"`", "kern.x", "YES")) })
}

func TestWithShellVarsRefusesDeclaration(t *testing.T) {
	cases := []struct {
		name  string
		kv    []string
		extra []FileOption
		want  string
	}{
		{name: "odd", kv: List("a", "1", "b"), want: "odd number of elements"},
		{name: "invalid key", kv: List("a", "1", "1bad", "2"), want: "not a shell variable name"},
		{name: "conflicting pair", kv: List("a", "1", "a", "2"), want: "two different lines"},
		{name: "conflicting singular", kv: List("a", "1"), extra: []FileOption{WithShellVar("a", "2")}, want: "two different lines"},
		{name: "multiline value", kv: List("a", "1\n2"), want: "line break"},
		{name: "whole content", kv: List("a", "1"), extra: []FileOption{WithContent("x")}, want: "cannot be combined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			path := filepath.Join(t.TempDir(), "rc.conf")
			File(path, append([]FileOption{WithShellVars(tc.kv...)}, tc.extra...)...)
			if err := declerr.First(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("declaration error = %v, want %q", err, tc.want)
			}
			if len(resource.RegisteredIDs()) != 0 {
				t.Fatal("refused resource registered")
			}
			if err := Apply(); err == nil {
				t.Fatal("Apply accepted declaration error")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("refused resource wrote file: %v", err)
			}
		})
	}
}

func TestWithShellVarsSnapshotReuseAndRoundTrip(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	kv := List("lodge", `a "b" $c\d`+"`", "kern.x", "YES", "kern.x", "YES")
	option := WithShellVars(kv...)
	kv[0] = "1bad"
	kv[1] = "changed"
	if err := os.WriteFile(first, []byte("# keep\nlodge=old\nlodge=duplicate\nother=mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Task("shellvars", "", func() {
		parent := Dir(filepath.Join(dir, "parent"), WithMode(0o755))
		File(first, option, WithShellVars(), WithMode(0o644), DependsOn(parent))
		File(second, option, WithMode(0o644), DependsOn(parent))
	})
	ops, err := RecordPlan("shellvars", "", "shellvars")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, op := range decoded {
		if op.Op == "file" {
			files++
			if len(op.Deps) != 1 || op.Deps[0] != "Directory["+filepath.Join(dir, "parent")+"]" {
				t.Fatalf("file dependencies = %v", op.Deps)
			}
		}
	}
	if files != 2 {
		t.Fatalf("decoded %d file operations, want 2", files)
	}

	assignment := "lodge=\"a \\\"b\\\" \\$c\\\\d\\`\"\nkern.x=\"YES\"\n"
	for run := 0; run < 2; run++ {
		if err := ApplyPlan(decoded, ""); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{first, second} {
			got, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			want := assignment
			if p == first {
				want = "# keep\n" + strings.Split(assignment, "\n")[0] + "\nother=mine\nkern.x=\"YES\"\n"
			}
			if string(got) != want {
				t.Fatalf("run %d %s: got %q, want %q", run, p, got, want)
			}
		}
		if run == 1 && resource.AnyChanged("File["+first+"]", "File["+second+"]") {
			t.Fatal("second apply changed files")
		}
	}
}
