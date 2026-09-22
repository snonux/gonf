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
// (table readers, tree walk, freeze rounds, the identity check before the
// kill) in isolation.

// trackedPid is a pid a validator script recorded; gone is set once the
// test confirmed the process no longer exists, so cleanup never signals a
// pid that may since have been recycled.
type trackedPid struct {
	pid  int
	gone bool
}

// setTimeout sets the process-wide command timeout for one test.
func setTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := gexec.DefaultTimeout()
	t.Cleanup(func() { gexec.SetDefaultTimeout(prev) })
	gexec.SetDefaultTimeout(d)
}

// readPid reads the pid a validator script wrote to path and registers a
// cleanup that SIGKILLs it unless waitGone confirmed it gone, so a
// regression never leaves the process behind.
func readPid(t *testing.T, path string) *trackedPid {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("validator did not record a pid in time: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("bad pid %q: %v", raw, err)
	}
	tracked := &trackedPid{pid: pid}
	t.Cleanup(func() {
		if !tracked.gone {
			_ = unix.Kill(pid, unix.SIGKILL)
		}
	})
	return tracked
}

// waitGone polls until the process no longer exists (the killed process
// was reaped by whichever process it was reparented to), marking it gone,
// or fails after 10 seconds.
func waitGone(t *testing.T, p *trackedPid) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(p.pid, 0); errors.Is(err, unix.ESRCH) {
			p.gone = true
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("validator descendant %d still exists after the timeout", p.pid)
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
	if err := unix.Kill(readPid(t, pidFile).pid, 0); err != nil {
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

// procs builds a process table from pid -> parent pid, every process with
// the start time "t0" (the tree tests do not care about identity).
func procs(parents map[int]int) map[int]procInfo {
	table := make(map[int]procInfo, len(parents))
	for pid, ppid := range parents {
		table[pid] = procInfo{ppid: ppid, start: "t0"}
	}
	return table
}

// ids names the processes pids, each with the start time "t0" as procs
// gives it.
func ids(pids ...int) []procID {
	var out []procID
	for _, pid := range pids {
		out = append(out, procID{pid: pid, start: "t0"})
	}
	return out
}

// tableSeq returns a table reader that serves tables one per call and
// repeats the last one, counting the calls in *calls.
func tableSeq(calls *int, tables ...map[int]procInfo) func(context.Context) (map[int]procInfo, error) {
	return func(context.Context) (map[int]procInfo, error) {
		*calls++
		return tables[min(*calls, len(tables))-1], nil
	}
}

// descendants walks the whole subtree below root, parents first, and
// ignores unrelated processes and self-parented entries (pid 0 on some
// systems).
func TestDescendants(t *testing.T) {
	table := procs(map[int]int{0: 0, 1: 0, 10: 1, 11: 10, 12: 10, 13: 11, 20: 1, 21: 20})
	tests := []struct {
		root int
		want []int
	}{
		{10, []int{11, 12, 13}},
		{13, nil},
		{99, nil},
	}
	for _, tt := range tests {
		got := descendants(table, tt.root)
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
	tables := []map[int]procInfo{
		procs(map[int]int{10: 1, 11: 10}),
		procs(map[int]int{10: 1, 11: 10, 12: 11}),
		procs(map[int]int{10: 1, 11: 10, 12: 11}),
	}
	call := 0
	table := func(context.Context) (map[int]procInfo, error) {
		if call == len(tables) {
			return nil, errors.New("unexpected extra round")
		}
		call++
		return tables[call-1], nil
	}
	var stops []int
	got := freezeDescendants(context.Background(), 10, table, func(pid int) error { stops = append(stops, pid); return nil })
	if !reflect.DeepEqual(got, ids(11, 12)) || !reflect.DeepEqual(stops, []int{11, 12}) {
		t.Fatalf("stopped %v (calls %v), want 11, 12", got, stops)
	}
	if call != 3 {
		t.Fatalf("read the table %d times, want 3 (until nothing new)", call)
	}
	failing := func(context.Context) (map[int]procInfo, error) { return nil, errors.New("no table") }
	if got := freezeDescendants(context.Background(), 10, failing, func(int) error { t.Fatal("stopped without a table"); return nil }); got != nil {
		t.Fatalf("stopped %v without a table, want nothing", got)
	}
}

// Only descendants whose stop succeeded are returned for the SIGKILL: a pid
// whose SIGSTOP failed (ESRCH, it exited and may be reused; EPERM, gonf may
// not signal it) is not held in place, so killing that number later could hit
// an unrelated process. It is still not stopped again in a later round, and
// the search goes on with its (stoppable) children.
func TestFreezeDescendantsSkipsFailedStops(t *testing.T) {
	tests := []struct {
		name string
		fail map[int]error
		want []procID
	}{
		{"none fails", nil, ids(11, 12, 13)},
		{"exited (ESRCH)", map[int]error{12: unix.ESRCH}, ids(11, 13)},
		{"not permitted (EPERM)", map[int]error{11: unix.EPERM}, ids(12, 13)},
		{"all fail", map[int]error{11: unix.ESRCH, 12: unix.EPERM, 13: unix.ESRCH}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A chain, so the order is fixed (siblings come in map order):
			// 13 appears only in the second round.
			calls := 0
			table := tableSeq(&calls,
				procs(map[int]int{11: 10, 12: 11}),
				procs(map[int]int{11: 10, 12: 11, 13: 12}),
			)
			attempts := map[int]int{}
			stop := func(pid int) error {
				attempts[pid]++
				return tt.fail[pid]
			}
			got := freezeDescendants(context.Background(), 10, table, stop)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("returned %v for the kill, want only the stopped %v", got, tt.want)
			}
			for _, pid := range []int{11, 12, 13} {
				if attempts[pid] != 1 {
					t.Errorf("stop(%d) called %d times, want once", pid, attempts[pid])
				}
			}
		})
	}
}

// A pid is known together with its start time: when a descendant whose stop
// failed (it exited) has its number reused by a new descendant in a later
// round, the new process is stopped and killed, while the same process seen
// again (same start time) is not stopped twice.
func TestFreezeDescendantsStopsReusedPid(t *testing.T) {
	tests := []struct {
		name  string
		fail  error
		want  []procID
		stops int
	}{
		{"failed stop, then reused", unix.ESRCH, []procID{{11, "t0"}, {12, "t1"}}, 2},
		{"stopped, then reused", nil, []procID{{11, "t0"}, {12, "t0"}, {12, "t1"}}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			table := tableSeq(&calls,
				map[int]procInfo{11: {10, "t0"}, 12: {11, "t0"}},
				map[int]procInfo{11: {10, "t0"}, 12: {11, "t1"}},
			)
			failed := false
			attempts := 0
			stop := func(pid int) error {
				if pid != 12 {
					return nil
				}
				attempts++
				if !failed {
					failed = true
					return tt.fail
				}
				return nil
			}
			got := freezeDescendants(context.Background(), 10, table, stop)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("stopped %v, want %v", got, tt.want)
			}
			if attempts != tt.stops {
				t.Fatalf("stop(12) called %d times, want %d (once per process)", attempts, tt.stops)
			}
		})
	}
}

// killStopped kills children first and only the processes that still carry
// the recorded start time: a pid that is gone or reused (resumed by an
// unstopped ancestor, which let it exit) is skipped; without a current table
// every stopped process is killed.
func TestKillStopped(t *testing.T) {
	stopped := []procID{{11, "a"}, {12, "b"}, {13, "c"}}
	tests := []struct {
		name    string
		current map[int]procInfo
		err     error
		want    []int
	}{
		{"all unchanged", map[int]procInfo{11: {10, "a"}, 12: {11, "b"}, 13: {12, "c"}}, nil, []int{13, 12, 11}},
		{"one reused", map[int]procInfo{11: {10, "a"}, 12: {1, "other"}, 13: {12, "c"}}, nil, []int{13, 11}},
		{"one gone", map[int]procInfo{11: {10, "a"}, 12: {11, "b"}}, nil, []int{12, 11}},
		{"no table", nil, errors.New("no table"), []int{13, 12, 11}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var killed []int
			killStopped(stopped, tt.current, tt.err, func(pid int) { killed = append(killed, pid) })
			if !reflect.DeepEqual(killed, tt.want) {
				t.Fatalf("killed %v, want %v", killed, tt.want)
			}
		})
	}
}

// One deadline bounds the whole search: a table reader that hangs until ctx
// ends (like a stuck ps) stops it at the deadline, keeping what earlier
// rounds found, and no round starts once ctx has ended.
func TestFreezeDescendantsHonoursBudget(t *testing.T) {
	calls := 0
	slow := func(ctx context.Context) (map[int]procInfo, error) {
		calls++
		if calls == 1 {
			return procs(map[int]int{11: 10}), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := freezeDescendants(ctx, 10, slow, func(int) error { return nil })
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("search took %v, want it cut at the 100ms deadline", elapsed)
	}
	if !reflect.DeepEqual(got, ids(11)) || calls != 2 {
		t.Fatalf("stopped %v after %d reads, want 11 after 2", got, calls)
	}
	if got := freezeDescendants(ctx, 10, slow, func(int) error { return nil }); got != nil || calls != 2 {
		t.Fatalf("expired search stopped %v after %d reads, want nothing and no read", got, calls)
	}
}

// parseProcStat takes the parent pid and start time (fields 4 and 22)
// counted after the last ')' of comm, which may itself contain spaces and
// parentheses.
func TestParseProcStat(t *testing.T) {
	// rest holds fields 5..21 of a stat line, then the start time 4242.
	const rest = " 1 1 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 4242 1000"
	tests := []struct {
		stat string
		want procInfo
		ok   bool
	}{
		{"123 (sh) S 45" + rest, procInfo{45, "4242"}, true},
		{"7 (a b) c) R 1" + rest, procInfo{1, "4242"}, true},
		{"7 (sh) S 45 123 123 0", procInfo{}, false}, // no start time
		{"7 (sh)", procInfo{}, false},
		{"garbage", procInfo{}, false},
		{"7 (sh) S x" + rest, procInfo{}, false},
	}
	for _, tt := range tests {
		got, ok := parseProcStat([]byte(tt.stat))
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseProcStat(%q) = %v, %v; want %v, %v", tt.stat, got, ok, tt.want, tt.ok)
		}
	}
}

// parsePsTable reads "pid ppid lstart" lines, collapsing the start time's
// padding, keeps lines without a start time with an empty one, skips
// malformed lines and rejects output without any process.
func TestParsePsTable(t *testing.T) {
	out := "    1     0 Sun Sep 20 22:38:51 2026\n  42 1 Tue Sep  2 01:02:03 2026\n 43 1\nbogus\n 7 x\n\n"
	want := map[int]procInfo{
		1:  {0, "Sun Sep 20 22:38:51 2026"},
		42: {1, "Tue Sep 2 01:02:03 2026"},
		43: {1, ""},
	}
	if got, err := parsePsTable([]byte(out)); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePsTable = %v, %v; want %v", got, err, want)
	}
	if _, err := parsePsTable([]byte("bogus\n")); err == nil {
		t.Fatal("parsePsTable accepted output without a process")
	}
}

// Both real table readers see this test process with its real parent and a
// start time that stays the same across reads: /proc on Linux, ps
// everywhere it is installed.
func TestProcessTablesSeeThisProcess(t *testing.T) {
	readers := map[string]func(context.Context) (map[int]procInfo, error){"processTable": processTable}
	if runtime.GOOS == "linux" {
		readers["procTable"] = func(context.Context) (map[int]procInfo, error) { return procTable("/proc") }
	}
	if _, err := exec.LookPath("ps"); err == nil {
		readers["psTable"] = psTable
	}
	for name, read := range readers {
		first, err := read(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		self := first[os.Getpid()]
		if self.ppid != os.Getppid() || self.start == "" {
			t.Errorf("%s: this process = %+v, want parent %d and a start time", name, self, os.Getppid())
		}
		second, err := read(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if again := second[os.Getpid()].start; again != self.start {
			t.Errorf("%s: start time changed between reads: %q, then %q", name, self.start, again)
		}
	}
}
