package testutil

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestProcessGone(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if ProcessGone(pid) {
		t.Fatal("running process reported gone")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// Not yet waited for: a zombie on Linux, where /proc shows it.
	if runtime.GOOS == "linux" {
		deadline := time.Now().Add(5 * time.Second)
		for !ProcessGone(pid) {
			if time.Now().After(deadline) {
				t.Fatal("killed, unreaped process not reported gone")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	_ = cmd.Wait()
	if !ProcessGone(pid) {
		t.Fatal("reaped process not reported gone")
	}
}
