package plan

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func sampleOps() []Op {
	exit1 := 1
	return []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "demo"},
		{
			Op:      KindLink,
			Path:    "${HOME}/.bashrc",
			Symlink: "/dotfiles/bash/bashrc",
			ID:      "Symlink[.bashrc]",
		},
		{
			Op: KindWhenBegin,
			ID: "when.linux",
			All: []Predicate{
				{Fact: "goos", Eq: "linux"},
				{PathExists: "${HOME}/Notes"},
			},
		},
		{
			Op:         KindFile,
			Path:       "${HOME}/.taskrc",
			Mode:       "0640",
			ContentB64: "Li4u",
		},
		{Op: KindWhenEnd},
		{
			Op:   KindCommand,
			Bin:  "systemctl",
			Args: []string{"--user", "enable", "x.timer"},
			Unless: &Guard{
				Bin:        "systemctl",
				Args:       []string{"--user", "is-enabled", "x.timer"},
				ExpectExit: &exit1,
			},
		},
		{
			Op:     KindLinkIfExists,
			Path:   "${HOME}/QuickEdit/Notes",
			Target: "${HOME}/Notes",
		},
		{
			Op:       KindSyncDir,
			Path:     "${HOME}/scripts",
			Blob:     "blobs/scripts",
			Prune:    true,
			FileMode: "0750",
		},
		{Op: KindPackage, Name: "fish"},
		{Op: KindEnsureDir, Path: "${HOME}/.cursor", Mode: "0750"},
		{Op: KindDir, Path: "${HOME}/data", Mode: "0700"},
		{
			Op:       KindCron,
			Name:     "backup",
			CronUser: "root",
			Command:  "/usr/local/bin/backup.sh",
			Schedule: "0 2 * * *",
			CronEnv:  []string{"PATH=/usr/bin:/bin", "MAILTO=root"},
			ID:       "Cron[root/backup]",
		},
		{
			Op:      KindService,
			Name:    "uptimed",
			Restart: true,
			User:    true,
			ID:      "Service[uptimed]",
		},
		{
			Op:     KindService,
			Name:   "olddaemon",
			Absent: true,
		},
	}
}

func TestEncodeDecodeOpRoundTrip(t *testing.T) {
	t.Parallel()
	for _, op := range sampleOps() {
		op := op
		t.Run(string(op.Op), func(t *testing.T) {
			t.Parallel()
			b, err := EncodeOp(op)
			if err != nil {
				t.Fatalf("EncodeOp: %v", err)
			}
			got, err := DecodeOp(b)
			if err != nil {
				t.Fatalf("DecodeOp: %v", err)
			}
			if !reflect.DeepEqual(got, op) {
				t.Fatalf("round-trip\ngot  %#v\nwant %#v", got, op)
			}
		})
	}
}

func TestEncodeDecodePlanRoundTrip(t *testing.T) {
	t.Parallel()
	want := sampleOps()
	raw, err := EncodePlan(want)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	got, err := DecodePlanBytes(raw)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan round-trip mismatch\ngot  %#v\nwant %#v", got, want)
	}

	// Inverse: JSONL → ops → JSONL → ops must be stable.
	raw2, err := EncodePlan(got)
	if err != nil {
		t.Fatalf("re-EncodePlan: %v", err)
	}
	got2, err := DecodePlanBytes(raw2)
	if err != nil {
		t.Fatalf("re-DecodePlanBytes: %v", err)
	}
	if !reflect.DeepEqual(got2, want) {
		t.Fatalf("inverse round-trip mismatch")
	}
}

func TestDecodePlanVersionGate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "empty",
			input:   "",
			wantErr: "missing header",
		},
		{
			name:    "whitespace only",
			input:   "\n\n  \n",
			wantErr: "missing header",
		},
		{
			name:    "first op not plan",
			input:   `{"op":"link","path":"/x","symlink":"/y"}` + "\n",
			wantErr: `first op must be "plan"`,
		},
		{
			name:    "missing version",
			input:   `{"op":"plan","id":"x"}` + "\n",
			wantErr: "missing version",
		},
		{
			name:    "unsupported version",
			input:   `{"op":"plan","version":99}` + "\n",
			wantErr: "unsupported plan version 99",
		},
		{
			name:    "duplicate header",
			input:   `{"op":"plan","version":1}` + "\n" + `{"op":"plan","version":1}` + "\n",
			wantErr: "duplicate plan header",
		},
		{
			name:    "invalid json line",
			input:   `{"op":"plan","version":1}` + "\n" + `{not-json` + "\n",
			wantErr: "line 2",
		},
		{
			name:    "empty op field",
			input:   `{"op":"plan","version":1}` + "\n" + `{"path":"/x"}` + "\n",
			wantErr: "missing op",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodePlan(strings.NewReader(tc.input))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestDecodePlanAcceptsOlderVersions pins backward compatibility: plans
// recorded by older binaries (v1, v2, v3) must still decode after a schema
// bump. The cron/service kinds bumped CurrentVersion to 3; the file/dir
// owner/group fields bumped it to 4; older binaries refuse v4 up-front, and
// newer binaries must keep applying v1/v2/v3 plans.
func TestDecodePlanAcceptsOlderVersions(t *testing.T) {
	t.Parallel()
	for _, version := range []int{1, 2, 3, CurrentVersion} {
		input := fmt.Sprintf(`{"op":"plan","version":%d}`+"\n", version)
		if _, err := DecodePlan(strings.NewReader(input)); err != nil {
			t.Errorf("version %d header should decode: %v", version, err)
		}
	}
}

func TestValidateHeader(t *testing.T) {
	t.Parallel()
	if err := ValidateHeader(Op{Op: KindPlan, Version: CurrentVersion}); err != nil {
		t.Fatalf("valid header: %v", err)
	}
	err := ValidateHeader(Op{Op: KindFile, Version: CurrentVersion})
	if err == nil || !strings.Contains(err.Error(), "first op must be") {
		t.Fatalf("got %v", err)
	}
}

func TestEncodeOpRejectsMissingKind(t *testing.T) {
	t.Parallel()
	_, err := EncodeOp(Op{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEncodePlanRejectsEmpty(t *testing.T) {
	t.Parallel()
	_, err := EncodePlan(nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeOpRejectsEmpty(t *testing.T) {
	t.Parallel()
	_, err := DecodeOp([]byte("   "))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodePlanSkipsBlankLines(t *testing.T) {
	t.Parallel()
	raw := "\n\n" + `{"op":"plan","version":1,"id":"a"}` + "\n\n" + `{"op":"when_end"}` + "\n\n"
	ops, err := DecodePlanBytes([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0].ID != "a" || ops[1].Op != KindWhenEnd {
		t.Fatalf("got %#v", ops)
	}
}

func TestDecodePlanReaderError(t *testing.T) {
	t.Parallel()
	_, err := DecodePlan(errReader{})
	if err == nil {
		t.Fatal("expected error")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("boom")
}

func TestDecodePlanAllKindsCorpus(t *testing.T) {
	t.Parallel()
	// One minimal op per kind after header, ensuring codec accepts every Kind.
	ops := []Op{{Op: KindPlan, Version: CurrentVersion}}
	for _, k := range AllKinds() {
		if k == KindPlan {
			continue
		}
		op := Op{Op: k, Path: "/p", Name: "n", Bin: "b", Target: "/t", Symlink: "/s"}
		if k == KindWhenBegin {
			op.All = []Predicate{{Fact: "goos", Eq: "linux"}}
		}
		ops = append(ops, op)
	}
	raw, err := EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePlan(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ops) {
		t.Fatalf("len=%d want %d", len(got), len(ops))
	}
}

func TestEncodeDecodeEmptyNonNilSlices(t *testing.T) {
	t.Parallel()
	in := Op{
		Op:   KindCommand,
		Bin:  "x",
		Args: []string{},
		Env:  map[string]string{},
		All:  []Predicate{},
		Unless: &Guard{
			Bin:  "y",
			Args: []string{},
		},
		OnlyIf: &Guard{
			Bin:  "z",
			Args: []string{},
		},
	}
	b, err := EncodeOp(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOp(b)
	if err != nil {
		t.Fatal(err)
	}
	want := Op{
		Op:     KindCommand,
		Bin:    "x",
		Unless: &Guard{Bin: "y"},
		OnlyIf: &Guard{Bin: "z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestNormalizeEmptySlices(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"op":"command","bin":"x","args":[],"unless":{"bin":"y","args":[]},"all":[]}`)
	// Note: "all" on command is odd but exercises normalizeOp.
	op, err := DecodeOp(raw)
	if err != nil {
		t.Fatal(err)
	}
	if op.Args != nil || op.All != nil || op.Unless.Args != nil {
		t.Fatalf("expected nil empty slices, got %#v", op)
	}
}

func TestFormatSupportedVersions(t *testing.T) {
	got := FormatSupportedVersions()
	if !strings.Contains(got, "1") || !strings.Contains(got, "2") {
		t.Fatalf("got %q", got)
	}
	supportedVersions[99] = struct{}{}
	defer delete(supportedVersions, 99)
	got = FormatSupportedVersions()
	if !strings.Contains(got, "1") || !strings.Contains(got, "99") {
		t.Fatalf("multi versions: %q", got)
	}
}

func TestEncodePlanPropagatesEncodeOpError(t *testing.T) {
	t.Parallel()
	_, err := EncodePlan([]Op{
		{Op: KindPlan, Version: 1},
		{}, // missing op
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodePlanTrailingReadError(t *testing.T) {
	t.Parallel()
	header := `{"op":"plan","version":1}` + "\n"
	r := io.MultiReader(strings.NewReader(header), errReader{})
	_, err := DecodePlan(r)
	if err == nil {
		t.Fatal("expected error")
	}
}
