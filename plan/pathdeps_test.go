package plan

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func pdDir(id, p string) Op    { return Op{Op: KindDir, ID: id, Path: p} }
func pdEnsure(id, p string) Op { return Op{Op: KindEnsureDir, ID: id, Path: p} }
func pdFile(id, p string, deps ...string) Op {
	return Op{Op: KindFile, ID: id, Path: p, Deps: deps, Payload: FilePayload{HasContent: true}}
}
func pdCmd(id string, deps ...string) Op {
	return Op{Op: KindCommand, ID: id, Deps: deps, Payload: CommandPayload{Bin: "true"}}
}

// TestInferParentDirDeps pins the inference rules on their own: which ops
// provide and consume, the nearest-ancestor choice, and the lexical match.
func TestInferParentDirDeps(t *testing.T) {
	absentDir := pdDir("d", "/a")
	absentDir.Absent = true
	absentFile := pdFile("f", "/a/x")
	absentFile.Absent = true
	set := Op{Op: KindConfigSet, ID: "set", Payload: ConfigSetPayload{Members: []ConfigMember{
		{Key: "a", Path: "/etc/mail/a"}, {Key: "b", Path: "/etc/mail/b"}, {Key: "c", Path: "/etc/other/c"},
	}}}
	cases := []struct {
		name string
		ops  []Op
		want []ParentDirEdge
	}{
		{"file inside dir", []Op{pdFile("f", "/a/x"), pdDir("d", "/a")}, []ParentDirEdge{{Dep: 1, Dependent: 0}}},
		{"ensure_dir and sync_dir provide", []Op{pdEnsure("e", "/a"), pdFile("f", "/a/x"),
			{Op: KindSyncDir, ID: "s", Path: "/b"}, {Op: KindLink, ID: "l", Path: "/b/l"}},
			[]ParentDirEdge{{Dep: 0, Dependent: 1}, {Dep: 2, Dependent: 3}}},
		{"nearest ancestor only", []Op{pdDir("a", "/a"), pdDir("ab", "/a/b"), pdFile("f", "/a/b/c/x")},
			[]ParentDirEdge{{Dep: 0, Dependent: 1}, {Dep: 1, Dependent: 2}}},
		{"all providers of the nearest path", []Op{pdDir("d", "/a"), pdEnsure("e", "/a"), pdFile("f", "/a/x")},
			[]ParentDirEdge{{Dep: 0, Dependent: 2}, {Dep: 1, Dependent: 2}}},
		{"same path is no ancestor", []Op{pdDir("d", "/a"), pdFile("f", "/a")}, nil},
		{"sibling prefix is no ancestor", []Op{pdDir("d", "/a"), pdFile("f", "/ab/x")}, nil},
		{"lexical clean", []Op{pdDir("d", "/a/b/"), pdFile("f", "/a//b/./x")}, []ParentDirEdge{{Dep: 0, Dependent: 1}}},
		{"symlinks are not resolved", []Op{pdDir("d", "/real"), {Op: KindLink, ID: "l", Path: "/alias"}, pdFile("f", "/alias/x")}, nil},
		{"link is no provider", []Op{{Op: KindLink, ID: "l", Path: "/a"}, pdFile("f", "/a/x")}, nil},
		{"absent dir provides nothing", []Op{absentDir, pdFile("f", "/a/x")}, nil},
		{"absent consumer gets no edge", []Op{pdDir("d", "/a"), absentFile}, nil},
		{"command has no path", []Op{pdDir("d", "/a"), pdCmd("c")}, nil},
		{"config set through its members", []Op{pdDir("m", "/etc/mail"), pdDir("o", "/etc/other"), set},
			[]ParentDirEdge{{Dep: 0, Dependent: 2}, {Dep: 1, Dependent: 2}}},
		{"unexpanded tokens compare lexically", []Op{pdEnsure("e", "${HOME}/.cursor"), {Op: KindLink, ID: "l", Path: "${HOME}/.cursor/commands"}},
			[]ParentDirEdge{{Dep: 0, Dependent: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InferParentDirDeps(tc.ops, nil); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("edges = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestInferParentDirDepsNeverClosesCycle: an edge whose dependent already
// reaches its dep through the caller's edges is dropped, and so is one that
// would close a cycle with an inferred edge accepted before it.
func TestInferParentDirDepsNeverClosesCycle(t *testing.T) {
	ops := []Op{pdDir("d", "/a"), pdFile("f", "/a/x")}
	fileBeforeDir := func(u int, visit func(int)) { // recorded: d depends on f
		if u == 1 {
			visit(0)
		}
	}
	if got := InferParentDirDeps(ops, fileBeforeDir); len(got) != 0 {
		t.Fatalf("edges = %v, want none (would close f -> d -> f)", got)
	}
}

// TestSortedApplyOrderParentDirs pins the inferred ordering inside
// plan.Apply's sort: a child declared before its directory moves after it,
// an explicit DependsOn alongside is harmless, and nothing crosses a
// when-block boundary or overrides a recorded dependency.
func TestSortedApplyOrderParentDirs(t *testing.T) {
	for _, tc := range parentDirOrderFixture() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sortedApplyOrder(tc.body)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("sortedApplyOrder: %v", err)
			}
			if ids := lineIDs(got); !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Fatalf("order = %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

func parentDirOrderFixture() []sortedApplyOrderCase {
	begin := Op{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "linux"}}}
	end := Op{Op: KindWhenEnd}
	return []sortedApplyOrderCase{
		{name: "child declared first moves after its dir",
			body:    []Op{pdFile("f", "/usr/local/sbin/x"), pdEnsure("e", "/usr/local/sbin")},
			wantIDs: []string{"e", "f"}},
		{name: "explicit DependsOn alongside is harmless",
			body:    []Op{pdFile("f", "/usr/local/sbin/x", "e"), pdEnsure("e", "/usr/local/sbin")},
			wantIDs: []string{"e", "f"}},
		{name: "nested dirs chain",
			body:    []Op{pdFile("f", "/a/b/x"), pdDir("ab", "/a/b"), pdDir("a", "/a")},
			wantIDs: []string{"a", "ab", "f"}},
		{name: "parents first keeps recorded order",
			body:    []Op{pdDir("a", "/a"), pdCmd("c"), pdFile("f", "/a/x"), pdFile("g", "/b")},
			wantIDs: []string{"a", "c", "f", "g"}},
		{name: "dir in a later when-block is not pulled forward",
			body:    []Op{pdFile("f", "/a/x"), begin, pdDir("d", "/a"), end},
			wantIDs: []string{"f", "when-begin", "d", "when-end"}},
		{name: "dir in an earlier when-block already applied",
			body:    []Op{begin, pdDir("d", "/a"), end, pdFile("f", "/a/x")},
			wantIDs: []string{"when-begin", "d", "when-end", "f"}},
		{name: "recorded dependency of the dir on its child wins",
			body:    []Op{pdDirWith("d", "/a", "f"), pdFile("f", "/a/x")},
			wantIDs: []string{"f", "d"}},
		{name: "recorded cycle still refused",
			body:    []Op{pdFile("f", "/a/x", "c"), pdCmd("c", "f"), pdDir("d", "/a")},
			wantErr: "circular dependency"},
	}
}

// pdDirWith is pdDir with recorded deps (the dir depends on them).
func pdDirWith(id, p string, deps ...string) Op {
	op := pdDir(id, p)
	op.Deps = deps
	return op
}

// TestParentDirInferenceKeepsClientOrder pins that plans shaped like the
// conf and dotfiles clients' — directories declared before their contents,
// under when-blocks and privilege chunks — apply in exactly the order they
// did before inference, both whole and per privilege chunk. The golden
// fixtures (dotfiles-shaped) and synthetic conf-shaped plans are compared
// against an oracle sort that reads the recorded deps only.
func TestParentDirInferenceKeepsClientOrder(t *testing.T) {
	plans := map[string][]Op{"conf rocky_script": confShapedRocky(), "conf freebsd": confShapedFreeBSD(), "dotfiles home": dotfilesShapedHome()}
	for _, name := range goldenPlans {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		ops, err := DecodePlanBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		plans[name] = ops
	}
	for name, ops := range plans {
		if n := countRunEdges(ops[1:]); n == 0 && strings.HasPrefix(name, "conf") {
			t.Fatalf("%s: fixture infers no edge, so it proves nothing", name)
		}
		for ci, ch := range append([]Chunk{{Ops: ops}}, SplitPrivilegeChunks(ops)...) {
			got, err := sortedApplyOrder(ch.Ops[1:])
			if err != nil {
				t.Fatalf("%s chunk %d: %v", name, ci, err)
			}
			if g, w := lineIDs(got), lineIDs(recordedDepsOrder(t, ch.Ops[1:])); !reflect.DeepEqual(g, w) {
				t.Fatalf("%s chunk %d: order = %v, want %v", name, ci, g, w)
			}
		}
	}
}

func confShapedRocky() []Op {
	e := pdEnsure("EnsureDir[/usr/local/sbin]", "/usr/local/sbin")
	e.Elevate = true
	f := pdFile("File[/usr/local/sbin/unattended-upgrade-rocky]", "/usr/local/sbin/unattended-upgrade-rocky")
	f.Elevate = true
	s := pdEnsure("EnsureDir[/var/lib/unattended-upgrade]", "/var/lib/unattended-upgrade")
	s.Elevate = true
	timer := Op{Op: KindSystemdTimer, ID: "SystemdTimer[unattended-upgrade-rocky]", Elevate: true,
		Deps: []string{f.ID, s.ID}}
	return []Op{{Op: KindPlan, Version: CurrentVersion, ID: "rocky"},
		{Op: KindWhenBegin, All: []Predicate{{Fact: "hostname_contains", Eq: "pi2"}}},
		e, f, {Op: KindWhenEnd}, s, timer}
}

func confShapedFreeBSD() []Op {
	pkg := Op{Op: KindPackage, ID: "Package[ksh93]", Elevate: true}
	e := pdEnsure("EnsureDir[/usr/local/sbin]", "/usr/local/sbin")
	e.Elevate = true
	f := pdFile("File[/usr/local/sbin/unattended-upgrade-freebsd]", "/usr/local/sbin/unattended-upgrade-freebsd", pkg.ID)
	f.Elevate = true
	svc := pdFile("File[/etc/unattended-upgrade-services]", "/etc/unattended-upgrade-services")
	svc.Elevate = true
	stamp := pdEnsure("EnsureDir[/var/lib/unattended-upgrade]", "/var/lib/unattended-upgrade")
	stamp.Elevate = true
	cron := Op{Op: KindCron, ID: "Cron[root/unattended-upgrade]", Elevate: true, Deps: []string{f.ID, svc.ID, stamp.ID}}
	return []Op{{Op: KindPlan, Version: CurrentVersion, ID: "freebsd"}, pkg, e, f, svc, stamp, cron}
}

func dotfilesShapedHome() []Op {
	home := "/home/u"
	return []Op{{Op: KindPlan, Version: CurrentVersion, ID: "home"},
		pdDir("Directory["+home+"/QuickEdit]", home+"/QuickEdit"),
		{Op: KindLink, ID: "Symlink[" + home + "/QuickEdit/data]", Path: home + "/QuickEdit/data"},
		{Op: KindSyncDir, ID: "SyncDir[" + home + "/.config/helix]", Path: home + "/.config/helix", Prune: true},
		pdFile("File["+home+"/.config/helix/languages.toml]", home+"/.config/helix/languages.toml"),
		{Op: KindWhenBegin, All: []Predicate{{PathExists: home + "/Notes"}}},
		pdEnsure("EnsureDir["+home+"/.cursor]", home+"/.cursor"),
		{Op: KindLink, ID: "Symlink[.cursor/commands]", Path: home + "/.cursor/commands"},
		{Op: KindWhenEnd},
		pdDir("Directory["+home+"/.local/bin]", home+"/.local/bin"),
		pdFile("File["+home+"/.local/bin/x]", home+"/.local/bin/x"),
	}
}

func lineIDs(ls []planLine) []string {
	ids := make([]string, len(ls))
	for i, l := range ls {
		switch l.op.Op {
		case KindWhenBegin:
			ids[i] = "when-begin"
		case KindWhenEnd:
			ids[i] = "when-end"
		default:
			ids[i] = l.op.ID
		}
	}
	return ids
}

// recordedDepsOrder is the pre-inference sortedApplyOrder: each run between
// control ops sorted by its recorded deps only (the oracle of
// TestParentDirInferenceKeepsClientOrder; deps outside a run are ignored,
// which is what the real sort does for every valid plan).
func recordedDepsOrder(t *testing.T, body []Op) []planLine {
	t.Helper()
	var out, run []planLine
	flush := func() {
		inRun := map[string][]int{}
		for pos, l := range run {
			inRun[l.op.ID] = append(inRun[l.op.ID], pos)
		}
		indeg, waiters := make([]int, len(run)), make([][]int, len(run))
		for pos, l := range run {
			for _, dep := range l.op.Deps {
				for _, p := range inRun[dep] {
					indeg[pos]++
					waiters[p] = append(waiters[p], pos)
				}
			}
		}
		sorted, err := kahnStable(run, indeg, waiters)
		if err != nil {
			t.Fatal(err)
		}
		out, run = append(out, sorted...), nil
	}
	for i, op := range body {
		if IsControlKind(op.Op) {
			flush()
			out = append(out, planLine{op: op, line: i + 2})
			continue
		}
		run = append(run, planLine{op: op, line: i + 2})
	}
	flush()
	return out
}

// countRunEdges counts the edges inference adds over body's runs.
func countRunEdges(body []Op) int {
	n := 0
	var run []Op
	for _, op := range append(body, Op{Op: KindWhenEnd}) {
		if IsControlKind(op.Op) {
			n += len(InferParentDirDeps(run, nil))
			run = nil
			continue
		}
		run = append(run, op)
	}
	return n
}

// TestParentDirAcrossRecordedChunksIsNoRefusal pins that a recorded plan
// whose file (unprivileged) precedes its elevated directory stays valid:
// the inferred edge would cross the recorded chunk order, which nothing may
// reorder, so it is simply not inferred (each chunk sorts alone) and the
// pre-flight, which reads recorded deps only, accepts the plan as before.
func TestParentDirAcrossRecordedChunksIsNoRefusal(t *testing.T) {
	dir := pdDir("d", "/a")
	dir.Elevate = true
	ops := []Op{{Op: KindPlan, Version: CurrentVersion, ID: "p"}, pdFile("f", "/a/x"), dir}
	chunks := SplitPrivilegeChunks(ops)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if err := ValidateChunks(chunks); err != nil {
		t.Fatalf("ValidateChunks: %v", err)
	}
	for i, ch := range chunks {
		if _, err := sortedApplyOrder(ch.Ops[1:]); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
}
