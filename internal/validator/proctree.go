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
	// on a hung ps or an overloaded host; it adds at most this much to a
	// timed-out RunIn.
	freezeBudget = 2 * time.Second
)

// killTree is the timeout kill: it SIGSTOPs the validator (leader) and then
// every descendant it finds in the process table within freezeBudget, so
// none of them can fork or reparent a child out of reach while the tree is
// collected. It then SIGKILLs the stopped descendants children first and
// finally the leader, whose result it returns (the verdict logic in
// killForTimeout only cares about the leader). Killing children first keeps
// each parent stopped, so a killed child stays its zombie, and its pid stays
// taken, until the parent itself is killed after it: no pid in the list can
// be recycled before its SIGKILL.
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
// only after freezeBudget ran out, and descendants gonf may not signal
// (EPERM; a root gonf may signal every process). Pids come from a snapshot,
// so a descendant that exits on its own and has its pid reused between the
// snapshot and its SIGSTOP could be hit instead; the window is the time
// between reading the table and signalling, as for any pid-based kill.
func killTree(leader *os.Process) error {
	switch err := leader.Signal(syscall.SIGSTOP); {
	case errors.Is(err, os.ErrProcessDone):
		return err
	case err != nil:
		return leader.Kill()
	}
	ctx, cancel := context.WithTimeout(context.Background(), freezeBudget)
	defer cancel()
	stopped := freezeDescendants(ctx, leader.Pid, processTable, sigstop)
	for _, pid := range slices.Backward(stopped) {
		_ = unix.Kill(pid, unix.SIGKILL)
	}
	return leader.Kill()
}

// freezeDescendants stops (stop, SIGSTOP in killTree) every descendant of
// root found in the process table read by table, rereading it until a round
// finds nothing new (or maxFreezeRounds), and returns the stopped pids in the
// order stopped, every parent before its children. A table that cannot be
// read, or ctx ending (the freezeBudget), ends the search with what was found
// so far; table must honour ctx.
func freezeDescendants(ctx context.Context, root int, table func(context.Context) (map[int]int, error), stop func(pid int)) []int {
	seen := map[int]bool{}
	var stopped []int
	for range maxFreezeRounds {
		if ctx.Err() != nil {
			break
		}
		parents, err := table(ctx)
		if err != nil {
			break
		}
		fresh := false
		for _, pid := range descendants(parents, root) {
			if seen[pid] {
				continue
			}
			seen[pid] = true
			fresh = true
			stopped = append(stopped, pid)
			stop(pid)
		}
		if !fresh {
			break
		}
	}
	return stopped
}

// sigstop stops pid; a failure (it exited, EPERM) leaves it to its fate.
func sigstop(pid int) {
	_ = unix.Kill(pid, unix.SIGSTOP)
}

// descendants returns the descendants of root (not root itself) in the
// pid -> parent pid table parents, parents before their children.
func descendants(parents map[int]int, root int) []int {
	children := map[int][]int{}
	for pid, ppid := range parents {
		if pid != ppid {
			children[ppid] = append(children[ppid], pid)
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

// processTable returns every process's parent pid, keyed by pid. On Linux it
// reads /proc (no external tool needed, e.g. in minimal containers) and
// falls back to ps when that fails; elsewhere (the BSDs, macOS), whose /proc
// is absent or differently formatted, it asks ps. The /proc read is local
// and fast, so only the ps call is bounded by ctx.
func processTable(ctx context.Context) (map[int]int, error) {
	if runtime.GOOS == "linux" {
		if parents, err := procTable("/proc"); err == nil {
			return parents, nil
		}
	}
	return psTable(ctx)
}

// procTable reads the parent pid of every process from the Linux procfs
// mounted at root. Processes that vanish while it reads are skipped.
func procTable(root string) (map[int]int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	parents := map[int]int{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(root, entry.Name(), "stat"))
		if err != nil {
			continue
		}
		if ppid, ok := parseProcStatPPID(stat); ok {
			parents[pid] = ppid
		}
	}
	if len(parents) == 0 {
		return nil, errors.New("list processes: no process found in " + root)
	}
	return parents, nil
}

// parseProcStatPPID extracts the parent pid from a /proc/<pid>/stat line
// ("pid (comm) state ppid ..."). comm may contain spaces and parentheses, so
// the fields are taken after its last ')'.
func parseProcStatPPID(stat []byte) (int, bool) {
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	return ppid, err == nil
}

// psTable lists every process's parent pid with ps, whose "-A -o pid= -o
// ppid=" form is understood by procps (Linux), the BSDs and macOS. ps is
// killed when ctx ends.
func psTable(ctx context.Context) (map[int]int, error) {
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=", "-o", "ppid=").Output()
	if err != nil {
		return nil, fmt.Errorf("list processes with ps: %w", err)
	}
	return parsePsTable(out)
}

// parsePsTable parses ps output of "pid ppid" lines; malformed lines are
// skipped, and output without any valid line is an error.
func parsePsTable(out []byte) (map[int]int, error) {
	parents := map[int]int{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		pid, errPid := strconv.Atoi(fields[0])
		ppid, errPPID := strconv.Atoi(fields[1])
		if errPid == nil && errPPID == nil {
			parents[pid] = ppid
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse ps output: %w", err)
	}
	if len(parents) == 0 {
		return nil, errors.New("parse ps output: no process listed")
	}
	return parents, nil
}
