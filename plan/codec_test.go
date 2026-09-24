package plan

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func sampleOps() []Op {
	exit1 := 1
	return []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "demo"},
		{
			Op:      KindLink,
			Path:    "${HOME}/.bashrc",
			ID:      "Symlink[.bashrc]",
			Payload: LinkPayload{Symlink: "/dotfiles/bash/bashrc"},
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
			Op:      KindFile,
			Path:    "${HOME}/.taskrc",
			Mode:    "0640",
			Payload: FilePayload{ContentB64: "Li4u"},
		},
		{Op: KindWhenEnd},
		{
			Op: KindCommand,
			Payload: CommandPayload{
				Bin:  "systemctl",
				Args: []string{"--user", "enable", "x.timer"},
				Unless: &Guard{
					Bin:        "systemctl",
					Args:       []string{"--user", "is-enabled", "x.timer"},
					ExpectExit: &exit1,
				},
			},
		},
		{
			Op:      KindLinkIfExists,
			Path:    "${HOME}/QuickEdit/Notes",
			Payload: LinkIfExistsPayload{Target: "${HOME}/Notes"},
		},
		{
			Op:      KindSyncDir,
			Path:    "${HOME}/scripts",
			Blob:    "blobs/scripts",
			Prune:   true,
			Payload: SyncDirPayload{FileMode: "0750"},
		},
		{
			// The glob flavor (schema v24): its prune must reach the
			// destination as glob prune, so the flag has to survive the codec.
			Op:      KindSyncDir,
			Path:    "${HOME}/bin",
			Blob:    "blobs/bin",
			Prune:   true,
			Payload: SyncDirPayload{SourceDir: "/dotfiles/bin", Glob: true, FileMode: "0750"},
		},
		{Op: KindPackage, Name: "fish", Payload: PackagePayload{}},
		{Op: KindEnsureDir, Path: "${HOME}/.cursor", Mode: "0750"},
		{Op: KindDir, Path: "${HOME}/data", Mode: "0700"},
		{
			Op:      KindCron,
			Name:    "backup",
			Command: "/usr/local/bin/backup.sh",
			ID:      "Cron[root/backup]",
			Payload: CronPayload{
				CronUser:      "root",
				LegacyCommand: "/usr/local/bin/old-backup.sh",
				Schedule:      "0 2 * * *",
				CronEnv:       []string{"PATH=/usr/bin:/bin", "MAILTO=root"},
			},
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
		{
			Op:      KindCommand,
			ID:      "DaemonReload[user]",
			Deps:    []string{"File[/etc/a]", "File[/etc/b]"},
			Payload: CommandPayload{Bin: "systemctl", Args: []string{"--user", "daemon-reload"}},
		},
	}
}

func TestEncodeDecodeOpRoundTrip(t *testing.T) {
	t.Parallel()
	for _, op := range sampleOps() {
		op := op
		t.Run(fmt.Sprintf("%s-%s", op.Op, op.Path), func(t *testing.T) {
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

// TestEncodeDecodeOpEmptyContentRoundTrip pins the k5 fix on the wire: a
// KindFile op recording legitimately empty content (has_content:true,
// content_b64 omitted/empty) must round-trip losslessly, and the emitted
// JSON must carry has_content so a destination apply can tell it apart from
// an op with no content data at all.
func TestEncodeDecodeOpEmptyContentRoundTrip(t *testing.T) {
	t.Parallel()
	op := Op{
		Op:      KindFile,
		Path:    "${HOME}/.empty-marker",
		Mode:    "0640",
		Payload: FilePayload{ContentB64: "", HasContent: true},
	}
	b, err := EncodeOp(op)
	if err != nil {
		t.Fatalf("EncodeOp: %v", err)
	}
	if !strings.Contains(string(b), `"has_content":true`) {
		t.Fatalf("encoded op missing has_content:true: %s", b)
	}
	if strings.Contains(string(b), `"content_b64"`) {
		t.Fatalf("encoded op should omit empty content_b64: %s", b)
	}
	got, err := DecodeOp(b)
	if err != nil {
		t.Fatalf("DecodeOp: %v", err)
	}
	if !reflect.DeepEqual(got, op) {
		t.Fatalf("round-trip\ngot  %#v\nwant %#v", got, op)
	}

	// A plain zero-value file op (no content, no has_content) must omit the
	// field entirely, keeping old plans byte-identical.
	plain := Op{Op: KindFile, Path: "${HOME}/.plain"}
	pb, err := EncodeOp(plain)
	if err != nil {
		t.Fatalf("EncodeOp: %v", err)
	}
	if strings.Contains(string(pb), "has_content") {
		t.Fatalf("encoded op should omit has_content when false: %s", pb)
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
// recorded by older binaries (v1–v20) must still decode after a schema bump.
// The cron/service kinds bumped CurrentVersion to 3; the file/dir owner/group
// fields bumped it to 4; the deps field (DependsOn ordering) bumped it to 5;
// the sync_dir source_dir field (stable {{.Param}} for tree templates) bumped
// it to 6; the systemd_timer op bumped it to 7. Older binaries refuse v7
// up-front, and newer binaries must keep applying every prior schema. The
// when_begin require field bumped it to 20 (VersionWhenRequire) and the
// config_set kinds to 21 (VersionConfigSet).
func TestDecodePlanAcceptsOlderVersions(t *testing.T) {
	t.Parallel()
	for _, version := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, CurrentVersion} {
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

// TestDecodePlanBytesRejectsSealedPlanAgeSkewWording pins the file-path half
// of docs/design/plan-encryption.md's "old-gonf given a sealed input" table: a
// gonf binary that predates the sealed-apply sniff (internal/cli, task 3b2)
// would call DecodePlanBytes directly on `gonf apply <plan.age>`'s file
// bytes, and age's own cleartext version banner is not valid JSON — so it
// fails at the header line, before any op is parsed, with an ordinary JSON
// decode error naming the header line rather than any op-level failure.
// This is DecodePlanBytes's existing, unchanged behavior; task 3b2's sniff
// only decides whether DecodePlanBytes is called at all for a given file's
// bytes, never what it does with bytes handed to it directly.
func TestDecodePlanBytesRejectsSealedPlanAgeSkewWording(t *testing.T) {
	t.Parallel()
	_, err := DecodePlanBytes([]byte("age-encryption.org/v1\n-> X25519 ...\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.HasPrefix(err.Error(), "plan: header: ") {
		t.Fatalf("DecodePlanBytes(sealed-looking file) = %v, want a %q-prefixed header decode error", err, "plan: header: ")
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
		op := Op{Op: k, Path: "/p", Name: "n"}
		switch k {
		case KindWhenBegin:
			op.All = []Predicate{{Fact: "goos", Eq: "linux"}}
		case KindLink:
			op.Payload = LinkPayload{Symlink: "/s"}
		case KindLinkIfExists:
			op.Payload = LinkIfExistsPayload{Target: "/t"}
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
		Op:  KindCommand,
		Env: map[string]string{},
		All: []Predicate{},
		Payload: CommandPayload{
			Bin:  "x",
			Args: []string{},
			Unless: &Guard{
				Bin:  "y",
				Args: []string{},
			},
			OnlyIf: &Guard{
				Bin:  "z",
				Args: []string{},
			},
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
		Op: KindCommand,
		Payload: CommandPayload{
			Bin:    "x",
			Unless: &Guard{Bin: "y"},
			OnlyIf: &Guard{Bin: "z"},
		},
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
	p, _ := op.Payload.(CommandPayload)
	if p.Args != nil || op.All != nil || p.Unless.Args != nil {
		t.Fatalf("expected nil empty slices, got %#v (payload %#v)", op, p)
	}

	// add_lines/remove_lines are File-exclusive (FilePayload, task ae2): a
	// separate decode of a "file" line exercises their own trimming.
	fileRaw := []byte(`{"op":"file","path":"/x","add_lines":[],"remove_lines":[]}`)
	fileOp, err := DecodeOp(fileRaw)
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := fileOp.Payload.(FilePayload)
	if fp.AddLines != nil || fp.RemoveLines != nil {
		t.Fatalf("expected nil empty slices, got %#v", fp)
	}
}

func TestEncodeDecodeLineArraysRoundTrip(t *testing.T) {
	t.Parallel()
	want := Op{
		Op:   KindFile,
		Path: "/etc/rc.local",
		Payload: FilePayload{
			AddLines:    []string{"first", "second"},
			RemoveLines: []string{"old", "stale"},
			KeyedLines:  []KeyedLine{{Key: "export PKG_PATH=", Line: `export PKG_PATH="https://repo/"`}},
		},
	}
	raw, err := EncodeOp(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOp(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip\ngot  %#v\nwant %#v", got, want)
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

// TestEncodePlanConcurrentSharedGuardNoRace reproduces the fleet/cluster push
// data race (agent task 16): internal/remote/fleet.go's Fanout hands the SAME
// ops []Op slice — and therefore the same *Guard pointers reachable through
// Op.Unless/Op.OnlyIf — to every per-host goroutine, and each host encodes it
// independently via plan.EncodePush -> EncodePlan -> EncodeOp. Before the
// fix, normalizeOp/normalizeGuard mutated the shared Guard in place
// (g.Args = nil) through that shared pointer, a write/write race that
// `go test -race` flags as WARNING: DATA RACE. Run with -race to verify;
// without -race this test passes even against the old, racy code.
func TestEncodePlanConcurrentSharedGuardNoRace(t *testing.T) {
	t.Parallel()
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion},
		{
			Op: KindCommand,
			Payload: CommandPayload{
				Bin: "true",
				Unless: &Guard{
					Bin:  "true",
					Args: []string{},
				},
				OnlyIf: &Guard{
					Bin:  "true",
					Args: []string{},
				},
			},
		},
	}

	const goroutines = 16
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := EncodePlan(ops); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	// Encoding must be side-effect free with respect to the caller's ops:
	// the correctness property the concurrency fix relies on. If EncodePlan
	// mutated the shared Guard, this would observe it having been nilled out
	// (or, under the race, could observe a torn/inconsistent value). Bin,
	// Unless and OnlyIf now live on CommandPayload (task 7e2), not flat on
	// Op, but the shared-pointer race the fix guards against is unchanged:
	// applyToWire (op_payload.go) copies the pointer VALUE onto wireOp, and
	// normalizeWire (wire.go) still does its own copy-before-mutate on its
	// own wireOp copy, never on the payload's pointee.
	cp, ok := ops[1].Payload.(CommandPayload)
	if !ok {
		t.Fatalf("ops[1].Payload = %#v, want CommandPayload", ops[1].Payload)
	}
	if cp.Unless.Args == nil || len(cp.Unless.Args) != 0 {
		t.Fatalf("EncodePlan mutated shared Unless.Args: %#v", cp.Unless.Args)
	}
	if cp.OnlyIf.Args == nil || len(cp.OnlyIf.Args) != 0 {
		t.Fatalf("EncodePlan mutated shared OnlyIf.Args: %#v", cp.OnlyIf.Args)
	}
}

// TestEncodeOpGuardNormalizationUnchanged pins the wire content produced for
// a guard with an empty (non-nil) Args slice: this must stay identical to
// pre-fix behavior (the "args" key omitted from the guard object) even
// though normalization now runs on a copy of the Guard rather than the
// original. The concurrency fix must not change single-host encoded output.
func TestEncodeOpGuardNormalizationUnchanged(t *testing.T) {
	t.Parallel()
	op := Op{
		Op: KindCommand,
		Payload: CommandPayload{
			Bin: "true",
			Unless: &Guard{
				Bin:  "true",
				Args: []string{},
			},
		},
	}
	b, err := EncodeOp(op)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, `"args"`) {
		t.Fatalf("expected empty args to be omitted from wire output, got %s", got)
	}
	if !strings.Contains(got, `"unless":{"bin":"true"}`) {
		t.Fatalf("unexpected unless encoding: %s", got)
	}
}
