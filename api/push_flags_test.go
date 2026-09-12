package api

import (
	"io"
	"reflect"
	"testing"

	"github.com/snonux/gonf/resource"
)

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

func TestCLIPushWithSSHOpts(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	Task("push_opts", "", func() {})

	oldRunner := sshRunner
	t.Cleanup(func() { sshRunner = oldRunner })
	var saw []string
	sshRunner = func(stdin io.Reader, argv []string) error {
		saw = append([]string(nil), argv...)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	// Leading ssh opts are not push FlagSet flags (takePushFlags peels them).
	if code := cliPush([]string{"-id", "x", "-p", "2222", "rex@host", "push_opts"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(saw) < 5 || saw[1] != "-p" || saw[2] != "2222" || saw[3] != "rex@host" {
		t.Fatalf("argv=%v", saw)
	}
}

func TestCLIPushGlobalDryRun(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	Task("push_dry", "", func() {})
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	oldRunner := sshRunner
	t.Cleanup(func() { sshRunner = oldRunner })
	var saw []string
	sshRunner = func(stdin io.Reader, argv []string) error {
		saw = append([]string(nil), argv...)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	if code := cliPush([]string{"host", "push_dry"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(saw) < 3 || saw[len(saw)-1] != "gonf apply -n -" {
		t.Fatalf("argv=%v want remote dry-run", saw)
	}
}

func TestCLIPushLoneID(t *testing.T) {
	if code := cliPush([]string{"-id"}); code != 2 {
		t.Fatalf("exit %d want 2", code)
	}
}
