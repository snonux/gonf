package options

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// acceptConfigSet completes the accept* helpers of options_test.go for the
// config-set family.
func acceptConfigSet(ConfigSetOption) {}

// TestWithSensitiveCompilesOnPayloadKinds is the compile-time half of the
// SensitiveOption contract: the one value is accepted by every
// payload-carrying family, and as a ConfigFile member option (a FileOption).
func TestWithSensitiveCompilesOnPayloadKinds(t *testing.T) {
	acceptSensitive := func(option SensitiveOption) {
		acceptFile(option)
		acceptDir(option)
		acceptPackage(option)
		acceptCron(option)
		acceptSystemdTimer(option)
		acceptCommand(option)
		acceptConfigSet(option)
	}
	acceptSensitive(WithSensitive)
	acceptConfigSet(ConfigFile("key", "/etc/key", WithContent("x"), WithSensitive))
}

// TestWithSensitiveDoesNotCompileOnPayloadFreeKinds is the negative half:
// passing WithSensitive to a kind whose op carries only identities and
// metadata is a type error, one per call, not a silent no-op or a runtime
// abort.
func TestWithSensitiveDoesNotCompileOnPayloadFreeKinds(t *testing.T) {
	const source = `package invalid

import (
	"github.com/snonux/gonf/resource/link"
	"github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/service"
	"github.com/snonux/gonf/resource/systemd"
	"github.com/snonux/gonf/resource/timer"
	"github.com/snonux/gonf/resource/user"
)

func invalid() {
	link.Present("/tmp/l", options.WithSymlink("/tmp/t"), options.WithSensitive)
	service.Present("sshd", options.WithSensitive)
	timer.Present("backup.timer", options.WithSensitive)
	systemd.Present(options.WithSensitive)
	user.Present("svc", options.WithSensitive)
}
`
	errs := typeErrors(t, source)
	lines := map[string]bool{}
	for _, e := range errs {
		if !strings.Contains(e.Msg, "WithSensitive") && !strings.Contains(e.Msg, "sensitiveOption") {
			t.Errorf("unexpected type error: %s: %s", e.Pos, e.Msg)
			continue
		}
		pos := e.Pos
		if i := strings.LastIndex(pos, ":"); i > 0 {
			pos = pos[:i] // file:line, without the column
		}
		lines[pos] = true
	}
	if len(lines) != 5 {
		t.Fatalf("got type errors on %d lines, want one on each of the 5 calls: %v", len(lines), errs)
	}
}

// typeErrors type-checks source as a throwaway module that requires this
// repository (via a replace directive) and returns its package errors.
func typeErrors(t *testing.T, source string) []packages.Error {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	root = filepath.Dir(filepath.Dir(root))
	tmp := t.TempDir()
	goMod := "module invalid\n\ngo 1.26.4\n\nrequire github.com/snonux/gonf v0.0.0\n\nreplace github.com/snonux/gonf => " + root + "\n"
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "invalid.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write invalid.go: %v", err)
	}
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps,
		Dir:  tmp,
		Env:  append(os.Environ(), "GOPACKAGESDRIVER=off"),
	}, ".")
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d packages, want 1", len(loaded))
	}
	return loaded[0].Errors
}
