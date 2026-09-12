package resource_test

import (
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/resource"
)

func TestAnyChangedExactID(t *testing.T) {
	resource.ResetReport()
	resource.Note("File[/tmp/a]", resource.StatusOK)
	if resource.AnyChanged("File[/tmp/a]") {
		t.Fatal("OK must not count as changed")
	}
	resource.Note("File[/tmp/a]", resource.StatusChanged)
	if !resource.AnyChanged("File[/tmp/a]") {
		t.Fatal("Changed must count")
	}
}

func TestAnyChangedDirectoryIncludesChildFiles(t *testing.T) {
	resource.ResetReport()
	dir := filepath.Join("/tmp", "units")
	resource.Note("Directory["+dir+"]", resource.StatusOK)
	if resource.AnyChanged("Directory[" + dir + "]") {
		t.Fatal("directory OK alone must not count")
	}
	resource.Note("File["+filepath.Join(dir, "x.timer")+"]", resource.StatusChanged)
	if !resource.AnyChanged("Directory[" + dir + "]") {
		t.Fatal("child File change under Directory must count")
	}
}

func TestAnyChangedWouldChange(t *testing.T) {
	resource.ResetReport()
	resource.Note("File[/tmp/b]", resource.StatusWouldChange)
	if !resource.AnyChanged("File[/tmp/b]") {
		t.Fatal("WouldChange must count")
	}
}
