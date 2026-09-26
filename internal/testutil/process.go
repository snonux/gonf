package testutil

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"syscall"
)

// ProcessGone reports whether the process pid has exited: it no longer
// exists, or it is a zombie nobody has reaped yet. The latter happens in a
// container whose PID 1 does not reap orphans (a plain `docker run`), where
// a killed, reparented descendant stays a zombie and kill(pid, 0) still
// succeeds. Zombies are detected through /proc, so off Linux only a
// vanished process counts as gone.
func ProcessGone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state follows the parenthesized command name, which may itself
	// contain ") ".
	i := bytes.LastIndexByte(stat, ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}
