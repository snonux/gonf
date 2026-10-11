package api

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestWhenNotOSRefusal(t *testing.T) {
	for _, values := range [][]string{nil, {""}, {"Linux"}, {"linux", "windows"}} {
		t.Run("invalid", func(t *testing.T) {
			requireDeclErr(t, "WhenNotOS:", func() { Task("invalid", "", func() {}, WhenNotOS(values...)) })
			if len(Matching("invalid")) != 0 {
				t.Fatal("refused task activated")
			}
		})
	}
}

func TestWhenNotOSRemoteRoundTripAndDependencies(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	dir := filepath.Join(t.TempDir(), "gonfy")
	path := filepath.Join(dir, "gitconfig")
	var directory Resource
	Task("base", "", func() { directory = Dir(dir) })
	// Foreign controller must still list, alias and aggregate this member.
	Task("home_git", "", func() {
		File(path, WithContent("gonfy\n"), DependsOn(directory))
	}, WhenNotOS("darwin"), WhenOS("linux", "openbsd"), WhenProfile("lodge"), WhenHostnameContains("gonfy"), Needs("base"))
	Alias("git", "", "home_git")
	Aggregate("home", "", "^home_")
	Activate(Facts{GOOS: "darwin", Profile: "lodge", Hostname: "gonfy"})
	if got := Matching("^home_"); !reflect.DeepEqual(got, []string{"home_git"}) {
		t.Fatal(got)
	}
	for _, info := range Tasks() {
		if (info.Name == "home_git" || info.Name == "git") && info.DestinationGuard != "goos!=darwin && goos=linux|openbsd && profile=lodge && hostname_contains=gonfy" {
			t.Fatal(info)
		}
	}
	ops, err := RecordPlan("remote", "", "home")
	if err != nil {
		t.Fatal(err)
	}
	if ops[0].Version != plan.VersionWhenNotOS {
		t.Fatal(ops[0])
	}
	data, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	ops, err = plan.DecodePlanBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, facts := range []plan.Facts{
		{GOOS: "darwin", Profile: "lodge", Hostname: "gonfy"},
		{GOOS: "freebsd", Profile: "lodge", Hostname: "gonfy"},
		{GOOS: "linux", Profile: "other", Hostname: "gonfy"},
		{GOOS: "linux", Profile: "lodge", Hostname: "other"},
	} {
		if err := plan.Apply(ops, facts, ""); err != nil {
			t.Fatal(err)
		}
		if exists(path) {
			t.Fatalf("excluded destination wrote file: %+v", facts)
		}
	}
	facts := plan.Facts{GOOS: "linux", Profile: "lodge", Hostname: "gonfy"}
	if err := plan.Apply(ops, facts, ""); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "gonfy\n" {
		t.Fatalf("file %q: %v", data, err)
	}
	resource.ResetReport()
	if err := plan.Apply(ops, facts, ""); err != nil {
		t.Fatal(err)
	}
	if resource.AnyChanged("File[" + path + "]") {
		t.Fatal("second apply changed file")
	}
}

func TestWhenNotOSCopiesCallerValues(t *testing.T) {
	values := []string{"linux", "darwin"}
	opt := WhenNotOS(values...)
	values[0] = "windows"
	var c taskCandidate
	opt(&c)
	if !reflect.DeepEqual(c.planWhen, []plan.Predicate{{Fact: "goos", In: []string{"linux", "darwin"}, Not: true}}) {
		t.Fatal(c.planWhen)
	}
}

func TestWhenNotOSMalformedPlanRefusesBeforeMutation(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	path := filepath.Join(t.TempDir(), "must-not-exist")
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.VersionWhenNotOS},
		{Op: plan.KindFile, ID: "File[" + path + "]", Path: path, Payload: plan.FilePayload{HasContent: true, ContentB64: "Z29uZnk="}},
		{Op: plan.KindWhenBegin, All: []plan.Predicate{{Fact: "goos", Not: true}}},
		{Op: plan.KindWhenEnd},
	}
	for _, dry := range []bool{false, true} {
		resource.SetDryRun(dry)
		if err := plan.Apply(ops, plan.Facts{GOOS: "linux"}, ""); err == nil {
			t.Fatal("malformed predicate accepted")
		}
		if exists(path) {
			t.Fatal("preceding file mutated")
		}
	}
}

func TestWhenNotOSLocalAggregateSelection(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("home_git", "", func() { File("/tmp/gonfy-never-applied-not-os") }, WhenNotOS("darwin"))
	Task("home_base", "", func() {})
	Aggregate("home", "", "^home_")
	defer setLocalDestination(Facts{GOOS: "darwin"})()
	ops, err := RecordPlan("local", "", "home")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.Op == plan.KindFile {
			t.Fatal("excluded local aggregate member recorded")
		}
	}
}
