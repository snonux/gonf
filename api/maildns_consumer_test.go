package api

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	tidyConsumerModule(t, consumer)

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

	publisher := consumerPlanOp(t, ops, plan.KindCommand, "Command[publish-nsd-zones]")
	if publisher.Bin != "/usr/local/bin/dns-publish.ksh" || publisher.IfChanged {
		t.Fatalf("DNS publisher command = %#v", publisher)
	}
	publisherScript := consumerPlanOp(t, ops, plan.KindFile, "File[/usr/local/bin/dns-publish.ksh]")
	if !contains(publisher.Deps, publisherScript.ID) {
		t.Errorf("publisher dependencies = %v, want installed publisher %s", publisher.Deps, publisherScript.ID)
	}
	stagedKey := consumerPlanOp(t, ops, plan.KindFile, "File[/var/nsd/etc/gonf-publisher/key.conf]")
	if !contains(publisher.Deps, stagedKey.ID) {
		t.Errorf("publisher dependencies = %v, want staged key %s", publisher.Deps, stagedKey.ID)
	}

	inputs := 0
	for _, op := range ops {
		if op.Op != plan.KindFile || !strings.HasPrefix(op.Path, "/var/nsd/etc/gonf-publisher/") {
			continue
		}
		inputs++
		if !contains(publisher.Deps, op.ID) && !strings.HasSuffix(op.Path, ".zone.tpl") {
			t.Errorf("publisher dependencies = %v, want %s", publisher.Deps, op.ID)
		}
	}
	if inputs == 0 {
		t.Fatal("frontend NSD plan did not contain immutable publisher inputs")
	}

	publisherConfig := consumerPlanOp(t, ops, plan.KindFile, "File[/var/nsd/etc/gonf-publisher/publisher.conf]")
	publisherContent, err := plan.DecodeContentB64(publisherConfig.ContentB64)
	if err != nil {
		t.Fatalf("decode publisher config: %v", err)
	}
	zoneTemplate := consumerPlanOp(t, ops, plan.KindFile, "File[/var/nsd/etc/gonf-publisher/zones/buetow.org.zone.tpl]")
	zoneContent, err := plan.DecodeContentB64(zoneTemplate.ContentB64)
	if err != nil {
		t.Fatalf("decode zone template: %v", err)
	}
	if !strings.Contains(string(zoneContent), "@SERIAL@") ||
		!strings.Contains(string(zoneContent), "SOA  blowfish.buetow.org.") ||
		strings.Contains(string(zoneContent), "<%= time() %>") {
		t.Fatalf("publisher zone template lost stable serial/MNAME policy: %q", zoneContent)
	}
	if !strings.Contains(string(publisherContent), `DEFAULT_ROLE="fishfinger"`) ||
		!strings.Contains(string(publisherContent), `PUBLISHER_FQDN="blowfish.buetow.org"`) ||
		!strings.Contains(string(publisherContent), `REMOVED_ZONES=""`) {
		t.Fatalf("publisher config lost stable role or identity: %q", publisherContent)
	}

	for _, op := range ops {
		if op.Op == plan.KindFile && strings.HasPrefix(op.Path, "/var/nsd/zones/master/") {
			t.Fatalf("ordinary Gonf plan directly writes effective zone %q", op.Path)
		}
	}
}

func TestConsumerDNSFailoverPlanUsesSolePublisher(t *testing.T) {
	consumerSource := consumerGonfSource(t)
	consumer := filepath.Join(t.TempDir(), "consumer")
	if err := copyConsumerSource(consumer, consumerSource); err != nil {
		t.Fatalf("copy consumer source: %v", err)
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
	tidyConsumerModule(t, consumer)

	planDir := filepath.Join(t.TempDir(), "plan")
	cmd := exec.Command("go", "run", "./cmd/gonf", "plan", "-o", planDir, "frontends_dns_failover")
	cmd.Dir = consumer
	cmd.Env = append(withoutEnv(os.Environ(), "GOWORK"), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("record frontend DNS failover plan: %v\n%s", err, output)
	}

	raw, err := os.ReadFile(filepath.Join(planDir, "plan.jsonl"))
	if err != nil {
		t.Fatalf("read recorded plan: %v", err)
	}
	ops, err := plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("decode recorded plan: %v", err)
	}
	publisher := consumerPlanOp(t, ops, plan.KindFile, "File[/usr/local/bin/dns-publish.ksh]")
	publisherContent, err := plan.DecodeContentB64(publisher.ContentB64)
	if err != nil {
		t.Fatalf("decode publisher script: %v", err)
	}
	if !strings.Contains(string(publisherContent), "acquire_lock") ||
		!strings.Contains(string(publisherContent), "ps -o lstart=") ||
		!strings.Contains(string(publisherContent), "token=$LOCK_TOKEN") ||
		!strings.Contains(string(publisherContent), "lock_is_ours || return 0") ||
		!strings.Contains(string(publisherContent), "mv \"$LOCK\" \"$stale_lock\"") ||
		!strings.Contains(string(publisherContent), "incomplete or unverifiable; refusing recovery") ||
		!strings.Contains(string(publisherContent), "create_journal()") ||
		!strings.Contains(string(publisherContent), "snapshot_file \"$LIVE_KEY\"") ||
		!strings.Contains(string(publisherContent), "render_candidate_config") ||
		!strings.Contains(string(publisherContent), "dns-zone-serial") ||
		!strings.Contains(string(publisherContent), "REMOVED_ZONES") ||
		!strings.Contains(string(publisherContent), "rm -f \"$JOURNAL/incomplete\"") {
		t.Fatalf("publisher script lacks transaction boundary: %q", publisherContent)
	}
	failover := consumerPlanOp(t, ops, plan.KindFile, "File[/usr/local/bin/dns-failover.ksh]")
	failoverContent, err := plan.DecodeContentB64(failover.ContentB64)
	if err != nil {
		t.Fatalf("decode failover script: %v", err)
	}
	if !strings.Contains(string(failoverContent), "exec \"$PUBLISH\" -r \"$desired\"") ||
		!strings.Contains(string(failoverContent), "readonly DOMAIN=buetow.org") ||
		!strings.Contains(string(failoverContent), "fqdn=$2.$DOMAIN") ||
		!strings.Contains(string(failoverContent), "https://$fqdn/index.txt") ||
		!strings.Contains(string(failoverContent), "print fishfinger") ||
		strings.Contains(string(failoverContent), "ZONES_DIR") || strings.Contains(string(failoverContent), "date +%U") {
		t.Fatalf("failover script remains a direct or wall-clock writer: %q", failoverContent)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func tidyConsumerModule(t *testing.T, consumer string) {
	t.Helper()
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = consumer
	cmd.Env = append(withoutEnv(os.Environ(), "GOWORK"), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tidy copied consumer module: %v\n%s", err, output)
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
