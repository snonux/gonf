package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

func TestMapUname(t *testing.T) {
	t.Parallel()
	cases := []struct {
		sys, mach, wantOS, wantArch string
	}{
		{"Linux", "x86_64", "linux", "amd64"},
		{"OpenBSD", "amd64", "openbsd", "amd64"},
		{"NetBSD", "aarch64", "netbsd", "arm64"},
		{"FreeBSD", "arm64", "freebsd", "arm64"},
	}
	for _, tc := range cases {
		goos, err := mapUnameGOOS(tc.sys)
		if err != nil || goos != tc.wantOS {
			t.Fatalf("GOOS(%q)=%q %v, want %q", tc.sys, goos, err, tc.wantOS)
		}
		goarch, err := mapUnameGOARCH(tc.mach)
		if err != nil || goarch != tc.wantArch {
			t.Fatalf("GOARCH(%q)=%q %v, want %q", tc.mach, goarch, err, tc.wantArch)
		}
	}
}

func TestRemoteInstallCmdPrivilege(t *testing.T) {
	t.Parallel()
	if plan.CurrentVersion < 1 {
		t.Fatal("CurrentVersion")
	}
	cmd, err := remoteInstallCmd(PushTarget{User: "root", Privilege: privilege.Sudo}, "/tmp/a", "/usr/local/bin/gonf")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cmd, "sudo") {
		t.Fatalf("root install must not use sudo: %q", cmd)
	}
	cmd, err = remoteInstallCmd(PushTarget{User: "rex", Privilege: privilege.Doas}, "/tmp/a", "/usr/local/bin/gonf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cmd, "doas ") {
		t.Fatalf("doas install: %q", cmd)
	}
	cmd, err = remoteInstallCmd(PushTarget{User: "paul", Privilege: privilege.Sudo}, "/tmp/a", "/usr/local/bin/gonf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cmd, "sudo -n ") {
		t.Fatalf("sudo install: %q", cmd)
	}
}

func TestSCPArgvTranslatesSSHPort(t *testing.T) {
	t.Parallel()
	argv := scpArgv(PushTarget{
		Host: "r0.lan.buetow.org", User: "root",
		ExtraSSH: []string{"-p", "22", "-o", "StrictHostKeyChecking=yes"},
	}, "/tmp/gonf", "/tmp/gonf.new")
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "scp -p ") || strings.Contains(joined, " -p 22") {
		t.Fatalf("scp must not get ssh -p: %v", argv)
	}
	if !strings.Contains(joined, "-P 22") {
		t.Fatalf("want -P 22 in %v", argv)
	}
	if argv[len(argv)-2] != "/tmp/gonf" || !strings.HasSuffix(argv[len(argv)-1], ":/tmp/gonf.new") {
		t.Fatalf("paths: %v", argv)
	}
}

func TestBuildGonfCache(t *testing.T) {
	old := GoBuildRunner
	t.Cleanup(func() {
		GoBuildRunner = old
		gonfBuildMu.Lock()
		gonfBuildCache = map[string]string{}
		gonfBuildMu.Unlock()
	})

	var builds int
	GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		builds++
		return os.WriteFile(out, []byte("fake"), 0o755)
	}
	p1, err := buildGonf(context.Background(), "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := buildGonf(context.Background(), "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("builds = %d, want 1 (cache)", builds)
	}
	if p1 != p2 {
		t.Fatalf("cache paths differ: %q vs %q", p1, p2)
	}
	if filepath.Base(p1) != "gonf" {
		t.Fatalf("path = %q", p1)
	}
}
