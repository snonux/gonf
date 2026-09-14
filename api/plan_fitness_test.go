package api

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// kindFitness pins one fixture per plan.Kind: every declared kind must lower
// from a resource draft (or be a header/control kind the recorder itself
// emits) AND survive an encode/decode round-trip. Adding a plan.Kind without
// a fixture, or a draftToOp case without a declared kind, fails this test —
// it is the executable form of the "Adding a resource kind" checklist in
// docs/plan.md.
type kindFixture struct {
	// draft is the resource draft lowering to this plan.Kind. Nil marks a
	// header/control kind that no resource emits.
	draft *resource.PlanDraft
	// op is the fixture op for control kinds; for draft kinds the op is
	// derived from the draft via draftToOp.
	op plan.Op
}

// controlKinds are the header/control kinds recorded by the recorder itself
// (planWhenForCandidate / FinishRecord), never by a resource draft.
var controlKinds = map[plan.Kind]bool{
	plan.KindPlan:      true,
	plan.KindWhenBegin: true,
	plan.KindWhenEnd:   true,
}

func kindFitnessTable() map[plan.Kind]kindFixture {
	b64 := base64.StdEncoding.EncodeToString([]byte("fit"))
	return map[plan.Kind]kindFixture{
		// Control kinds.
		plan.KindPlan: {op: plan.Op{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "fit"}},
		plan.KindWhenBegin: {op: plan.Op{
			Op:  plan.KindWhenBegin,
			ID:  "when.fit",
			All: []plan.Predicate{{Fact: "goos", Eq: "linux"}},
		}},
		plan.KindWhenEnd: {op: plan.Op{Op: plan.KindWhenEnd}},

		// Resource kinds, one per draft kind string draftToOp accepts.
		plan.KindFile: {draft: &resource.PlanDraft{
			Kind:       "file",
			ID:         "File[/tmp/fit.conf]",
			Path:       "/tmp/fit.conf",
			Mode:       "0640",
			ContentB64: b64,
			Deps:       []string{"Package[fit-dep]"},
		}},
		plan.KindDir: {draft: &resource.PlanDraft{
			Kind: "dir",
			Path: "/tmp/fitdir",
			Mode: "0750",
		}},
		plan.KindSyncDir: {draft: &resource.PlanDraft{
			Kind:      "sync_dir",
			Path:      "/tmp/fitsync",
			Blob:      "blobs/fit",
			FileMode:  "0640",
			SourceDir: "assets/fit",
		}},
		plan.KindLink: {draft: &resource.PlanDraft{
			Kind:    "link",
			Path:    "/tmp/fitlink",
			Symlink: "/tmp/fit-target",
		}},
		plan.KindLinkIfExists: {draft: &resource.PlanDraft{
			Kind:   "link_if_exists",
			Path:   "/tmp/fitlinkif",
			Target: "/tmp/fit-target",
		}},
		plan.KindEnsureDir: {draft: &resource.PlanDraft{
			Kind: "ensure_dir",
			Path: "/tmp/fitensure",
			Mode: "0750",
		}},
		plan.KindPackage: {draft: &resource.PlanDraft{
			Kind: "package",
			ID:   "Package[fit-pkg]",
			Name: "fit-pkg",
			Deps: []string{"Package[fit-base]"},
		}},
		plan.KindCommand: {draft: &resource.PlanDraft{
			Kind: "command",
			Bin:  "true",
			Args: []string{"fit"},
		}},
		plan.KindTimer: {draft: &resource.PlanDraft{
			Kind:    "timer",
			Name:    "fit.timer",
			User:    true,
			Restart: true,
		}},
		plan.KindDaemonReload: {draft: &resource.PlanDraft{
			Kind:      "daemon_reload",
			ID:        "DaemonReload[user]",
			User:      true,
			IfChanged: true,
			Watch:     []string{"File[/tmp/fit]"},
			Deps:      []string{"File[/tmp/fit]"},
		}},
		plan.KindCron: {draft: &resource.PlanDraft{
			Kind:     "cron",
			ID:       "Cron[root/fitjob]",
			Name:     "fitjob",
			CronUser: "root",
			Command:  "true",
			Schedule: "0 0 * * *",
			CronEnv:  []string{"FIT=1"},
		}},
		plan.KindService: {draft: &resource.PlanDraft{
			Kind:    "service",
			Name:    "fitsvc",
			Restart: true,
		}},
	}
}

func TestPlanKindFitness(t *testing.T) {
	table := kindFitnessTable()
	kinds := plan.AllKinds()

	// Every declared Kind must have exactly one fixture.
	for _, k := range kinds {
		if _, ok := table[k]; !ok {
			t.Errorf("no fitness fixture for plan.Kind %q (add one to kindFitnessTable; see docs/plan.md)", k)
		}
	}
	if len(table) != len(kinds) {
		t.Errorf("fitness table covers %d kinds, want %d (stale fixtures?)", len(table), len(kinds))
	}

	seenDrafts := map[string]plan.Kind{}
	for _, k := range kinds {
		f, ok := table[k]
		if !ok {
			continue
		}
		// Classification: control kinds must not have a draft, resource
		// kinds must have one.
		if controlKinds[k] != (f.draft == nil) {
			t.Errorf("kind %q: control=%t but draft=%v, classification mismatch", k, controlKinds[k], f.draft)
			continue
		}
		if f.draft != nil {
			if prev, dup := seenDrafts[f.draft.Kind]; dup {
				t.Errorf("draft kind %q mapped by both %s and %s", f.draft.Kind, prev, k)
			}
			seenDrafts[f.draft.Kind] = k
		}

		op := f.op
		if f.draft != nil {
			got, err := draftToOp(*f.draft)
			if err != nil {
				t.Errorf("kind %q: draftToOp: %v", k, err)
				continue
			}
			if got.Op != k {
				t.Errorf("draft kind %q lowered to plan op %q, want %q", f.draft.Kind, got.Op, k)
			}
			if !plan.IsKnownKind(got.Op) {
				t.Errorf("draft kind %q lowered to undeclared plan kind %q", f.draft.Kind, got.Op)
			}
			op = got
		}

		// Wire round-trip: the fixture op must survive encode → decode
		// unchanged (kind plus payload fields).
		raw, err := plan.EncodeOp(op)
		if err != nil {
			t.Errorf("kind %q: EncodeOp: %v", k, err)
			continue
		}
		got, err := plan.DecodeOp(raw)
		if err != nil {
			t.Errorf("kind %q: DecodeOp: %v", k, err)
			continue
		}
		if !reflect.DeepEqual(got, op) {
			t.Errorf("kind %q round-trip mismatch\ngot:  %#v\nwant: %#v", k, got, op)
		}
	}
}

func TestDraftToOpRejectsUnknownKinds(t *testing.T) {
	// Unknown draft kinds, control kinds used as drafts, and the empty kind
	// must all fail the record loudly instead of reaching the wire.
	for _, kind := range []string{"", "widget", "fli", "when_begin", "when_end", "plan"} {
		op, err := draftToOp(resource.PlanDraft{Kind: kind, ID: "Fit[bad]"})
		if err == nil {
			t.Fatalf("draft kind %q must not lower (got op %v)", kind, op)
		}
		if op.Op != "" {
			t.Fatalf("draft kind %q: failed draftToOp must not produce an op line, got %q", kind, op.Op)
		}
		if !strings.Contains(err.Error(), kind) {
			t.Errorf("error for draft kind %q must name it: %v", kind, err)
		}
	}
}
