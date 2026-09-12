//go:build remote_smoke

package api

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/resource"
)

// GONF_REMOTE_HOST=rocky go test -tags remote_smoke ./api/ -run RemoteSmoke -v
func TestRemoteSmokePush(t *testing.T) {
	host := os.Getenv("GONF_REMOTE_HOST")
	if host == "" {
		t.Skip("set GONF_REMOTE_HOST")
	}
	ResetTasks()
	ResetInventory()
	resource.ResetRepository()

	userPath := "/tmp/gonf-remote-smoke-user"
	privPath := "/tmp/gonf-remote-smoke-priv"
	Task("smoke_user", "", func() {
		File(userPath, options.WithContent("user-ok\n"))
	})
	Task("smoke_priv", "", func() {
		File(privPath, options.WithContent("priv-ok\n"))
	}, Privileged())

	// Prepend PATH so non-interactive ssh finds ~/go/bin/gonf.
	old := sshRunner
	t.Cleanup(func() { sshRunner = old })
	sshRunner = func(stdin io.Reader, argv []string) error {
		if len(argv) >= 3 {
			remote := argv[len(argv)-1]
			argv = append(append([]string{}, argv[:len(argv)-1]...),
				"export PATH=$HOME/go/bin:$PATH; "+remote)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin = stdin
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	mode := privilege.None
	if exec.Command("ssh", "-o", "BatchMode=yes", host, "sudo -n true").Run() == nil {
		mode = privilege.Sudo
	}
	t.Logf("host=%s privilege=%s", host, mode)

	if err := PushTo(PushTarget{Host: host, Privilege: mode}, "smoke", "smoke_user"); err != nil {
		t.Fatalf("unprivileged push: %v", err)
	}
	out, err := exec.Command("ssh", "-o", "BatchMode=yes", host, "cat "+userPath).CombinedOutput()
	if err != nil || string(out) != "user-ok\n" {
		t.Fatalf("user file: %q err=%v", out, err)
	}

	if mode != privilege.Sudo && mode != privilege.Doas {
		t.Log("skipping privileged remote smoke (no passwordless sudo/doas)")
		return
	}
	if err := PushTo(PushTarget{Host: host, Privilege: mode}, "smoke2", "smoke_user", "smoke_priv"); err != nil {
		t.Fatalf("mixed push: %v", err)
	}
	out, err = exec.Command("ssh", "-o", "BatchMode=yes", host, "cat "+privPath).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "priv-ok") {
		t.Fatalf("priv file: %q err=%v", out, err)
	}
}
