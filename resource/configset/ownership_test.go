package configset

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// missingAccount is an owner and group name no test host defines. It stands
// for an account a recipe creates earlier in the same run (a User or Group
// resource), which does not exist yet while a dry-run previews the set.
const missingAccount = "gonf-a82-no-such-account"

// accountCases are the member attributes under test: [owner, group], where
// empty means "not set" (the applying user or group).
var accountCases = map[string][2]string{
	"missing owner": {missingAccount, ""},
	"missing group": {"", missingAccount},
}

// accountOptions returns the fixture set with the aliases member owned by
// owner and group.
func (f *fixture) accountOptions(owner, group string) []opt.ConfigSetOption {
	member := []opt.FileOption{opt.WithContent("root: paul\n"), opt.WithMode(0o644)}
	if owner != "" {
		member = append(member, opt.WithOwner(owner))
	}
	if group != "" {
		member = append(member, opt.WithGroup(group))
	}
	opts := f.options("root: paul\n")
	opts[0] = opt.ConfigFile("aliases", f.aliasesPath(), member...)
	return opts
}

// TestDryRunWithMissingAccountReportsWouldChange covers a82: a dry-run must
// not resolve member owners and groups, because the accounts may only be
// created by an earlier resource of the same run. It reports would-change
// like a File with the same owner does, and still writes nothing.
func TestDryRunWithMissingAccountReportsWouldChange(t *testing.T) {
	for name, acct := range accountCases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			resource.SetDryRun(true)
			t.Cleanup(func() { resource.SetDryRun(false) })
			resource.ResetReport()
			if err := Ensure("mail", f.accountOptions(acct[0], acct[1])...); err != nil {
				t.Fatalf("dry-run with a not-yet-existing account failed: %v", err)
			}
			if !outcomeOf(t, "aliases") || !resource.AnyChanged(setID("mail")) {
				t.Fatal("dry-run must report would-change for the set and its members")
			}
			mustNotExist(t, f.aliasesPath())
			mustNotExist(t, f.confPath())
			if runs := f.validatorRuns(); len(runs) != 0 {
				t.Fatalf("dry-run ran the validator: %q", runs)
			}
			f.noStagingLeft()
		})
	}
}

// TestPlanDryRunWithMissingAccount drives the destination-side path a remote
// (or strict) preview takes: the recorded draft is lowered to a plan op and
// applied by the handler under dry-run. The op carries the missing owner, and
// the preview still reports would-change instead of failing the lookup.
func TestPlanDryRunWithMissingAccount(t *testing.T) {
	f := newFixture(t)
	c, err := build("mail", f.accountOptions(missingAccount, missingAccount))
	if err != nil {
		t.Fatal(err)
	}
	op, err := (setHandler{}).ToOp(c.spec.planDraft(setID("mail"), nil))
	if err != nil {
		t.Fatalf("lowering a set owned by a not-yet-existing account: %v", err)
	}
	if op.Members[0].Owner != missingAccount || op.Members[0].Group != missingAccount {
		t.Fatalf("op member ownership = %q:%q, want the recorded account", op.Members[0].Owner, op.Members[0].Group)
	}
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	resource.ResetReport()
	if err := (setHandler{}).Apply(op, plan.ApplyContext{}); err != nil {
		t.Fatalf("plan dry-run with a not-yet-existing account failed: %v", err)
	}
	if !resource.AnyChanged(setID("mail")) {
		t.Fatal("plan dry-run must report would-change for the set")
	}
	mustNotExist(t, f.aliasesPath())
	f.noStagingLeft()
}

// TestApplyWithMissingAccountFailsBeforeAnyWrite is the negative side of
// a82: a real apply still resolves every owner and group before staging, so
// an account that really is missing fails clearly, names the member, and
// leaves no member published, no validator run and no staging directory.
func TestApplyWithMissingAccountFailsBeforeAnyWrite(t *testing.T) {
	for name, acct := range accountCases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			resource.ResetReport()
			err := Ensure("mail", f.accountOptions(acct[0], acct[1])...)
			if err == nil {
				t.Fatal("apply with a missing account must fail")
			}
			for _, want := range []string{"config set mail", "member aliases", missingAccount} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("apply error = %v, want it to mention %q", err, want)
				}
			}
			mustNotExist(t, f.aliasesPath())
			mustNotExist(t, f.confPath())
			if runs := f.validatorRuns(); len(runs) != 0 {
				t.Fatalf("validator ran despite the missing account: %q", runs)
			}
			f.noStagingLeft()
		})
	}
}
