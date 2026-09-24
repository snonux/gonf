package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/options"
)

// pathOp builds a filesystem op for the parent-directory ordering tests.
func pathOp(kind plan.Kind, id, path string, elevate bool, deps ...string) plan.Op {
	op := plan.Op{Op: kind, ID: id, Path: path, Elevate: elevate, Deps: deps}
	if kind == plan.KindFile {
		op.Payload = plan.FilePayload{HasContent: true}
	}
	return op
}

// TestOrderForPrivilegeSplitParentDirs pins the inferred parent-directory
// edges in Apply's privilege split: an unprivileged File inside an elevated
// Directory lands in a later chunk than the directory (without inference the
// ID order a,File,Directory ties at two chunks and keeps the file first), an
// explicit DependsOn alongside changes nothing, and an inferred edge that
// would close a cycle with a recorded dependency is dropped instead of
// refusing the plan.
func TestOrderForPrivilegeSplitParentDirs(t *testing.T) {
	dir := pathOp(plan.KindDir, "Directory[/x]", "/x", true)
	file := pathOp(plan.KindFile, "File[/x/y]", "/x/y", false)
	fileDep := pathOp(plan.KindFile, "File[/x/y]", "/x/y", false, dir.ID)
	dirAfterFile := pathOp(plan.KindDir, "Directory[/x]", "/x", true, file.ID)
	cases := []struct {
		name string
		ops  []plan.Op
		want string
	}{
		{"file after its elevated dir", []plan.Op{orderHdr, orderOp("a", false), dir, file}, "Directory[/x],a,File[/x/y]"},
		{"explicit DependsOn alongside", []plan.Op{orderHdr, orderOp("a", false), dir, fileDep}, "Directory[/x],a,File[/x/y]"},
		{"recorded dir-after-child wins", []plan.Op{orderHdr, orderOp("a", false), dirAfterFile, file}, "a,File[/x/y],Directory[/x]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, conflicts, err := orderForPrivilegeSplit(tc.ops)
			if err != nil {
				t.Fatalf("orderForPrivilegeSplit() error = %v", err)
			}
			if ids := bodyIDs(t, got); ids != tc.want {
				t.Fatalf("order = %s, want %s", ids, tc.want)
			}
			if err := validateApplyDeps(plan.SplitPrivilegeChunks(got), conflicts); err != nil {
				t.Fatalf("pre-flight refused: %v", err)
			}
		})
	}
}

// TestOrderForPrivilegeSplitParentDirKeepsWatch pins that inference never
// breaks a kept change-gate watch: g (unprivileged) watches File[/x/y] and
// the elevated Directory[/x] depends on g. The inferred edge Directory ->
// File would force the file into a chunk after g's, so the watch could not
// be kept and the pre-flight would refuse a plan that is valid without
// inference; the edge is dropped instead and the plan applies as before.
func TestOrderForPrivilegeSplitParentDirKeepsWatch(t *testing.T) {
	ops := []plan.Op{orderHdr,
		pathOp(plan.KindDir, "Directory[/x]", "/x", true, "g"),
		pathOp(plan.KindFile, "File[/x/y]", "/x/y", false),
		watchOp("g", false, []string{"File[/x/y]"}),
	}
	got, conflicts, err := orderForPrivilegeSplit(ops)
	if err != nil {
		t.Fatalf("orderForPrivilegeSplit() error = %v", err)
	}
	if ids := bodyIDs(t, got); ids != "File[/x/y],g,Directory[/x]" {
		t.Fatalf("order = %s, want File[/x/y],g,Directory[/x]", ids)
	}
	if err := validateApplyDeps(plan.SplitPrivilegeChunks(got), conflicts); err != nil {
		t.Fatalf("pre-flight refused: %v", err)
	}
}

// TestRunInfersParentDirOrder is the end-to-end case through a recorded
// task: the body declares the File before the Dir it lives in, so the
// recorded order writes the file first, which used to fail on the missing
// parent. The recorded plan carries no dep for it (plan bytes unchanged);
// the apply-time sort places the file after its directory.
func TestRunInfersParentDirOrder(t *testing.T) {
	ResetTasks()
	resource.ResetForTest()
	dst := filepath.Join(t.TempDir(), "dst")
	child := filepath.Join(dst, "child.conf")
	Task("child_first", "", func() {
		File(child, options.WithContent("child"))
		Dir(dst, options.WithMode(0o750))
	})
	ops, err := RecordPlan("child_first", "", "child_first")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	for _, op := range ops[1:] {
		if len(op.Deps) != 0 {
			t.Fatalf("%s recorded deps %v, want none (inference is apply-time only)", op.ID, op.Deps)
		}
	}
	if err := ApplyChunks(ops, t.TempDir(), privilege.None); err != nil {
		t.Fatalf("ApplyChunks: %v", err)
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatal(err)
	}
}
