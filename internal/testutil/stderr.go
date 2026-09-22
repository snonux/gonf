package testutil

import (
	"io"
	"os"
	"sync"
	"testing"
)

// CaptureStderr runs fn with os.Stderr redirected to a pipe and returns what
// fn wrote to it. Code under test prints its summary lines to os.Stderr
// directly, so swapping the file is the only seam; tests using it must not
// run in parallel.
//
// A goroutine drains the pipe while fn runs, so output larger than the pipe
// buffer cannot block fn. The restore (put os.Stderr back, close the write
// end) runs once: right after fn returns, or from t.Cleanup when fn exits
// early via t.Fatal (runtime.Goexit). Either way the write end is closed, so
// the reader goroutine sees EOF and exits instead of leaking.
func CaptureStderr(t testing.TB, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	restore := sync.OnceFunc(func() {
		os.Stderr = old
		_ = w.Close()
	})
	t.Cleanup(restore)
	done := make(chan string, 1) // buffered: the send never blocks after an early exit
	go func() {
		b, _ := io.ReadAll(r)
		_ = r.Close()
		done <- string(b)
	}()
	fn()
	restore()
	return <-done
}
