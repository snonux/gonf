package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
)

// These tests pin how RecordPlan applies plan.SecureDir's directory rule (m62)
// to the plan directory and to a blobs/ directory inside it: the
// user-private-group exception, the read-only refusal and its advice, and the
// commit-time refusal of an unsafe existing blobs/.

// blobTask registers "blobtask", a task that packages a source tree as a blob
// (so the plan needs blobs/), and returns whether its body ran.
func blobTask(t *testing.T) *bool {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "f"), []byte("payload"))
	dst := filepath.Join(t.TempDir(), "dst")
	ran := new(bool)
	Task("blobtask", "", func() {
		*ran = true
		Dir(dst, options.WithSource(src))
	})
	return ran
}

// TestRecordPlanAcceptsPrivateGroupWritablePlanDir: a plan directory that is
// group-writable by the caller's private group (a UPG checkout made under
// umask 002, mode 0775) is accepted by the pre-check and by SecureDir at
// commit time, keeps its mode, and gets the plan's blobs. It changes the
// process umask, so it must not run in parallel.
func TestRecordPlanAcceptsPrivateGroupWritablePlanDir(t *testing.T) {
	testutil.RequirePrivateGroupUser(t)
	ran := blobTask(t)
	planDir := filepath.Join(t.TempDir(), "checkout")
	testutil.WithUmask(0o002, func() {
		if err := os.Mkdir(planDir, 0o777); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.Chown(planDir, -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(planDir); err != nil || fi.Mode().Perm() != 0o775 {
		t.Fatalf("test setup: %v, %v; want 0775 from mkdir under umask 002", fi, err)
	}
	if _, err := RecordPlan("x", planDir, "blobtask"); err != nil {
		t.Fatalf("RecordPlan into a 0775 private-group directory = %v, want success", err)
	}
	if !*ran {
		t.Fatal("the task body did not run")
	}
	if fi, err := os.Stat(planDir); err != nil || fi.Mode().Perm() != 0o775 {
		t.Fatalf("plan dir after record: %v, %v; want 0775 unchanged", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(planDir, "blobs")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("blobs after record: %v, %v; want the created directory 0700", fi, err)
	}
}

// TestRecordPlanReadOnlyPlanDirSaysHowToFixIt: a read-only directory of the
// caller (0555, 0500) is refused up front. That is new since m62: before it,
// SecureDir chmod'ed such a directory to 0700 and the run worked; now the
// directory is left alone, so the refusal must tell the operator what to do.
func TestRecordPlanReadOnlyPlanDirSaysHowToFixIt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are not enforced for root")
	}
	for _, mode := range []os.FileMode{0o555, 0o500} {
		t.Run(mode.String(), func(t *testing.T) {
			ran := blobTask(t)
			planDir := chmodedDir(t, filepath.Join(t.TempDir(), "out"), mode)
			before := testutil.Snapshot(t, planDir)
			_, err := RecordPlan("x", planDir, "blobtask")
			for _, part := range []string{"RecordPlan: plan dir: ", "cannot write to " + planDir + ": permission denied",
				"chmod u+w", "-o <private dir>"} {
				if err == nil || !strings.Contains(err.Error(), part) {
					t.Fatalf("RecordPlan error = %v, want a refusal containing %q", err, part)
				}
			}
			if *ran {
				t.Fatal("the task body ran; a read-only plan dir must fail before any body")
			}
			testutil.RequireUnchanged(t, before, planDir)
		})
	}
}

// requireSinglePrefixRefusal pins the exact wording shape of a commit-time blob
// refusal: it starts with "RecordPlan: ", carries that prefix and no other
// "plan:" segment anywhere (neither "plan dir:" nor the blob store's own "plan:"
// behind it: "RecordPlan: plan: write blob ..."), names the blob store
// operation and the wanted parts, and keeps the typed cause reachable. It does
// not depend on OS error text.
func requireSinglePrefixRefusal(t *testing.T, err error, wantParts ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("RecordPlan = nil, want a refusal")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "RecordPlan: ") || strings.Count(msg, "RecordPlan:") != 1 || strings.Contains(msg, "plan: ") || strings.Contains(msg, "plan dir:") {
		t.Fatalf("RecordPlan error = %q, want one leading %q and no other \"plan:\" segment", msg, "RecordPlan: ")
	}
	var refusal plan.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("RecordPlan error = %q: the blob store's refusal is not reachable through errors.As", msg)
	}
	for _, part := range append([]string{`blob "blobs/`}, wantParts...) {
		if !strings.Contains(msg, part) {
			t.Fatalf("RecordPlan error = %q, want it to contain %q", msg, part)
		}
	}
}

// TestRecordPlanReadOnlyDefaultDirNamesTheWorkingDirectory: the read-only
// refusal words the directory like every other refusal of the shared rule, as
// an absolute path, so the default `-o .` reads "cannot write to <cwd>", not
// "cannot write to .".
func TestRecordPlanReadOnlyDefaultDirNamesTheWorkingDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are not enforced for root")
	}
	ran := blobTask(t)
	t.Chdir(chmodedDir(t, filepath.Join(t.TempDir(), "checkout"), 0o555))
	cwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	_, err = RecordPlan("x", ".", "blobtask")
	if err == nil || !strings.Contains(err.Error(), "cannot write to "+cwd+": permission denied") || strings.Contains(err.Error(), "cannot write to .") {
		t.Fatalf("RecordPlan(%q) error = %v, want \"cannot write to %s: permission denied\"", ".", err, cwd)
	}
	if *ran {
		t.Fatal("the task body ran; a read-only plan dir must fail before any body")
	}
}

// unsafeBlobsDir is one existing blobs/ directory (inside an otherwise fine
// plan directory) that must be refused, and the text the refusal must contain.
type unsafeBlobsDir struct {
	name string
	// setup creates the unsafe blobs/ in planDir and returns a second directory
	// the test must find unchanged as well ("" if none): what a symlink points at.
	setup func(t *testing.T, planDir string) (also string)
	// want is a substring of the refusal. A setup that needs the environment
	// (a foreign group) skips its own subtest from within (testutil.ChgrpForeign
	// is called on the subtest's t), so the case never silently drops out.
	want string
}

func unsafeBlobsDirs(t *testing.T) []unsafeBlobsDir {
	t.Helper()
	cases := []unsafeBlobsDir{
		{"world-writable", func(t *testing.T, planDir string) string {
			chmodedDir(t, filepath.Join(planDir, "blobs"), 0o777)
			return ""
		}, "world-writable"},
		{"sticky and world-writable", func(t *testing.T, planDir string) string {
			chmodedDir(t, filepath.Join(planDir, "blobs"), 0o777|os.ModeSticky)
			return ""
		}, "world-writable"},
		{"symlink", func(t *testing.T, planDir string) string {
			target := chmodedDir(t, filepath.Join(t.TempDir(), "elsewhere"), 0o700)
			if err := os.Symlink(target, filepath.Join(planDir, "blobs")); err != nil {
				t.Fatal(err)
			}
			return target
		}, `component "blobs"`},
		{"group-writable by a shared group", func(t *testing.T, planDir string) string {
			blobs := chmodedDir(t, filepath.Join(planDir, "blobs"), 0o700)
			testutil.ChgrpForeign(t, blobs) // skips this subtest when no foreign group is usable
			if err := os.Chmod(blobs, 0o775); err != nil {
				t.Fatal(err)
			}
			return ""
		}, "group-writable by group"},
	}
	return cases
}

// TestRecordPlanRefusesUnsafeBlobsDirAtCommit: an existing blobs/ that is a
// symlink, world-writable or group-writable by a shared group is refused when
// the blobs are committed, after the task body ran (the pre-check does not
// look at blobs/, see checkPlanDirUsable) and before anything is written: the
// plan directory, and whatever a symlink points at, stay exactly as they were.
// The message names the blobs directory and the reason and reads with a single
// "RecordPlan: " prefix (requireSinglePrefixRefusal).
func TestRecordPlanRefusesUnsafeBlobsDirAtCommit(t *testing.T) {
	for _, tc := range unsafeBlobsDirs(t) {
		t.Run(tc.name, func(t *testing.T) {
			ran := blobTask(t)
			planDir := chmodedDir(t, filepath.Join(t.TempDir(), "out"), 0o755)
			also := tc.setup(t, planDir)
			before := testutil.Snapshot(t, planDir)
			var alsoBefore testutil.DirSnapshot
			if also != "" {
				alsoBefore = testutil.Snapshot(t, also)
			}
			ops, err := RecordPlan("x", planDir, "blobtask")
			requireSinglePrefixRefusal(t, err, filepath.Join(planDir, "blobs"), tc.want)
			if ops != nil || !*ran {
				t.Fatalf("ops = %v, body ran = %v; want no ops and a commit-time (post-body) refusal", ops, *ran)
			}
			testutil.RequireUnchanged(t, before, planDir)
			if also != "" {
				testutil.RequireUnchanged(t, alsoBefore, also)
			}
		})
	}
}

// TestRecordPlanBloblessPlanIgnoresUnsafeBlobsDir documents why checkPlanDirUsable
// does not inspect blobs/: a plan that packages no blob never touches it, so an
// unsafe leftover blobs/ must not make such a plan fail.
func TestRecordPlanBloblessPlanIgnoresUnsafeBlobsDir(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	ran := planDirProbe("probe")
	planDir := chmodedDir(t, filepath.Join(t.TempDir(), "out"), 0o755)
	chmodedDir(t, filepath.Join(planDir, "blobs"), 0o777)
	if _, err := RecordPlan("x", planDir, "probe"); err != nil {
		t.Fatalf("RecordPlan of a blob-less plan next to a world-writable blobs/ = %v, want success", err)
	}
	if !*ran {
		t.Fatal("the task body did not run")
	}
}
