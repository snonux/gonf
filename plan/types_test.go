package plan

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSupportsVersion(t *testing.T) {
	t.Parallel()
	if !SupportsVersion(CurrentVersion) {
		t.Fatalf("SupportsVersion(%d) = false, want true", CurrentVersion)
	}
	if !SupportsVersion(1) {
		t.Fatal("SupportsVersion(1) = false, want true")
	}
	if SupportsVersion(0) {
		t.Fatal("SupportsVersion(0) = true, want false")
	}
	if SupportsVersion(CurrentVersion + 1) {
		t.Fatalf("SupportsVersion(%d) = true, want false", CurrentVersion+1)
	}
}

// TestSchemaBumpsArePinned pins the config_set and sensitive bumps: a merge
// that loses one would let an older destination accept a plan whose
// config_set op it only discovers mid-apply, after earlier ops already
// mutated the host, or apply a secret-bearing op while echoing its
// validator's output. Every version 1..CurrentVersion staying supported is
// pinned by TestWhenRequireVersionPinned (require_test.go).
func TestSchemaBumpsArePinned(t *testing.T) {
	t.Parallel()
	if VersionUserManageHome != 19 || VersionWhenRequire != 20 || VersionConfigSet != 21 ||
		VersionSensitive != 22 || CurrentVersion != VersionSensitive {
		t.Fatalf("versions: manage_home=%d require=%d config_set=%d sensitive=%d current=%d, want 19/20/21/22/22",
			VersionUserManageHome, VersionWhenRequire, VersionConfigSet, VersionSensitive, CurrentVersion)
	}
}

func TestAllKindsExhaustiveAndUnique(t *testing.T) {
	t.Parallel()
	want := map[Kind]string{
		KindPlan:         "plan",
		KindLink:         "link",
		KindFile:         "file",
		KindDir:          "dir",
		KindPackage:      "package",
		KindCommand:      "command",
		KindSyncDir:      "sync_dir",
		KindEnsureDir:    "ensure_dir",
		KindEnsureFile:   "ensure_file",
		KindLinkIfExists: "link_if_exists",
		KindWhenBegin:    "when_begin",
		KindWhenEnd:      "when_end",
		KindTimer:        "timer",
		KindDaemonReload: "daemon_reload",
		KindCron:         "cron",
		KindService:      "service",
		KindSystemdTimer: "systemd_timer",
		KindUser:         "user",
		// Schema v21 (VersionConfigSet).
		KindConfigSet:       "config_set",
		KindConfigSetMember: "config_set_member",
	}
	kinds := AllKinds()
	if len(kinds) != len(want) {
		t.Fatalf("AllKinds len = %d, want %d", len(kinds), len(want))
	}
	seen := map[Kind]bool{}
	for _, k := range kinds {
		wire, ok := want[k]
		if !ok {
			t.Errorf("AllKinds has unexpected Kind %q", k)
			continue
		}
		if string(k) != wire {
			t.Errorf("Kind %q wire = %q, want %q", k, k, wire)
		}
		if seen[k] {
			t.Errorf("AllKinds duplicate %q", k)
		}
		seen[k] = true
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("AllKinds missing %q", k)
		}
	}
}

func TestOpJSONTagsMatchPlanExamples(t *testing.T) {
	t.Parallel()
	exit1 := 1
	cases := []struct {
		name string
		op   Op
		want string
	}{
		{
			name: "header",
			op:   Op{Op: KindPlan, Version: 1, ID: "demo"},
			want: `{"op":"plan","version":1,"id":"demo"}`,
		},
		{
			name: "link",
			op: Op{
				Op:      KindLink,
				Path:    "${HOME}/.bashrc",
				Symlink: "/home/paul/git/dotfiles/bash/bashrc",
				ID:      "Symlink[${HOME}/.bashrc]",
			},
			want: `{"op":"link","id":"Symlink[${HOME}/.bashrc]","path":"${HOME}/.bashrc","symlink":"/home/paul/git/dotfiles/bash/bashrc"}`,
		},
		{
			name: "when_begin facts",
			op: Op{
				Op: KindWhenBegin,
				ID: "when.home_taskwarrior",
				All: []Predicate{
					{Fact: "goos", Eq: "linux"},
				},
			},
			want: `{"op":"when_begin","id":"when.home_taskwarrior","all":[{"fact":"goos","eq":"linux"}]}`,
		},
		{
			name: "file content",
			op: Op{
				Op:         KindFile,
				Path:       "${HOME}/.taskrc",
				Mode:       "0640",
				ContentB64: "Li4u",
				ID:         "File[${HOME}/.taskrc]",
			},
			want: `{"op":"file","id":"File[${HOME}/.taskrc]","path":"${HOME}/.taskrc","mode":"0640","content_b64":"Li4u"}`,
		},
		{
			name: "sync_dir source_dir",
			op: Op{
				Op:        KindSyncDir,
				Path:      "${HOME}/.config/app",
				Blob:      "blobs/app",
				SourceDir: "assets/testfiles",
			},
			want: `{"op":"sync_dir","path":"${HOME}/.config/app","blob":"blobs/app","source_dir":"assets/testfiles"}`,
		},
		{
			name: "path_exists predicate",
			op: Op{
				Op: KindWhenBegin,
				All: []Predicate{
					{PathExists: "${HOME}/Notes/prompts/commands"},
				},
			},
			want: `{"op":"when_begin","all":[{"path_exists":"${HOME}/Notes/prompts/commands"}]}`,
		},
		{
			name: "link_if_exists",
			op: Op{
				Op:     KindLinkIfExists,
				Path:   "${HOME}/QuickEdit/Notes",
				Target: "${HOME}/Notes",
			},
			want: `{"op":"link_if_exists","path":"${HOME}/QuickEdit/Notes","target":"${HOME}/Notes"}`,
		},
		{
			name: "command unless",
			op: Op{
				Op:   KindCommand,
				Name: "systemctl.enable.random-wallpaper",
				Bin:  "systemctl",
				Args: []string{"--user", "enable", "random-wallpaper.timer"},
				Unless: &Guard{
					Bin:  "systemctl",
					Args: []string{"--user", "is-enabled", "random-wallpaper.timer"},
				},
			},
			want: `{"op":"command","name":"systemctl.enable.random-wallpaper","bin":"systemctl","args":["--user","enable","random-wallpaper.timer"],"unless":{"bin":"systemctl","args":["--user","is-enabled","random-wallpaper.timer"]}}`,
		},
		{
			name: "command only_if creates expect_exit",
			op: Op{
				Op:      KindCommand,
				Bin:     "npm",
				Args:    []string{"install", "-g", "@ampcode/cli"},
				Creates: "/usr/local/bin/amp",
				OnlyIf: &Guard{
					Bin:        "command",
					Args:       []string{"-v", "npm"},
					ExpectExit: &exit1,
				},
			},
			want: `{"op":"command","bin":"npm","args":["install","-g","@ampcode/cli"],"creates":"/usr/local/bin/amp","only_if":{"bin":"command","args":["-v","npm"],"expect_exit":1}}`,
		},
		{
			name: "sync_dir",
			op: Op{
				Op:       KindSyncDir,
				Path:     "${HOME}/.config/systemd/user",
				Blob:     "blobs/systemd-user",
				Prune:    true,
				FileMode: "0750",
			},
			want: `{"op":"sync_dir","path":"${HOME}/.config/systemd/user","file_mode":"0750","blob":"blobs/systemd-user","prune":true}`,
		},
		{
			name: "cron with env",
			op: Op{
				Op:            KindCron,
				Name:          "backup",
				CronUser:      "root",
				Command:       "/usr/local/bin/backup.sh",
				LegacyCommand: "/usr/local/bin/old-backup.sh",
				Schedule:      "0 2 * * *",
				CronEnv:       []string{"PATH=/usr/bin:/bin"},
				ID:            "Cron[root/backup]",
			},
			want: `{"op":"cron","id":"Cron[root/backup]","name":"backup","cron_user":"root","command":"/usr/local/bin/backup.sh","legacy_command":"/usr/local/bin/old-backup.sh","schedule":"0 2 * * *","cron_env":["PATH=/usr/bin:/bin"]}`,
		},
		{
			name: "cron absent",
			op:   Op{Op: KindCron, Name: "old", CronUser: "paul", Absent: true},
			want: `{"op":"cron","absent":true,"name":"old","cron_user":"paul"}`,
		},
		{
			name: "service restart",
			op:   Op{Op: KindService, Name: "httpd", Restart: true, ID: "Service[httpd]"},
			want: `{"op":"service","id":"Service[httpd]","name":"httpd","restart":true}`,
		},
		{
			name: "service absent user",
			op:   Op{Op: KindService, Name: "foo", User: true, Reload: true, Absent: true},
			want: `{"op":"service","absent":true,"name":"foo","user":true,"reload":true}`,
		},
		{
			name: "timer restart",
			op:   Op{Op: KindTimer, Name: "fit.timer", User: true, Restart: true, ID: "Timer[fit.timer]"},
			want: `{"op":"timer","id":"Timer[fit.timer]","name":"fit.timer","user":true,"restart":true}`,
		},
		{
			name: "systemd_timer",
			op: Op{
				Op:                 KindSystemdTimer,
				Name:               "fit-job",
				Command:            "/bin/true",
				OnCalendar:         "*-*-* *:05:00",
				OnBootSec:          "10min",
				Persistent:         true,
				Description:        "fit timer",
				ServiceDescription: "fit oneshot",
				After:              []string{"network-online.target"},
				Wants:              []string{"network-online.target"},
				ID:                 "SystemdTimer[fit-job]",
			},
			want: `{"op":"systemd_timer","id":"SystemdTimer[fit-job]","name":"fit-job","command":"/bin/true","on_calendar":"*-*-* *:05:00","on_boot_sec":"10min","persistent":true,"description":"fit timer","service_description":"fit oneshot","after":["network-online.target"],"wants":["network-online.target"]}`,
		},
		{
			name: "user",
			op: Op{
				Op:                  KindUser,
				ID:                  "User[svc]",
				Name:                "svc",
				PrimaryGroup:        "svc",
				SupplementaryGroups: []string{"audio", "wheel"},
				Home:                "/var/lib/svc",
				CreateHome:          true,
				Shell:               "/sbin/nologin",
				LoginClass:          "daemon",
				System:              true,
			},
			want: `{"op":"user","id":"User[svc]","primary_group":"svc","supplementary_groups":["audio","wheel"],"home":"/var/lib/svc","create_home":true,"shell":"/sbin/nologin","login_class":"daemon","system":true,"name":"svc"}`,
		},
		{
			name: "when_end",
			op:   Op{Op: KindWhenEnd},
			want: `{"op":"when_end"}`,
		},
		{
			name: "file content with owner group",
			op: Op{
				Op:         KindFile,
				Path:       "${HOME}/secret.conf",
				Mode:       "0640",
				Owner:      "daemon",
				Group:      "wheel",
				ContentB64: "Li4u",
			},
			want: `{"op":"file","path":"${HOME}/secret.conf","mode":"0640","owner":"daemon","group":"wheel","content_b64":"Li4u"}`,
		},
		{
			name: "sync_dir with owner group",
			op: Op{
				Op:       KindSyncDir,
				Path:     "${HOME}/.config/systemd/user",
				Blob:     "blobs/systemd-user",
				FileMode: "0640",
				Owner:    "paul",
				Group:    "1000",
			},
			want: `{"op":"sync_dir","path":"${HOME}/.config/systemd/user","file_mode":"0640","owner":"paul","group":"1000","blob":"blobs/systemd-user"}`,
		},
		{
			name: "ensure_dir",
			op: Op{
				Op:   KindEnsureDir,
				Path: "${HOME}/.cursor",
				Mode: "0750",
				ID:   "EnsureDir[${HOME}/.cursor]",
			},
			want: `{"op":"ensure_dir","id":"EnsureDir[${HOME}/.cursor]","path":"${HOME}/.cursor","mode":"0750"}`,
		},
		{
			name: "ensure_dir with owner group",
			op: Op{
				Op:    KindEnsureDir,
				Path:  "${HOME}/owned",
				Mode:  "0750",
				Owner: "paul",
				Group: "wheel",
			},
			want: `{"op":"ensure_dir","path":"${HOME}/owned","mode":"0750","owner":"paul","group":"wheel"}`,
		},
		{
			name: "package",
			op:   Op{Op: KindPackage, Name: "helix", ID: "Package[helix]"},
			want: `{"op":"package","id":"Package[helix]","name":"helix"}`,
		},
		{
			name: "file add_lines remove_lines",
			op: Op{
				Op:          KindFile,
				Path:        "${HOME}/.config/tmux/tmux.conf",
				AddLines:    []string{"source-file ~/.config/tmux/tmux.rocky.conf", "set -g mouse on"},
				RemoveLines: []string{"old-line", "stale-line"},
			},
			want: `{"op":"file","path":"${HOME}/.config/tmux/tmux.conf","add_lines":["source-file ~/.config/tmux/tmux.rocky.conf","set -g mouse on"],"remove_lines":["old-line","stale-line"]}`,
		},
		{
			name: "ensure_file",
			op:   Op{Op: KindEnsureFile, ID: "EnsureFile[/etc/daily.local]", Path: "/etc/daily.local", Mode: "0644"},
			want: `{"op":"ensure_file","id":"EnsureFile[/etc/daily.local]","path":"/etc/daily.local","mode":"0644"}`,
		},
		{
			name: "command with deps",
			op: Op{
				Op:   KindCommand,
				Bin:  "systemctl",
				Args: []string{"--user", "restart", "x.service"},
				ID:   "Command[restart.x]",
				Deps: []string{"File[/etc/x]", "Package[x]"},
			},
			want: `{"op":"command","id":"Command[restart.x]","bin":"systemctl","args":["--user","restart","x.service"],"deps":["File[/etc/x]","Package[x]"]}`,
		},
		{
			name: "link hardlink absent",
			op: Op{
				Op:       KindLink,
				Path:     "/tmp/a",
				Hardlink: "/tmp/b",
				Absent:   true,
			},
			want: `{"op":"link","path":"/tmp/a","hardlink":"/tmp/b","absent":true}`,
		},
		{
			name: "command dir env expect_stdout",
			op: Op{
				Op:   KindCommand,
				Bin:  "git",
				Args: []string{"config", "--global", "--get", "user.name"},
				Dir:  "/tmp",
				Env:  map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null"},
				Unless: &Guard{
					Bin:          "git",
					Args:         []string{"config", "--global", "--get", "user.name"},
					ExpectStdout: "Paul Buetow",
				},
			},
			want: `{"op":"command","bin":"git","args":["config","--global","--get","user.name"],"dir":"/tmp","env":{"GIT_CONFIG_GLOBAL":"/dev/null"},"unless":{"bin":"git","args":["config","--global","--get","user.name"],"expect_stdout":"Paul Buetow"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tc.op)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("Marshal mismatch\ngot:  %s\nwant: %s", got, tc.want)
			}
			var round Op
			if err := json.Unmarshal(got, &round); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(round, tc.op) {
				t.Fatalf("round-trip DeepEqual failed\ngot:  %#v\nwant: %#v", round, tc.op)
			}
		})
	}
}

func TestOpZeroValueOmitemptyReady(t *testing.T) {
	t.Parallel()
	var op Op
	if op.Unless != nil || op.OnlyIf != nil || op.All != nil || op.Args != nil || op.Env != nil || op.Deps != nil {
		t.Fatalf("zero Op has non-nil omitempty fields: %+v", op)
	}
}

func TestUnsupportedJSONStillUnmarshalsKnownFields(t *testing.T) {
	t.Parallel()
	// Negative-ish: unknown fields must not break decoding of known ones
	// (encoding/json default). Ensures later version bumps can stay additive
	// only when intentionally designed that way.
	const raw = `{"op":"file","path":"/tmp/x","unknown_future":true}`
	var op Op
	if err := json.Unmarshal([]byte(raw), &op); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if op.Op != KindFile || op.Path != "/tmp/x" {
		t.Fatalf("got %#v", op)
	}
}
