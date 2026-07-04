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

	return nil
}
