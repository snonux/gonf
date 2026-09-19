package api

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"golang.org/x/sys/unix"
)

func useSecretWorkDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	return dir
}

func writeSecret(t *testing.T, path, value string) {
	t.Helper()
	path = filepath.Join("secrets", path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMustSecretRecordsExactBytes(t *testing.T) {
	ResetForTest()
	useSecretWorkDir(t)
	const value = " leading\ntrailing \x00bytes\n"
	writeSecret(t, "var/key", value)

	Task("required", "", func() {
		File("/tmp/secret", options.WithContent(MustSecret("/var/key")))
	})
	ops, err := RecordPlan("secrets", "", "required")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("ops = %#v", ops)
	}
	got, err := base64.StdEncoding.DecodeString(ops[1].ContentB64)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != value {
		t.Fatalf("secret bytes = %q, want %q", got, value)
	}
}

func TestSecretFailuresAreRecordErrorsWithoutValues(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		setup    func(t *testing.T)
		optional bool
		want     string
	}{
		{name: "required missing", path: "missing", want: "missing"},
		{name: "required empty", path: "empty", setup: func(t *testing.T) { writeSecret(t, "empty", "") }, want: "empty"},
		{name: "optional empty", path: "empty", setup: func(t *testing.T) { writeSecret(t, "empty", "") }, optional: true, want: "empty"},
		{name: "unreadable directory", path: "directory", setup: func(t *testing.T) {
			if err := os.MkdirAll(filepath.Join("secrets", "directory"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, want: "not a regular file"},
		{name: "empty path", path: "", want: "secret path must not be empty"},
		{name: "root path", path: "///", want: "secret path must not be empty"},
		{name: "escape", path: "nested/../../outside", want: "invalid secret path"},
		{name: "symlink escape", path: "link", setup: func(t *testing.T) {
			if err := os.WriteFile("outside", []byte("never-report-this-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll("secrets", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../outside", filepath.Join("secrets", "link")); err != nil {
				t.Fatal(err)
			}
		}, want: "contains a symlink"},
		{name: "dangling symlink escape", path: "link", optional: true, setup: func(t *testing.T) {
			if err := os.MkdirAll("secrets", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../outside-not-yet-created", filepath.Join("secrets", "link")); err != nil {
				t.Fatal(err)
			}
		}, want: "contains a symlink"},
		{name: "secrets root symlink", path: "key", setup: func(t *testing.T) {
			if err := os.MkdirAll("external", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join("external", "key"), []byte("never-report-this-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("external", "secrets"); err != nil {
				t.Fatal(err)
			}
		}, want: "contains a symlink"},
		{name: "intermediate symlink", path: "nested/key", setup: func(t *testing.T) {
			writeSecret(t, "actual/key", "never-report-this-secret")
			if err := os.Symlink("actual", filepath.Join("secrets", "nested")); err != nil {
				t.Fatal(err)
			}
		}, want: "contains a symlink"},
		{name: "fifo", path: "fifo", setup: func(t *testing.T) {
			if err := os.MkdirAll("secrets", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(filepath.Join("secrets", "fifo"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: "not a regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ResetForTest()
			useSecretWorkDir(t)
			if tc.setup != nil {
				tc.setup(t)
			}
			const secretValue = "never-report-this-secret"
			Task("secret", "", func() {
				if tc.optional {
					OptionalSecret(tc.path)
					return
				}
				MustSecret(tc.path)
			})
			ops, err := RecordPlan("secrets", "", "secret")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RecordPlan error = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), secretValue) {
				t.Fatalf("error leaked secret value: %v", err)
			}
			if len(ops) != 0 {
				t.Fatalf("unsafe secret produced plan ops: %#v", ops)
			}
		})
	}
}

func TestOptionalSecretMissingDoesNotFollowInternalSymlinks(t *testing.T) {
	ResetForTest()
	useSecretWorkDir(t)
	writeSecret(t, "actual", "secret\n")
	if err := os.Symlink("actual", filepath.Join("secrets", "link")); err != nil {
		t.Fatal(err)
	}
	Task("secret", "", func() { OptionalSecret("link") })
	_, err := RecordPlan("secrets", "", "secret")
	if err == nil || !strings.Contains(err.Error(), "contains a symlink") {
		t.Fatalf("RecordPlan error = %v, want symlink rejection", err)
	}
}

func TestOptionalSecretMissingOmitsHostFragment(t *testing.T) {
	ResetForTest()
	useSecretWorkDir(t)
	writeSecret(t, "etc/goprecords/f0.token", "f0-token\\n")

	Task("hosts", "", func() {
		for _, host := range []string{"f0", "f1"} {
			host := host
			WhenHostname(host, func() {
				secret, ok := OptionalSecret("etc/goprecords/" + host + ".token")
				if !ok {
					return
				}
				File("/tmp/"+host, options.WithContent(secret))
			})
		}
	})
	ops, err := RecordPlan("secrets", "", "hosts")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, op := range ops {
		if op.Path != "" {
			paths = append(paths, op.Path)
		}
	}
	if strings.Join(paths, ",") != "/tmp/f0" {
		t.Fatalf("secret-backed host fragments = %v, want [/tmp/f0]", paths)
	}
}

func TestSecretFailureStopsRunAndPushBeforeMutation(t *testing.T) {
	ResetForTest()
	dir := useSecretWorkDir(t)
	tempDir := filepath.Join(dir, "tmp")
	t.Setenv("TMPDIR", tempDir)
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "destination")
	Task("secret", "", func() {
		File(destination, options.WithContent(MustSecret("missing")))
	})
	if err := Run("secret"); err == nil {
		t.Fatal("Run succeeded with missing required secret")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Run mutated destination: %v", err)
	}
	left, err := filepath.Glob(filepath.Join(tempDir, "gonf-plan-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("Run leaked plan directories: %v", left)
	}

	calls := captureSSH(t)
	if err := PushTo(PushTarget{Host: "example.invalid"}, "secrets", "secret"); err == nil {
		t.Fatal("PushTo succeeded with missing required secret")
	}
	if len(*calls) != 0 {
		t.Fatalf("PushTo opened SSH connections after secret failure: %#v", *calls)
	}
}
