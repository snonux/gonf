package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandPath(t *testing.T) {
	t.Setenv("HOME", "/tmp/gonf-home-expand")

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "no tokens", in: "/abs/path", want: "/abs/path"},
		{name: "home alone", in: "${HOME}", want: "/tmp/gonf-home-expand"},
		{name: "home suffix", in: "${HOME}/.bashrc", want: "/tmp/gonf-home-expand/.bashrc"},
		{name: "home twice", in: "${HOME}/a/${HOME}/b", want: "/tmp/gonf-home-expand/a//tmp/gonf-home-expand/b"},
		{name: "literal dollar", in: "cost$5", want: "cost$5"},
		{name: "unknown token", in: "${HOME}/${FOO}/x", wantErr: "unknown path token ${FOO}"},
		{name: "unknown only", in: "${USER}", wantErr: "unknown path token ${USER}"},
		{name: "empty token", in: "${}/x", wantErr: "empty path token"},
		{name: "unclosed", in: "${HOME", wantErr: "unclosed path token"},
		{name: "unclosed mid", in: "pre/${HOME/post", wantErr: "unclosed path token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandPath(tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ExpandPath(%q) = %q, want error %q", tt.in, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExpandPath(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ExpandPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestApplyExpandsHomeToken(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)

	ops := []Op{
		header(),
		{Op: KindEnsureDir, Path: "${HOME}/nested/dir", Mode: "0750"},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := filepath.Join(root, "nested", "dir")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expanded path missing: %v", err)
	}
}

func TestApplyUnknownPathTokenHardError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ops := []Op{
		header(),
		{Op: KindEnsureDir, Path: "${UNKNOWN}/dir", Mode: "0750"},
	}
	err := Apply(ops, Facts{}, "")
	if err == nil {
		t.Fatal("expected unknown token error")
	}
	if !strings.Contains(err.Error(), "unknown path token ${UNKNOWN}") {
		t.Fatalf("error = %v, want unknown path token", err)
	}
}

func TestApplyPathExistsExpandsHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	gate := filepath.Join(root, "gate")
	if err := os.Mkdir(gate, 0o750); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(root, "created")

	ops := []Op{
		header(),
		{Op: KindWhenBegin, All: []Predicate{{PathExists: "${HOME}/gate"}}},
		{Op: KindEnsureDir, Path: created, Mode: "0750"},
		{Op: KindWhenEnd},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(created); err != nil {
		t.Fatalf("path_exists with ${HOME} should match: %v", err)
	}
}

func TestApplyLinkIfExistsExpandsTokens(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	target := filepath.Join(root, "notes")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}

	ops := []Op{
		header(),
		{Op: KindLinkIfExists, Path: "${HOME}/link", Payload: LinkIfExistsPayload{Target: "${HOME}/notes"}},
	}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.Readlink(filepath.Join(root, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("symlink = %q, want %q", got, target)
	}
}

// TestPreHomeTokenDestinationRefusesHomeTokenConfigSet simulates a v25
// destination (schema v26, VersionHomeToken, unsupported): a plan whose
// config_set carries ${HOME} declares v26, so that destination refuses it at
// the header gate, before the earlier ensure_dir op mutates anything,
// instead of meeting the literal "${HOME}" member path mid-apply.
func TestPreHomeTokenDestinationRefusesHomeTokenConfigSet(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	body := []Op{
		{Op: KindEnsureDir, Path: "${HOME}/first", Mode: "0750"},
		{Op: KindConfigSet, Name: "app", Payload: ConfigSetPayload{
			Members: []ConfigMember{{Key: "conf", Path: "${HOME}/app.conf"}},
		}},
	}
	version := RequiredVersion(body)
	if version != VersionHomeToken {
		t.Fatalf("RequiredVersion = %d, want %d", version, VersionHomeToken)
	}
	delete(supportedVersions, VersionHomeToken)
	t.Cleanup(func() { supportedVersions[VersionHomeToken] = struct{}{} })

	ops := append([]Op{{Op: KindPlan, Version: version, ID: "home"}}, body...)
	err := Apply(ops, Facts{}, "")
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("Apply on a v25 destination = %v, want a version refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "first")); statErr == nil {
		t.Fatal("the version gate must refuse before the first op mutates the host")
	}
}
