package plan

import "testing"

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
