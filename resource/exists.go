package resource

import "os"

// Exists reports whether path can be stat'ed. It is a small shared helper
// for backend detection (service managers, package managers, unit
// locations); os.Stat follows symlinks, so dangling links report false,
// and every os.Stat error, not just "not found", reports false.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
