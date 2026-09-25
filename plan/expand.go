package plan

import (
	"fmt"

	"github.com/snonux/gonf/internal/pathtoken"
)

// ExpandPath expands ${TOKEN} placeholders in plan paths for the destination
// host. ${HOME} resolves to the applying process's $HOME (else its user
// database entry; empty or relative is refused); unknown or malformed tokens
// return an error. The token rules live in internal/pathtoken, shared with
// the controller-side source-path refusal and api.DestHome.
//
// Every destination path field is expanded by its handler before use: the
// path of every filesystem kind, link symlink/hardlink targets,
// link_if_exists target, command dir/creates, the path_exists predicate,
// and (schema v26, VersionHomeToken) config_set member paths, chroot and
// staging_dir.
func ExpandPath(p string) (string, error) {
	out, err := pathtoken.Expand(p)
	if err != nil {
		return "", fmt.Errorf("plan: %w", err)
	}
	return out, nil
}
