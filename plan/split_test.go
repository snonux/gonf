package plan

import (
	"strings"
	"testing"
)

// TestValidateChunkDeps pins the cross-chunk dependency pre-flight: a dep
// recorded in the dependent's own chunk (sorted within it) or in an earlier
// chunk (applies first) is fine; a dep recorded in a later privilege chunk
// or nowhere at all is refused, with the error naming both chunk numbers.
func TestValidateChunkDeps(t *testing.T) {
	hdr := func(id string) Op { return Op{Op: KindPlan, Version: CurrentVersion, ID: id} }
	cmd := func(id string, deps ...string) Op {
		return Op{Op: KindCommand, Bin: "true", ID: id, Deps: deps}
	}
	cases := []struct {
		name    string
		chunks  [][]Op
		wantErr string // "" = valid
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
			wantErr: "dangling dependency",
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
			if !strings.Contains(err.Error(), "chunk 0") {
				t.Fatalf("error %q must name the dependent's chunk 0", err.Error())
			}
		})
	}
}

// TestValidateChunkDepsNamesLaterChunk pins that the forward-cross error
// names the dependent's chunk, the dep's chunk, and both ops.
func TestValidateChunkDepsNamesLaterChunk(t *testing.T) {
	cmd := func(id string, deps ...string) Op {
		return Op{Op: KindCommand, Bin: "true", ID: id, Deps: deps}
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
		{Op: KindCommand, Bin: "true", Elevate: true},
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
