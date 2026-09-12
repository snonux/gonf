package plan

// Chunk is a consecutive run of ops that share the same elevate flag
// (after when-block promotion). Header KindPlan is duplicated into each chunk.
type Chunk struct {
	Elevate bool
	Ops     []Op
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
		h.ID = header.ID
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
