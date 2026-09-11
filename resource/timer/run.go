package timer

import "github.com/snonux/gonf/internal/exec"

// runCmd executes an external command. Swapped in unit tests.
var runCmd = exec.Run
