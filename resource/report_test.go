package resource_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
)

func TestDryRunDoesNotWrite(t *testing.T) {
	resource.ResetRepository()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	file.Present(path, options.WithContent("hello"))
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("dry-run should not create the file")
	}
}
