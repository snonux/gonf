package user

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fakeHost is a permissive account database for the conformance test. It
// answers every probe any backend issues (getent, id, pw usershow/groupshow)
// for one account "svc" whose primary group is "svc" and whose explicit
// supplementary memberships are memberOf, reports every requested group as
// existing except those in missingGroups, and records every command.
// Unknown commands fail the test, so a backend cannot issue a command the
// fake does not model.
type fakeHost struct {
	t             *testing.T
	exists        bool
	memberOf      []string
	missingGroups map[string]bool
	calls         [][]string
}

const (
	fakeGetentRecord = "svc:*:1001:1001::/home/svc:/bin/sh\n"
	fakePwRecord     = "svc:*:1001:1001::0:0::/home/svc:/bin/sh\n"
)

// groupDB renders the group database (getent group / pw groupshow -a).
func (h *fakeHost) groupDB() string {
	var db strings.Builder
	db.WriteString("svc:*:1001:\n")
	for i, group := range h.memberOf {
		fmt.Fprintf(&db, "%s:*:%d:svc\n", group, 2000+i)
	}
	return db.String()
}

func (h *fakeHost) run(command string, args ...string) (string, string, int, error) {
	h.calls = append(h.calls, append([]string{command}, args...))
	key := command + " " + strings.Join(args, " ")
	switch {
	case key == "getent passwd svc" || key == "pw usershow -n svc":
		return h.lookupAccount(command)
	case strings.HasPrefix(key, "getent group ") || strings.HasPrefix(key, "pw groupshow -n "):
		return h.lookupGroup(command, args[len(args)-1])
	case key == "getent group" || key == "pw groupshow -a":
		return h.groupDB(), "", 0, nil
	case key == "id --groups --name svc" || key == "id -Gn svc":
		return strings.Join(append([]string{"svc"}, h.memberOf...), " ") + "\n", "", 0, nil
	case isMutation(append([]string{command}, args...)):
		return "", "", 0, nil
	}
	h.t.Fatalf("fakeHost: unmodelled command %q", key)
	return "", "", 0, nil
}

func (h *fakeHost) lookupAccount(command string) (string, string, int, error) {
	switch {
	case h.exists && command == "pw":
		return fakePwRecord, "", 0, nil
	case h.exists:
		return fakeGetentRecord, "", 0, nil
	case command == "pw":
		return "", "", freeBSDNoUserExit, nil
	default:
		return "", "", 2, nil
	}
}

// lookupGroup answers a single-group probe: found unless listed in
// missingGroups, where it answers with the platform's not-found exit.
func (h *fakeHost) lookupGroup(command, group string) (string, string, int, error) {
	switch {
	case !h.missingGroups[group]:
		return "", "", 0, nil
	case command == "pw":
		return "", "", freeBSDNoUserExit, nil
	default:
		return "", "", 2, nil
	}
}

// isMutation reports whether argv changes the account database.
func isMutation(argv []string) bool {
	if argv[0] == "pw" && len(argv) > 1 {
		argv = argv[1:]
	}
	return slices.Contains([]string{"useradd", "usermod", "groupadd"}, argv[0])
}

func (h *fakeHost) mutations() []string {
	var out []string
	for _, call := range h.calls {
		if isMutation(call) {
			out = append(out, strings.Join(call, " "))
		}
	}
	return out
}

// conformanceRun drives the backend for goos against a fresh fakeHost.
func conformanceRun(t *testing.T, goos string, exists bool, want DesiredUser) (*fakeHost, error) {
	t.Helper()
	host := &fakeHost{t: t, exists: exists}
	backend, ok := ForGOOS(goos, host.run)
	if !ok {
		t.Fatalf("ForGOOS(%q) found no backend", goos)
	}
	return host, ensureAs(backend, want)
}

// fieldTreatment is how a backend must treat one DesiredUser field.
type fieldTreatment int

const (
	// honoured: creation argv carries the field; an existing account is left
	// alone unless the field is one of the additive updates.
	honoured fieldTreatment = iota
	// requestRefused: refused before any command, account state irrelevant.
	requestRefused
	// creationRefused: refused after the probe for a missing account only.
	creationRefused
)

// conformanceField sets one DesiredUser field to a sentinel and says how the
// declared capabilities classify it.
type conformanceField struct {
	set      func(*DesiredUser)
	sentinel string // text that must reach the creation argv when honoured
	// flag is, per GOOS, the argv token a boolean field adds to creation. The
	// token must appear only when the field is set, so a backend that always
	// (or never) passes it is caught.
	flag      map[string]string
	updates   bool // an existing account gets an additive update for it
	treatment func(Capabilities) fieldTreatment
}

// honouredEverywhere classifies a field no capability can refuse.
func honouredEverywhere(Capabilities) fieldTreatment { return honoured }

// conformanceFields covers every optional DesiredUser field;
// TestConformanceCoversEveryDesiredUserField keeps it complete.
func conformanceFields() map[string]conformanceField {
	return map[string]conformanceField{
		"PrimaryGroup":        {set: func(u *DesiredUser) { u.PrimaryGroup = "pgrp" }, sentinel: "pgrp", treatment: honouredEverywhere},
		"SupplementaryGroups": {set: func(u *DesiredUser) { u.SupplementaryGroups = []string{"sgrp"} }, sentinel: "sgrp", updates: true, treatment: honouredEverywhere},
		"Home":                {set: func(u *DesiredUser) { u.Home = "/srv/sentinel-home" }, sentinel: "/srv/sentinel-home", treatment: honouredEverywhere},
		"CreateHome": {set: func(u *DesiredUser) { u.CreateHome = true }, treatment: honouredEverywhere,
			flag: map[string]string{"linux": "--create-home", "openbsd": "-m", "netbsd": "-m", "freebsd": "-m"}},
		"Shell": {set: func(u *DesiredUser) { u.Shell = "/sentinel/shell" }, sentinel: "/sentinel/shell", treatment: honouredEverywhere},
		"LoginClass": {set: func(u *DesiredUser) { u.LoginClass = "sentinelclass" }, sentinel: "sentinelclass", treatment: func(c Capabilities) fieldTreatment {
			if c.LoginClass {
				return honoured
			}
			return requestRefused
		}},
		"System": {set: func(u *DesiredUser) { u.System = true }, flag: map[string]string{"linux": "--system"}, treatment: func(c Capabilities) fieldTreatment {
			if c.SystemAccount {
				return honoured
			}
			return creationRefused
		}},
		// ManageHome is exercised on top of Home (it requires one): creation
		// is unchanged and an existing account's differing home is updated.
		"ManageHome": {set: func(u *DesiredUser) { u.Home, u.ManageHome = "/srv/sentinel-home", true }, sentinel: "/srv/sentinel-home", updates: true, treatment: honouredEverywhere},
	}
}

func TestConformanceCoversEveryDesiredUserField(t *testing.T) {
	fields := conformanceFields()
	typ := reflect.TypeOf(DesiredUser{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if _, ok := fields[name]; !ok && name != "Name" {
			t.Errorf("DesiredUser.%s has no conformance entry: declare how every backend treats it", name)
		}
		delete(fields, name)
	}
	for name := range fields {
		t.Errorf("conformance entry %s names no DesiredUser field", name)
	}
}

// TestBackendConformance drives every registered backend over every
// DesiredUser field, for a missing and an existing account, and requires the
// observed behaviour to match the backend's declared capabilities.
func TestBackendConformance(t *testing.T) {
	for _, goos := range SupportedGOOS() {
		backend, _ := ForGOOS(goos, nil)
		caps := backend.Capabilities()
		for name, field := range conformanceFields() {
			t.Run(goos+"/"+name, func(t *testing.T) {
				want := DesiredUser{Name: "svc"}
				field.set(&want)
				switch field.treatment(caps) {
				case honoured:
					checkHonouredOnCreate(t, goos, name, field, want)
					checkHonouredOnExisting(t, goos, field, want)
				case requestRefused:
					checkRequestRefused(t, goos, want, caps.checkRequest(want))
				case creationRefused:
					checkCreationRefused(t, goos, want, caps.checkCreate(want))
				}
			})
		}
	}
}

func checkHonouredOnCreate(t *testing.T, goos, name string, field conformanceField, want DesiredUser) {
	t.Helper()
	base := DesiredUser{Name: "svc"}
	if name == "ManageHome" {
		base.Home = want.Home // creation must be exactly the Home-only creation
	}
	baseHost, err := conformanceRun(t, goos, false, base)
	if err != nil {
		t.Fatalf("baseline Ensure() = %v", err)
	}
	host, err := conformanceRun(t, goos, false, want)
	if err != nil {
		t.Fatalf("declared honoured, Ensure() = %v", err)
	}
	got, baseline := host.mutations(), baseHost.mutations()
	if name == "ManageHome" {
		if !slices.Equal(got, baseline) {
			t.Fatalf("ManageHome changed creation: %q, want %q", got, baseline)
		}
		return
	}
	if slices.Equal(got, baseline) {
		t.Fatalf("declared honoured but creation commands ignore the field: %q", got)
	}
	if field.sentinel != "" && !strings.Contains(strings.Join(got, "\n"), field.sentinel) {
		t.Fatalf("creation commands %q do not carry %q", got, field.sentinel)
	}
	if flag, ok := field.flag[goos]; ok && (!hasToken(got, flag) || hasToken(baseline, flag)) {
		t.Fatalf("flag %q must appear only when the field is set: set %q, unset %q", flag, got, baseline)
	} else if !ok && field.flag != nil {
		t.Fatalf("no %s creation flag recorded for honoured field", goos)
	}
}

func checkHonouredOnExisting(t *testing.T, goos string, field conformanceField, want DesiredUser) {
	t.Helper()
	host, err := conformanceRun(t, goos, true, want)
	if err != nil {
		t.Fatalf("existing account: Ensure() = %v", err)
	}
	got := strings.Join(host.mutations(), "\n")
	switch {
	case field.updates && !strings.Contains(got, field.sentinel):
		t.Fatalf("existing account: additive update missing %q in %q", field.sentinel, got)
	case !field.updates && got != "":
		t.Fatalf("existing account: creation-only field mutated the account: %q", got)
	}
}

func checkRequestRefused(t *testing.T, goos string, want DesiredUser, declared error) {
	t.Helper()
	if declared == nil {
		t.Fatal("capabilities do not refuse a field classified as refused")
	}
	for _, exists := range []bool{false, true} {
		host, err := conformanceRun(t, goos, exists, want)
		if err == nil || err.Error() != declared.Error() {
			t.Fatalf("exists=%v: Ensure() = %v, want %q", exists, err, declared)
		}
		if len(host.calls) != 0 {
			t.Fatalf("exists=%v: request refusal ran commands %q", exists, host.calls)
		}
	}
}

func checkCreationRefused(t *testing.T, goos string, want DesiredUser, declared error) {
	t.Helper()
	if declared == nil {
		t.Fatal("capabilities do not refuse a field classified as refused")
	}
	host, err := conformanceRun(t, goos, false, want)
	if err == nil || err.Error() != declared.Error() {
		t.Fatalf("missing account: Ensure() = %v, want %q", err, declared)
	}
	if len(host.calls) != 1 || len(host.mutations()) != 0 {
		t.Fatalf("missing account: creation refusal must follow only the probe, ran %q", host.calls)
	}
	host, err = conformanceRun(t, goos, true, want)
	if err != nil || len(host.mutations()) != 0 {
		t.Fatalf("existing account: creation-only refusal must not apply, got %v with %q", err, host.mutations())
	}
}

// hasToken reports whether any command in commands has the argv token.
func hasToken(commands []string, token string) bool {
	for _, command := range commands {
		if slices.Contains(strings.Fields(command), token) {
			return true
		}
	}
	return false
}

// TestBackendConformanceSupplementaryGroupLimit drives the declared limit:
// exactly the limit is accepted, one more is refused before any command, and
// an unlimited backend accepts far more than any known platform limit.
func TestBackendConformanceSupplementaryGroupLimit(t *testing.T) {
	for _, goos := range SupportedGOOS() {
		t.Run(goos, func(t *testing.T) {
			backend, _ := ForGOOS(goos, nil)
			limit := backend.Capabilities().MaxSupplementaryGroups
			accepted := limit
			if limit == 0 {
				accepted = 64
			}
			if _, err := conformanceRun(t, goos, false, DesiredUser{Name: "svc", SupplementaryGroups: conformanceGroups(accepted)}); err != nil {
				t.Fatalf("%d groups: Ensure() = %v", accepted, err)
			}
			if limit == 0 {
				return
			}
			host, err := conformanceRun(t, goos, false, DesiredUser{Name: "svc", SupplementaryGroups: conformanceGroups(limit + 1)})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at most %d supplementary groups", limit)) || len(host.calls) != 0 {
				t.Fatalf("%d groups: Ensure() = %v after %q, want refusal before any command", limit+1, err, host.calls)
			}
		})
	}
}

// TestBackendConformanceGroupLimitCoversExistingMemberships drives the
// declared limit on an existing account that already has exactly that many
// explicit memberships and gains one more. A backend whose membership command
// appends passes only the new group; one that must pass the full union has
// to refuse, citing the declared limit, before any mutation. Either way no
// membership command may name more groups than the backend declares.
func TestBackendConformanceGroupLimitCoversExistingMemberships(t *testing.T) {
	for _, goos := range SupportedGOOS() {
		backend, _ := ForGOOS(goos, nil)
		caps := backend.Capabilities()
		if caps.MaxSupplementaryGroups == 0 {
			continue
		}
		t.Run(goos, func(t *testing.T) {
			host := &fakeHost{t: t, exists: true, memberOf: conformanceGroups(caps.MaxSupplementaryGroups)}
			b, _ := ForGOOS(goos, host.run)
			err := ensureAs(b, DesiredUser{Name: "svc", SupplementaryGroups: []string{"extra"}})
			if err != nil {
				want := fmt.Sprintf("at most %d are supported on %s", caps.MaxSupplementaryGroups, caps.Platform)
				if !strings.Contains(err.Error(), want) || len(host.mutations()) != 0 {
					t.Fatalf("refusal %v after %q, want %q before any mutation", err, host.mutations(), want)
				}
				return
			}
			for _, command := range host.mutations() {
				fields := strings.Fields(command)
				if i := slices.Index(fields, "-G"); i >= 0 && len(strings.Split(fields[i+1], ",")) > caps.MaxSupplementaryGroups {
					t.Fatalf("membership command %q exceeds the declared %d groups", command, caps.MaxSupplementaryGroups)
				}
			}
		})
	}
}

// TestBackendConformanceCreatedHomeSafety drives CreateHomeNeedsSafeHome.
func TestBackendConformanceCreatedHomeSafety(t *testing.T) {
	for _, goos := range SupportedGOOS() {
		for _, home := range []string{"relative", "/", "//"} {
			t.Run(goos+"/"+home, func(t *testing.T) {
				backend, _ := ForGOOS(goos, nil)
				host, err := conformanceRun(t, goos, false, DesiredUser{Name: "svc", Home: home, CreateHome: true})
				if backend.Capabilities().CreateHomeNeedsSafeHome {
					if err == nil || !strings.Contains(err.Error(), "home must") || len(host.calls) != 0 {
						t.Fatalf("Ensure() = %v after %q, want refusal before any command", err, host.calls)
					}
					return
				}
				if err != nil || !strings.Contains(strings.Join(host.mutations(), "\n"), home) {
					t.Fatalf("Ensure() = %v with %q, want the home passed through", err, host.mutations())
				}
			})
		}
	}
}

func conformanceGroups(count int) []string {
	groups := make([]string, count)
	for i := range groups {
		groups[i] = fmt.Sprintf("g%02d", i)
	}
	return groups
}

// TestDeclaredCapabilities pins each platform's declaration (documented in
// docs/user.md), so a declaration cannot drift together with its behaviour
// unnoticed.
func TestDeclaredCapabilities(t *testing.T) {
	want := map[string]Capabilities{
		"linux":   {Platform: "Linux", SystemAccount: true},
		"openbsd": {Platform: "BSD", LoginClass: true, MaxSupplementaryGroups: 16},
		"netbsd":  {Platform: "BSD", LoginClass: true, MaxSupplementaryGroups: 16},
		"freebsd": {Platform: "FreeBSD", LoginClass: true, CreateHomeNeedsSafeHome: true},
	}
	if got := SupportedGOOS(); !slices.Equal(got, []string{"freebsd", "linux", "netbsd", "openbsd"}) {
		t.Fatalf("SupportedGOOS() = %v", got)
	}
	for goos, caps := range want {
		backend, ok := ForGOOS(goos, nil)
		if !ok || backend.Capabilities() != caps {
			t.Errorf("ForGOOS(%q) capabilities = %+v, want %+v", goos, backend.Capabilities(), caps)
		}
	}
	if _, ok := ForGOOS("darwin", nil); ok {
		t.Error("ForGOOS(darwin) found a backend")
	}
}

func TestForGOOSSelectsTheMatchingBackend(t *testing.T) {
	for goos, typ := range map[string]string{"linux": "user.Linux", "openbsd": "user.OpenBSD", "netbsd": "user.NetBSD", "freebsd": "user.FreeBSD"} {
		backend, _ := ForGOOS(goos, nil)
		if got := fmt.Sprintf("%T", backend); got != typ {
			t.Errorf("ForGOOS(%q) = %s, want %s", goos, got, typ)
		}
	}
}

func TestValidateForAnyBackend(t *testing.T) {
	tests := []struct {
		name string
		want DesiredUser
		err  string
	}{
		{"valid", DesiredUser{Name: "svc"}, ""},
		{"login class some backend accepts", DesiredUser{Name: "svc", LoginClass: "daemon"}, ""},
		{"many groups some backend accepts", DesiredUser{Name: "svc", SupplementaryGroups: conformanceGroups(17)}, ""},
		{"malformed name", DesiredUser{Name: "-svc"}, "starts with -"},
		{"refused everywhere", DesiredUser{Name: "svc", LoginClass: "daemon", SupplementaryGroups: conformanceGroups(17), Home: "relative", CreateHome: true},
			`user "svc": no supported platform accepts this request (freebsd: home must be absolute when CreateHome is set; linux: login classes are not supported on Linux; netbsd: at most 16 supplementary groups are supported on BSD; openbsd: at most 16 supplementary groups are supported on BSD)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateForAnyBackend(tt.want)
			if tt.err == "" {
				if err != nil {
					t.Fatalf("ValidateForAnyBackend() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Fatalf("ValidateForAnyBackend() = %v, want %q", err, tt.err)
			}
		})
	}
}
