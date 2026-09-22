package remote

import (
	"context"
	"os"
	"os/exec"
)

// The real cross-compile of the gonf binary for a remote platform: Pusher's
// default GoBuildRunner. Where the output lands and how it is cached and
// re-verified is crossbuild.go's and sync_buildcache.go's business.

// gonfCmdPackage is built for remote hosts when their plan schema is too old.
const gonfCmdPackage = "github.com/snonux/gonf/cmd/gonf"

// defaultGoBuildRunner cross-compiles a package. It is Pusher's default
// GoBuildRunner implementation (see NewPusher); tests override a Pusher's
// GoBuildRunner field directly instead of this function.
func defaultGoBuildRunner(ctx context.Context, goos, goarch, out, pkg string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, pkg)
	cmd.Env = append(os.Environ(),
		"GOOS="+goos,
		"GOARCH="+goarch,
		"CGO_ENABLED=0",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
