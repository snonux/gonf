package cli

import (
	"os"
	"reflect"
	"testing"

	"github.com/snonux/gonf/api"
)

func TestCLIProfileActivates(t *testing.T) {
	api.ResetTasks()
	api.Task("pkg_fedora", "", func() {}, api.WhenProfile("fedora"))
	api.Task("pkg_rocky", "", func() {}, api.WhenProfile("rocky"))

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"gonf", "-profile=rocky", "-list"}

	code := CLI()
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	// Task 8h2: -profile decides the destination-guarded mark, not
	// membership: both tasks stay matchable, pkg_fedora is marked.
	got := api.Matching("^pkg_")
	if !reflect.DeepEqual(got, []string{"pkg_fedora", "pkg_rocky"}) {
		t.Fatalf("got %v", got)
	}
	guards := map[string]string{}
	for _, info := range api.Tasks() {
		guards[info.Name] = info.DestinationGuard
	}
	if want := map[string]string{"pkg_fedora": "profile=fedora", "pkg_rocky": ""}; !reflect.DeepEqual(guards, want) {
		t.Fatalf("guards = %v, want %v", guards, want)
	}
}

func TestCLIList(t *testing.T) {
	api.ResetTasks()
	api.Task("alpha", "first", func() {})
	api.Task("beta", "second", func() {})

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"gonf", "-list"}

	code := CLI()
	if code != 0 {
		t.Fatalf("CLI exit = %d, want 0", code)
	}
}

func TestCLIRequiresTask(t *testing.T) {
	api.ResetTasks()
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"gonf"}

	code := CLI()
	if code != 2 {
		t.Fatalf("CLI exit = %d, want 2", code)
	}
}
