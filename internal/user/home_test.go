package user

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// homePlatform scripts the account probe and the expected home-field update
// for one backend, so every ManageHome scenario runs against all four
// supported platforms with the same expectations.
type homePlatform struct {
	name string
	// backend constructs the platform backend over a scripted runner.
	backend func(Runner) interface{ Ensure(DesiredUser) error }
	// existing probes an existing, locked, non-login account whose passwd
	// home field is home.
	existing func(home string) bsdCall
	// missing probes an account that does not exist.
	missing bsdCall
	// update is the only command allowed to rewrite the home field.
	update func(home string) bsdCall
	// create is the complete creation sequence after the missing probe.
	create func(home string) []bsdCall
}

func homePlatforms() []homePlatform {
	return []homePlatform{
		rockyHomePlatform(),
		getentBSDHomePlatform("openbsd", func(r Runner) interface{ Ensure(DesiredUser) error } { return NewOpenBSD(r) }),
		getentBSDHomePlatform("netbsd", func(r Runner) interface{ Ensure(DesiredUser) error } { return NewNetBSD(r) }),
		freeBSDHomePlatform(),
	}
}

// getentExisting probes a locked ("*" password) nologin account through
// getent, as the Rocky, OpenBSD and NetBSD backends do.
func getentExisting(home string) bsdCall {
	return bsdCall{command: "getent", args: []string{"passwd", "svc"}, stdout: "svc:*:1001:1001:svc:" + home + ":/sbin/nologin\n"}
}

var getentMissing = bsdCall{command: "getent", args: []string{"passwd", "svc"}, code: 2}

func rockyHomePlatform() homePlatform {
	return homePlatform{
		name:     "rocky",
		backend:  func(r Runner) interface{ Ensure(DesiredUser) error } { return NewRocky(r) },
		existing: getentExisting,
		missing:  getentMissing,
		update: func(home string) bsdCall {
			return bsdCall{command: "usermod", args: []string{"--home", home, "--", "svc"}}
		},
		create: func(home string) []bsdCall {
			return []bsdCall{{command: "useradd", args: []string{"--no-create-home", "--home", home, "--", "svc"}}}
		},
	}
}

// getentBSDHomePlatform covers OpenBSD and NetBSD, which share the bsd
// backend and its useradd/usermod argv.
func getentBSDHomePlatform(name string, backend func(Runner) interface{ Ensure(DesiredUser) error }) homePlatform {
	return homePlatform{
		name:     name,
		backend:  backend,
		existing: getentExisting,
		missing:  getentMissing,
		update: func(home string) bsdCall {
			return bsdCall{command: "usermod", args: []string{"-d", home, "svc"}}
		},
		create: func(home string) []bsdCall {
			return []bsdCall{{command: "useradd", args: []string{"-d", home, "svc"}}}
		},
	}
}

func freeBSDHomePlatform() homePlatform {
	return homePlatform{
		name:    "freebsd",
		backend: func(r Runner) interface{ Ensure(DesiredUser) error } { return NewFreeBSD(r) },
		existing: func(home string) bsdCall {
			// pw usershow prints the master.passwd layout; *LOCKED* marks a
			// locked account whose password must stay untouched.
			return bsdCall{command: "pw", args: []string{"usershow", "-n", "svc"}, stdout: "svc:*LOCKED*:1001:1001:daemon:0:0:svc:" + home + ":/usr/sbin/nologin\n"}
		},
		missing: bsdCall{command: "pw", args: []string{"usershow", "-n", "svc"}, code: freeBSDNoUserExit},
		update: func(home string) bsdCall {
			return bsdCall{command: "pw", args: []string{"usermod", "-n", "svc", "-d", home}}
		},
		create: func(home string) []bsdCall {
			return []bsdCall{
				{command: "pw", args: []string{"groupshow", "-n", "svc"}},
				{command: "pw", args: []string{"useradd", "-n", "svc", "-g", "svc", "-d", home}},
			}
		},
	}
}

const (
	oldHome = "/home/svc"
	newHome = "/var/run/svc"
)

// ensureHome runs one scripted Ensure with a fresh change report and returns
// whether User[svc] was reported as changed plus its error.
func ensureHome(t *testing.T, p homePlatform, want DesiredUser, calls []bsdCall) (bool, error) {
	t.Helper()
	resource.ResetReport()
	t.Cleanup(resource.ResetReport)
	err := p.backend(scriptedBSDRunner(t, calls)).Ensure(want)
	return resource.AnyChanged("User[svc]"), err
}

func TestManageHomeRewritesOnlyTheExistingHomeField(t *testing.T) {
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Home: newHome, ManageHome: true},
				[]bsdCall{p.existing(oldHome), p.update(newHome)})
			if err != nil || !changed {
				t.Fatalf("Ensure() = %v, changed = %v; want nil, true", err, changed)
			}
		})
	}
}

func TestManageHomeIsIdempotentWhenFieldMatches(t *testing.T) {
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Home: newHome, ManageHome: true},
				[]bsdCall{p.existing(newHome)})
			if err != nil || changed {
				t.Fatalf("Ensure() = %v, changed = %v; want nil, false", err, changed)
			}
		})
	}
}

// TestHomeStaysCreationOnlyWithoutOptIn pins the backward-compatible default:
// a differing home on an existing account is left alone unless ManageHome is
// set, and CreateHome never creates a directory for an existing account.
func TestHomeStaysCreationOnlyWithoutOptIn(t *testing.T) {
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Home: newHome, CreateHome: true},
				[]bsdCall{p.existing(oldHome)})
			if err != nil || changed {
				t.Fatalf("Ensure() = %v, changed = %v; want nil, false", err, changed)
			}
		})
	}
}

// TestManageHomeCreatesMissingAccountExactlyAsBefore proves the opt-in adds
// nothing to account creation: the creation argv is the one WithHome alone
// produces, and no separate home update follows.
func TestManageHomeCreatesMissingAccountExactlyAsBefore(t *testing.T) {
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			calls := append([]bsdCall{p.missing}, p.create(newHome)...)
			changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Home: newHome, ManageHome: true}, calls)
			if err != nil || !changed {
				t.Fatalf("Ensure() = %v, changed = %v; want nil, true", err, changed)
			}
		})
	}
}

func TestManageHomeRejectsMalformedPasswdRecordBeforeMutation(t *testing.T) {
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			probe := p.existing(oldHome)
			probe.stdout = "svc:*:1001\n"
			changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Home: newHome, ManageHome: true}, []bsdCall{probe})
			if err == nil || !strings.Contains(err.Error(), "malformed passwd entry") || changed {
				t.Fatalf("Ensure() = %v, changed = %v; want malformed passwd error", err, changed)
			}
		})
	}
}

// TestManageHomeRefusesAnotherAccountsRecord covers glibc getent resolving
// an all-digit name as a UID: the probe answers with a different account's
// entry, which must be reported as such and never lead to a rewrite.
func TestManageHomeRefusesAnotherAccountsRecord(t *testing.T) {
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			probe := p.existing(oldHome)
			probe.stdout = strings.Replace(probe.stdout, "svc:", "daemon:", 1)
			changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Home: newHome, ManageHome: true}, []bsdCall{probe})
			if err == nil || !strings.Contains(err.Error(), `returned the entry of account "daemon"`) || changed {
				t.Fatalf("Ensure() = %v, changed = %v; want account-mismatch error", err, changed)
			}
		})
	}
}

// TestManageHomeNumericNameResolvedAsUID reproduces the Linux case end to end:
// "1001" is looked up, getent answers with the account whose UID is 1001.
func TestManageHomeNumericNameResolvedAsUID(t *testing.T) {
	calls := []bsdCall{{command: "getent", args: []string{"passwd", "1001"}, stdout: "svc:x:1001:1001::" + oldHome + ":/bin/sh\n"}}
	err := NewRocky(scriptedBSDRunner(t, calls)).Ensure(DesiredUser{Name: "1001", Home: newHome, ManageHome: true})
	if err == nil || !strings.Contains(err.Error(), `returned the entry of account "svc"`) || !strings.Contains(err.Error(), "UID") {
		t.Fatalf("Ensure() = %v, want account-mismatch error naming the UID lookup", err)
	}
}

func TestManageHomeSurfacesUpdateFailure(t *testing.T) {
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			update := p.update(newHome)
			update.code, update.stderr = 1, "user svc is currently used by process 42\n"
			_, err := ensureHome(t, p, DesiredUser{Name: "svc", Home: newHome, ManageHome: true},
				[]bsdCall{p.existing(oldHome), update})
			if err == nil || !strings.Contains(err.Error(), "currently used by process 42") {
				t.Fatalf("Ensure() = %v, want the usermod failure", err)
			}
		})
	}
}

func TestManageHomeDryRunProbesWithoutMutating(t *testing.T) {
	original := resource.DryRun()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(original) })
	for _, p := range homePlatforms() {
		t.Run(p.name, func(t *testing.T) {
			// Only the probe is scripted: a dry-run usermod would fail the
			// scripted runner as an unexpected command.
			changed, err := ensureHome(t, p, DesiredUser{Name: "svc", Home: newHome, ManageHome: true},
				[]bsdCall{p.existing(oldHome)})
			if err != nil || !changed {
				t.Fatalf("Ensure() = %v, changed = %v; want nil, true (would change)", err, changed)
			}
		})
	}
}

// TestManageHomeRejectsBadHomeBeforeAnyCommand covers the opt-in contract:
// every malformed home is refused before the account is even probed.
func TestManageHomeRejectsBadHomeBeforeAnyCommand(t *testing.T) {
	homes := map[string]string{
		"":                   "requires a home directory",
		"var/run/svc":        "must be absolute",
		"/var/run/svc/":      "must be a clean path",
		"/var/run/../run/sv": "must be a clean path",
		"/var/./run/svc":     "must be a clean path",
		"/var/run/s:vc":      "contains ':'",
		"/var/run/s\nvc":     "a line break",
		"/var/run/s\rvc":     "a line break",
		// DesiredUser.Validate rejects NUL in any home before the managed
		// home check, hence its own wording.
		"/var/run/s\x00vc": "home contains NUL",
	}
	for _, p := range homePlatforms() {
		for home, wantErr := range homes {
			t.Run(p.name+"/"+home, func(t *testing.T) {
				_, err := ensureHome(t, p, DesiredUser{Name: "svc", Home: home, ManageHome: true}, nil)
				if err == nil || !strings.Contains(err.Error(), wantErr) {
					t.Fatalf("Ensure(home %q) = %v, want %q", home, err, wantErr)
				}
			})
		}
	}
}

// TestManageHomeAfterMembershipsKeepsBothAdditive checks the combined order
// on every platform: memberships are added first, then the home field;
// neither step removes anything.
func TestManageHomeAfterMembershipsKeepsBothAdditive(t *testing.T) {
	want := DesiredUser{Name: "svc", Home: newHome, ManageHome: true, SupplementaryGroups: []string{"wheel"}}
	rocky := []bsdCall{
		{command: "getent", args: []string{"passwd", "svc"}, stdout: "svc:x:1001:1001::" + oldHome + ":/bin/sh\n"},
		{command: "getent", args: []string{"group", "wheel"}},
		{command: "id", args: []string{"--groups", "--name", "svc"}, stdout: "svc\n"},
		{command: "usermod", args: []string{"--append", "--groups", "wheel", "--", "svc"}},
		{command: "usermod", args: []string{"--home", newHome, "--", "svc"}},
	}
	if err := NewRocky(scriptedBSDRunner(t, rocky)).Ensure(want); err != nil {
		t.Fatalf("Rocky Ensure() = %v", err)
	}
	// OpenBSD appends only the missing group; NetBSD first enumerates the
	// group database and passes the existing explicit membership (audio)
	// along, so a replacing usermod -G keeps it. Membership probes run before
	// the requested group is checked or created.
	bsdCalls := combinedBSDCalls
	openbsd := bsdCalls(nil, bsdCall{command: "usermod", args: []string{"-G", "wheel", "svc"}})
	if err := NewOpenBSD(scriptedBSDRunner(t, openbsd)).Ensure(want); err != nil {
		t.Fatalf("OpenBSD Ensure() = %v", err)
	}
	netbsd := bsdCalls(
		[]bsdCall{{command: "getent", args: []string{"group"}, stdout: "svc:*:1001:\naudio:*:1002:svc\nwheel:*:0:root\n"}},
		bsdCall{command: "usermod", args: []string{"-G", "audio,wheel", "svc"}},
	)
	if err := NewNetBSD(scriptedBSDRunner(t, netbsd)).Ensure(want); err != nil {
		t.Fatalf("NetBSD Ensure() = %v", err)
	}
	freebsd := []bsdCall{
		{command: "pw", args: []string{"usershow", "-n", "svc"}, stdout: "svc:*:1001:1001::0:0::" + oldHome + ":/bin/sh\n"},
		{command: "pw", args: []string{"groupshow", "-a"}, stdout: "svc:*:1001:\naudio:*:1002:svc\nwheel:*:0:root\n"},
		{command: "pw", args: []string{"groupshow", "-n", "wheel"}},
		{command: "pw", args: []string{"usermod", "-n", "svc", "-G", "audio,wheel"}},
		{command: "pw", args: []string{"usermod", "-n", "svc", "-d", newHome}},
	}
	if err := NewFreeBSD(scriptedBSDRunner(t, freebsd)).Ensure(want); err != nil {
		t.Fatalf("FreeBSD Ensure() = %v", err)
	}
}

// combinedBSDCalls is the OpenBSD/NetBSD command script for
// TestManageHomeAfterMembershipsKeepsBothAdditive: passwd and membership
// probes (plus the platform's union probes), the requested-group check, the
// membership command, then the home-field update.
func combinedBSDCalls(unionProbes []bsdCall, usermod bsdCall) []bsdCall {
	calls := []bsdCall{
		{command: "getent", args: []string{"passwd", "svc"}, stdout: "svc:*:1001:1001::" + oldHome + ":/bin/sh\n"},
		{command: "id", args: []string{"-Gn", "svc"}, stdout: "svc audio\n"},
	}
	calls = append(calls, unionProbes...)
	return append(calls,
		bsdCall{command: "getent", args: []string{"group", "wheel"}},
		usermod,
		bsdCall{command: "usermod", args: []string{"-d", newHome, "svc"}},
	)
}

func TestPasswdHome(t *testing.T) {
	got, err := passwdHome("svc:x:1:1:gecos:/var/run/svc:/bin/sh\nignored\n", "svc", getentHomeField)
	if err != nil || got != "/var/run/svc" {
		t.Fatalf("passwdHome(getent) = %q, %v", got, err)
	}
	got, err = passwdHome("svc:*:1:1:class:0:0:gecos:/var/run/svc:/bin/sh\n", "svc", freeBSDHomeField)
	if err != nil || got != "/var/run/svc" {
		t.Fatalf("passwdHome(pw) = %q, %v", got, err)
	}
	if _, err := passwdHome("svc:x:1:1:gecos", "svc", getentHomeField); err == nil {
		t.Fatal("short record accepted")
	}
	if _, err := passwdHome("", "svc", getentHomeField); err == nil {
		t.Fatal("empty record accepted")
	}
	_, err = passwdHome("root:x:0:0:root:/root:/bin/sh\n", "0", getentHomeField)
	if err == nil || !strings.Contains(err.Error(), `returned the entry of account "root"`) {
		t.Fatalf("passwdHome(other account) = %v, want account-mismatch error", err)
	}
}
