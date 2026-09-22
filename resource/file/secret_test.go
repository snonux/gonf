package file

import (
	"strings"
	"testing"

	opt "github.com/snonux/gonf/resource/options"
)

// fakeSecretContent is synthetic secret material.
const fakeSecretContent = "fake-file-secret-5e6f\n"

// buildSecret defaults the mode to 0600, keeps an explicit mode, and uses
// the secret bytes verbatim as content.
func TestBuildSecretModeAndContent(t *testing.T) {
	cases := []struct {
		name string
		opts []opt.FileOption
		want uint32
	}{
		{"default 0600", nil, 0o600},
		{"explicit mode wins", []opt.FileOption{opt.WithMode(0o640)}, 0o640},
		{"ownership keeps default", []opt.FileOption{opt.WithOwner("root"), opt.WithGroup("wheel")}, 0o600},
	}
	for _, tc := range cases {
		f, err := buildSecret("/etc/secret", []byte(fakeSecretContent), tc.opts...)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if uint32(f.mode.Perm()) != tc.want || f.content != fakeSecretContent || !f.contentSet {
			t.Fatalf("%s: mode %o content %q", tc.name, f.mode.Perm(), f.content)
		}
	}
}

// Options that would replace or reinterpret the secret content are refused,
// and the refusal names no content.
func TestBuildSecretRefusesContentOptions(t *testing.T) {
	cases := []struct {
		name string
		path string
		opts []opt.FileOption
	}{
		{"WithContent", "/etc/s", []opt.FileOption{opt.WithContent("other")}},
		{"WithSource", "/etc/s", []opt.FileOption{opt.WithSource("/tmp/src")}},
		{"WithTemplate", "/etc/s", []opt.FileOption{opt.WithTemplate}},
		{"WithTemplateData", "/etc/s", []opt.FileOption{opt.WithTemplateData(map[string]string{"k": "v"})}},
		{"tmpl path", "/etc/s.tmpl", nil},
		{"WithLine", "/etc/s", []opt.FileOption{opt.WithLine("x")}},
		{"IsAbsent", "/etc/s", []opt.FileOption{opt.IsAbsent}},
	}
	for _, tc := range cases {
		_, err := buildSecret(tc.path, []byte(fakeSecretContent), tc.opts...)
		if err == nil || !strings.Contains(err.Error(), "SecretFile sets the content itself") {
			t.Errorf("%s: err = %v, want the content refusal", tc.name, err)
			continue
		}
		if strings.Contains(err.Error(), strings.TrimSpace(fakeSecretContent)) {
			t.Errorf("%s: refusal leaks the secret: %v", tc.name, err)
		}
	}
}
