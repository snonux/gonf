package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/snonux/gonf/api"
)

// This file is task qg2: sealed artifacts go through one write path
// (sealedOutput, plan_seal_output.go) that stages each -for host's sealed
// frame to disk before the next host is recorded (bounded memory) and only
// publishes the final plan-<host>.age files once every host succeeded
// (all-or-nothing), naming what is and is not written when the final
// commit itself fails partway.

// dirEntryNames returns the names in dir, or nil when dir does not exist.
func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}

// requireNoHiddenEntries fails when dir holds any dot-entry: a staging file
// left behind after a run that finished (successfully or not).
func requireNoHiddenEntries(t *testing.T, dir string) {
	t.Helper()
	for _, name := range dirEntryNames(t, dir) {
		if strings.HasPrefix(name, ".") {
			t.Fatalf("%s holds a leftover hidden entry %q (all entries: %v)", dir, name, dirEntryNames(t, dir))
		}
	}
}

// TestCLIPlanSealForStagesEachHostBeforeRecordingTheNext: hostA's sealed
// frame is already on disk (as a hidden staging file, not yet as
// plan-hostA.age) while hostB is being recorded, so -for never holds more
// than one host's frame in memory. Before task qg2 every host's frame was
// held in memory until all were sealed, and the output directory did not
// even exist yet at this point. It also pins that the final "wrote" output
// keeps task 4g2's format, in host order, with no staging file left over.
func TestCLIPlanSealForStagesEachHostBeforeRecordingTheNext(t *testing.T) {
	isolateXDGConfig(t)
	registerSealForInventory(t)
	operatorRecipient, _ := genSealKeyPair(t)
	dir := filepath.Join(t.TempDir(), "out")

	var seenDuringHostB []string
	recordedHostB := false
	api.Task("cli_seal_for_probe", "", func() {
		api.ForHosts("secretkey", func(host string, _ string) {
			if host == "hostB" {
				recordedHostB = true
				seenDuringHostB = dirEntryNames(t, dir)
			}
		})
	}, api.WithTaskCluster("edge"))

	code, stdout, stderr := runGonfCaptureStdout(t, "plan", "-o", dir, "-seal", "-recipient", operatorRecipient,
		"-for", "edge", "cli_seal_for_task", "cli_seal_for_probe")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !recordedHostB {
		t.Fatal("hostB's ForHosts body never ran; the probe did not observe anything")
	}
	stagedA := false
	for _, name := range seenDuringHostB {
		if name == "plan-hostA.age" {
			t.Fatalf("plan-hostA.age was published before hostB was recorded (entries %v)", seenDuringHostB)
		}
		stagedA = stagedA || strings.HasPrefix(name, ".plan-hostA.age"+stagingSuffix)
	}
	if !stagedA {
		t.Fatalf("while recording hostB, %s held %v; want hostA's sealed frame already staged to disk", dir, seenDuringHostB)
	}
	requireNoHiddenEntries(t, dir)

	wrote := regexp.MustCompile(`^wrote ` + regexp.QuoteMeta(dir) + `/plan-(host[AB])\.age \(\d+ ops, 2 recipients\)\n` +
		`(  recipient age1pq1…\S+ sha256:[0-9a-f]{16}\n){2}` +
		`wrote ` + regexp.QuoteMeta(dir) + `/plan-(host[AB])\.age \(\d+ ops, 2 recipients\)\n` +
		`(  recipient age1pq1…\S+ sha256:[0-9a-f]{16}\n){2}$`)
	m := wrote.FindStringSubmatch(stdout)
	if m == nil || m[1] != "hostA" || m[3] != "hostB" {
		t.Fatalf("stdout %q, want one 4g2-format wrote report per host, hostA then hostB", stdout)
	}
}

// TestCLIPlanSealForPartialCommitNamesWrittenArtifacts: when publishing
// fails partway (here plan-hostB.age is a pre-existing directory, which a
// rename cannot replace), hostA's artifact is already in place, so the
// refusal must say so and name what is not written, instead of the bare
// "write .../plan-hostB.age: ..." every earlier version printed while
// plan-hostA.age silently sat on disk. No staging file is left behind.
func TestCLIPlanSealForPartialCommitNamesWrittenArtifacts(t *testing.T) {
	isolateXDGConfig(t)
	registerSealForInventory(t)
	operatorRecipient, _ := genSealKeyPair(t)
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(filepath.Join(dir, "plan-hostB.age", "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}

	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", operatorRecipient, "-for", "edge", "cli_seal_for_task")
	if code == 0 {
		t.Fatalf("exit 0, want a refusal; stderr %q", stderr)
	}
	pathA, pathB := filepath.Join(dir, "plan-hostA.age"), filepath.Join(dir, "plan-hostB.age")
	for _, want := range []string{"plan: write " + pathB + ": ", "already written: " + pathA, "not written: " + pathB} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q, want it to contain %q", stderr, want)
		}
	}
	if _, err := os.Stat(pathA); err != nil {
		t.Fatalf("stderr says %s was written, but: %v", pathA, err)
	}
	requireNoHiddenEntries(t, dir)
}

// TestCLIPlanSealForLaterHostFailureLeavesNothingWritten: a record failure
// on the SECOND host (hostB's secret is missing), after hostA's frame was
// already staged to disk, still leaves nothing written: the staged file is
// removed again, and so is the output directory this run created.
func TestCLIPlanSealForLaterHostFailureLeavesNothingWritten(t *testing.T) {
	isolateXDGConfig(t)
	work, _, _ := registerSealForInventory(t)
	if err := os.Remove(filepath.Join(work, "secrets", "hostB", "token")); err != nil {
		t.Fatal(err)
	}
	operatorRecipient, _ := genSealKeyPair(t)
	dir := filepath.Join(t.TempDir(), "out")

	code, stderr := runGonf(t, "plan", "-o", dir, "-seal", "-recipient", operatorRecipient, "-for", "edge", "cli_seal_for_task")
	if code == 0 {
		t.Fatalf("exit 0, want a refusal; stderr %q", stderr)
	}
	if !strings.Contains(stderr, "host hostB") {
		t.Fatalf("stderr %q, want it to name the failing host hostB", stderr)
	}
	if names := dirEntryNames(t, dir); names != nil {
		t.Fatalf("output directory %s holds %v; nothing should be written when a later host fails", dir, names)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("output directory %s exists (err=%v); the run created it, so discarding must remove it", dir, err)
	}
}
