package api

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestParsePushArgs(t *testing.T) {
	opts, pos := parsePushArgs([]string{"--", "-p", "2222", "rex@host", "home_bash"})
	if len(opts) != 2 || opts[0] != "-p" || opts[1] != "2222" {
		t.Fatalf("opts=%v", opts)
	}
	if len(pos) != 2 || pos[0] != "rex@host" || pos[1] != "home_bash" {
		t.Fatalf("pos=%v", pos)
	}
	_, pos = parsePushArgs([]string{"host", "t1", "t2"})
	if len(pos) != 3 {
		t.Fatalf("pos=%v", pos)
	}
}

func TestCLIPushStreamsToSSH(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	Task("push_demo", "", func() {
		File(filepath.Join(t.TempDir(), "x"), options.WithContent("via-push"))
	})

	oldRunner := sshRunner
	t.Cleanup(func() { sshRunner = oldRunner })

	var sawArgv []string
	var sawStdin []byte
	sshRunner = func(stdin io.Reader, argv []string) error {
		sawArgv = append([]string(nil), argv...)
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, stdin)
		sawStdin = buf.Bytes()
		return nil
	}

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "push", "-id", "demo", "user@host", "push_demo"}
	if code := CLI(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(sawArgv) < 3 || sawArgv[0] != "ssh" || sawArgv[1] != "user@host" {
		t.Fatalf("argv=%v", sawArgv)
	}
	if !strings.Contains(sawArgv[len(sawArgv)-1], "gonf apply") {
		t.Fatalf("remote cmd %q", sawArgv[len(sawArgv)-1])
	}
	if !bytes.Contains(sawStdin, []byte("GONF-PUSH/1")) {
		t.Fatalf("stdin missing magic: %q", sawStdin[:min(40, len(sawStdin))])
	}
	payload, err := plan.DecodePush(bytes.NewReader(sawStdin), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Ops) < 2 || payload.Ops[0].ID != "demo" {
		t.Fatalf("ops=%#v", payload.Ops)
	}
}

func TestCLIPushUsage(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "push", "onlyhost"}
	if code := CLI(); code != 2 {
		t.Fatalf("exit %d want 2", code)
	}
}
