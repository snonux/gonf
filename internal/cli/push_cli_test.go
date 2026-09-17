package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// hasPortPair reports whether argv contains an adjacent "-p port" pair.
func hasPortPair(argv []string, port string) bool {
	for i, a := range argv {
		if a == "-p" && i+1 < len(argv) && argv[i+1] == port {
			return true
		}
	}
	return false
}

// captureSSH installs a fake remote.SSHRunner recording every invocation.
func captureSSH(t *testing.T) *[]sshCall {
	t.Helper()
	old := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbe()
	})
	calls := &[]sshCall{}
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, stdin)
		*calls = append(*calls, sshCall{
			argv:   append([]string(nil), argv...),
			remote: argv[len(argv)-1],
			stdin:  buf.Bytes(),
		})
		return nil
	}
	return calls
}

// sshCall captures one fake remote.SSHRunner invocation.
type sshCall struct {
	argv   []string
	remote string // last argv element: the remote shell command
	stdin  []byte // payload streamed to ssh
}

func TestTakePushFlags(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		pf   []string
		rest []string
	}{
		{
			name: "id then ssh -p",
			in:   []string{"-id", "x", "-p", "2222", "host", "task"},
			pf:   []string{"-id", "x"},
			rest: []string{"-p", "2222", "host", "task"},
		},
		{
			name: "explicit -- separator",
			in:   []string{"-n", "--", "-p", "22", "host", "task"},
			pf:   []string{"-n"},
			rest: []string{"-p", "22", "host", "task"},
		},
		{
			name: "id equals form",
			in:   []string{"-id=demo", "host", "task"},
			pf:   []string{"-id=demo"},
			rest: []string{"host", "task"},
		},
		{
			name: "dry-run long",
			in:   []string{"-dry-run", "host", "task"},
			pf:   []string{"-dry-run"},
			rest: []string{"host", "task"},
		},
		{
			name: "lone -id kept for FlagSet",
			in:   []string{"-id"},
			pf:   []string{"-id"},
			rest: nil,
		},
		{
			name: "no push flags",
			in:   []string{"host", "task"},
			pf:   nil,
			rest: []string{"host", "task"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pf, rest := takePushFlags(tt.in)
			if !reflect.DeepEqual(pf, tt.pf) {
				t.Fatalf("pushFlags=%v want %v", pf, tt.pf)
			}
			if !reflect.DeepEqual(rest, tt.rest) {
				t.Fatalf("rest=%v want %v", rest, tt.rest)
			}
		})
	}
}

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
	api.ResetTasks()
	resource.ResetRepository()
	api.Task("push_demo", "", func() {
		api.File(filepath.Join(t.TempDir(), "x"), options.WithContent("via-push"))
	})

	oldRunner := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = oldRunner
		restoreProbe()
	})

	var sawArgv []string
	var sawStdin []byte
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
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
	if len(sawArgv) < 3 || sawArgv[0] != "ssh" || sawArgv[len(sawArgv)-2] != "user@host" {
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

func TestCLIPushWithSSHOpts(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	api.Task("push_opts", "", func() {})

	oldRunner := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = oldRunner
		restoreProbe()
	})
	var saw []string
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		saw = append([]string(nil), argv...)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	// Leading ssh opts are not push FlagSet flags (takePushFlags peels them).
	// The generated ConnectTimeout option sits between the ssh opts and the
	// destination, so match position-independently.
	if code := cliPush(context.Background(), []string{"-id", "x", "-p", "2222", "rex@host", "push_opts"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(saw) < 5 || !hasPortPair(saw, "2222") || saw[len(saw)-2] != "rex@host" {
		t.Fatalf("argv=%v", saw)
	}
}

func TestCLIPushGlobalDryRun(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	api.Task("push_dry", "", func() {})
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	oldRunner := remote.SSHRunner
	restoreProbe := remote.AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = oldRunner
		restoreProbe()
	})
	var saw []string
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		saw = append([]string(nil), argv...)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	if code := cliPush(context.Background(), []string{"host", "push_dry"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(saw) < 3 || saw[len(saw)-1] != "gonf apply -n -" {
		t.Fatalf("argv=%v want remote dry-run", saw)
	}
}

func TestCLIPushLoneID(t *testing.T) {
	if code := cliPush(context.Background(), []string{"-id"}); code != 2 {
		t.Fatalf("exit %d want 2", code)
	}
}
