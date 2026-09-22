package validator

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// A timed-out validator is killed together with the processes it started
// (task b82), so a wrapper script's hung child (e.g. nsd-checkzone) does not
// survive each apply and pile up on the host.
//
// The validator deliberately stays in gonf's process group rather than
// leading its own (Setpgid): that keeps every signal aimed at gonf's group
// reaching it exactly as before, i.e. a terminal's Ctrl-C, hangup and Ctrl-Z,
// and a SIGKILL to the whole group (timeout -s KILL, kill -9 -- -pgid, a CI
// job kill) that would otherwise orphan it with no timeout left, without
// gonf touching any process-wide signal disposition. The price is that the
// group cannot be the kill target, so killTree finds the descendants in the
// process table instead.

const (
	// maxFreezeRounds bounds how often freezeDescendants re-reads the
	// process table to catch processes forked while it was stopping their
	// parents. Each round stops everything it finds, so a later round only
	// has to catch what a not yet stopped descendant forked meanwhile; a
	// tree still growing after the last round (a fork bomb) keeps the
	// processes no round found; they are neither stopped nor killed.
	maxFreezeRounds = 8
	// freezeBudget bounds the whole descendant search of one timeout kill
	// (all rounds, including every ps call), so the validator is never kept
	// stopped for long: when it runs out, killTree kills what it found so
	// far. A /proc or ps read takes milliseconds, so the budget only matters
	// on a hung ps or an overloaded host.
	freezeBudget = 2 * time.Second
	// verifyBudget bounds the one table read with which killTree re-checks
	// the stopped descendants' identity right before killing them. Together
	// with freezeBudget it is the most a timeout kill adds to a timed-out
	// RunIn.
	verifyBudget = time.Second
)

// procInfo is one process-table entry: the parent pid and the start time.
// start is an opaque token only ever compared for equality: the starttime
// field of /proc/<pid>/stat on Linux (clock ticks since boot), ps's lstart
// (to the second) elsewhere, and empty when a ps prints none.
type procInfo struct {
	ppid  int
	start string
}

// procID identifies one process across table reads: a pid alone may be
// reused by another process, a pid with its start time names one process
// (to the start time's resolution).
type procID struct {
	pid   int
	start string
}

// killTree is the timeout kill: it SIGSTOPs the validator (leader) and then
// every descendant it finds in the process table within freezeBudget, so
// none of them can fork or reparent a child out of reach while the tree is
// collected. It then SIGKILLs the stopped descendants children first and
// finally the leader, whose result it returns (the verdict logic in
// killForTimeout only cares about the leader).
//
// Only descendants whose SIGSTOP succeeded are in the kill list (task t82):
// a pid whose SIGSTOP failed (ESRCH: it exited; EPERM) may already name
// another process by the time of the kill. A stopped descendant whose parent
// is stopped too is held in place: it cannot exit, and killing children
// first keeps its parent stopped, so a killed child stays its zombie, and
// its pid stays taken, until the parent itself is killed after it. That hold
// is not complete: a process gonf did not stop can resume (SIGCONT) a
// stopped descendant, which may then exit and have its pid reused before the
// kill. The prime example is an ancestor whose own SIGSTOP failed with
// EPERM: it keeps running (and is not killed), and as the parent it can also
// reap the child; any other process of the same user can resume it too.
// killTree therefore reads the table once more (within verifyBudget) right
// before the kills and skips every pid that is gone or whose start time no
// longer matches the one recorded when it was stopped (killStopped). The
// window left is the time between that read and the SIGKILL, plus the start
// time's resolution (one second where ps supplies it). When that read fails,
// the stopped descendants are killed unverified: leaving them stopped for
// good would be worse than that residual window.
//
// A leader that Wait already reaped yields os.ErrProcessDone without
// signalling anything: its pid may already name an unrelated process, and
// its orphaned descendants are no longer identifiable. When the leader
// cannot be stopped (EPERM, e.g. a validator run through sudo/doas by a
// non-root gonf) or the process table cannot be read, killTree falls back to
// killing the leader alone.
//
// Not reached: descendants that were already reparented away from the tree
// before the timeout (a double fork, a daemonizing child), descendants found
// only after freezeBudget ran out, descendants whose SIGSTOP failed (EPERM,
// gonf may not signal them, while a root gonf may signal every process; or
// ESRCH, they exited), and stopped descendants that are gone or changed
// identity at the final check (resumed by another process, see above). Pids
// come from a snapshot, so a descendant that exits on its own and has its
// pid reused between the snapshot and its SIGSTOP could be stopped instead;
// the final check then spares it from the kill when its start time differs,
// but it stays stopped. That window is the time between reading the table
// and signalling, as for any pid-based kill.
func killTree(leader *os.Process) error {
	switch err := leader.Signal(syscall.SIGSTOP); {
	case errors.Is(err, os.ErrProcessDone):
		return err
	case err != nil:
		return leader.Kill()
	}
	freezeCtx, cancelFreeze := context.WithTimeout(context.Background(), freezeBudget)
	defer cancelFreeze()
	stopped := freezeDescendants(freezeCtx, leader.Pid, processTable, sigstop)
	if len(stopped) > 0 {
		verifyCtx, cancelVerify := context.WithTimeout(context.Background(), verifyBudget)
		defer cancelVerify()
		current, err := processTable(verifyCtx)
		killStopped(stopped, current, err, sigkill)
	}
	return leader.Kill()
}

// freezeDescendants stops (stop, SIGSTOP in killTree) every descendant of
// root found in the process table read by table, rereading it until a round
// finds nothing new (or maxFreezeRounds), and returns the stopped processes
// in the order stopped, every parent before its children, each with the
// start time the table gave it.
//
// A process is known by its pid and start time (procID), so a pid that a
// new descendant reuses during the search counts as new and is stopped too,
// even when the process that had the number before was already seen. A
// process whose stop fails is tried once and left out of the result: it is
// not held in place (ESRCH: it exited, so its number may be reused; EPERM:
// gonf may not signal it), so a later SIGKILL of that number could hit an
// unrelated process (task t82). Its children are still looked for and
// stopped. A table that cannot be read, or ctx ending (the freezeBudget),
// ends the search with what was found so far; table must honour ctx.
func freezeDescendants(ctx context.Context, root int, table func(context.Context) (map[int]procInfo, error), stop func(pid int) error) []procID {
	seen := map[procID]bool{}
	var stopped []procID
	for range maxFreezeRounds {
		if ctx.Err() != nil {
			break
		}
		procs, err := table(ctx)
		if err != nil {
			break
		}
		fresh := false
		for _, pid := range descendants(procs, root) {
			id := procID{pid: pid, start: procs[pid].start}
			if seen[id] {
				continue
			}
			// seen even when the stop fails, so a later round neither
			// retries this process nor counts it as new; only a process
			// that was really stopped goes into the kill list.
			seen[id] = true
			fresh = true
			if stop(pid) == nil {
				stopped = append(stopped, id)
			}
		}
		if !fresh {
			break
		}
	}
	return stopped
}

// killStopped kills (kill, SIGKILL in killTree) the stopped processes in
// reverse order, children before their parents, skipping every one whose pid
// is missing from the current table or now has another start time: that
// number no longer names the process that was stopped. When the table could
// not be read (tableErr), nothing can be checked and every one is killed.
func killStopped(stopped []procID, current map[int]procInfo, tableErr error, kill func(pid int)) {
	for _, id := range slices.Backward(stopped) {
		if tableErr == nil {
			if now, ok := current[id.pid]; !ok || now.start != id.start {
				continue
			}
		}
		kill(id.pid)
	}
}

// sigstop stops pid and returns the kill error; a failure (it exited, EPERM)
// leaves the process to its fate, and freezeDescendants then does not kill it.
func sigstop(pid int) error {
	return unix.Kill(pid, unix.SIGSTOP)
}

// sigkill kills pid; a failure (it is gone, EPERM) leaves nothing to do.
func sigkill(pid int) {
	_ = unix.Kill(pid, unix.SIGKILL)
}

// descendants returns the descendants of root (not root itself) in the
// process table procs, parents before their children.
func descendants(procs map[int]procInfo, root int) []int {
	children := map[int][]int{}
	for pid, p := range procs {
		if pid != p.ppid {
			children[p.ppid] = append(children[p.ppid], pid)
		}
	}
	var found []int
	queue := []int{root}
	for len(queue) > 0 {
		next := children[queue[0]]
		queue = queue[1:]
		found = append(found, next...)
		queue = append(queue, next...)
	}
	return found
}

// processTable returns every process's parent pid and start time, keyed by
// pid. On Linux it reads /proc (no external tool needed, e.g. in minimal
// containers) and falls back to ps when that fails; elsewhere (the BSDs,
// macOS), whose /proc is absent or differently formatted, it asks ps. The
// /proc read is local and fast, so only the ps call is bounded by ctx.
func processTable(ctx context.Context) (map[int]procInfo, error) {
	if runtime.GOOS == "linux" {
		if procs, err := procTable("/proc"); err == nil {
			return procs, nil
		}
	}
	return psTable(ctx)
}

// procTable reads the parent pid and start time of every process from the
// Linux procfs mounted at root. Processes that vanish while it reads are
// skipped.
func procTable(root string) (map[int]procInfo, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	procs := map[int]procInfo{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(root, entry.Name(), "stat"))
		if err != nil {
			continue
		}
		if p, ok := parseProcStat(stat); ok {
			procs[pid] = p
		}
	}
	if len(procs) == 0 {
		return nil, errors.New("list processes: no process found in " + root)
	}
	return procs, nil
}

// parseProcStat extracts the parent pid (field 4) and the start time (field
// 22, clock ticks since boot) from a /proc/<pid>/stat line ("pid (comm)
// state ppid ..."). comm may contain spaces and parentheses, so the fields
// are counted after its last ')', where field 3 (state) comes first.
func parseProcStat(stat []byte) (procInfo, bool) {
	const ppidField, startField = 4 - 3, 22 - 3
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return procInfo{}, false
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) <= startField {
		return procInfo{}, false
	}
	ppid, err := strconv.Atoi(fields[ppidField])
	if err != nil {
		return procInfo{}, false
	}
	return procInfo{ppid: ppid, start: fields[startField]}, true
}

// psTable lists every process's parent pid and start time with ps, whose
// "-A -o pid= -o ppid= -o lstart=" form is understood by procps (Linux), the
// BSDs and macOS. LC_ALL=C keeps lstart in one fixed format. ps is killed
// when ctx ends.
func psTable(ctx context.Context) (map[int]procInfo, error) {
	cmd := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list processes with ps: %w", err)
	}
	return parsePsTable(out)
}

// parsePsTable parses ps output of "pid ppid lstart" lines. The start time
// is the rest of the line after the parent pid with its runs of spaces
// collapsed (lstart pads a one-digit day), and empty when a ps prints none
// (then the pid alone identifies a process). Malformed lines are skipped,
// and output without any valid line is an error.
func parsePsTable(out []byte) (map[int]procInfo, error) {
	procs := map[int]procInfo{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		pid, errPid := strconv.Atoi(fields[0])
		ppid, errPPID := strconv.Atoi(fields[1])
		if errPid == nil && errPPID == nil {
			procs[pid] = procInfo{ppid: ppid, start: strings.Join(fields[2:], " ")}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse ps output: %w", err)
	}
	if len(procs) == 0 {
		return nil, errors.New("parse ps output: no process listed")
	}
	return procs, nil
}
