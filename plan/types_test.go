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
		KindLinkIfExists: "link_if_exists",
		KindWhenBegin:    "when_begin",
		KindWhenEnd:      "when_end",
		KindTimer:        "timer",
		KindDaemonReload: "daemon_reload",
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
			name: "when_end",
			op:   Op{Op: KindWhenEnd},
			want: `{"op":"when_end"}`,
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
			name: "package",
			op:   Op{Op: KindPackage, Name: "helix", ID: "Package[helix]"},
			want: `{"op":"package","id":"Package[helix]","name":"helix"}`,
		},
		{
			name: "file add_line remove_line",
			op: Op{
				Op:         KindFile,
				Path:       "${HOME}/.config/tmux/tmux.conf",
				AddLine:    "source-file ~/.config/tmux/tmux.rocky.conf",
				RemoveLine: "old-line",
			},
			want: `{"op":"file","path":"${HOME}/.config/tmux/tmux.conf","add_line":"source-file ~/.config/tmux/tmux.rocky.conf","remove_line":"old-line"}`,
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
	if op.Unless != nil || op.OnlyIf != nil || op.All != nil || op.Args != nil || op.Env != nil {
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
