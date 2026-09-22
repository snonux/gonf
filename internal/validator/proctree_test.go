package validator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	gexec "github.com/snonux/gonf/internal/exec"
)

// These tests pin the timeout kill of a validator's descendants (task b82):
// end to end through RunIn with real processes, and the process-tree pieces
// (table readers, tree walk, freeze rounds) in isolation.

// setTimeout sets the process-wide command timeout for one test.
func setTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := gexec.DefaultTimeout()
	t.Cleanup(func() { gexec.SetDefaultTimeout(prev) })
	gexec.SetDefaultTimeout(d)
}

// readPid reads the pid a validator script wrote to path and registers a
// cleanup that SIGKILLs it, so a regression never leaves the process behind.
func readPid(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("validator did not record a pid in time: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("bad pid %q: %v", raw, err)
	}
	t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
	return pid
}

// waitGone polls until pid no longer exists (the killed process was reaped
// by whichever process it was reparented to) or fails after 10 seconds.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("validator descendant %d still exists after the timeout", pid)
}

// A timed-out wrapper is killed together with its child and grandchild (the
// grandchild holds the output pipe): both are gone afterwards, and since no
// survivor holds the pipe RunIn returns without the WaitDelay drain that the
// validator-only kill needed. The 2s timeout leaves the shells ample time to
// record the pids before the deadline, even under -race.
func TestRunInTimeoutKillsDescendants(t *testing.T) {
	const timeout = 2 * time.Second
	setTimeout(t, timeout)
	dir := t.TempDir()
	childPid, grandchildPid := filepath.Join(dir, "child"), filepath.Join(dir, "grandchild")
	script := `sh -c 'sleep 30 & echo $! > "$1"; wait' inner "` + grandchildPid + `" &
echo $! > "` + childPid + `"
wait`
	start := time.Now()
	err := RunIn("", "sh", []string{"-c", script})
	elapsed := time.Since(start)
	child, grandchild := readPid(t, childPid), readPid(t, grandchildPid)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if limit := timeout + WaitDelay*3/4; elapsed > limit {
		t.Fatalf("RunIn took %v, want at most %v (no descendant left holding the pipe)", elapsed, limit)
	}
	waitGone(t, child)
	waitGone(t, grandchild)
}

// Descendants are killed on timeout only: a validator that exits on its own
// leaves its background child running (and not holding the pipe, so RunIn
// returns promptly with the validator's success).
func TestRunInLeavesDescendantsOfFinishedValidator(t *testing.T) {
	setTimeout(t, time.Minute)
	pidFile := filepath.Join(t.TempDir(), "child")
	script := `sleep 30 >/dev/null 2>&1 &
echo $! > "` + pidFile + `"
exit 0`
	if err := RunIn("", "sh", []string{"-c", script}); err != nil {
		t.Fatalf("err = %v, want success", err)
	}
	if err := unix.Kill(readPid(t, pidFile), 0); err != nil {
		t.Fatalf("background child of a finished validator was killed: %v", err)
	}
}

// Validators that finish before the deadline keep their own verdict and
// return promptly.
func TestRunInKeepsVerdictBeforeDeadline(t *testing.T) {
	setTimeout(t, time.Minute)
	tests := []struct {
		name   string
		script string
		code   int
	}{
		{"success", "echo ok; exit 0", 0},
		{"failure", "echo bad >&2; exit 3", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			err := RunIn("", "sh", []string{"-c", tt.script})
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Fatalf("RunIn took %v, want a prompt return", elapsed)
			}
			if tt.code == 0 {
				if err != nil {
					t.Fatalf("err = %v, want success", err)
				}
				return
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != tt.code || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want exit status %d and no timeout", err, tt.code)
			}
		})
	}
}

// killTree never signals a validator Wait already reaped: its pid may be
// reused, so it reports os.ErrProcessDone instead.
func TestKillTreeSkipsReapedLeader(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := killTree(cmd.Process); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("killTree on a reaped leader = %v, want os.ErrProcessDone", err)
	}
}

// descendants walks the whole subtree below root, parents first, and
// ignores unrelated processes and self-parented entries (pid 0 on some
// systems).
func TestDescendants(t *testing.T) {
	parents := map[int]int{0: 0, 1: 0, 10: 1, 11: 10, 12: 10, 13: 11, 20: 1, 21: 20}
	tests := []struct {
		root int
		want []int
	}{
		{10, []int{11, 12, 13}},
		{13, nil},
		{99, nil},
	}
	for _, tt := range tests {
		got := descendants(parents, tt.root)
		slices.Sort(got[:min(len(got), 2)]) // siblings come in map order
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("descendants(%d) = %v, want %v", tt.root, got, tt.want)
		}
	}
}

// freezeDescendants rereads the table until a round finds nothing new, so a
// child forked while its parent was being stopped is caught too; each pid is
// stopped once; a table error ends the search with what was found.
func TestFreezeDescendants(t *testing.T) {
	tables := []map[int]int{
		{10: 1, 11: 10},
		{10: 1, 11: 10, 12: 11},
		{10: 1, 11: 10, 12: 11},
	}
	call := 0
	table := func() (map[int]int, error) {
		if call == len(tables) {
			return nil, errors.New("unexpected extra round")
		}
		call++
		return tables[call-1], nil
	}
	var stops []int
	got := freezeDescendants(10, table, func(pid int) { stops = append(stops, pid) })
	if want := []int{11, 12}; !reflect.DeepEqual(got, want) || !reflect.DeepEqual(stops, want) {
		t.Fatalf("stopped %v (calls %v), want %v", got, stops, want)
	}
	if call != 3 {
		t.Fatalf("read the table %d times, want 3 (until nothing new)", call)
	}
	failing := func() (map[int]int, error) { return nil, errors.New("no table") }
	if got := freezeDescendants(10, failing, func(int) { t.Fatal("stopped without a table") }); got != nil {
		t.Fatalf("stopped %v without a table, want nothing", got)
	}
}

// parseProcStatPPID takes the parent pid after the last ')' of comm, which
// may itself contain spaces and parentheses.
func TestParseProcStatPPID(t *testing.T) {
	tests := []struct {
		stat string
		ppid int
		ok   bool
	}{
		{"123 (sh) S 45 123 123 0", 45, true},
		{"7 (a b) c) R 1 7 7", 1, true},
		{"7 (sh)", 0, false},
		{"garbage", 0, false},
		{"7 (sh) S x", 0, false},
	}
	for _, tt := range tests {
		ppid, ok := parseProcStatPPID([]byte(tt.stat))
		if ppid != tt.ppid || ok != tt.ok {
			t.Errorf("parseProcStatPPID(%q) = %d, %v; want %d, %v", tt.stat, ppid, ok, tt.ppid, tt.ok)
		}
	}
}

// parsePsTable reads "pid ppid" lines, skips malformed ones and rejects
// output without any process.
func TestParsePsTable(t *testing.T) {
	got, err := parsePsTable([]byte("    1     0\n  42 1\nbogus\n 7 x\n\n 8 1 extra\n"))
	if want := map[int]int{1: 0, 42: 1}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePsTable = %v, %v; want %v", got, err, want)
	}
	if _, err := parsePsTable([]byte("bogus\n")); err == nil {
		t.Fatal("parsePsTable accepted output without a process")
	}
}

// Both real table readers see this test process with its real parent: /proc
// on Linux, ps everywhere it is installed.
func TestProcessTablesSeeThisProcess(t *testing.T) {
	readers := map[string]func() (map[int]int, error){"processTable": processTable}
	if runtime.GOOS == "linux" {
		readers["procTable"] = func() (map[int]int, error) { return procTable("/proc") }
	}
	if _, err := exec.LookPath("ps"); err == nil {
		readers["psTable"] = psTable
	}
	for name, read := range readers {
		parents, err := read()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, want := parents[os.Getpid()], os.Getppid(); got != want {
			t.Errorf("%s: parent of %d = %d, want %d", name, os.Getpid(), got, want)
		}
	}
}
