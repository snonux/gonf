package plan

import "fmt"

// Chunk is a consecutive run of ops that share the same elevate flag
// (after when-block promotion). Header KindPlan is duplicated into each chunk,
// and each chunk starts with empty stubs of every requirement block recorded
// in a later chunk (see hoistRequirements), so it refuses before mutating.
type Chunk struct {
	Elevate bool
	Ops     []Op
}

// Refusal is implemented by every error ValidateChunkDeps,
// ValidateChangeGates and ValidateRequirementScopes return, and by every
// "plan: "-prefixed error of the blob store's write path (blobError:
// Store.WriteFile, WriteTree and WriteGlob, their packaging scans, BlobRefFor
// and the missing-plan-directory error). Reason is
// the refusal without the plan engine's "plan: " prefix, so a caller that adds
// its own prefix (RecordPlanTo's "RecordPlan: ...", api.Apply's "Apply: ...")
// can show one prefix instead of "plan: plan: ..." or "record: plan: ...".
// Callers reach it with errors.As against a Refusal target; they never parse
// the message.
type Refusal interface {
	error
	Reason() string
}

// refusal is the untyped-detail Refusal: a plan-level pre-flight failure that
// needs no fields of its own (a forward cross-chunk dependency, a cross-chunk
// watch, a gate without a watch).
type refusal struct{ reason string }

func (r *refusal) Error() string  { return "plan: " + r.reason }
func (r *refusal) Reason() string { return r.reason }

// DanglingDepError reports an op whose dependency is recorded by no op of the
// validated plan: a typo'd or never-registered DependsOn ID. It is a typed
// error so callers that hold a friendlier vocabulary than the plan engine
// (api.Apply and api.RecordPlanTo speak of registered resources) can re-word it
// via errors.As without parsing strings. The message deliberately avoids the
// privilege-chunk bookkeeping: for a dangling dependency the chunk index
// carries no information (the dep is in no chunk at all) and only confuses
// users.
type DanglingDepError struct {
	Op  string // ID of the dependent op
	Dep string // dependency ID that no op in the plan carries
}

// Error names the op, the missing dependency and how to fix it.
func (e *DanglingDepError) Error() string { return "plan: " + e.Reason() }

// Reason is Error without the "plan: " prefix (see Refusal).
func (e *DanglingDepError) Reason() string {
	return fmt.Sprintf(
		"op %s depends on %s, which no resource in the plan provides (dangling dependency); "+
			"check the spelling of the ID passed to DependsOn and make sure that resource is registered in the same plan",
		e.Op, e.Dep)
}

// DanglingWatchError reports a change-gated op watching an ID recorded by no
// op of the validated plan: a typo'd OnChange/WatchChanges target. It is the
// change-gate twin of DanglingDepError, typed for the same reason: callers
// re-word it in their own vocabulary through errors.As.
type DanglingWatchError struct {
	Op    string // ID of the change-gated op
	Watch string // watched ID that no op in the plan carries
}

// Error names the op, the missing watch target and how to fix it.
func (e *DanglingWatchError) Error() string { return "plan: " + e.Reason() }

// Reason is Error without the "plan: " prefix (see Refusal).
func (e *DanglingWatchError) Reason() string {
	return fmt.Sprintf(
		"op %s watches %s, which no resource in the plan provides (dangling watch); "+
			"check the spelling of the ID passed to OnChange/WatchChanges and make sure that resource is registered in the same plan",
		e.Op, e.Watch)
}

// ValidateChunkDeps checks dependency direction across privilege chunks
// before any chunk is applied: every dep must be recorded in the dependent's
// own chunk (the apply-side sort reorders within a chunk body) or in an
// earlier chunk (chunks apply in recorded order and never reorder, so the
// earlier chunk satisfies the dep). A dep recorded in a LATER chunk crosses
// the elevation boundary and is refused, as is a dep recorded in no chunk at
// all (a *DanglingDepError — the repository path refuses "depended upon but
// not registered" the same way). chunks holds one op body per privilege chunk
// in apply order (SplitPrivilegeChunks output, headers included); for a
// single-chunk plan it reduces to "every dep is recorded in the chunk".
//
// Wiring: the check runs wherever the WHOLE plan is in hand, before anything
// is applied or uploaded — api.RecordPlanTo (record time: Run, `gonf plan`,
// push, cluster and fleet all record through it), api.ApplyChunks (local
// apply), remote.PushChunks (SSH push) and api.Apply (registered resources;
// the whole plan as a single chunk) — so a rejected plan mutates no
// destination and, on push, sends zero SSH traffic. plan.Apply and
// api.ApplyPlan deliberately do NOT run it: they execute one already-split
// chunk (the elevated re-exec child, a pushed chunk, `gonf apply <file|->`),
// where a dep recorded in an earlier chunk is legitimately absent. A plan
// file written by an older gonf, or edited by hand, and then applied with
// `gonf apply` therefore gets no dangling-dependency protection.
func ValidateChunkDeps(chunks [][]Op) error {
	firstChunk := firstChunkOf(chunks)
	for i, chunk := range chunks {
		for _, op := range chunk {
			for _, dep := range op.Deps {
				j, ok := firstChunk[dep]
				if !ok {
					return &DanglingDepError{Op: op.ID, Dep: dep}
				}
				if j > i {
					return &refusal{reason: fmt.Sprintf(
						"chunk %d: op %s depends on %s which is recorded in later chunk %d; a dependency recorded after its dependent crosses the privilege boundary",
						i, op.ID, dep, j)}
				}
			}
		}
	}
	return nil
}

// ValidateChunks is the whole-plan pre-flight every controller-side entry
// point runs over the split privilege chunks: ValidateChunkDeps (dangling and
// forward cross-chunk dependencies), ValidateChangeGates (watches must live
// in the gated op's own chunk) and ValidateRequirementScopes (requirement
// blocks may only use and sit under host-fact conditions; this is the
// record-time refusal of that rule). api.RecordPlanTo, api.ApplyChunks,
// api.Apply and remote.PushChunks all call this one helper instead of each
// composing the three checks, so a check added here reaches all of them at once.
// The error is a Refusal (see there for the exact concrete types).
func ValidateChunks(chunks []Chunk) error {
	bodies := make([][]Op, len(chunks))
	for i, ch := range chunks {
		bodies[i] = ch.Ops
	}
	if err := ValidateChunkDeps(bodies); err != nil {
		return err
	}
	if err := ValidateChangeGates(bodies); err != nil {
		return err
	}
	return ValidateRequirementScopes(bodies)
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
// watch recorded in no chunk at all (a *DanglingWatchError, worded without
// chunk indexes, which would only leak bookkeeping) and a gated op with no
// watch ids at all (its gate could never fire). Like ValidateChunkDeps, this is
// a controller-side pre-flight, run through ValidateChunks by
// api.RecordPlanTo (record time), api.ApplyChunks, api.Apply and
// remote.PushChunks, so a rejected plan mutates no destination and, on push,
// sends zero SSH traffic.
func ValidateChangeGates(chunks [][]Op) error {
	firstChunk := firstChunkOf(chunks)
	for i, chunk := range chunks {
		for _, op := range chunk {
			if !op.IfChanged {
				continue
			}
			if len(op.Watch) == 0 {
				return &refusal{reason: fmt.Sprintf(
					"chunk %d: op %s is change-gated (if_changed) but watches nothing; the gate can never fire",
					i, op.ID)}
			}
			for _, watch := range op.Watch {
				j, ok := firstChunk[watch]
				if !ok {
					// No chunk index: the watch is in no chunk at all, so an
					// index would only leak the engine's bookkeeping.
					return &DanglingWatchError{Op: op.ID, Watch: watch}
				}
				if j != i {
					return &refusal{reason: fmt.Sprintf(
						"chunk %d: op %s watches %s which is recorded in chunk %d; change reports are chunk-local (each privilege chunk applies as its own process), so a watch must live in the same chunk as the gated op",
						i, op.ID, watch, j)}
				}
			}
		}
	}
	return nil
}

// SplitPrivilegeChunks splits ops into ordered chunks by Elevate.
// when_begin/when_end blocks are never split; if any op inside is elevate,
// the whole block is treated as elevate. Every requirement block (schema 20)
// is then copied, as an empty stub inside its host-fact openers, to the front
// of each earlier chunk (hoistRequirements), because chunks apply as separate
// processes and an earlier one must not mutate before a later one refuses.
// Requirements with a non-host-fact scope are not copied; ValidateChunks,
// which every caller runs on the result, refuses them.
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
	return hoistRequirements(chunks)
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
