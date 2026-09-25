package api

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/pathtoken"
	"github.com/snonux/gonf/plan"
)

// Expand resolves a leading "~" or "~/" to the CONTROLLER's $HOME, like
// Home. Other paths are returned cleaned via filepath.Clean, which keeps a
// leading ${HOME} token (DestHome) intact.
func Expand(path string) string {
	if path == "~" {
		return homeDir()
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir(), path[2:])
	}
	return filepath.Clean(path)
}

// Home joins elem under $HOME of the CONTROLLER, the host where the recipe
// runs (filepath.Join). Use it for paths read there: SyncDir/InstallFile
// sources and the recipe's own checkout. For a destination path, the place
// a plan applies to, use DestHome: Home would record the controller's home
// literally, which is wrong whenever the destination's home differs (e.g.
// /Users/paul on macOS versus /home/paul on Linux).
func Home(elem ...string) string {
	parts := make([]string, 0, 1+len(elem))
	parts = append(parts, homeDir())
	parts = append(parts, elem...)
	return filepath.Join(parts...)
}

// DestHome joins elem under the DESTINATION's home: it returns the path
// token "${HOME}" followed by the cleaned elem, and the destination expands
// the token at apply (to the applying process's $HOME, see plan.ExpandPath).
// Use it for every target path (File, Dir, Link and link targets, SyncDir/
// InstallFile destinations, WhenPathExists, ...); sources read on the
// controller take Home instead and refuse the token.
//
// elem that escapes the home (e.g. "..") is a declaration error: the token
// could not survive the join, and the path would silently leave the home.
func DestHome(elem ...string) string {
	joined := filepath.Join(append([]string{pathtoken.Home}, elem...)...)
	if joined != pathtoken.Home && !strings.HasPrefix(joined, pathtoken.Home+"/") {
		declerr.Reportf("DestHome(%q): the path escapes the home directory", elem)
	}
	return joined
}

// localPath expands the path tokens of a destination path p for a
// decision the direct (non-recording) mode makes on this host, which is
// then the destination. A token that cannot be expanded is a declaration
// error; p is returned unexpanded, so the caller's probe simply finds
// nothing there.
func localPath(p string) string {
	out, err := plan.ExpandPath(p)
	if err != nil {
		declerr.Reportf("%s: %w", p, err)
		return p
	}
	return out
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
