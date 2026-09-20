package api

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
)

// TestConsumerMailDNSNSDPlan records the real frontend NSD consumer against
// this checkout. The consumer's recipe tree intentionally contains no tests;
// this fixture gives the library a regression boundary without reading a real
// TSIG key or modifying the configuration checkout.
func TestConsumerMailDNSNSDPlan(t *testing.T) {
	consumerSource := consumerGonfSource(t)
	consumer := filepath.Join(t.TempDir(), "consumer")
	if err := copyConsumerSource(consumer, consumerSource); err != nil {
		t.Fatalf("copy consumer source: %v", err)
	}

	secret := filepath.Join(consumer, "secrets", "frontends", "var", "nsd", "etc", "nsd_key.txt")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatalf("create synthetic secret directory: %v", err)
	}
	if err := os.WriteFile(secret, []byte("synthetic-nsd-key\n"), 0o600); err != nil {
		t.Fatalf("write synthetic secret: %v", err)
	}

	gonfRoot := filepath.Dir(filepath.Dir(sourceFile(t)))
	goMod := filepath.Join(consumer, "go.mod")
	mod, err := os.OpenFile(goMod, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open copied go.mod: %v", err)
	}
	if _, err := fmt.Fprintf(mod, "\nreplace github.com/snonux/gonf => %s\n", gonfRoot); err != nil {
		_ = mod.Close()
		t.Fatalf("point consumer at this checkout: %v", err)
	}
	if err := mod.Close(); err != nil {
		t.Fatalf("close copied go.mod: %v", err)
	}

	planDir := filepath.Join(t.TempDir(), "plan")
	cmd := exec.Command("go", "run", "./cmd/gonf", "plan", "-o", planDir, "frontends_nsd")
	cmd.Dir = consumer
	cmd.Env = withoutEnv(os.Environ(), "GOWORK")
	cmd.Env = append(cmd.Env, "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("record frontend NSD plan: %v\n%s", err, output)
	}

	raw, err := os.ReadFile(filepath.Join(planDir, "plan.jsonl"))
	if err != nil {
		t.Fatalf("read recorded plan: %v", err)
	}
	ops, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("decode recorded plan: %v", err)
	}

	const validatorID = "Command[validate-nsd-config]"
	const candidateKeyID = "File[/var/nsd/etc/gonf-validate/key.conf]"
	const candidateConfigID = "File[/var/nsd/etc/gonf-validate/nsd.conf]"
	candidateKey := consumerPlanOp(t, ops, plan.KindFile, candidateKeyID)
	candidateConfig := consumerPlanOp(t, ops, plan.KindFile, candidateConfigID)
	validator := consumerPlanOp(t, ops, plan.KindCommand, validatorID)
	if validator.Bin != "nsd-checkconf" || !slices.Equal(validator.Args, []string{"/var/nsd/etc/gonf-validate/nsd.conf"}) {
		t.Fatalf("NSD config validator = %#v", validator)
	}
	for _, id := range []string{candidateKey.ID, candidateConfig.ID} {
		if !slices.Contains(validator.Deps, id) {
			t.Errorf("NSD config validator dependencies = %v, want %s", validator.Deps, id)
		}
	}
	zoneChecks := 0
	for _, op := range ops {
		if op.Op != plan.KindCommand || !strings.HasPrefix(op.ID, "Command[validate-nsd-zone-") {
			continue
		}
		zoneChecks++
		if !slices.Contains(validator.Deps, op.ID) {
			t.Errorf("NSD config validator dependencies = %v, want %s", validator.Deps, op.ID)
		}
	}
	if zoneChecks == 0 {
		t.Fatal("frontend NSD plan did not validate any candidate zones")
	}

	candidateContent, err := plan.DecodeContentB64(candidateConfig.ContentB64)
	if err != nil {
		t.Fatalf("decode candidate NSD config: %v", err)
	}
	const directives = "server:\n\thide-version: yes\n\tverbosity: 1\n\tdatabase: \"\" # disable database\n\tdebug-mode: no\n\nremote-control:\n\tcontrol-enable: yes\n\tcontrol-interface: /var/run/nsd.sock\n"
	if !strings.Contains(string(candidateContent), directives) || strings.Contains(string(candidateContent), `\t`) {
		t.Fatalf("candidate NSD directives are malformed: %q", candidateContent)
	}

	liveResources := 0
	for _, op := range ops {
		if op.Op != plan.KindFile || !strings.HasPrefix(op.Path, "/var/nsd/") ||
			strings.Contains(op.Path, "/gonf-validate/") {
			continue
		}
		liveResources++
		if !slices.Contains(op.Deps, validatorID) {
			t.Errorf("live NSD resource %s dependencies = %v, want %s", op.ID, op.Deps, validatorID)
		}
	}
	if liveResources == 0 {
		t.Fatal("frontend NSD plan did not contain live NSD resources")
	}
}

func consumerGonfSource(t *testing.T) string {
	t.Helper()
	gonfRoot := filepath.Dir(filepath.Dir(sourceFile(t)))
	consumer := filepath.Join(filepath.Dir(gonfRoot), "conf", "gonf")
	if _, err := os.Stat(filepath.Join(consumer, "go.mod")); os.IsNotExist(err) {
		t.Skipf("consumer checkout is unavailable at %s", consumer)
	} else if err != nil {
		t.Fatalf("stat consumer checkout: %v", err)
	}
	return consumer
}

// copyConsumerSource copies the recipe code and assets, deliberately omitting
// local secrets and generated plans. The test supplies the only secret it
// needs after the copy.
func copyConsumerSource(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "secrets" && entry.IsDir() {
			return filepath.SkipDir
		}
		if relative == "plan.jsonl" || relative == "gonf" {
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("consumer source contains symlink %q", relative)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
}

func sourceFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	return file
}

func consumerPlanOp(t *testing.T, ops []plan.Op, kind plan.Kind, id string) plan.Op {
	t.Helper()
	for _, op := range ops {
		if op.Op == kind && op.ID == id {
			return op
		}
	}
	t.Fatalf("missing %s operation %q", kind, id)
	return plan.Op{}
}

func withoutEnv(env []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
