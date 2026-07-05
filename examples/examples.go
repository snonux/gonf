package examples

import (
	"os"

	"codeberg.org/snonux/gonf/internal/resource/dir"
	"codeberg.org/snonux/gonf/internal/resource/file"
	"codeberg.org/snonux/gonf/internal/resource/link"
	. "codeberg.org/snonux/gonf/internal/resource/opt"
)

func Run() error {
	// 1. Regular file with content
	file.Have("/tmp/gonf_hello.txt", WithContent("Hello World!"))

	// 2. Regular file from a source template
	file.Have("/tmp/gonf_example.conf", WithSource("assets/testfiles/test.tmpl"))

	// 3. Regular file with specific mode and owner
	file.Have(
		"/tmp/gonf_secret.txt",
		WithContent("top secret"),
		WithMode(0o600),
	)

	// 4. A directory
	dir.Have("/tmp/gonf_dir", WithMode(0o755))

	// 5. A symlink
	link.Have("/tmp/gonf_link", WithSymlink("/tmp/gonf_hello.txt"))

	// 6. A hardlink
	link.Have("/tmp/gonf_hardlink", WithHardlink("/tmp/gonf_hello.txt"))

	// 7. Ensuring something is absent (two equivalent styles; each resource
	// path may only be declared once per run, so they use distinct paths)
	file.Have("/tmp/gonf_old.txt", IsAbsent())
	file.Absent("/tmp/gonf_old_alt.txt") // Alternative way

	// 8. A directory tree copied from source, reconciled, with a distinct
	// file mode from the directory's own mode
	dir.Have(
		"/tmp/gonf_dir_from_source",
		WithSource("assets/testfiles"),
		WithPrune(),
		WithFileMode(0o644),
	)

	// 9. Recursively removing a directory tree. Each resource path can only
	// be declared once per run, so this pre-populates its own scratch tree
	// (rather than reusing #8's path) to give WithPrune's recursive removal
	// something real to demonstrate.
	_ = os.MkdirAll("/tmp/gonf_stale_dir/nested", 0o755)
	dir.Have("/tmp/gonf_stale_dir", IsAbsent(), WithPrune())
	_ = os.MkdirAll("/tmp/gonf_stale_dir_alt/nested", 0o755)
	dir.Absent("/tmp/gonf_stale_dir_alt", WithPrune()) // Alternative way

	// 10. Non-recursively removing an empty directory
	_ = os.Mkdir("/tmp/gonf_stale_empty_dir", 0o755)
	dir.Have("/tmp/gonf_stale_empty_dir", IsAbsent())

	// 11. Ensuring a symlink is absent
	_ = os.Symlink("/tmp/gonf_hello.txt", "/tmp/gonf_stale_link")
	link.Have("/tmp/gonf_stale_link", IsAbsent())
	_ = os.Symlink("/tmp/gonf_hello.txt", "/tmp/gonf_stale_link_alt")
	link.Absent("/tmp/gonf_stale_link_alt") // Alternative way

	// 12. Ensuring a hardlink is absent
	_ = os.Link("/tmp/gonf_hello.txt", "/tmp/gonf_stale_hardlink")
	link.Have("/tmp/gonf_stale_hardlink", IsAbsent())

	return nil
}
