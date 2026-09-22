package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/snonux/gonf/api/options"
	gexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource"
)

// An apply interrupted while a validator runs must not publish the
// candidate, even though the validator then passes: the live file stays as
// it was and the op fails with the cancellation.
func TestValidatedFileNotPublishedAfterInterrupt(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(gexec.BindContext(ctx))
	time.AfterFunc(200*time.Millisecond, cancel)
	validator := writeValidationScript(t, "sleep 1; exit 0")

	err := Ensure(target, WithContent("new"), WithValidation(validator, []string{CandidatePath}))
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "candidate not published") {
		t.Fatalf("err = %v, want canceled with candidate not published", err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "old" {
		t.Fatalf("live file = %q, %v; want the old content untouched", got, readErr)
	}
}

// Negative case: with a live bound context a passing validator publishes.
func TestValidatedFilePublishedWithLiveContext(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(gexec.BindContext(ctx))

	if err := Ensure(target, WithContent("new"), WithValidation("true", []string{CandidatePath})); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "new" {
		t.Fatalf("live file = %q, %v; want new", got, err)
	}
}
