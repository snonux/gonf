package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// configSetFixture records a mail-like task: a two-member set validated by a
// shell validator, newaliases gated on the aliases member only, a restart
// stand-in gated on the config member, and one gated on the whole set.
type configSetFixture struct {
	t                  *testing.T
	dir, log, failFlag string
	aliases, conf      string
}

func newConfigSetFixture(t *testing.T) *configSetFixture {
	t.Helper()
	root := t.TempDir()
	f := &configSetFixture{t: t, dir: filepath.Join(root, "mail"), log: filepath.Join(root, "gates.log"), failFlag: filepath.Join(root, "fail")}
	if err := os.Mkdir(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.aliases, f.conf = "root: paul\n", "listen on all\n"
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.ResetForTest()
		resource.ResetForTest()
	})
	return f
}

// record registers the task afresh and returns the encoded plan bytes.
func (f *configSetFixture) record() []byte {
	f.t.Helper()
	ResetTasks()
	resource.ResetForTest()
	Task("mail", "", func() {
		set := ConfigSet("mail",
			options.ConfigFile("aliases", filepath.Join(f.dir, "aliases"), options.WithContent(f.aliases), options.WithMode(0o644)),
			options.ConfigFile("smtpd.conf", filepath.Join(f.dir, "smtpd.conf"),
				options.WithContent(f.conf+"table aliases file:"+options.MemberPath("aliases")+"\n")),
			options.WithSetValidation("sh", []string{"-c", `grep -q "^table aliases file:$2$" "$1" && test ! -e "$3"`,
				"v", options.MemberPath("smtpd.conf"), options.MemberPath("aliases"), f.failFlag}))
		gate := func(name string, watch ...resource.Dependency) {
			Command("sh", List("-c", `echo "$1" >> "$2"`, "gate", name, f.log),
				options.OnChange(watch...), options.WithName(name))
		}
		gate("newaliases", set.Member("aliases"))
		gate("restart", set.Members("smtpd.conf")...)
		gate("any", set)
	})
	ops, err := RecordPlan("configset", "", "mail")
	if err != nil {
		f.t.Fatalf("RecordPlan: %v", err)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

// apply decodes raw (the wire round trip) and applies it, returning the gate
// names that fired.
func (f *configSetFixture) apply(raw []byte) ([]string, error) {
	f.t.Helper()
	_ = os.Remove(f.log)
	ops, err := plan.DecodePlanBytes(raw)
	if err != nil {
		f.t.Fatalf("DecodePlan: %v", err)
	}
	applyErr := plan.Apply(ops, plan.Facts{}, "")
	data, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil, applyErr
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Fields(string(data)), applyErr
}

func TestConfigSetPlanRoundTripAndMemberGates(t *testing.T) {
	f := newConfigSetFixture(t)
	raw := f.record()
	if !strings.Contains(string(raw), `"op":"config_set"`) || !strings.Contains(string(raw), `"id":"ConfigSetMember[mail/aliases]"`) {
		t.Fatalf("plan lacks the config_set op or member handles:\n%s", raw)
	}
	if !strings.Contains(string(raw), fmt.Sprintf(`"version":%d`, plan.VersionConfigSet)) {
		t.Fatalf("plan header must carry schema v%d (config_set; no sensitive op):\n%s", plan.VersionConfigSet, raw)
	}

	fired, err := f.apply(raw)
	if err != nil || strings.Join(fired, ",") != "newaliases,restart,any" {
		t.Fatalf("first apply fired %v (err %v), want every gate", fired, err)
	}
	if fired, err = f.apply(raw); err != nil || len(fired) != 0 {
		t.Fatalf("replay fired %v (err %v), want no gate", fired, err)
	}

	f.conf = "listen on lo0\n"
	fired, err = f.apply(f.record())
	if err != nil || strings.Join(fired, ",") != "restart,any" {
		t.Fatalf("config-only change fired %v (err %v), want restart and the aggregate, never newaliases", fired, err)
	}

	f.aliases = "root: paul\npostmaster: root\n"
	fired, err = f.apply(f.record())
	if err != nil || strings.Join(fired, ",") != "newaliases,any" {
		t.Fatalf("aliases-only change fired %v (err %v), want newaliases and the aggregate", fired, err)
	}
}

func TestConfigSetPlanValidationFailureHoldsGates(t *testing.T) {
	f := newConfigSetFixture(t)
	if _, err := f.apply(f.record()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.failFlag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.aliases = "broken\n"
	fired, err := f.apply(f.record())
	if err == nil || len(fired) != 0 {
		t.Fatalf("failed validation: fired %v, err %v; want an error and no gate", fired, err)
	}
	live, readErr := os.ReadFile(filepath.Join(f.dir, "aliases"))
	if readErr != nil || string(live) != "root: paul\n" {
		t.Fatalf("live aliases = %q (%v), want the previous content", live, readErr)
	}
}

// TestConfigSetWireRejectsMisconfiguredSets covers both ends of the wire:
// a malformed draft fails the record (draftToOp), and a crafted or stale
// config_set line fails destination apply before any file is written.
func TestConfigSetWireRejectsMisconfiguredSets(t *testing.T) {
	f := newConfigSetFixture(t)
	target := filepath.Join(f.dir, "x.conf")
	draft := resource.PlanDraft{
		Kind: "config_set", ID: "ConfigSet[x]", Name: "x",
		ConfigMembers: []resource.PlanConfigMember{{Key: "x", Path: "relative/x.conf", Content: []byte("x")}},
		Validators:    []resource.PlanArgv{{Bin: "true"}},
	}
	if _, err := newDraftPackager(nil).draftToOp(draft); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("draftToOp accepted a relative member path: %v", err)
	}
	crafted := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion},
		{Op: plan.KindConfigSet, ID: "ConfigSet[x]", Name: "x",
			Members: []plan.ConfigMember{{Key: "x", Path: target, ContentB64: "eAo="}}},
	}
	if err := plan.Apply(crafted, plan.Facts{}, ""); err == nil || !strings.Contains(err.Error(), "validator") {
		t.Fatalf("apply of a set without validators: %v", err)
	}
	orphan := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion},
		{Op: plan.KindConfigSetMember, ID: "ConfigSetMember[x/x]", Name: "x", Member: "x", Path: target},
	}
	if err := plan.Apply(orphan, plan.Facts{}, ""); err == nil {
		t.Fatal("a member handle whose set did not apply must fail rather than never firing")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("%s must not exist after refused applies (err %v)", target, err)
	}
}

// TestConfigSetInsideWhenBlocksAndPrivilegeChunks pins how the set and its
// member handles interact with the plan engine's control flow: they record
// inside the same when block (so a non-matching host skips set, handles and
// gates together, without the member handles failing), and a privileged
// set keeps its handles and gates in its own privilege chunk, which the
// chunk validation (deps, change gates, requirement scopes) accepts.
func TestConfigSetInsideWhenBlocksAndPrivilegeChunks(t *testing.T) {
	f := newConfigSetFixture(t)
	target := filepath.Join(f.dir, "app.conf")
	ResetTasks()
	resource.ResetForTest()
	Task("guarded", "", func() {
		WhenHostname("only-this-host", func() {
			set := ConfigSet("app", options.ConfigFile("conf", target, options.WithContent("x\n")),
				options.WithSetValidation("true", nil))
			Command("sh", List("-c", `echo fired >> "$1"`, "gate", f.log), options.OnChange(set.Member("conf")), options.WithName("gate"))
		})
	}, Privileged())
	ops, err := RecordPlan("guarded", "", "guarded")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	chunks := plan.SplitPrivilegeChunks(ops)
	if err := plan.ValidateChunks(chunks); err != nil {
		t.Fatalf("ValidateChunks: %v", err)
	}
	var elevated int
	for _, ch := range chunks {
		for _, op := range ch.Ops {
			if op.Op == plan.KindConfigSet || op.Op == plan.KindConfigSetMember {
				if !ch.Elevate {
					t.Fatalf("%s landed in an unprivileged chunk", op.ID)
				}
				elevated++
			}
		}
	}
	if elevated != 2 {
		t.Fatalf("found %d config-set ops in privileged chunks, want set + member", elevated)
	}

	if err := plan.Apply(ops, plan.Facts{Hostname: "other"}, ""); err != nil {
		t.Fatalf("apply on a non-matching host: %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("skipped block must not publish (err %v)", err)
	}
	if err := plan.Apply(ops, plan.Facts{Hostname: "only-this-host"}, ""); err != nil {
		t.Fatalf("apply on the matching host: %v", err)
	}
	if got, _ := os.ReadFile(f.log); string(got) != "fired\n" {
		t.Fatalf("gate log = %q, want one firing", got)
	}
}
