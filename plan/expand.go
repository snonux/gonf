package plan

import (
	"fmt"
	"os"
	"strings"
)

// ExpandPath expands ${TOKEN} placeholders in plan paths for the destination
// host. Known tokens are resolved against the live environment; unknown or
// malformed tokens return an error.
func ExpandPath(p string) (string, error) {
	if !strings.Contains(p, "${") {
		return p, nil
	}

	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); {
		if i+1 < len(p) && p[i] == '$' && p[i+1] == '{' {
			end := strings.IndexByte(p[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("plan: unclosed path token in %q", p)
			}
			end += i + 2
			name := p[i+2 : end]
			if name == "" {
				return "", fmt.Errorf("plan: empty path token ${} in %q", p)
			}
			val, err := lookupPathToken(name)
			if err != nil {
				return "", err
			}
			b.WriteString(val)
			i = end + 1
			continue
		}
		b.WriteByte(p[i])
		i++
	}
	return b.String(), nil
}

func lookupPathToken(name string) (string, error) {
	switch name {
	case "HOME":
		return resolveHome()
	default:
		return "", fmt.Errorf("plan: unknown path token ${%s}", name)
	}
}

func resolveHome() (string, error) {
	if home := os.Getenv("HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("plan: path token ${HOME}: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("plan: path token ${HOME}: home directory not set")
	}
	return home, nil
}
