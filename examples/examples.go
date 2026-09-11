package examples

import (
	"os"

	. "github.com/snonux/gonf/api"
	. "github.com/snonux/gonf/api/options"
)

// Demo holds example task methods registered via RegisterMethods.
type Demo struct{}

func (Demo) DescFiles() string { return "Demo file and directory resources" }
func (Demo) Files() {
	File("/tmp/gonf_hello.txt", WithContent("Hello World!"))
	InstallFile("/tmp/gonf_example.conf", "assets/testfiles/test.tmpl")
	File("/tmp/gonf_secret.txt", WithContent("top secret"), WithMode(0o600))
	Dir("/tmp/gonf_dir", WithMode(0o755))

	File("/tmp/gonf_old.txt", IsAbsent)
	NoFile("/tmp/gonf_old_alt.txt")

	Dir(
		"/tmp/gonf_dir_from_source",
		WithSource("assets/testfiles"),
		WithPrune,
		WithFileMode(0o644),
	)

	_ = os.MkdirAll("/tmp/gonf_stale_dir/nested", 0o755)
	Dir("/tmp/gonf_stale_dir", IsAbsent, WithPrune)
	_ = os.MkdirAll("/tmp/gonf_stale_dir_alt/nested", 0o755)
	NoDir("/tmp/gonf_stale_dir_alt", WithPrune)

	_ = os.Mkdir("/tmp/gonf_stale_empty_dir", 0o755)
	Dir("/tmp/gonf_stale_empty_dir", IsAbsent)

	File(Elems(
		"/tmp/gonf_multi1.txt",
		"/tmp/gonf_multi2.txt",
	), WithContent("Multi-file content"), WithMode(0o644))

	Dir(Elems(
		"/tmp/gonf_multi_dir1",
		"/tmp/gonf_multi_dir2",
	), WithMode(0o755))

	NoFile(Elems(
		"/tmp/stale1.txt",
		"/tmp/stale2.txt",
	))

	_ = os.WriteFile("/tmp/gonf_line_base.conf", []byte("keep-me\nstale-line\n"), 0o644)
	File("/tmp/gonf_line_base.conf", WithoutLine("stale-line"), WithLine("desired-line"))
	File("/tmp/gonf_line_append.conf", WithLine("source-file rocky.conf"))

	SyncDir("/tmp/gonf_glob_dst", "assets/testfiles/*", WithMode(0o755), WithFileMode(0o644))
}

func (Demo) DescLinks() string { return "Demo symlink and hardlink resources" }
func (Demo) Links() {
	File("/tmp/gonf_hello.txt", WithContent("Hello World!"))

	Link("/tmp/gonf_link", WithSymlink("/tmp/gonf_hello.txt"))
	Link("/tmp/gonf_hardlink", WithHardlink("/tmp/gonf_hello.txt"))
	LinkIfExists("/tmp/gonf_link_if", "/tmp/gonf_hello.txt")

	_ = os.Symlink("/tmp/gonf_hello.txt", "/tmp/gonf_stale_link")
	Link("/tmp/gonf_stale_link", IsAbsent)
	_ = os.Symlink("/tmp/gonf_hello.txt", "/tmp/gonf_stale_link_alt")
	NoLink("/tmp/gonf_stale_link_alt")

	_ = os.Link("/tmp/gonf_hello.txt", "/tmp/gonf_stale_hardlink")
	Link("/tmp/gonf_stale_hardlink", IsAbsent)
}

func (Demo) DescDeps() string { return "Demo DependsOn ordering" }
func (Demo) Deps() {
	fooRes := File("/tmp/gonf_foo.txt", WithContent("foo"))
	File("/tmp/gonf_bar.txt", WithContent("bar"), DependsOn(fooRes))

	multiRes := File(Elems(
		"/tmp/gonf_dep1.txt",
		"/tmp/gonf_dep2.txt",
	), WithContent("dep"))
	File("/tmp/gonf_after_multi.txt", WithContent("after"), DependsOn(multiRes))
	Dir("/tmp/gonf_after_dir", DependsOn(fooRes, multiRes))
}

func (Demo) DescCommands() string { return "Demo Command resource with guards" }
func (Demo) Commands() {
	Command("touch", Elems("/tmp/gonf_cmd_marker"),
		Creates("/tmp/gonf_cmd_marker"),
		WithName("touch-marker"),
	)
	Command("true", nil,
		Unless("true", nil),
		WithName("unless-demo"),
	)

	EachKV(Elems(
		"demo.key", "demo-value",
		"demo.other", "other-value",
	), func(key, val string) {
		Command("true", nil,
			WithName("kv."+key+"="+val),
		)
	})
}
