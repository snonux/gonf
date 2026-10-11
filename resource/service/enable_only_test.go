package service

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

func TestEnableOnlyPolicy(t *testing.T) {
	for _, absent := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			for _, running := range []bool{false, true} {
				for _, dry := range []bool{false, true} {
					resource.ResetForTest()
					s := newService("gonfy", []opt.ServiceOption{opt.WithEnableOnly})
					s.Absent = absent
					b := &fakeBackend{isRunning: running, isEnabled: enabled, runningErr: errors.New("runtime probe forbidden")}
					resource.SetDryRun(dry)
					if err := s.applyWith(b); err != nil {
						t.Fatal(err)
					}
					var want []verb
					status := resource.StatusOK
					if absent == enabled {
						status = resource.StatusChanged
						if absent {
							want = []verb{verbDisable}
						} else {
							want = []verb{verbEnable}
						}
						if dry {
							want = nil
							status = resource.StatusWouldChange
						}
					}
					if !slices.Equal(b.done, want) || len(b.probedUnits) != 1 {
						t.Fatalf("absent=%v enabled=%v running=%v dry=%v: actions %v probes %v", absent, enabled, running, dry, b.done, b.probedUnits)
					}
					assertStatus(t, "Service[gonfy]", status)
				}
			}
		}
	}
	resource.ResetForTest()
}

func TestEnableOnlyRefusesRuntimeOptions(t *testing.T) {
	for _, other := range []opt.ServiceOption{opt.WithRestart, opt.WithReload, opt.OnChange(resource.Resource{Type: "File", Name: "gonfy"})} {
		for _, absent := range []bool{false, true} {
			for _, reverse := range []bool{false, true} {
				resource.ResetForTest()
				opts := []opt.ServiceOption{opt.WithEnableOnly, other}
				if reverse {
					slices.Reverse(opts)
				}
				if absent {
					opts = append(opts, opt.IsAbsent)
				}
				Present("gonfy", opts...)
				if err := declerr.First(); err == nil || !strings.Contains(err.Error(), "WithEnableOnly cannot be combined") {
					t.Fatalf("declaration error: %v", err)
				}
				if len(resource.RegisteredIDs()) != 0 || len(resource.RegisteredPlanDrafts()) != 0 {
					t.Fatal("refused declaration registered")
				}
				calls := 0
				sr := &runners.ServiceRunners{Manager: func() (string, error) { calls++; return "rcctl", nil }}
				if err := EnsureWith(sr, nil, "gonfy", opts...); err == nil || calls != 0 {
					t.Fatalf("Ensure refusal: %v detector calls %d", err, calls)
				}
			}
		}
	}
	resource.ResetForTest()
}

func TestEnableOnlyFlags(t *testing.T) {
	resource.ResetForTest()
	defer resource.ResetForTest()
	s := newService("gonfy", []opt.ServiceOption{opt.WithEnableOnly, opt.WithFlags("-v")})
	b := &fakeFlagBackend{fakeBackend: fakeBackend{runningErr: errors.New("no runtime probe")}}
	if err := s.applyWith(b); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.done, []verb{verbEnable, "flags"}) {
		t.Fatal(b.done)
	}
	b.isEnabled = true
	b.done = nil
	if err := s.applyWith(b); err != nil {
		t.Fatal(err)
	}
	if len(b.done) != 0 {
		t.Fatal(b.done)
	}
	Present("absent", opt.WithEnableOnly, opt.WithFlags(""), opt.IsAbsent)
	if declerr.First() == nil || len(resource.RegisteredIDs()) != 0 {
		t.Fatal("absent flags accepted")
	}
}

// Exercise every backend through one apply's runner injection. NetBSD's
// actual file mutation is covered separately with a private rc.conf.d.
func TestEnableOnlyPerApplyBackends(t *testing.T) {
	for _, manager := range []string{"systemd", "rcctl", "freebsd", "netbsd"} {
		for _, absent := range []bool{false, true} {
			for _, dry := range []bool{false, true} {
				if manager == "netbsd" && !dry {
					continue
				}
				resource.ResetForTest()
				resource.SetDryRun(dry)
				enabled := absent
				var mutations []string
				fake := func(bin string, args ...string) (string, string, int, error) {
					joined := strings.Join(args, " ")
					if slices.Contains(args, "is-active") || slices.Contains(args, "status") && bin == "service" || slices.Contains(args, "onestatus") || slices.Contains(args, "check") {
						t.Fatalf("runtime probe: %s %v", bin, args)
					}
					probe := slices.Contains(args, "is-enabled") || bin == "rcctl" && len(args) > 0 && args[0] == "get" || slices.Contains(args, "enabled") || slices.Contains(args, "-e")
					if probe {
						if enabled {
							return "enabled\n", "", 0, nil
						}
						return "disabled\n", "", 1, nil
					}
					mutations = append(mutations, joined)
					if slices.Contains(args, "enable") {
						enabled = true
					} else if slices.Contains(args, "disable") {
						enabled = false
					} else {
						t.Fatalf("unexpected runtime mutation: %v", args)
					}
					return "", "", 0, nil
				}
				rs := &runners.Set{Service: &runners.ServiceRunners{Run: fake, Manager: func() (string, error) { return manager, nil }}, Systemd: &runners.SystemdRunners{Run: fake}}
				opts := []opt.ServiceOption{opt.WithEnableOnly}
				if absent {
					opts = append(opts, opt.IsAbsent)
				}
				if manager == "systemd" {
					opts = append(opts, opt.WithUser)
				}
				Present("gonfy", opts...)
				if err := testapply.ApplyWithRunners(rs); err != nil {
					t.Fatal(manager, absent, dry, err)
				}
				if dry {
					if len(mutations) != 0 {
						t.Fatal(mutations)
					}
					assertStatus(t, "Service[gonfy]", resource.StatusWouldChange)
					continue
				}
				if len(mutations) != 1 {
					t.Fatal(manager, mutations)
				}
				if err := testapply.ApplyWithRunners(rs); err != nil {
					t.Fatal(err)
				}
				if len(mutations) != 1 {
					t.Fatal("not idempotent", mutations)
				}
				assertStatus(t, "Service[gonfy]", resource.StatusOK)
			}
		}
	}
	resource.ResetForTest()
}

func TestEnableOnlyNetBSDWritesBootState(t *testing.T) {
	resource.ResetForTest()
	defer resource.ResetForTest()
	enabled := false
	dir := t.TempDir()
	b := netbsdBackend{rcConfD: dir, run: func(bin string, args ...string) (string, string, int, error) {
		if !slices.Equal(args, []string{"-e", "gonfy"}) {
			t.Fatalf("runtime command: %v", args)
		}
		if enabled {
			return "", "", 0, nil
		}
		return "", "", 1, nil
	}}
	s := newService("gonfy", []opt.ServiceOption{opt.WithEnableOnly})
	for _, absent := range []bool{false, true} {
		s.Absent = absent
		if err := s.applyWith(b); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(dir, "gonfy"))
		if err != nil {
			t.Fatal(err)
		}
		want := "gonfy=YES\n"
		if absent {
			want = "gonfy=NO\n"
		}
		if string(content) != want {
			t.Fatalf("%q want %q", content, want)
		}
		enabled = !absent
		resource.ResetReport()
		if err := s.applyWith(b); err != nil {
			t.Fatal(err)
		}
		assertStatus(t, "Service[gonfy]", resource.StatusOK)
	}
}

func TestEnableOnlyPlanRefusesRuntimeOptions(t *testing.T) {
	for _, op := range []plan.Op{
		{Op: plan.KindService, Name: "gonfy", EnableOnly: true, Restart: true},
		{Op: plan.KindService, Name: "gonfy", EnableOnly: true, Reload: true},
		{Op: plan.KindService, Name: "gonfy", EnableOnly: true, IfChanged: true, Watch: []string{"File[gonfy]"}},
	} {
		err := (planHandler{}).Apply(op, plan.ApplyContext{})
		if err == nil || !strings.Contains(err.Error(), "WithEnableOnly cannot be combined") {
			t.Fatal(err)
		}
	}
}

func TestEnableOnlyDraftDependenciesAndDuplicate(t *testing.T) {
	resource.ResetForTest()
	defer resource.ResetForTest()
	dep := resource.Resource{Type: "File", Name: "gonfy"}
	Present("gonfy", opt.WithEnableOnly, opt.DependsOn(dep))
	drafts := resource.RegisteredPlanDrafts()
	if len(drafts) != 1 {
		t.Fatal(drafts)
	}
	op, err := (planHandler{}).ToOp(drafts[0])
	if err != nil {
		t.Fatal(err)
	}
	if !op.EnableOnly || !slices.Equal(op.Deps, []string{"File[gonfy]"}) || plan.RequiredVersion([]plan.Op{op}) != 29 {
		t.Fatalf("draft lost enablement/dependency: %+v", op)
	}
	Present("gonfy")
	if declerr.First() == nil {
		t.Fatal("duplicate not refused")
	}
	kept := resource.RegisteredPlanDrafts()
	if len(kept) != 1 || !kept[0].EnableOnly {
		t.Fatal("duplicate replaced accepted draft", kept)
	}
}

func TestEnableOnlyBackendErrors(t *testing.T) {
	resource.ResetForTest()
	defer resource.ResetForTest()
	s := newService("gonfy", []opt.ServiceOption{opt.WithEnableOnly})
	probeErr := errors.New("boot probe failed")
	b := &fakeBackend{enabledErr: probeErr}
	if err := s.applyWith(b); !errors.Is(err, probeErr) || len(b.done) != 0 {
		t.Fatalf("probe error: %v actions %v", err, b.done)
	}
	assertNotNoted(t, "Service[gonfy]")
	b.enabledErr = nil
	b.failVerb = verbEnable
	if err := s.applyWith(b); err == nil {
		t.Fatal("mutation failure ignored")
	}
	assertNotNoted(t, "Service[gonfy]")
	for _, manager := range []string{"rcctl", "freebsd", "netbsd"} {
		calls := 0
		sr := &runners.ServiceRunners{Manager: func() (string, error) { return manager, nil }, Run: func(string, ...string) (string, string, int, error) { calls++; return "", "", 0, nil }}
		if err := EnsureWith(sr, nil, "gonfy", opt.WithEnableOnly, opt.WithUser); !errors.Is(err, errUserNeedsSystemd) || calls != 0 {
			t.Fatalf("user support: %v calls %d", err, calls)
		}
	}
	calls := 0
	sr := &runners.ServiceRunners{Manager: func() (string, error) { return "systemd", nil }}
	sys := &runners.SystemdRunners{Run: func(string, ...string) (string, string, int, error) { calls++; return "", "", 0, nil }}
	if err := EnsureWith(sr, sys, "gonfy", opt.WithEnableOnly, opt.WithFlags("")); !errors.Is(err, errFlagsUnsupported) || calls != 0 {
		t.Fatalf("flags support: %v calls %d", err, calls)
	}
}

func TestEnableOnlySystemdEnablementStates(t *testing.T) {
	for _, tt := range []struct {
		state  string
		absent bool
		want   []string
	}{
		{"static", false, nil}, {"static", true, nil},
		{"enabled-runtime", true, []string{"disable", "--runtime", "gonfy"}},
		{"enabled-runtime", false, nil},
	} {
		resource.ResetForTest()
		var calls [][]string
		fake := func(bin string, args ...string) (string, string, int, error) {
			if slices.Contains(args, "is-enabled") {
				return tt.state + "\n", "", 0, nil
			}
			calls = append(calls, slices.Clone(args))
			return "", "", 0, nil
		}
		sr := &runners.ServiceRunners{Manager: func() (string, error) { return "systemd", nil }}
		opts := []opt.ServiceOption{opt.WithEnableOnly}
		if tt.absent {
			opts = append(opts, opt.IsAbsent)
		}
		if err := EnsureWith(sr, &runners.SystemdRunners{Run: fake}, "gonfy", opts...); err != nil {
			t.Fatal(err)
		}
		if tt.want == nil {
			if len(calls) != 0 {
				t.Fatal(tt.state, calls)
			}
		} else if len(calls) != 1 || !slices.Equal(calls[0], tt.want) {
			t.Fatal(tt.state, calls)
		}
	}
	resource.ResetForTest()
}
