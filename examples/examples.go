package examples

import (
	"os"

	. "codeberg.org/snonux/gonf/api"
	. "codeberg.org/snonux/gonf/api/options"
)

func Run() error {
	// 1. Regular file with content
	File("/tmp/gonf_hello.txt", WithContent("Hello World!"))

	// 2. Regular file from a source template
	File("/tmp/gonf_example.conf", WithSource("assets/testfiles/test.tmpl"))

	// 3. Regular file with specific mode and owner
	File(
		"/tmp/gonf_secret.txt",
		WithContent("top secret"),
		WithMode(0o600),
	)

	// 4. A directory
	Dir("/tmp/gonf_dir", WithMode(0o755))

	// 5. A symlink
	Link("/tmp/gonf_link", WithSymlink("/tmp/gonf_hello.txt"))

	// 6. A hardlink
	Link("/tmp/gonf_hardlink", WithHardlink("/tmp/gonf_hello.txt"))

	// 7. Ensuring something is absent (two equivalent styles; each resource
	// path may only be declared once per run, so they use distinct paths)
	File("/tmp/gonf_old.txt", IsAbsent)
	NoFile("/tmp/gonf_old_alt.txt") // Alternative way

	// 8. A directory tree copied from source, reconciled, with a distinct
	// file mode from the directory's own mode
	Dir(
		"/tmp/gonf_dir_from_source",
		WithSource("assets/testfiles"),
		WithPrune,
		WithFileMode(0o644),
	)

	// 9. Recursively removing a directory tree. Each resource path can only
	// be declared once per run, so this pre-populates its own scratch tree
	// (rather than reusing #8's path) to give WithPrune's recursive removal
	// something real to demonstrate.
	_ = os.MkdirAll("/tmp/gonf_stale_dir/nested", 0o755)
	Dir("/tmp/gonf_stale_dir", IsAbsent, WithPrune)
	_ = os.MkdirAll("/tmp/gonf_stale_dir_alt/nested", 0o755)
	NoDir("/tmp/gonf_stale_dir_alt", WithPrune) // Alternative way

	// 10. Non-recursively removing an empty directory
	_ = os.Mkdir("/tmp/gonf_stale_empty_dir", 0o755)
	Dir("/tmp/gonf_stale_empty_dir", IsAbsent)

	// 11. Ensuring a symlink is absent
	_ = os.Symlink("/tmp/gonf_hello.txt", "/tmp/gonf_stale_link")
	Link("/tmp/gonf_stale_link", IsAbsent)
	_ = os.Symlink("/tmp/gonf_hello.txt", "/tmp/gonf_stale_link_alt")
	NoLink("/tmp/gonf_stale_link_alt") // Alternative way

	// 12. Ensuring a hardlink is absent
	_ = os.Link("/tmp/gonf_hello.txt", "/tmp/gonf_stale_hardlink")
	Link("/tmp/gonf_stale_hardlink", IsAbsent)

	// 13. Package management
	Package("tig")           // Ensure installed (Present)
	Package("vim", IsLatest) // Ensure installed and latest version
	NoPackage("nano")        // Ensure absent

	// 14. Multi-resource declarations
	// Create multiple files with the same options
	File(Elems(
		"/tmp/gonf_multi1.txt",
		"/tmp/gonf_multi2.txt",
	), WithContent("Multi-file content"), WithMode(0o644))

	// Create multiple directories
	Dir(Elems(
		"/tmp/gonf_multi_dir1",
		"/tmp/gonf_multi_dir2",
	), WithMode(0o755))

	// Install multiple packages and ensure they are latest
	Package(Elems(
		"htop",
		"curl",
		"wget",
	), IsLatest)

	// Remove multiple packages
	NoPackage(Elems(
		"old-pkg1",
		"old-pkg2",
	))

	// Remove multiple files
	NoFile(Elems(
		"/tmp/stale1.txt",
		"/tmp/stale2.txt",
	))

	return Apply()
}
