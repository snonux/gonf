//go:build remote_smoke

package api

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/resource"
)

// Example:
//
//	GONF_REMOTE_HOST=rex@fishfinger.buetow.org GONF_REMOTE_PORT=2 GONF_REMOTE_PRIVILEGE=doas \
//	  go test -tags remote_smoke ./api/ -run RemoteSmoke -v
//	GONF_REMOTE_HOST=paul@pi0.lan GONF_REMOTE_PRIVILEGE=doas \
//	  go test -tags remote_smoke ./api/ -run RemoteSmoke -v
func TestRemoteSmokePush(t *testing.T) {
	host := os.Getenv("GONF_REMOTE_HOST")
	if host == "" {
		t.Skip("set GONF_REMOTE_HOST")
	}
	port := 0
	if p := os.Getenv("GONF_REMOTE_PORT"); p != "" {
		var err error
		port, err = strconv.Atoi(p)
		if err != nil {
			t.Fatalf("GONF_REMOTE_PORT: %v", err)
		}
	}
	mode := privilege.None
	if raw := os.Getenv("GONF_REMOTE_PRIVILEGE"); raw != "" {
		m, err := privilege.ParseMode(raw)
		if err != nil {
			t.Fatal(err)
		}
		mode = m
	} else if remoteOK(t, host, port, "doas -n true") {
		mode = privilege.Doas
	} else if remoteOK(t, host, port, "sudo -n true") {
		mode = privilege.Sudo
	}

	ResetTasks()
	ResetInventory()
	resource.ResetRepository()
	resource.SetDryRun(false)

	userPath := "/tmp/gonf-remote-smoke-user"
	privPath := "/tmp/gonf-remote-smoke-priv"
	Task("smoke_user", "", func() {
		File(userPath, options.WithContent("user-ok\n"))
	})
	Task("smoke_priv", "", func() {
		File(privPath, options.WithContent("priv-ok\n"))
	}, Privileged())

	old := sshRunner
	t.Cleanup(func() { sshRunner = old })
	sshRunner = func(stdin io.Reader, argv []string) error {
		if len(argv) >= 3 {
			remote := argv[len(argv)-1]
			argv = append(append([]string{}, argv[:len(argv)-1]...),
				"export PATH=$HOME/bin:$HOME/go/bin:$PATH; "+remote)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin = stdin
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	t.Logf("host=%s port=%d privilege=%s", host, port, mode)

	target := PushTarget{Host: host, Port: port, Privilege: mode}
	if err := PushTo(target, "smoke", "smoke_user"); err != nil {
		t.Fatalf("unprivileged push: %v", err)
	}
	out := remoteOut(t, host, port, "cat "+userPath)
	if out != "user-ok\n" {
		t.Fatalf("user file: %q", out)
	}

	if mode != privilege.Sudo && mode != privilege.Doas {
		t.Log("skipping privileged remote smoke (no passwordless sudo/doas)")
		return
	}

	var saw []string
	sshRunner = func(stdin io.Reader, argv []string) error {
		if len(argv) >= 3 {
			remote := argv[len(argv)-1]
			saw = append(saw, remote)
			argv = append(append([]string{}, argv[:len(argv)-1]...),
				"export PATH=$HOME/bin:$HOME/go/bin:$PATH; "+remote)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin = stdin
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	if err := PushTo(target, "smoke2", "smoke_user", "smoke_priv"); err != nil {
		t.Fatalf("mixed push: %v", err)
	}
	if len(saw) != 2 {
		t.Fatalf("expected 2 remote applies, got %v", saw)
	}
	if !strings.Contains(saw[0], "gonf apply") || strings.Contains(saw[0], "doas ") || strings.Contains(saw[0], "sudo ") {
		t.Fatalf("unpriv remote cmd=%q", saw[0])
	}
	wantPrefix := "doas "
	if mode == privilege.Sudo {
		wantPrefix = "sudo -n "
	}
	if !strings.HasPrefix(saw[1], wantPrefix) {
		t.Fatalf("priv remote cmd=%q want prefix %q", saw[1], wantPrefix)
	}

	out = remoteOut(t, host, port, "doas cat "+privPath)
	if !strings.Contains(out, "priv-ok") {
		t.Fatalf("priv file: %q", out)
	}
	owner := strings.TrimSpace(remoteOut(t, host, port, "doas ls -ln "+privPath))
	// OpenBSD/NetBSD ls -ln: permissions links uid gid …
	fields := strings.Fields(owner)
	if len(fields) < 4 || fields[2] != "0" {
		t.Fatalf("priv file listing=%q want uid 0", owner)
	}
}

func remoteOK(t *testing.T, host string, port int, remoteCmd string) bool {
	t.Helper()
	argv := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}
	if port > 0 {
		argv = append(argv, "-p", strconv.Itoa(port))
	}
	argv = append(argv, host, remoteCmd)
	return exec.Command("ssh", argv...).Run() == nil
}

func remoteOut(t *testing.T, host string, port int, remoteCmd string) string {
	t.Helper()
	argv := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}
	if port > 0 {
		argv = append(argv, "-p", strconv.Itoa(port))
	}
	argv = append(argv, host, remoteCmd)
	out, err := exec.Command("ssh", argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh %s %q: %v (%s)", host, remoteCmd, err, out)
	}
	return string(out)
}