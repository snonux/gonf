package validator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gexec "github.com/snonux/gonf/internal/exec"
)

// bindCtx binds ctx for the rest of the test (internal/exec BindContext).
func bindCtx(t *testing.T, ctx context.Context) {
	t.Helper()
	t.Cleanup(gexec.BindContext(ctx))
}

// Once the apply's bound context is done, no validator starts.
func TestValidatorNotStartedAfterInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bindCtx(t, ctx)
	marker := filepath.Join(t.TempDir(), "ran")

	err := Run("touch", []string{marker})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "before the validator started") {
		t.Fatalf("err = %v, want canceled before start", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("validator ran after the interrupt (stat: %v)", statErr)
	}
}

// A validator that passes after the interrupt has its verdict discarded.
func TestValidatorVerdictDiscardedAfterInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bindCtx(t, ctx)
	time.AfterFunc(100*time.Millisecond, cancel)

	err := Run("sh", []string{"-c", "sleep 0.5; exit 0"})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "verdict discarded") {
		t.Fatalf("err = %v, want the verdict discarded", err)
	}
}

// Negative case: a live bound context leaves the verdict alone, pass or fail.
func TestValidatorVerdictKeptWithLiveContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bindCtx(t, ctx)
	if err := Run("true", nil); err != nil {
		t.Fatalf("passing validator: %v", err)
	}
	if err := Run("false", nil); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("failing validator: err = %v, want its own failure", err)
	}
}
