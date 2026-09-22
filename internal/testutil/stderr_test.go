package testutil

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

// fakeTB is the part of testing.TB that CaptureStderr uses: Helper, Cleanup
// and Fatal. Cleanups are collected so the test decides when they run, and
// Fatal exits the calling goroutine the way testing.T.Fatal does. Any other
// method hits the nil embedded TB and panics, flagging an unexpected call.
type fakeTB struct {
	testing.TB
	mu       sync.Mutex
	cleanups []func()
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Cleanup(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanups = append(f.cleanups, fn)
}

func (f *fakeTB) Fatal(...any) { runtime.Goexit() }

// runCleanups runs the collected cleanups last-in first-out, like testing.
func (f *fakeTB) runCleanups() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
}

// CaptureStderr returns everything fn wrote to os.Stderr and puts the
// original os.Stderr back before returning.
func TestCaptureStderrCapturesAndRestores(t *testing.T) {
	orig := os.Stderr
	got := CaptureStderr(t, func() {
		if os.Stderr == orig {
			t.Error("os.Stderr not redirected while fn runs")
		}
		fmt.Fprint(os.Stderr, "hello ")
		fmt.Fprintln(os.Stderr, "stderr")
	})
	if got != "hello stderr\n" {
		t.Fatalf("captured %q, want %q", got, "hello stderr\n")
	}
	if os.Stderr != orig {
		t.Fatal("os.Stderr not restored after CaptureStderr returned")
	}
}

// When fn exits its goroutine early (t.Fatal inside fn), CaptureStderr never
// returns, but its cleanup must still restore os.Stderr and close the pipe's
// write end, so the reader goroutine sees EOF and exits instead of leaking.
func TestCaptureStderrFatalInsideFnDoesNotLeak(t *testing.T) {
	orig := os.Stderr
	before := runtime.NumGoroutine()
	tb := &fakeTB{}
	var captured *os.File
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		CaptureStderr(tb, func() {
			captured = os.Stderr
			fmt.Fprint(os.Stderr, "partial")
			tb.Fatal("boom")
		})
		t.Error("CaptureStderr returned although fn called Fatal")
	}()
	<-exited
	tb.runCleanups()

	if os.Stderr != orig {
		t.Fatal("os.Stderr not restored by the cleanup after Fatal in fn")
	}
	if _, err := captured.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("pipe write end still open after cleanup: Write error = %v", err)
	}
	// The write end is closed, so the reader goroutine must finish; wait for
	// the goroutine count to drop back (bounded, yielding instead of sleeping).
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("reader goroutine leaked: %d goroutines, want <= %d", runtime.NumGoroutine(), before)
		}
		runtime.Gosched()
	}
}
