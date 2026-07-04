package examples

import (
	"fmt"

	"codeberg.org/snonux/gonf/internal/file"
)

func Run() error {
	if err := file.Have("/tmp/gonf_example.conf", file.WithSource("source://assets/testfiles/test.tmpl")); err != nil {
		return fmt.Errorf("failed to create example conf: %w", err)
	}

	if err := file.Have("/tmp/foo.txt", file.WithContent("hi")); err != nil {
		return fmt.Errorf("failed to create foo.txt: %w", err)
	}

	if err := file.Have("/tmp/gonf_example_dir", file.IsDirectory(), file.WithMode(0o755)); err != nil {
		return fmt.Errorf("failed to create example dir: %w", err)
	}

	if err := file.Have("/tmp/gonf_example_link", file.IsSymlink("/tmp/foo.txt")); err != nil {
		return fmt.Errorf("failed to create example symlink: %w", err)
	}

	if err := file.Have("/tmp/gonf_example_hardlink", file.Hardlink("/tmp/foo.txt")); err != nil {
		return fmt.Errorf("failed to create example hardlink: %w", err)
	}

	return nil
}
