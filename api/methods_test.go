package api

import (
	"os"
	"reflect"
	"testing"
)

func TestActivateWhenFilters(t *testing.T) {
	ResetTasks()

	Task("always", "a", func() {})
	Task("linux_only", "l", func() {}, WhenLinux())
	Task("fedora_only", "f", func() {}, WhenProfile("fedora"))
	Task("rocky_host", "r", func() {}, WhenHostnameContains("rocky"))

	Activate(Facts{Profile: "fedora", GOOS: "linux", Hostname: "earth"})
	got := taskNames(t)
	want := []string{"always", "fedora_only", "linux_only"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fedora/linux = %v, want %v", got, want)
	}

	Activate(Facts{Profile: "rocky", GOOS: "darwin", Hostname: "rocky-box"})
	got = taskNames(t)
	want = []string{"always", "rocky_host"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rocky/darwin = %v, want %v", got, want)
	}
}

func taskNames(t *testing.T) []string {
	t.Helper()
	infos := Tasks()
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = info.Name
	}
	return names
}

func TestRegisterMethods(t *testing.T) {
	ResetTasks()
	RegisterMethods(reflectHome{}, WithPrefix("home_"))
	Activate(Facts{GOOS: "linux", Profile: "fedora"})

	got := Matching("^home_")
	want := []string{"home_helix", "home_hexai"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Matching = %v, want %v", got, want)
	}

	infos := Tasks()
	var helixDesc string
	for _, info := range infos {
		if info.Name == "home_helix" {
			helixDesc = info.Description
		}
	}
	if helixDesc != "Install helix" {
		t.Fatalf("helix desc = %q", helixDesc)
	}

	Activate(Facts{GOOS: "darwin", Profile: "fedora"})
	got = Matching("^home_")
	want = []string{"home_helix"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("darwin Matching = %v, want %v", got, want)
	}
}

type reflectHome struct{}

func (reflectHome) Helix()            {}
func (reflectHome) DescHelix() string { return "Install helix" }

func (reflectHome) Hexai()                 {}
func (reflectHome) DescHexai() string      { return "Install hexai" }
func (reflectHome) WhenHexai(f Facts) bool { return f.GOOS == "linux" }

func TestRegisterMethodsGroupWhen(t *testing.T) {
	ResetTasks()
	RegisterMethods(reflectPkg{}, WithPrefix("pkg_"), WithGroupWhen(WhenProfile("fedora")))

	Activate(Facts{Profile: "fedora"})
	if got := Matching("^pkg_"); !reflect.DeepEqual(got, []string{"pkg_fedora"}) {
		t.Fatalf("fedora: %v", got)
	}

	Activate(Facts{Profile: "rocky"})
	if got := Matching("^pkg_"); len(got) != 0 {
		t.Fatalf("rocky should skip pkg: %v", got)
	}
}

type reflectPkg struct{}

func (reflectPkg) Fedora()            {}
func (reflectPkg) DescFedora() string { return "Fedora packages" }

func TestCamelToSnake(t *testing.T) {
	cases := map[string]string{
		"Helix":           "helix",
		"TmuxRocky":       "tmux_rocky",
		"FishCompletions": "fish_completions",
		"Ssh":             "ssh",
		"SystemdUser":     "systemd_user",
	}
	for in, want := range cases {
		if got := camelToSnake(in); got != want {
			t.Errorf("camelToSnake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCLIProfileActivates(t *testing.T) {
	ResetTasks()
	Task("pkg_fedora", "", func() {}, WhenProfile("fedora"))
	Task("pkg_rocky", "", func() {}, WhenProfile("rocky"))

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"gonf", "-profile=rocky", "-list"}

	code := CLI()
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	got := Matching("^pkg_")
	if !reflect.DeepEqual(got, []string{"pkg_rocky"}) {
		t.Fatalf("got %v", got)
	}
}
