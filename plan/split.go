package plan

import "fmt"

// Chunk is a consecutive run of ops that share the same elevate flag
// (after when-block promotion). Header KindPlan is duplicated into each chunk.
type Chunk struct {
	Elevate bool
	Ops     []Op
}

// ValidateChunkDeps checks dependency direction across privilege chunks
// before any chunk is applied: every dep must be recorded in the dependent's
// own chunk (the apply-side sort reorders within a chunk body) or in an
// earlier chunk (chunks apply in recorded order and never reorder, so the
// earlier chunk satisfies the dep). A dep recorded in a LATER chunk crosses
// the elevation boundary and is refused, as is a dep recorded in no chunk at
// all (dangling — the repository path refuses "depended upon but not
// registered" the same way). chunks holds one op body per privilege chunk in
// apply order (SplitPrivilegeChunks output, headers included); for a
// single-chunk plan it reduces to "every dep is recorded in the chunk".
// Wiring: ApplyChunks (api, local apply) and remote.PushChunks (SSH push) run
// this pre-flight before applying or
// uploading anything, so a rejected plan mutates no destination and, on
// push, sends zero SSH traffic.
func ValidateChunkDeps(chunks [][]Op) error {
	firstChunk := firstChunkOf(chunks)
	for i, chunk := range chunks {
		for _, op := range chunk {
			for _, dep := range op.Deps {
				j, ok := firstChunk[dep]
				if !ok {
					return fmt.Errorf(
						"plan: chunk %d: op %s depends on %s which is recorded in no chunk (dangling dependency)",
						i, op.ID, dep)
				}
				if j > i {
					return fmt.Errorf(
						"plan: chunk %d: op %s depends on %s which is recorded in later chunk %d; a dependency recorded after its dependent crosses the privilege boundary",
						i, op.ID, dep, j)
				}
			}
		}
	}
	return nil
}

// firstChunkOf maps an op ID to the first chunk index carrying it.
func firstChunkOf(chunks [][]Op) map[string]int {
	firstChunk := map[string]int{}
	for i, chunk := range chunks {
		for _, op := range chunk {
			if op.ID != "" {
				if _, seen := firstChunk[op.ID]; !seen {
					firstChunk[op.ID] = i
				}
			}
		}
	}
	return firstChunk
}

// ValidateChangeGates checks change-gate watch locality across privilege
// chunks before any chunk is applied: change reports (resource.Note) are
// chunk-local — every privilege chunk is applied as its own plan.Apply
// invocation, and an elevated chunk is a separate sudo/doas process with a
// report of its own — so a gated op can only see change reports from
// resources recorded in its OWN chunk. A watch crossing the privilege
// boundary (earlier or later chunk) can never fire and is refused; so is a
// watch recorded in no chunk at all (dangling) and a gated op with no watch
// ids at all (its gate could never fire). Like ValidateChunkDeps, this is a
// controller-side pre-flight: api.ApplyChunks, remote.PushChunks, and
// RecordPlanTo (record time) all run it, so a rejected plan mutates no
// destination and, on push, sends zero SSH traffic.
func ValidateChangeGates(chunks [][]Op) error {
	firstChunk := firstChunkOf(chunks)
	for i, chunk := range chunks {
		for _, op := range chunk {
			if !op.IfChanged {
				continue
			}
			if len(op.Watch) == 0 {
				return fmt.Errorf(
					"plan: chunk %d: op %s is change-gated (if_changed) but watches nothing; the gate can never fire",
					i, op.ID)
			}
			for _, watch := range op.Watch {
				j, ok := firstChunk[watch]
				if !ok {
					return fmt.Errorf(
						"plan: chunk %d: op %s watches %s which is recorded in no chunk (dangling watch); change reports are chunk-local",
						i, op.ID, watch)
				}
				if j != i {
					return fmt.Errorf(
						"plan: chunk %d: op %s watches %s which is recorded in chunk %d; change reports are chunk-local (each privilege chunk applies as its own process), so a watch must live in the same chunk as the gated op",
						i, op.ID, watch, j)
				}
			}
		}
	}
	return nil
}

// SplitPrivilegeChunks splits ops into ordered chunks by Elevate.
// when_begin/when_end blocks are never split; if any op inside is elevate,
// the whole block is treated as elevate.
func SplitPrivilegeChunks(ops []Op) []Chunk {
	if len(ops) == 0 {
		return nil
	}
	header := Op{Op: KindPlan, Version: CurrentVersion}
	body := ops
	if ops[0].Op == KindPlan {
		header = ops[0]
		body = ops[1:]
	}
	if len(body) == 0 {
		return []Chunk{{Elevate: false, Ops: []Op{header}}}
	}

	eff := effectiveElevate(body)
	var chunks []Chunk
	start := 0
	cur := eff[0]
	for i := 1; i <= len(body); i++ {
		if i < len(body) && eff[i] == cur {
			continue
		}
		part := make([]Op, 0, 1+(i-start))
		h := header
		if h.ID == "" {
			h.ID = "chunk"
		}
		part = append(part, h)
		part = append(part, body[start:i]...)
		chunks = append(chunks, Chunk{Elevate: cur, Ops: part})
		if i < len(body) {
			start = i
			cur = eff[i]
		}
	}
	return chunks
}

func effectiveElevate(body []Op) []bool {
	eff := make([]bool, len(body))
	for i, op := range body {
		eff[i] = op.Elevate
	}
	// Promote whole when-blocks when any inner op is elevate (nesting-safe).
	type frame struct{ start int }
	var stack []frame
	for i, op := range body {
		switch op.Op {
		case KindWhenBegin:
			stack = append(stack, frame{start: i})
		case KindWhenEnd:
			if len(stack) == 0 {
				continue
			}
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			need := false
			for j := f.start; j <= i; j++ {
				if eff[j] {
					need = true
					break
				}
			}
			if need {
				for j := f.start; j <= i; j++ {
					eff[j] = true
				}
			}
		}
	}
	return eff
}
