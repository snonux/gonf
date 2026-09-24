// systemctl as a resource.Action: Timer and the service systemctl backend
// build their convergence steps from a Client's Command and run them through
// the shared runner resource.Converge, and DaemonReload renders its log text
// with Describe, so all three run and describe systemctl identically.

package systemd

import (
	"fmt"

	"github.com/snonux/gonf/resource"
)

var _ resource.Action = Command{}

// Command is the resource.Action for one systemctl invocation: its argument
// vector after "systemctl" (build it with Args to get --user handling) and
// the runner override (run) it was built with (Client.Command); the zero
// run reaches the real internal/exec runner. Built only through
// Client.Command (task 4e2: Command used to be a bare []string a caller
// could convert to directly, which left no room to carry a per-apply
// runner override).
type Command struct {
	args []string
	run  RunFunc
}

// Describe renders the log text of a systemctl invocation with args: would
// ("run systemctl [args]") follows the "dry-run: would " prefix in a dry
// run, did ("systemctl [args]") is logged after the command succeeded.
func Describe(args []string) (would, did string) {
	return fmt.Sprintf("run systemctl %v", args), fmt.Sprintf("systemctl %v", args)
}

// Do runs systemctl with the command's arguments through its runner; the
// errors are Client.Run's.
func (c Command) Do() error { return (Client{run: c.run}).Run(c.args...) }

// Describe returns the command's log text via the package-level
// systemd.Describe function.
func (c Command) Describe() (would, did string) { return Describe(c.args) }
