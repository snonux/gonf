package plan

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateChunkDeps pins the cross-chunk dependency pre-flight: a dep
// recorded in the dependent's own chunk (sorted within it) or in an earlier
// chunk (applies first) is fine; a dep recorded in a later privilege chunk
// or nowhere at all is refused. A forward dep names both chunk numbers; a
// dangling dep is a *DanglingDepError naming the op and the missing dep and
// deliberately no chunk (the dep is in no chunk, so an index would mislead).
func TestValidateChunkDeps(t *testing.T) {
	hdr := func(id string) Op { return Op{Op: KindPlan, Version: CurrentVersion, ID: id} }
	cmd := func(id string, deps ...string) Op {
		return Op{Op: KindCommand, ID: id, Deps: deps, Payload: CommandPayload{Bin: "true"}}
	}
	cases := []struct {
		name    string
		chunks  [][]Op
		wantErr string // "" = valid
		// dangling marks the refusal as a typed *DanglingDepError.
		dangling bool
	}{
		{
			name:   "dep in same chunk is fine (sorted within it)",
			chunks: [][]Op{[]Op{hdr("p"), cmd("b", "a"), cmd("a")}},
		},
		{
			name:   "dep in earlier chunk is fine (backward cross)",
			chunks: [][]Op{[]Op{hdr("p"), cmd("a")}, []Op{hdr("p"), cmd("b", "a")}},
		},
		{
			name:    "dep in later chunk is refused (forward cross)",
			chunks:  [][]Op{[]Op{hdr("p"), cmd("b", "a")}, []Op{hdr("p"), cmd("a")}},
			wantErr: "later chunk 1",
		},
		{
			name:    "dangling dep is refused",
			chunks:  [][]Op{[]Op{hdr("p"), cmd("a", "File[missing]")}},
			wantErr: "dangling dependency", dangling: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateChunkDeps(tc.chunks)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
			requireRefusalKind(t, err, tc.dangling)
		})
	}
}

// requireRefusalKind asserts how a ValidateChunkDeps refusal is classified: a
// dangling dep is a *DanglingDepError for op a -> File[missing] that never
// mentions chunks; every other refusal is not one and names the dependent's
// chunk 0.
func requireRefusalKind(t *testing.T, err error, wantDangling bool) {
	t.Helper()
	var dangling *DanglingDepError
	isDangling := errors.As(err, &dangling)
	switch {
	case wantDangling && (!isDangling || dangling.Op != "a" || dangling.Dep != "File[missing]"):
		t.Fatalf("error %#v, want a *DanglingDepError for a -> File[missing]", err)
	case wantDangling && strings.Contains(err.Error(), "chunk"):
		t.Fatalf("dangling error %q must not mention chunks", err.Error())
	case !wantDangling && isDangling:
		t.Fatalf("error %q must not be a dangling-dependency error", err.Error())
	case !wantDangling && !strings.Contains(err.Error(), "chunk 0"):
		t.Fatalf("error %q must name the dependent's chunk 0", err.Error())
	}
}

// TestValidateChunkDepsNamesLaterChunk pins that the forward-cross error
// names the dependent's chunk, the dep's chunk, and both ops.
func TestValidateChunkDepsNamesLaterChunk(t *testing.T) {
	cmd := func(id string, deps ...string) Op {
		return Op{Op: KindCommand, ID: id, Deps: deps, Payload: CommandPayload{Bin: "true"}}
	}
	chunks := [][]Op{
		[]Op{cmd("Command[b]", "Command[a]")},
		[]Op{cmd("Command[c]")},
		[]Op{cmd("Command[a]")},
	}
	err := ValidateChunkDeps(chunks)
	if err == nil {
		t.Fatal("want forward cross-chunk refusal")
	}
	for _, want := range []string{"chunk 0", "Command[b]", "Command[a]", "later chunk 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must name %q", err.Error(), want)
		}
	}
}

// TestValidateChangeGates pins the additional safety rule for OnChange: a
// change report only exists within the process applying its privilege chunk,
// so every watched resource must be in the gated op's own chunk.
func TestValidateChangeGates(t *testing.T) {
	cmd := func(id string, watch ...string) Op {
		return Op{Op: KindCommand, ID: id, IfChanged: true, Watch: watch, Payload: CommandPayload{Bin: "true"}}
	}
	cases := []struct {
		name    string
		chunks  [][]Op
		wantErr string
	}{
		{
			name:   "same chunk fan in is valid",
			chunks: [][]Op{{cmd("Command[reload]", "File[a]", "File[b]"), {Op: KindFile, ID: "File[a]"}, {Op: KindFile, ID: "File[b]"}}},
		},
		{
			name:    "empty watch is refused",
			chunks:  [][]Op{{cmd("Command[reload]")}},
			wantErr: "watches nothing",
		},
		{
			name:    "dangling watch is refused",
			chunks:  [][]Op{{cmd("Command[reload]", "File[missing]")}},
			wantErr: "dangling watch",
		},
		{
			name:    "earlier privilege chunk is refused",
			chunks:  [][]Op{{{Op: KindFile, ID: "File[unit]"}}, {cmd("Command[reload]", "File[unit]")}},
			wantErr: "must live in the same chunk",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateChangeGates(tc.chunks)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateChangeGates: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestSplitPrivilegeChunks(t *testing.T) {
	header := Op{Op: KindPlan, Version: 2, ID: "t"}
	ops := []Op{
		header,
		{Op: KindFile, Path: "/tmp/a"},
		{Op: KindFile, Path: "/tmp/b"},
		{Op: KindPackage, Name: "git", Elevate: true},
		{Op: KindPackage, Name: "tmux", Elevate: true},
		{Op: KindFile, Path: "/tmp/c"},
	}
	chunks := SplitPrivilegeChunks(ops)
	if len(chunks) != 3 {
		t.Fatalf("chunks=%d %#v", len(chunks), chunks)
	}
	if chunks[0].Elevate || len(chunks[0].Ops) != 3 { // header+2 files
		t.Fatalf("chunk0=%#v", chunks[0])
	}
	if !chunks[1].Elevate || len(chunks[1].Ops) != 3 {
		t.Fatalf("chunk1=%#v", chunks[1])
	}
	if chunks[2].Elevate || len(chunks[2].Ops) != 2 {
		t.Fatalf("chunk2=%#v", chunks[2])
	}
}

func TestSplitPrivilegeChunksWhenPromotion(t *testing.T) {
	ops := []Op{
		{Op: KindPlan, Version: 2},
		{Op: KindWhenBegin},
		{Op: KindFile, Path: "/tmp/a"},
		{Op: KindCommand, Elevate: true, Payload: CommandPayload{Bin: "true"}},
		{Op: KindWhenEnd},
	}
	chunks := SplitPrivilegeChunks(ops)
	if len(chunks) != 1 || !chunks[0].Elevate {
		t.Fatalf("chunks=%#v", chunks)
	}
	for _, op := range chunks[0].Ops[1:] {
		if op.Op == KindPlan {
			continue
		}
		// effective elevate promoted onto chunk; ops themselves may still differ
	}
	if len(chunks[0].Ops) != 5 {
		t.Fatalf("ops=%d", len(chunks[0].Ops))
	}
}

func TestSplitPrivilegeAllUnpriv(t *testing.T) {
	ops := []Op{
		{Op: KindPlan, Version: 2},
		{Op: KindFile, Path: "/tmp/a"},
	}
	chunks := SplitPrivilegeChunks(ops)
	if len(chunks) != 1 || chunks[0].Elevate {
		t.Fatalf("%#v", chunks)
	}
}

// TestValidateChunksRefusalsAreTypedAndPrefixed pins the shape callers rely on
// to word a refusal once: every error of the shared ValidateChunks helper is a
// Refusal whose Reason has no "plan: " prefix (Error adds exactly one), the
// dangling cases are their dedicated types, and dependency problems are
// reported before change-gate problems.
func TestValidateChunksRefusalsAreTypedAndPrefixed(t *testing.T) {
	hdr := Op{Op: KindPlan, Version: CurrentVersion, ID: "p"}
	dep := func(id string, deps ...string) Op {
		return Op{Op: KindCommand, ID: id, Deps: deps, Payload: CommandPayload{Bin: "true"}}
	}
	gate := func(id string, watch ...string) Op {
		return Op{Op: KindCommand, ID: id, IfChanged: true, Watch: watch, Payload: CommandPayload{Bin: "true"}}
	}
	chunk := func(ops ...Op) Chunk { return Chunk{Ops: append([]Op{hdr}, ops...)} }
	cases := []struct {
		name   string
		chunks []Chunk
		want   string
		typed  func(error) bool
	}{
		{"dangling dep", []Chunk{chunk(dep("a", "missing"))}, "dangling dependency",
			func(err error) bool { var e *DanglingDepError; return errors.As(err, &e) && e.Dep == "missing" }},
		{"dangling watch", []Chunk{chunk(gate("g", "missing"))}, "dangling watch",
			func(err error) bool { var e *DanglingWatchError; return errors.As(err, &e) && e.Watch == "missing" }},
		{"forward dep", []Chunk{chunk(dep("b", "a")), chunk(dep("a"))}, "later chunk 1", nil},
		{"cross-chunk watch", []Chunk{chunk(dep("u")), chunk(gate("g", "u"))}, "same chunk as the gated op", nil},
		{"gate without watch", []Chunk{chunk(gate("g"))}, "watches nothing", nil},
		{"dependency error wins over gate error", []Chunk{chunk(gate("g", "u"), dep("x", "missing"))}, "dangling dependency", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateChunks(tc.chunks)
			var refusal Refusal
			if err == nil || !errors.As(err, &refusal) {
				t.Fatalf("ValidateChunks = %v, want a Refusal", err)
			}
			if !strings.Contains(refusal.Reason(), tc.want) || strings.Contains(refusal.Reason(), "plan: ") {
				t.Fatalf("Reason = %q, want %q without a plan: prefix", refusal.Reason(), tc.want)
			}
			if err.Error() != "plan: "+refusal.Reason() {
				t.Fatalf("Error = %q, want plan: + Reason (%q)", err.Error(), refusal.Reason())
			}
			if tc.typed != nil && !tc.typed(err) {
				t.Fatalf("error %#v is not the expected typed refusal", err)
			}
		})
	}
	if err := ValidateChunks([]Chunk{chunk(dep("a"), dep("b", "a"))}); err != nil {
		t.Fatalf("valid plan: %v", err)
	}
}
