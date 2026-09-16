package api

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestPrivilegedTaskTagsElevate(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	Task("user_f", "", func() {
		File(filepath.Join(t.TempDir(), "u"), options.WithContent("u"))
	})
	Task("root_f", "", func() {
		File(filepath.Join(t.TempDir(), "r"), options.WithContent("r"))
	}, Privileged())

	dir := t.TempDir()
	ops, err := RecordPlan("t", dir, "user_f", "root_f")
	if err != nil {
		t.Fatal(err)
	}
	var sawUser, sawRoot bool
	for _, op := range ops {
		if op.Op != plan.KindFile {
			continue
		}
		if strings.HasSuffix(op.Path, "u") {
			sawUser = true
			if op.Elevate {
				t.Fatalf("user file elevated: %#v", op)
			}
		}
		if strings.HasSuffix(op.Path, "r") {
			sawRoot = true
			if !op.Elevate {
				t.Fatalf("root file not elevated: %#v", op)
			}
		}
	}
	if !sawUser || !sawRoot {
		t.Fatalf("ops=%#v", ops)
	}
	chunks := plan.SplitPrivilegeChunks(ops)
	if len(chunks) != 2 || chunks[0].Elevate || !chunks[1].Elevate {
		t.Fatalf("chunks=%#v", chunks)
	}
}

func TestPushSplitsPrivilegeChunks(t *testing.T) {
	ResetTasks()
	ResetInventory()
	resource.ResetRepository()
	Task("u", "", func() {
		File(filepath.Join(t.TempDir(), "u"), options.WithContent("u"))
	})
	Task("p", "", func() {
		File(filepath.Join(t.TempDir(), "p"), options.WithContent("p"))
	}, Privileged())

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	var remotes []string
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		remotes = append(remotes, argv[len(argv)-1])
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	err := PushTo(PushTarget{Host: "h.example", Privilege: privilege.Doas}, "demo", "u", "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(remotes) != 2 {
		t.Fatalf("remotes=%v", remotes)
	}
	if remotes[0] != "gonf apply -" {
		t.Fatalf("unpriv remote=%q", remotes[0])
	}
	if remotes[1] != "doas gonf apply -" {
		t.Fatalf("priv remote=%q", remotes[1])
	}
}

func TestPushPrivilegeNoneRejectsElevated(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	Task("p", "", func() {
		File(filepath.Join(t.TempDir(), "p"), options.WithContent("p"))
	}, Privileged())

	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}
	err := PushTo(PushTarget{Host: "h.example", Privilege: privilege.None}, "demo", "p")
	if err == nil {
		t.Fatal("expected error for privileged chunk without helper")
	}
}

func TestWithElevateOnCommand(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	Task("mixed", "", func() {
		Command("true", nil, options.WithElevate)
	})
	ops, err := RecordPlan("t", t.TempDir(), "mixed")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range ops {
		if op.Op == plan.KindCommand {
			found = true
			if !op.Elevate {
				t.Fatalf("command not elevated: %#v", op)
			}
		}
	}
	if !found {
		t.Fatal("no command op")
	}
}

// TestApplyChunksRefusesForwardCrossChunkDep pins the controller-side
// pre-flight: a dep recorded in a LATER privilege chunk fails ApplyChunks
// before ANY chunk is applied — no in-process chunk-0 apply, no elevated
// runner call, no marker mutation.
func TestApplyChunksRefusesForwardCrossChunkDep(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "chunks"},
		{Op: plan.KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[b]", Deps: []string{"Command[a]"}},
		{Op: plan.KindCommand, Bin: "true", ID: "Command[a]", Elevate: true},
	}
	old := elevatedApplyRunner
	t.Cleanup(func() { elevatedApplyRunner = old })
	elevatedApplyRunner = func(mode privilege.Mode, chunk []plan.Op, planDir string) error {
		t.Error("elevated runner must not be invoked before the dep pre-flight")
		return nil
	}
	err := ApplyChunks(ops, dir, privilege.Sudo)
	if err == nil || !strings.Contains(err.Error(), "later chunk 1") {
		t.Fatalf("want forward cross-chunk dep refusal, got %v", err)
	}
	if _, serr := os.Stat(marker); !os.IsNotExist(serr) {
		t.Fatal("no chunk may apply before the dep pre-flight refusal")
	}
}

// TestPushRefusesForwardCrossChunkDepsWithZeroSSH pins the push-side
// pre-flight: the same forward cross-chunk dep fails remote.PushChunks before any
// SSH traffic (no chunk upload, no blob upload).
func TestPushRefusesForwardCrossChunkDepsWithZeroSSH(t *testing.T) {
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "chunks"},
		{Op: plan.KindCommand, Bin: "true", ID: "Command[b]", Deps: []string{"Command[a]"}},
		{Op: plan.KindCommand, Bin: "true", ID: "Command[a]", Elevate: true},
	}
	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		t.Error("ssh must not be invoked for a plan that fails the dep pre-flight")
		return nil
	}
	err := remote.PushChunks(context.Background(), PushTarget{Host: "h.example", Privilege: privilege.Doas}, "demo", ops, nil)
	if err == nil || !strings.Contains(err.Error(), "later chunk 1") {
		t.Fatalf("want forward cross-chunk dep refusal, got %v", err)
	}
}

func TestApplyChunksElevatedRunner(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	Task("u", "", func() {
		File(path, options.WithContent("ok"))
	})
	Task("p", "", func() {
		File(filepath.Join(dir, "elev.txt"), options.WithContent("e"))
	}, Privileged())

	ops, err := RecordPlan("t", dir, "u", "p")
	if err != nil {
		t.Fatal(err)
	}
	var elevated int
	old := elevatedApplyRunner
	t.Cleanup(func() { elevatedApplyRunner = old })
	elevatedApplyRunner = func(mode privilege.Mode, chunk []plan.Op, planDir string) error {
		elevated++
		if mode != privilege.Sudo {
			t.Fatalf("mode=%v", mode)
		}
		return nil
	}
	if err := ApplyChunks(ops, dir, privilege.Sudo); err != nil {
		t.Fatal(err)
	}
	if elevated != 1 {
		t.Fatalf("elevated=%d", elevated)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "ok" {
		t.Fatalf("file=%q err=%v", raw, err)
	}
}
