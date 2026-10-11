package api

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	internalexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	resourceoptions "github.com/snonux/gonf/resource/options"
)

func TestSysctlsLiveDriftAndRoundTrip(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	path := filepath.Join(t.TempDir(), "sysctl.conf")
	if err := os.WriteFile(path, []byte("# Gonfy's lodge\nvm.swappiness=60\nother=mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kv := List("vm.swappiness", "10", "net.ipv4.ip_forward", "0", "vm.swappiness", "10")
	Task("sysctls", "", func() { Sysctls(path, kv, WithMode(0o600), WithName("lodge-sysctls")) })
	ops, err := RecordPlan("test", "", "sysctls")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	ops, err = plan.DecodePlanBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	commands := 0
	for _, op := range ops {
		if op.Op == "command" {
			commands++
			if !reflect.DeepEqual(op.Deps, List("File[lodge-sysctls]")) {
				t.Fatalf("deps=%v", op.Deps)
			}
		}
	}
	if commands != 2 {
		t.Fatalf("commands=%d", commands)
	}
	kv[1] = "99"
	live := map[string]string{"vm.swappiness": "60", "net.ipv4.ip_forward": "0"}
	mutations := 0
	probes := 0
	rs := &runners.Set{Command: &runners.CommandRunners{
		Probe: func(name string, args ...string) (string, string, int, error) {
			probes++
			if name != "sysctl" || len(args) != 2 || args[0] != "-n" {
				t.Fatalf("probe %s %v", name, args)
			}
			return live[args[1]] + "\n", "", 0, nil
		},
		Run: func(_ internalexec.Opts, name string, args ...string) (string, string, int, error) {
			got, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(got), "vm.swappiness=10") {
				t.Fatalf("command ran before config: %q %v", got, err)
			}
			if name != "sysctl" || len(args) != 1 {
				t.Fatalf("mutation %s %v", name, args)
			}
			pair := strings.SplitN(args[0], "=", 2)
			live[pair[0]] = pair[1]
			mutations++
			return "", "", 0, nil
		},
	}}
	for run := 0; run < 3; run++ {
		if run == 2 {
			live["vm.swappiness"] = "20"
		}
		if err := ApplyPlanContext(runners.WithSet(t.Context(), rs), ops, ""); err != nil {
			t.Fatal(err)
		}
		wantMutations := 1
		if run == 2 {
			wantMutations = 2
		}
		if mutations != wantMutations || probes != 2*(run+1) {
			t.Fatalf("run %d mutations=%d probes=%d", run, mutations, probes)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "# Gonfy's lodge\nvm.swappiness=10\nother=mine\nnet.ipv4.ip_forward=0\n" {
			t.Fatalf("content=%q", got)
		}
		if run > 0 && resource.AnyChanged("File[lodge-sysctls]") {
			t.Fatal("converged persistent file changed")
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", info, err)
	}
}

func TestSysctlsRefusal(t *testing.T) {
	cases := []struct {
		name string
		kv   []string
		opts []FileOption
	}{
		{name: "odd", kv: List("vm.x")},
		{name: "bad key", kv: List("-w", "1")},
		{name: "assignment key", kv: List("vm.x=y", "1")},
		{name: "empty", kv: List("vm.x", "")},
		{name: "whitespace", kv: List("vm.x", " 1")},
		{name: "newline", kv: List("vm.x", "1\nvm.y=2")},
		{name: "comment", kv: List("vm.x", "1#2")},
		{name: "conflict", kv: List("vm.x", "1", "vm.x", "2")},
		{name: "absent", kv: List("vm.x", "1"), opts: []FileOption{IsAbsent}},
		{name: "nil option", kv: List("vm.x", "1"), opts: []FileOption{nil}},
		{name: "content option", kv: List("vm.x", "1"), opts: []FileOption{WithContent("x")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			path := filepath.Join(t.TempDir(), "sysctl.conf")
			handle := Sysctls(path, tc.kv, tc.opts...)
			if declerr.First() == nil {
				t.Fatal("misuse accepted")
			}
			if len(resource.RegisteredIDs()) != 0 || len(handle.Dependencies()) != 0 {
				t.Fatal("refused helper registered partial resources")
			}
			if err := Apply(); err == nil {
				t.Fatal("Apply accepted misuse")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("refused helper wrote: %v", err)
			}
		})
	}
}

func TestSysctlsDryRunAndFailures(t *testing.T) {
	for _, scenario := range []string{"dry-run", "probe error", "probe exit", "mutation error", "mutation exit"} {
		t.Run(scenario, func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			path := filepath.Join(t.TempDir(), "sysctl.conf")
			handle := Sysctls(path, List("vm.x", "1"))
			if len(handle.Dependencies()) != 2 {
				t.Fatalf("dependencies=%v", handle.Dependencies())
			}
			mutations := 0
			failure := errors.New("runner failure")
			rs := &runners.Set{Command: &runners.CommandRunners{
				Probe: func(string, ...string) (string, string, int, error) {
					switch scenario {
					case "probe error":
						return "", "", -1, failure
					case "probe exit":
						return "", "", 1, nil
					}
					return "0", "", 0, nil
				},
				Run: func(internalexec.Opts, string, ...string) (string, string, int, error) {
					mutations++
					switch scenario {
					case "mutation error":
						return "", "", -1, failure
					case "mutation exit":
						return "", "", 1, nil
					}
					return "", "", 0, nil
				},
			}}
			if scenario == "dry-run" {
				resource.SetDryRun(true)
			}
			err := applyWithRunners(rs)
			wantFailure := scenario == "probe error" || scenario == "mutation error" || scenario == "mutation exit"
			if (err != nil) != wantFailure {
				t.Fatalf("error=%v", err)
			}
			if scenario == "probe error" || scenario == "mutation error" {
				if !errors.Is(err, failure) {
					t.Fatalf("cause lost: %v", err)
				}
			}
			if scenario == "dry-run" || scenario == "probe error" {
				if mutations != 0 {
					t.Fatalf("mutations=%d", mutations)
				}
			} else if mutations != 1 {
				t.Fatalf("mutations=%d", mutations)
			}
			if scenario == "dry-run" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("dry run wrote file: %v", err)
				}
			}
		})
	}
}

func TestSysctlsDuplicateFileAndLiveIDs(t *testing.T) {
	for _, samePath := range []bool{true, false} {
		t.Run("duplicate", func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			dir := t.TempDir()
			path := filepath.Join(dir, "one")
			Sysctls(path, List("vm.x", "1"))
			second := path
			if !samePath {
				second = filepath.Join(dir, "two")
			}
			handle := Sysctls(second, List("vm.x", "2"))
			if declerr.First() == nil {
				t.Fatal("duplicate accepted")
			}
			if samePath && len(handle.Dependencies()) != 0 {
				t.Fatal("refused file returned live resources")
			}
			if err := Apply(); err == nil {
				t.Fatal("duplicate applied")
			}
		})
	}
	ResetForTest()
	t.Cleanup(ResetForTest)
	if handle := Sysctls("", nil); len(handle.Dependencies()) != 0 || len(resource.RegisteredIDs()) != 0 {
		t.Fatal("empty settings registered resources")
	}
}

func TestSysctlsCustomOptionsRunOnceAndSensitivity(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	calls := 0
	custom := resourceoptions.Option(func(target any) { calls++; WithSensitive.Apply(target) })
	Sysctls(filepath.Join(t.TempDir(), "sysctl.conf"), List("vm.x", "1"), ToFileOptions(custom)...)
	if calls != 1 {
		t.Fatalf("custom option ran %d times", calls)
	}
	drafts := resource.RegisteredPlanDrafts()
	if len(drafts) != 2 {
		t.Fatalf("drafts=%d", len(drafts))
	}
	for _, draft := range drafts {
		if !draft.Sensitive {
			t.Fatalf("draft %s lost sensitivity", draft.Name)
		}
	}
}

func TestSysctlsRefusalDuringRecording(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	path := filepath.Join(t.TempDir(), "sysctl.conf")
	Task("sysctls", "", func() {
		handle := Sysctls(path, List("vm.x", "1"), IsAbsent)
		if len(handle.Dependencies()) != 0 || len(resource.RegisteredIDs()) != 0 {
			t.Fatal("refused recording registered partial resources")
		}
	})
	if _, err := RecordPlan("test", "", "sysctls"); err == nil || !strings.Contains(err.Error(), "IsAbsent") {
		t.Fatalf("recording error=%v", err)
	}
}

func TestSysctlsEmptyPathRefused(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	handle := Sysctls("", List("vm.x", "1"))
	if declerr.First() == nil || len(handle.Dependencies()) != 0 || len(resource.RegisteredIDs()) != 0 {
		t.Fatal("empty path accepted")
	}
}
