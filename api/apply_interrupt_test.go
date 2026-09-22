package api

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	gexec "github.com/snonux/gonf/internal/exec"
)

// lockedBuffer is a bytes.Buffer safe for the AfterFunc goroutine writing
// while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor polls cond for up to d.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// An interrupt while a validator runs prints the one-line notice; with no
// validator running, on a deadline, or once disarmed, nothing is printed.
func TestNoteValidatorWait(t *testing.T) {
	cases := []struct {
		name     string
		running  bool
		deadline bool
		disarm   bool
		want     bool
	}{
		{name: "interrupt during validator", running: true, want: true},
		{name: "interrupt without validator"},
		{name: "deadline during validator", running: true, deadline: true},
		{name: "disarmed before interrupt", running: true, disarm: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out lockedBuffer
			ctx, cancel := context.WithCancel(context.Background())
			if tc.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
			}
			defer cancel()
			stop := noteValidatorWait(ctx, &out, func() bool { return tc.running })
			if tc.disarm {
				stop()
			}
			if tc.deadline {
				<-ctx.Done() // the deadline, not cancel, must end ctx
			}
			cancel()
			wait := 200 * time.Millisecond
			if tc.want {
				wait = 2 * time.Second
			}
			got := waitFor(wait, func() bool { return strings.Contains(out.String(), "waiting for the running validator") })
			if got != tc.want {
				t.Fatalf("notice printed = %v, want %v (output %q)", got, tc.want, out.String())
			}
		})
	}
}

// The elevated wrapper's grace covers the command timeout (a validator the
// elevated child waits for) plus the child's own graceful stop, and follows
// a changed -cmd-timeout.
func TestElevatedCancelGraceCoversCommandTimeout(t *testing.T) {
	orig := CommandTimeout()
	t.Cleanup(func() { SetCommandTimeout(orig) })
	SetCommandTimeout(90 * time.Second)
	if got, want := elevatedCancelGrace(), 90*time.Second+2*gexec.CancelGrace; got != want {
		t.Fatalf("elevatedCancelGrace() = %v, want %v", got, want)
	}
}
