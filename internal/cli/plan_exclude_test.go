package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

func TestCLIPlanOutputFilteredSources(t *testing.T) {
	for _, glob := range []bool{false, true} {
		t.Run(map[bool]string{false: "tree", true: "glob"}[glob], func(t *testing.T) {
			api.ResetForTest()
			t.Cleanup(api.ResetForTest)
			isolateXDGConfig(t)
			src := t.TempDir()
			for name, data := range map[string]string{"keep": "public bytes", "secret": "excluded secret bytes"} {
				if err := os.WriteFile(filepath.Join(src, name), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			api.Task("filtered", "", func() {
				if glob {
					api.SyncDir("/gonfy/lodge", filepath.Join(src, "*"), api.WithExclude("secret"), api.WithPrune)
				} else {
					api.Dir("/gonfy/lodge", api.WithSource(src), api.WithExclude("secret"), api.WithPrune)
				}
			})
			out := filepath.Join(t.TempDir(), "out")
			code, _, stderr := runGonfCaptureStdout(t, "plan", "-no-default-recipients", "-plaintext", "-o", out, "filtered")
			if code != 0 {
				t.Fatalf("plan exit %d: %s", code, stderr)
			}
			raw, err := os.ReadFile(filepath.Join(out, "plan.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			ops, err := plan.DecodePlanBytes(raw)
			if err != nil {
				t.Fatal(err)
			}
			sync := findOpByKind(t, ops, "sync_dir")
			payload := plan.PayloadOf[plan.SyncDirPayload](sync)
			if !sync.Prune || payload.Glob != glob || !reflect.DeepEqual(payload.Exclude, []string{"secret"}) {
				t.Fatalf("sync settings lost: %+v %+v", sync, payload)
			}
			blob, err := plan.Resolve(out, sync.Blob)
			if err != nil {
				t.Fatal(err)
			}
			files, err := os.ReadDir(blob)
			if err != nil || len(files) != 1 || files[0].Name() != "keep" {
				t.Fatalf("packaged excluded path: %v %v", files, err)
			}
			data, err := os.ReadFile(filepath.Join(blob, "keep"))
			if err != nil || string(data) != "public bytes" {
				t.Fatalf("blob bytes %q: %v", data, err)
			}
		})
	}
}

func TestCLIPlanSealFilteredSourceWritesNoPlaintext(t *testing.T) {
	api.ResetForTest()
	t.Cleanup(api.ResetForTest)
	isolateXDGConfig(t)
	src, tmp := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", tmp)
	for name, data := range map[string]string{"keep": "included sensitive bytes", "secret": "excluded secret bytes"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	api.Task("filtered", "", func() { api.Dir("/gonfy/lodge", api.WithSource(src), api.WithExclude("secret"), api.WithSensitive) })
	recipient, identity := genSealKeyPair(t)
	out := filepath.Join(t.TempDir(), "out")
	code, _, stderr := runGonfCaptureStdout(t, "plan", "-no-default-recipients", "-seal", "-recipient", recipient, "-o", out, "filtered")
	if code != 0 {
		t.Fatalf("sealed plan exit %d: %s", code, stderr)
	}
	files, err := os.ReadDir(out)
	if err != nil || len(files) != 1 || files[0].Name() != "plan.age" {
		t.Fatalf("plaintext artifact present: %v %v", files, err)
	}
	data, err := os.ReadFile(filepath.Join(out, "plan.age"))
	if err != nil || bytes.Contains(data, []byte("sensitive bytes")) || bytes.Contains(data, []byte("excluded secret bytes")) {
		t.Fatalf("sealed bytes leak or read error: %v", err)
	}
	stages, err := os.ReadDir(tmp)
	if err != nil || len(stages) != 0 {
		t.Fatalf("plaintext staging artifacts: %v %v", stages, err)
	}
	identityPath := filepath.Join(t.TempDir(), "identity")
	writeIdentityFile(t, identityPath, identity)
	identities, err := seal.LoadIdentities(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := seal.Open(bytes.NewReader(data), identities)
	if err != nil {
		t.Fatal(err)
	}
	unpacked := t.TempDir()
	decoded, err := plan.DecodePush(reader, unpacked)
	if err != nil {
		t.Fatal(err)
	}
	sync := findOpByKind(t, decoded.Ops, "sync_dir")
	if !sync.Sensitive || !reflect.DeepEqual(plan.PayloadOf[plan.SyncDirPayload](sync).Exclude, []string{"secret"}) {
		t.Fatalf("sealed sync settings lost: %+v", sync)
	}
	blob, err := plan.Resolve(unpacked, sync.Blob)
	if err != nil {
		t.Fatal(err)
	}
	files, err = os.ReadDir(blob)
	if err != nil || len(files) != 1 || files[0].Name() != "keep" {
		t.Fatalf("sealed package includes excluded paths: %v %v", files, err)
	}
	content, err := os.ReadFile(filepath.Join(blob, "keep"))
	if err != nil || string(content) != "included sensitive bytes" {
		t.Fatalf("sealed blob bytes: %q %v", content, err)
	}
}

func TestCLIPlanFilteredSourceFailureWritesNothing(t *testing.T) {
	api.ResetForTest()
	t.Cleanup(api.ResetForTest)
	isolateXDGConfig(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "keep"), []byte("private staged bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	api.Task("filtered", "", func() {
		api.Dir("/gonfy/first", api.WithSource(src), api.WithExclude("secret"))
		api.SyncDir("/gonfy/bad", filepath.Join(src, "["), api.WithExclude("secret"))
	})
	out := filepath.Join(t.TempDir(), "out")
	code, _, stderr := runGonfCaptureStdout(t, "plan", "-no-default-recipients", "-plaintext", "-o", out, "filtered")
	if code == 0 || strings.Contains(stderr, "does not support exclusions") {
		t.Fatalf("want malformed source failure: exit %d %s", code, stderr)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("failed plan committed output: %v", err)
	}
}
