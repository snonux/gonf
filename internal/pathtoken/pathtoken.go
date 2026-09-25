// Package pathtoken owns gonf's destination path tokens: the ${TOKEN}
// placeholders a recorded plan may carry in a destination path, which the
// DESTINATION expands at apply time. ${HOME} is the only token so far.
//
// It lives under internal so the three layers that need it share one
// definition without an import cycle: plan expands tokens at apply,
// resource/options refuses them in controller-side source paths, and api
// builds them (api.DestHome).
package pathtoken

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// Home is the destination home token. A plan path containing it names a
// place under the applying process's home directory on the destination.
const Home = "${HOME}"

// marker opens every token; a path without it carries no token.
const marker = "${"

// HasToken reports whether p contains a ${...} token opener, i.e. whether
// Expand would change or refuse it.
func HasToken(p string) bool { return strings.Contains(p, marker) }

// Expand replaces every ${TOKEN} in p with its value on the running host.
// Unknown, empty or unclosed tokens are errors, never passed through
// literally: a literal "${HOME}" directory is never what a recipe meant.
func Expand(p string) (string, error) {
	if !HasToken(p) {
		return p, nil
	}
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); {
		if !strings.HasPrefix(p[i:], marker) {
			b.WriteByte(p[i])
			i++
			continue
		}
		end := strings.IndexByte(p[i+len(marker):], '}')
		if end < 0 {
			return "", fmt.Errorf("unclosed path token in %q", p)
		}
		end += i + len(marker)
		val, err := lookup(p, p[i+len(marker):end])
		if err != nil {
			return "", err
		}
		b.WriteString(val)
		i = end + 1
	}
	return b.String(), nil
}

// lookup resolves one token name found in p.
func lookup(p, name string) (string, error) {
	switch name {
	case "":
		return "", fmt.Errorf("empty path token ${} in %q", p)
	case "HOME":
		return resolveHome()
	default:
		return "", fmt.Errorf("unknown path token ${%s}", name)
	}
}

// currentUserHome is the user-database lookup behind resolveHome's fallback;
// a variable so tests can simulate an account without a home.
var currentUserHome = func() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

// resolveHome returns the applying process's home: $HOME, else the user
// database entry of the process's own uid. Under an elevated apply
// (sudo/doas) that is whatever HOME the elevation tool left, usually
// root's; gonf deliberately does not guess a different user's home. An
// empty or relative home is refused, since expanding to it would silently
// retarget the path at "/" or the working directory.
func resolveHome() (string, error) {
	home := os.Getenv("HOME")
	if home == "" {
		h, err := currentUserHome()
		if err != nil {
			return "", fmt.Errorf("path token %s: $HOME unset and user lookup failed: %w", Home, err)
		}
		home = h
	}
	if home == "" {
		return "", fmt.Errorf("path token %s: home directory not set", Home)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("path token %s: home directory %q is not absolute", Home, home)
	}
	return home, nil
}
