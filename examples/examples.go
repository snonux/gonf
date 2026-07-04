package examples

import (
	"codeberg.org/snonux/gonf/internal/file"
)

func Run() error {
	// 1. Regular file with content
	file.Have("/tmp/gonf_hello.txt", file.WithContent("Hello World!"))

	// 2. Regular file from a source template
	file.Have("/tmp/gonf_example.conf", file.WithSource("assets/testfiles/test.tmpl"))

	// 3. Regular file with specific mode and owner
	file.Have(
		"/tmp/gonf_secret.txt",
		file.WithContent("top secret"),
		file.WithMode(0o600),
	)

	// 4. A directory
	file.Have("/tmp/gonf_dir", file.IsDirectory(), file.WithMode(0o755))

	// 5. A symlink
	file.Have("/tmp/gonf_link", file.IsSymlink("/tmp/gonf_hello.txt"))

	// 6. A hardlink
	file.Have("/tmp/gonf_hardlink", file.IsHardlink("/tmp/gonf_hello.txt"))

	// 7. Ensuring something is absent
	file.Have("/tmp/gonf_old.txt", file.IsAbsent())

	return nil
}
