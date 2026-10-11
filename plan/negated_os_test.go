package plan

import "testing"

func TestNegatedOSMalformed(t *testing.T) {
	for _, p := range []Predicate{
		{Not: true}, {Fact: "goos", Not: true}, {Fact: "goos", Eq: "windows", Not: true},
		{Fact: "goos", In: []string{"darwin", ""}, Not: true},
		{Fact: "goos", Eq: "linux", In: []string{"darwin"}, Not: true},
		{Fact: "profile", Eq: "lodge", Not: true},
		{Fact: "goos", Eq: "darwin", PathExists: "/tmp", Not: true},
	} {
		if ok, err := EvalPredicates([]Predicate{p}, Facts{GOOS: "linux"}); err == nil || ok {
			t.Fatalf("evaluation accepted %+v: %v", p, err)
		}
		// Inactive enclosing scopes must not hide malformed negation.
		ops := []Op{header(), {Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "darwin"}}}, {Op: KindWhenBegin, All: []Predicate{p}}, {Op: KindWhenEnd}, {Op: KindWhenEnd}}
		if err := ValidateChunks([]Chunk{{Ops: ops}}); err == nil {
			t.Fatalf("preflight accepted %+v", p)
		}
		if err := Apply(ops, Facts{GOOS: "linux"}, ""); err == nil {
			t.Fatalf("apply accepted %+v", p)
		}
	}
	for _, goos := range []string{"", "windows", "Linux"} {
		if ok, err := EvalPredicates([]Predicate{{Fact: "goos", Eq: "darwin", Not: true}}, Facts{GOOS: goos}); ok || err == nil {
			t.Fatalf("undefined destination %q: %v, %v", goos, ok, err)
		}
	}
}

func TestNegatedOSSchemaFloor(t *testing.T) {
	op := Op{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "darwin", Not: true}}}
	if RequiredVersion([]Op{op}) != 31 {
		t.Fatal("negation needs schema31")
	}
	op.All[0].Not = false
	if RequiredVersion([]Op{op}) != VersionConfigSet {
		t.Fatal("positive guard raised floor")
	}
	for version := 1; version <= 31; version++ {
		if !SupportsVersion(version) {
			t.Fatalf("lost schema%d", version)
		}
	}
	// Simulate an older destination's header refusal before interpreting ops.
	delete(supportedVersions, VersionWhenNotOS)
	t.Cleanup(func() { supportedVersions[VersionWhenNotOS] = struct{}{} })
	if err := ValidateHeader(Op{Op: KindPlan, Version: 31}); err == nil {
		t.Fatal("old destination accepted schema31")
	}
}
