package examples

import (
	"os"

	. "github.com/snonux/gonf/api"
	. "github.com/snonux/gonf/api/options"
)

// Register declares the demo tasks used by cmd/gonf.
func Register() {
	Task("demo_files", "Demo file and directory resources", demoFiles)
	Task("demo_links", "Demo symlink and hardlink resources", demoLinks)
	Task("demo_deps", "Demo DependsOn ordering", demoDeps)
	Task("demo_commands", "Demo Command resource with guards", demoCommands)
	Task("demo", "Run all demo_* tasks", func() {
		_ = Run(Matching("^demo_")...)
	})
}

func demoFiles() {
	File("/tmp/gonf_hello.txt", WithContent("Hello World!"))
	File("/tmp/gonf_example.conf", WithSource("assets/testfiles/test.tmpl"))
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

	// Line-in-file: ensure a line is present / absent idempotently.
	_ = os.WriteFile("/tmp/gonf_line_base.conf", []byte("keep-me\nstale-line\n"), 0o644)
	File("/tmp/gonf_line_base.conf", WithoutLine("stale-line"), WithLine("desired-line"))
	File("/tmp/gonf_line_append.conf", WithLine("source-file rocky.conf"))

	// Flat glob install into a destination directory.
	Dir("/tmp/gonf_glob_dst",
		WithSourceGlob("assets/testfiles/*"),
		WithMode(0o755),
		WithFileMode(0o644),
	)
}

func demoLinks() {
	// Ensure a target exists so symlink/hardlink demos have something to point at.
	File("/tmp/gonf_hello.txt", WithContent("Hello World!"))

	Link("/tmp/gonf_link", WithSymlink("/tmp/gonf_hello.txt"))
	Link("/tmp/gonf_hardlink", WithHardlink("/tmp/gonf_hello.txt"))

	_ = os.Symlink("/tmp/gonf_hello.txt", "/tmp/gonf_stale_link")
	Link("/tmp/gonf_stale_link", IsAbsent)
	_ = os.Symlink("/tmp/gonf_hello.txt", "/tmp/gonf_stale_link_alt")
	NoLink("/tmp/gonf_stale_link_alt")

	_ = os.Link("/tmp/gonf_hello.txt", "/tmp/gonf_stale_hardlink")
	Link("/tmp/gonf_stale_hardlink", IsAbsent)
}

func demoDeps() {
	fooRes := File("/tmp/gonf_foo.txt", WithContent("foo"))
	File("/tmp/gonf_bar.txt", WithContent("bar"), DependsOn(fooRes))

	multiRes := File(Elems(
		"/tmp/gonf_dep1.txt",
		"/tmp/gonf_dep2.txt",
	), WithContent("dep"))
	File("/tmp/gonf_after_multi.txt", WithContent("after"), DependsOn(multiRes))
	Dir("/tmp/gonf_after_dir", DependsOn(fooRes, multiRes))
}

func demoCommands() {
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
