//go:build unix

package api

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
)

func TestControllerSourceChecksRefuseFIFO(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if OptionalControllerDir(path, "Gonfy's calendar") || declerr.First() == nil {
		t.Fatal("FIFO accepted as directory")
	}
	ResetForTest()
	if RequireControllerFile(path, "Gonfy's banner", "Restore a regular file") != "" || declerr.First() == nil {
		t.Fatal("FIFO accepted as file")
	}
}
