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

// A DestHome resource is recorded as File[${HOME}/x] but notes its outcome
// under the path the apply touched. A watch on the recorded id must see it.
func TestAnyChangedExpandsHomeToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	resource.ResetReport()
	resource.Note("File["+home+"/x]", resource.StatusOK)
	if resource.AnyChanged("File[${HOME}/x]") {
		t.Fatal("OK under the expanded path must not count")
	}
	resource.Note("File["+home+"/x]", resource.StatusChanged)
	if !resource.AnyChanged("File[${HOME}/x]") {
		t.Fatal("change under the expanded path must fire a ${HOME} watch")
	}
	if !resource.AnyChanged("Directory[${HOME}]") {
		t.Fatal("change of a child file must fire a ${HOME} directory watch")
	}
	if resource.AnyChanged("File[${HOME}/y]") {
		t.Fatal("another path must not count")
	}
}
