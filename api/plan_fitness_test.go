package api

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/configset"
	"github.com/snonux/gonf/resource/cron"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/link"
	"github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/pkg"
	"github.com/snonux/gonf/resource/service"
	"github.com/snonux/gonf/resource/systemdtimer"
	"github.com/snonux/gonf/resource/user"
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
			Kind:    "file",
			ID:      "File[/tmp/fit.conf]",
			Path:    "/tmp/fit.conf",
			Mode:    "0640",
			Payload: file.Payload{ContentB64: b64},
			Deps:    []string{"Package[fit-dep]"},
		}},
		plan.KindDir: {draft: &resource.PlanDraft{
			Kind: "dir",
			Path: "/tmp/fitdir",
			Mode: "0750",
		}},
		plan.KindSyncDir: {draft: &resource.PlanDraft{
			Kind: "sync_dir",
			Path: "/tmp/fitsync",
			Blob: "blobs/fit",
			Payload: dir.SyncPayload{
				FileMode:  "0640",
				SourceDir: "assets/fit",
			},
		}},
		plan.KindLink: {draft: &resource.PlanDraft{
			Kind:    "link",
			Path:    "/tmp/fitlink",
			Payload: link.Payload{Symlink: "/tmp/fit-target"},
		}},
		plan.KindLinkIfExists: {draft: &resource.PlanDraft{
			Kind:    "link_if_exists",
			Path:    "/tmp/fitlinkif",
			Payload: link.IfExistsPayload{Target: "/tmp/fit-target"},
		}},
		plan.KindEnsureDir: {draft: &resource.PlanDraft{
			Kind: "ensure_dir",
			Path: "/tmp/fitensure",
			Mode: "0750",
		}},
		plan.KindEnsureFile: {draft: &resource.PlanDraft{
			Kind: "ensure_file",
			Path: "/tmp/fitensurefile",
			Mode: "0644",
		}},
		plan.KindPackage: {draft: &resource.PlanDraft{
			Kind:    "package",
			ID:      "Package[fit-pkg]",
			Name:    "fit-pkg",
			Payload: pkg.Payload{},
			Deps:    []string{"Package[fit-base]"},
		}},
		plan.KindCommand: {draft: &resource.PlanDraft{
			Kind:    "command",
			Payload: cmd.Payload{Bin: "true", Args: []string{"fit"}},
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
			Kind:    "cron",
			ID:      "Cron[root/fitjob]",
			Name:    "fitjob",
			Command: "true",
			Payload: cron.Payload{
				CronUser: "root",
				Schedule: "0 0 * * *",
				CronEnv:  []string{"FIT=1"},
			},
		}},
		plan.KindService: {draft: &resource.PlanDraft{
			Kind:    "service",
			Name:    "fitsvc",
			Restart: true,
			Payload: service.Payload{},
		}},
		plan.KindSystemdTimer: {draft: &resource.PlanDraft{
			Kind:    "systemd_timer",
			ID:      "SystemdTimer[fit-job]",
			Name:    "fit-job",
			Command: "/bin/true",
			Payload: systemdtimer.Payload{
				OnCalendar:         "*-*-* *:05:00",
				OnBootSec:          "10min",
				Persistent:         true,
				Description:        "fit timer",
				ServiceDescription: "fit oneshot",
				After:              []string{"network-online.target"},
				Wants:              []string{"network-online.target"},
			},
		}},
		plan.KindUser: {draft: &resource.PlanDraft{
			Kind: "user",
			ID:   "User[fit-user]",
			Name: "fit-user",
			Payload: user.Payload{
				PrimaryGroup:        "fit-user",
				SupplementaryGroups: []string{"audio", "wheel"},
				Home:                "/var/lib/fit-user",
				CreateHome:          true,
				Shell:               "/sbin/nologin",
				LoginClass:          "daemon",
				System:              true,
				ManageHome:          true,
			},
			Deps: []string{"Package[fit-base]"},
		}},
		plan.KindConfigSet: {draft: &resource.PlanDraft{
			Kind: "config_set",
			ID:   "ConfigSet[fit]",
			Name: "fit",
			Payload: configset.SetPayload{
				ConfigMembers: []resource.PlanConfigMember{
					{Key: "main.conf", Path: "/etc/fit/main.conf", Content: []byte("include " + options.MemberPath("keys") + "\n"), Mode: "0640", Owner: "root", Group: "wheel"},
					{Key: "keys", Path: "/etc/fit/keys", Content: []byte("secret\n"), Mode: "0600"},
				},
				Validators: []resource.PlanArgv{{Bin: "fitcheck", Args: []string{"-f", options.MemberPath("main.conf")}}},
				Chroot:     "/etc",
				StagingDir: "/etc/fit",
			},
			Deps: []string{"Package[fit-base]"},
		}},
		plan.KindConfigSetMember: {draft: &resource.PlanDraft{
			Kind:    "config_set_member",
			ID:      "ConfigSetMember[fit/keys]",
			Name:    "fit",
			Payload: configset.MemberPayload{Member: "keys"},
			Path:    "/etc/fit/keys",
			Deps:    []string{"ConfigSet[fit]"},
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
			got, err := newDraftPackager(nil).draftToOp(*f.draft)
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

// sourcePayloadFitness is one row of sourcePayloadFitnessTable: whether a
// concrete DraftPayload type is allowed to implement
// resource.SourceFilePayload / resource.SourceDirPayload.
type sourcePayloadFitness struct {
	payload  resource.DraftPayload
	wantFile bool
	wantDir  bool
}

// sourcePayloadFitnessTable pins the exact set of payload types allowed to
// implement resource.SourceFilePayload/resource.SourceDirPayload (task 0e2:
// gate the blob-source marker interfaces on the draft kind). Before this
// task, api/packager.go's sourceFilePath/syncDirSource and
// internal/testapply's packageSource type-asserted these two kind-neutral
// markers against WHATEVER concrete type a draft's Payload held, with no
// check that the draft's Kind was one that was ever meant to carry a
// controller-local source — pure structural typing. A probe proved the
// consequence: adding a same-named SourceFilePath() method to an unrelated
// kind's payload (e.g. cron.Payload) kept go build/vet/staticcheck and the
// entire test suite green while silently making every op of that kind carry
// the bytes of a file the recipe never named — a silent secret leak into
// the recorded plan. Both call sites now gate the assertion on d.Kind
// first (see their updated docs and resource/draft.go's interface docs);
// this table is the second, independent layer: it fails loudly, by name,
// the moment ANY payload type other than the two listed below implements
// either marker, whether or not the caller-side gate also catches it.
//
// Only file.Payload (kind "file", also reused unmodified for "ensure_file")
// and dir.SyncPayload (kind "sync_dir") may implement these interfaces
// today; extending this list is a deliberate, reviewed decision that must
// land together with the matching d.Kind gate in api/packager.go and
// internal/testapply, never as a side effect of an unrelated payload
// gaining a like-named method.
func sourcePayloadFitnessTable() []sourcePayloadFitness {
	return []sourcePayloadFitness{
		{payload: file.Payload{}, wantFile: true},
		{payload: dir.SyncPayload{}, wantDir: true},
		{payload: link.Payload{}},
		{payload: link.IfExistsPayload{}},
		{payload: pkg.Payload{}},
		{payload: cmd.Payload{}},
		{payload: cron.Payload{}},
		{payload: service.Payload{}},
		{payload: systemdtimer.Payload{}},
		{payload: user.Payload{}},
		{payload: configset.SetPayload{}},
		{payload: configset.MemberPayload{}},
	}
}

// TestSourcePayloadFitness is the fitness test the task 0e2 annotation asks
// for, placed next to TestPlanKindFitness because it guards the same class
// of "adding a kind silently misses a checklist step" defect. It has two
// parts:
//
//  1. Completeness: every concrete Payload type kindFitnessTable's draft
//     fixtures carry must have a row in sourcePayloadFitnessTable, so a
//     future resource kind's new payload type cannot go unchecked simply
//     because nobody remembered to add it here (mirroring how
//     TestPlanKindFitness itself cross-checks against plan.AllKinds()).
//  2. The pin itself: for every row, the payload's actual
//     SourceFilePayload/SourceDirPayload implementation must match the
//     row's want — neither more (an accidental new implementor) nor less
//     (file.Payload/dir.SyncPayload losing the accessor packaging depends
//     on).
func TestSourcePayloadFitness(t *testing.T) {
	table := sourcePayloadFitnessTable()

	seen := map[string]bool{}
	for _, fx := range table {
		seen[reflect.TypeOf(fx.payload).String()] = true
	}
	for kind, fx := range kindFitnessTable() {
		if fx.draft == nil || fx.draft.Payload == nil {
			continue // control kind, or a kind with no exclusive payload
		}
		typ := reflect.TypeOf(fx.draft.Payload).String()
		if !seen[typ] {
			t.Errorf("kind %q carries payload %s with no row in sourcePayloadFitnessTable (add one; see TestSourcePayloadFitness doc)", kind, typ)
		}
	}

	for _, fx := range table {
		typ := reflect.TypeOf(fx.payload).String()
		if _, got := fx.payload.(resource.SourceFilePayload); got != fx.wantFile {
			t.Errorf("%s implements resource.SourceFilePayload = %t, want %t -- api/packager.go's sourceFilePath and internal/testapply's packageSource only consult this marker for a \"file\"/\"ensure_file\" draft; an unlisted implementor would leak its file's bytes into an unrelated kind's op if that d.Kind gate were ever weakened (see resource/draft.go)",
				typ, got, fx.wantFile)
		}
		if _, got := fx.payload.(resource.SourceDirPayload); got != fx.wantDir {
			t.Errorf("%s implements resource.SourceDirPayload = %t, want %t -- gated on \"sync_dir\" only; see resource/draft.go",
				typ, got, fx.wantDir)
		}
	}
}

func TestDraftToOpRejectsUnknownKinds(t *testing.T) {
	// Unknown draft kinds, control kinds used as drafts, and the empty kind
	// must all fail the record loudly instead of reaching the wire.
	for _, kind := range []string{"", "widget", "fli", "when_begin", "when_end", "plan"} {
		op, err := newDraftPackager(nil).draftToOp(resource.PlanDraft{Kind: kind, ID: "Fit[bad]"})
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
