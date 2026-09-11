package api

import (
	"os"
	"path/filepath"
	"strings"
)

// Expand resolves a leading "~" or "~/" to $HOME. Other paths are returned
// cleaned via filepath.Clean.
func Expand(path string) string {
	if path == "~" {
		return homeDir()
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir(), path[2:])
	}
	return filepath.Clean(path)
}

// Home joins elem under $HOME (filepath.Join).
func Home(elem ...string) string {
	parts := make([]string, 0, 1+len(elem))
	parts = append(parts, homeDir())
	parts = append(parts, elem...)
	return filepath.Join(parts...)
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
