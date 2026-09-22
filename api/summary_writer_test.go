package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/snonux/gonf/internal/remote"
)

// fleetGroupCount is how many single-host clusters setupWideFleet puts in
// its fleet: enough concurrent groups that an unguarded shared summary
// writer reliably races under -race.
const fleetGroupCount = 8

// setupWideFleet registers fleetGroupCount single-host clusters c0..cN under
// the fleet "wide", so a fleet run fans out to that many concurrent groups.
func setupWideFleet(t *testing.T) {
	t.Helper()
	resetForHostsState(t)
	clusters := make([]ClusterRef, 0, fleetGroupCount)
	for i := range fleetGroupCount {
		h := Host(fmt.Sprintf("w%d", i), WithSSHHost(fmt.Sprintf("w%d.example", i)),
			WithValue(forHostsKey, [2]string{"1", "2"}))
		clusters = append(clusters, Cluster(fmt.Sprintf("c%d", i), h))
	}
	Fleet("wide", clusters...)
}

// TestFleetSummaryLinesShareUnsafeWriter drives a fleet run whose groups all
// finish at once and write their summary line through the same injected,
// non-concurrency-safe writer (a bytes.Buffer). Under -race an unguarded
// shared writer is reported as a data race; with the guard every group's
// line arrives whole, exactly once, and nothing is interleaved.
func TestFleetSummaryLinesShareUnsafeWriter(t *testing.T) {
	setupWideFleet(t)
	registerForHostsTask("iter", "c0")
	installModeRecorder(t)
	var buf bytes.Buffer
	old := pushOutput
	pushOutput = &buf
	t.Cleanup(func() { pushOutput = old })

	if err := PushFleetRun(context.Background(), "wide", "", 0, remote.DefaultHostTimeout, "iter"); err != nil {
		t.Fatalf("PushFleetRun: %v", err)
	}
	checkFleetSummaryLines(t, buf.String())
}

// checkFleetSummaryLines asserts out holds exactly one intact summary line
// per setupWideFleet cluster and nothing else.
func checkFleetSummaryLines(t *testing.T, out string) {
	t.Helper()
	line := regexp.MustCompile(`^pushed fleet-wide \(\d+ ops\) to (c\d+) \(1/1 hosts\)$`)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	var got []string
	for _, l := range lines {
		m := line.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("mangled summary line %q in output %q", l, out)
		}
		got = append(got, m[1])
	}
	sort.Strings(got)
	var want []string
	for i := range fleetGroupCount {
		want = append(want, fmt.Sprintf("c%d", i))
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("summary clusters = %v, want %v (output %q)", got, want, out)
	}
}

// errWriter fails every Write with its err, having written nothing.
type errWriter struct{ err error }

func (e errWriter) Write([]byte) (int, error) { return 0, e.err }

// TestLockedWriter pins lockedWriter's contract: concurrent Writes into a
// non-concurrency-safe destination each arrive whole, and the destination's
// result (count and error) is returned unchanged.
func TestLockedWriter(t *testing.T) {
	t.Run("concurrent writes stay whole", func(t *testing.T) {
		var buf bytes.Buffer
		w := newLockedWriter(&buf)
		const writers, perWriter = 16, 50
		var wg sync.WaitGroup
		for i := range writers {
			wg.Go(func() {
				for j := range perWriter {
					_, _ = fmt.Fprintf(w, "writer %02d line %02d\n", i, j)
				}
			})
		}
		wg.Wait()
		lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
		if len(lines) != writers*perWriter {
			t.Fatalf("got %d lines, want %d", len(lines), writers*perWriter)
		}
		whole := regexp.MustCompile(`^writer \d\d line \d\d$`)
		for _, l := range lines {
			if !whole.MatchString(l) {
				t.Fatalf("interleaved line %q", l)
			}
		}
	})
	t.Run("destination error is returned", func(t *testing.T) {
		want := errors.New("disk full")
		n, err := newLockedWriter(errWriter{want}).Write([]byte("x\n"))
		if n != 0 || !errors.Is(err, want) {
			t.Fatalf("Write = (%d, %v), want (0, %v)", n, err, want)
		}
	})
}
