package cmd

import (
	"os"
	"path/filepath"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

func TestCreatesSkipsWhenPathExists(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	marker := filepath.Join(dir, "done")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "should-not-exist")
	Present("touch", []string{out}, opt.Creates(marker), opt.WithName("creates-skip"))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("command should have been skipped, but %s exists", out)
	}
}

func TestCreatesRunsWhenPathMissing(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	marker := filepath.Join(dir, "missing")
	out := filepath.Join(dir, "created")

	Present("touch", []string{out}, opt.Creates(marker), opt.WithName("creates-run"))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("expected %s to exist: %v", out, err)
	}
}

func TestUnlessSkipsOnSuccess(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "should-not-exist")

	Present("touch", []string{out},
		opt.Unless("true", nil),
		opt.WithName("unless-skip"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("command should have been skipped")
	}
}

func TestUnlessRunsOnFailure(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "created")

	Present("touch", []string{out},
		opt.Unless("false", nil),
		opt.WithName("unless-run"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("expected %s: %v", out, err)
	}
}

func TestOnlyIfSkipsOnFailure(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "should-not-exist")

	Present("touch", []string{out},
		opt.OnlyIf("false", nil),
		opt.WithName("onlyif-skip"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("command should have been skipped")
	}
}

func TestOnlyIfRunsOnSuccess(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "created")

	Present("touch", []string{out},
		opt.OnlyIf("true", nil),
		opt.WithName("onlyif-run"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("expected %s: %v", out, err)
	}
}

func TestUnlessExpectStdout(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "should-not-exist")

	Present("touch", []string{out},
		opt.Unless("echo", []string{"hello"}, opt.ExpectStdout("hello")),
		opt.WithName("unless-stdout"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("command should have been skipped")
	}
}

func TestCommandFailure(t *testing.T) {
	resource.ResetRepository()
	Present("false", nil, opt.WithName("fail"))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected Apply to fail")
	}
}

func TestWithDir(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	Present("touch", []string{"in-dir"},
		opt.WithDir(dir),
		opt.WithName("with-dir"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "in-dir")); err != nil {
		t.Fatalf("expected file in dir: %v", err)
	}
}

func TestWithEnv(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	// sh -c writes $GONF_TEST_ENV into out
	Present("sh", []string{"-c", "printf '%s' \"$GONF_TEST_ENV\" > " + out},
		opt.WithEnv(map[string]string{"GONF_TEST_ENV": "from-gonf"}),
		opt.WithName("with-env"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from-gonf" {
		t.Fatalf("env = %q, want from-gonf", got)
	}
}

func TestDefaultName(t *testing.T) {
	if got := defaultName("echo", []string{"a", "b"}); got != "echo a b" {
		t.Fatalf("defaultName = %q", got)
	}
	if got := defaultName("true", nil); got != "true" {
		t.Fatalf("defaultName = %q", got)
	}
}
