package plan

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// requirePlan is: an unconditional file write, then a requirement block
// (goos == openbsd) guarding a second file write.
func requirePlan(dir string) []Op {
	return []Op{
		{Op: KindPlan, Version: CurrentVersion},
		{Op: KindFile, ID: "File[before]", Path: filepath.Join(dir, "before"), ContentB64: "eAo=", Mode: "0600"},
		{Op: KindWhenBegin, ID: "when.require_goos:openbsd:X", All: []Predicate{{Fact: "goos", Eq: "openbsd"}}, Require: "X needs OpenBSD"},
		{Op: KindFile, ID: "File[inside]", Path: filepath.Join(dir, "inside"), ContentB64: "eAo=", Mode: "0600"},
		{Op: KindWhenEnd},
	}
}

// TestWhenRequireVersionPinned fails loudly if a merge loses the v20 bump:
// an older destination would silently skip a requirement block.
func TestWhenRequireVersionPinned(t *testing.T) {
	if VersionWhenRequire != 20 || CurrentVersion < VersionWhenRequire {
		t.Fatalf("VersionWhenRequire = %d, CurrentVersion = %d", VersionWhenRequire, CurrentVersion)
	}
	for v := 1; v <= CurrentVersion; v++ {
		if !SupportsVersion(v) {
			t.Fatalf("SupportsVersion(%d) = false; every schema up to %d must stay applicable", v, CurrentVersion)
		}
	}
}

func TestApplyRequirementRefusesBeforeAnyMutation(t *testing.T) {
	for _, dry := range []bool{false, true} {
		dir := t.TempDir()
		resource.SetDryRun(dry)
		err := Apply(requirePlan(dir), Facts{GOOS: "freebsd"}, "")
		resource.SetDryRun(false)
		if err == nil || !strings.Contains(err.Error(), "goos=freebsd") ||
			!strings.Contains(err.Error(), "X needs OpenBSD") || !strings.Contains(err.Error(), "nothing was applied") {
			t.Fatalf("dry=%v: err = %v, want requirement refusal naming goos=freebsd", dry, err)
		}
		for _, name := range []string{"before", "inside"} {
			if _, statErr := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(statErr) {
				t.Fatalf("dry=%v: %s written despite refusal", dry, name)
			}
		}
	}
}

func TestApplyRequirementMetAppliesBody(t *testing.T) {
	dir := t.TempDir()
	if err := Apply(requirePlan(dir), Facts{GOOS: "openbsd"}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "inside")); err != nil {
		t.Fatalf("guarded body not applied: %v", err)
	}
}

func TestApplyRequirementInsideInactiveBlockIsIgnored(t *testing.T) {
	dir := t.TempDir()
	body := requirePlan(dir)
	ops := append([]Op{body[0],
		{Op: KindWhenBegin, ID: "when.hostname:elsewhere", All: []Predicate{{Fact: "hostname_contains", Eq: "elsewhere"}}}},
		append(body[1:], Op{Op: KindWhenEnd})...)
	if err := Apply(ops, Facts{GOOS: "linux", Hostname: "here"}, ""); err != nil {
		t.Fatalf("requirement in an inactive scope refused: %v", err)
	}
}

// badScopeCase is a hand-written plan whose requirement breaks the
// host-fact scope rule, preceded by a file op (marker) that must not be
// written, plus the refusal text naming the requirement and condition.
type badScopeCase struct {
	name, want string
	ops        []Op
}

func badScopeCases(dir string) []badScopeCase {
	marker := filepath.Join(dir, "marker")
	write := Op{Op: KindFile, ID: "File[marker]", Path: marker, ContentB64: "eAo=", Mode: "0600"}
	goos := []Predicate{{Fact: "goos", Eq: "openbsd"}}
	header := Op{Op: KindPlan, Version: CurrentVersion}
	return []badScopeCase{{
		// The enclosing path_exists would turn true only after the earlier
		// op ran: exactly the mid-apply flip the rule forbids.
		name: "nested-path-exists",
		want: "requirement req is nested under when.path_exists:marker (path_exists " + marker + ")",
		ops: []Op{header, write,
			{Op: KindWhenBegin, ID: "when.path_exists:marker", All: []Predicate{{PathExists: marker}}},
			{Op: KindWhenBegin, ID: "req", All: goos, Require: "needs OpenBSD"},
			{Op: KindWhenEnd}, {Op: KindWhenEnd}},
	}, {
		name: "own-path-exists",
		want: "requirement req itself uses (path_exists " + marker + ")",
		ops: []Op{header, write,
			{Op: KindWhenBegin, ID: "req", All: []Predicate{{PathExists: marker}}, Require: "needs marker"},
			{Op: KindWhenEnd}},
	}, {
		name: "nested-unknown-fact",
		want: `requirement req is nested under odd (fact "uid")`,
		ops: []Op{header, write,
			{Op: KindWhenBegin, ID: "odd", All: []Predicate{{Fact: "uid", Eq: "0"}}},
			{Op: KindWhenBegin, ID: "req", All: goos, Require: "needs OpenBSD"},
			{Op: KindWhenEnd}, {Op: KindWhenEnd}},
	}}
}

// TestRequirementScopeRefusedAtRecordAndApply: a requirement whose scope
// uses a non-host-fact condition is refused by ValidateChunks (the record-
// time pre-flight) and by Apply's pre-check, in apply and dry run alike, on
// a host where it would hold as well as one where it would not, before the
// earlier file op is written.
func TestRequirementScopeRefusedAtRecordAndApply(t *testing.T) {
	for i := range badScopeCases(t.TempDir()) {
		for _, goos := range []string{"openbsd", "freebsd"} {
			for _, dry := range []bool{false, true} {
				dir := t.TempDir()
				tc := badScopeCases(dir)[i]
				err := ValidateChunks(SplitPrivilegeChunks(tc.ops))
				var refused Refusal
				if err == nil || !errors.As(err, &refused) || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("%s: ValidateChunks = %v, want a Refusal containing %q", tc.name, err, tc.want)
				}
				resource.SetDryRun(dry)
				err = Apply(tc.ops, Facts{GOOS: goos}, "")
				resource.SetDryRun(false)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("%s goos=%s dry=%v: Apply = %v, want %q", tc.name, goos, dry, err, tc.want)
				}
				if _, statErr := os.Stat(filepath.Join(dir, "marker")); !os.IsNotExist(statErr) {
					t.Fatalf("%s goos=%s dry=%v: marker written despite refusal", tc.name, goos, dry)
				}
			}
		}
	}
}

// TestSplitDoesNotCopyBadScopeRequirements: a requirement under path_exists
// in a later chunk is not copied into the earlier chunk (the copy would not
// evaluate like the original); ValidateChunks refuses the plan instead.
func TestSplitDoesNotCopyBadScopeRequirements(t *testing.T) {
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion},
		{Op: KindFile, ID: "File[user]", Path: "/tmp/x", ContentB64: "eAo="},
		{Op: KindWhenBegin, ID: "when.path_exists:/p", All: []Predicate{{PathExists: "/p"}}},
		{Op: KindWhenBegin, ID: "req", All: []Predicate{{Fact: "goos", Eq: "openbsd"}}, Require: "r"},
		{Op: KindFile, ID: "File[root]", Path: "/tmp/y", ContentB64: "eAo=", Elevate: true},
		{Op: KindWhenEnd}, {Op: KindWhenEnd},
	}
	chunks := SplitPrivilegeChunks(ops)
	if len(chunks) != 2 || len(chunks[0].Ops) != 2 {
		t.Fatalf("first chunk = %+v, want header + user op only", chunks[0].Ops)
	}
	if err := ValidateChunks(chunks); err == nil || !strings.Contains(err.Error(), "nested under when.path_exists:/p") {
		t.Fatalf("ValidateChunks = %v, want the scope refusal", err)
	}
}

// TestSplitHoistsRequirementsIntoEarlierChunks: a user-level op followed by
// an elevated requirement block splits into two chunks; the first chunk must
// carry the requirement stub so it refuses before its own mutation.
func TestSplitHoistsRequirementsIntoEarlierChunks(t *testing.T) {
	dir := t.TempDir()
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion},
		{Op: KindFile, ID: "File[user]", Path: filepath.Join(dir, "user"), ContentB64: "eAo=", Mode: "0600"},
		{Op: KindWhenBegin, ID: "when.hostname:h", All: []Predicate{{Fact: "hostname_contains", Eq: "h"}}},
		{Op: KindWhenBegin, ID: "req", All: []Predicate{{Fact: "goos", Eq: "openbsd"}}, Require: "needs OpenBSD"},
		{Op: KindFile, ID: "File[root]", Path: filepath.Join(dir, "root"), ContentB64: "eAo=", Mode: "0600", Elevate: true},
		{Op: KindWhenEnd},
		{Op: KindWhenEnd},
	}
	chunks := SplitPrivilegeChunks(ops)
	if len(chunks) != 2 || chunks[0].Elevate || !chunks[1].Elevate {
		t.Fatalf("chunks = %+v", chunks)
	}
	var kinds []string
	for _, op := range chunks[0].Ops {
		kinds = append(kinds, string(op.Op)+":"+op.ID)
	}
	want := "plan:chunk,when_begin:when.hostname:h,when_begin:req,when_end:,when_end:,file:File[user]"
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("first chunk = %s, want %s", got, want)
	}
	if err := ValidateChunks(chunks); err != nil {
		t.Fatalf("ValidateChunks: %v", err)
	}
	err := Apply(chunks[0].Ops, Facts{GOOS: "freebsd", Hostname: "h1"}, "")
	if err == nil || !strings.Contains(err.Error(), "goos=freebsd") {
		t.Fatalf("first chunk on freebsd = %v, want refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "user")); !os.IsNotExist(statErr) {
		t.Fatal("user chunk mutated before the requirement refused")
	}
	// Another host (inactive enclosing block) and OpenBSD both pass.
	if err := Apply(chunks[0].Ops, Facts{GOOS: "freebsd", Hostname: "zz"}, ""); err != nil {
		t.Fatalf("inactive host: %v", err)
	}
	if err := Apply(chunks[0].Ops, Facts{GOOS: "openbsd", Hostname: "h1"}, ""); err != nil {
		t.Fatalf("openbsd: %v", err)
	}
	// Plans without requirements split exactly as before.
	plain := append([]Op(nil), ops...)
	plain[3].Require = ""
	if got := SplitPrivilegeChunks(plain); len(got[0].Ops) != 2 {
		t.Fatalf("requirement-free first chunk = %+v, want header + user op", got[0].Ops)
	}
}

// TestSplitHoistsRequirementIntoEveryEarlierChunk: user → elevated → user
// with a requirement gives three chunks; the stub must reach BOTH earlier
// chunks (a hoist that only copied into the previous chunk would let chunk 0
// mutate first), and the last chunk keeps the original block.
func TestSplitHoistsRequirementIntoEveryEarlierChunk(t *testing.T) {
	req := Op{Op: KindWhenBegin, ID: "req", All: []Predicate{{Fact: "goos", Eq: "openbsd"}}, Require: "needs OpenBSD"}
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion},
		{Op: KindFile, ID: "File[u1]", Path: "/tmp/u1", ContentB64: "eAo="},
		{Op: KindFile, ID: "File[root]", Path: "/tmp/root", ContentB64: "eAo=", Elevate: true},
		req,
		{Op: KindFile, ID: "File[u2]", Path: "/tmp/u2", ContentB64: "eAo="},
		{Op: KindWhenEnd},
	}
	chunks := SplitPrivilegeChunks(ops)
	if len(chunks) != 3 || chunks[0].Elevate || !chunks[1].Elevate || chunks[2].Elevate {
		t.Fatalf("chunks = %+v, want user, elevated, user", chunks)
	}
	ids := func(c Chunk) string {
		var out []string
		for _, op := range c.Ops {
			out = append(out, string(op.Op)+":"+op.ID)
		}
		return strings.Join(out, ",")
	}
	for i, want := range []string{
		"plan:chunk,when_begin:req,when_end:,file:File[u1]",
		"plan:chunk,when_begin:req,when_end:,file:File[root]",
		"plan:chunk,when_begin:req,file:File[u2],when_end:",
	} {
		if got := ids(chunks[i]); got != want {
			t.Fatalf("chunk %d = %s, want %s", i, got, want)
		}
	}
	for i := range chunks {
		if err := Apply(chunks[i].Ops, Facts{GOOS: "linux"}, ""); err == nil || !strings.Contains(err.Error(), "goos=linux") {
			t.Fatalf("chunk %d on linux = %v, want the requirement refusal", i, err)
		}
	}
}

func TestPlansWithoutRequirementsSkipTheWalk(t *testing.T) {
	// A predicate error in a plan without requirements must surface from
	// applyLine as before, not from the requirement pre-flight.
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion},
		{Op: KindWhenBegin, ID: "bad", All: []Predicate{{Fact: "nope", Eq: "x"}}},
		{Op: KindWhenEnd},
	}
	if err := checkRequirements([]planLine{{op: ops[1], line: 2}, {op: ops[2], line: 3}}, Facts{}); err != nil {
		t.Fatalf("checkRequirements on a requirement-free plan = %v, want nil", err)
	}
}
