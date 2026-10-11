// Package sourceexclude shares directory-sync exclusion rules between source
// packaging, destination copy and pruning.
package sourceexclude

import (
	"fmt"
	"path"
	"strings"
)

// Validate rejects malformed or non-relative patterns. Patterns use path.Match
// without backslash escapes or globstar; slashless patterns match any component.
func Validate(patterns []string) error {
	for _, pattern := range patterns {
		if pattern == "" || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "\\") || strings.Contains(pattern, "**") {
			return fmt.Errorf("invalid exclusion pattern %q: use relative slash-separated patterns without escapes or **", pattern)
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == "" || part == "." || part == ".." {
				return fmt.Errorf("invalid exclusion pattern %q: empty, . and .. components are not allowed", pattern)
			}
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("invalid exclusion pattern %q: %w", pattern, err)
		}
	}
	return nil
}

// Matches reports whether rel or an ancestor is excluded. rel is a slash path
// relative to the sync root. A trailing .tmpl also matches its rendered name.
// Callers validate patterns before walking; malformed patterns cannot match.
func Matches(patterns []string, rel string) bool {
	if rel == "." || rel == "" {
		return false
	}
	for rel != "." && rel != "" {
		for _, pattern := range patterns {
			candidate := rel
			if !strings.Contains(pattern, "/") {
				candidate = path.Base(rel)
			}
			for _, name := range []string{candidate, strings.TrimSuffix(candidate, ".tmpl")} {
				if ok, _ := path.Match(pattern, name); ok {
					return true
				}
			}
		}
		rel = path.Dir(rel)
	}
	return false
}
