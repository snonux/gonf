package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// Only a sticky "-apply-dir" chunk accepts a keyed GONF-PUSH/2 frame (task
// 0g2, sealed_sticky.go); every other "gonf apply -" path decodes with
// plan.DecodePush, which refuses it (task zf2) before extracting or
// applying anything: a keyed frame without -apply-dir fails closed,
// applies no op, and the refusal on stderr never carries the key.
func TestCLIApplyStdinRefusesKeyedPushFrame(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out")
	id, _, err := seal.GenerateEphemeral()
	if err != nil {
		t.Fatal(err)
	}
	line, err := seal.EncodeEphemeral(id)
	if err != nil {
		t.Fatal(err)
	}
	key, err := plan.NewPushKey(line)
	if err != nil {
		t.Fatal(err)
	}
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "v2"},
		{Op: plan.KindFile, ID: "File[out]", Path: target, Mode: "0600", Payload: plan.FilePayload{ContentB64: "eAo="}},
	}
	var frame bytes.Buffer
	if err := plan.EncodePushWithKey(&frame, ops, nil, key); err != nil {
		t.Fatal(err)
	}
	withStdinBytes(t, frame.Bytes())

	var code int
	stderr := testutil.CaptureStderr(t, func() {
		code = cliApply(context.Background(), []string{"-"})
	})
	if code != 1 || !strings.Contains(stderr, "does not accept") {
		t.Fatalf("apply(/2 frame) = %d, stderr %d bytes; want exit 1 and the keyed-frame refusal", code, len(stderr))
	}
	if strings.Contains(stderr, line) || strings.Contains(stderr, "AGE-SECRET") {
		t.Fatal("refusal leaks the key (stderr not echoed)")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("refused keyed frame still applied its op: %v", err)
	}
}

// withStdinBytes replaces os.Stdin, until t's cleanup, with a pipe already
// holding data and closed for writing (data must fit the pipe buffer).
func withStdinBytes(t *testing.T, data []byte) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		_ = r.Close()
	})
}
