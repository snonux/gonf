package plan

import (
	"fmt"
	"slices"
)

// Requirement blocks (schema 20) are when_begin ops carrying Require. When
// the block's enclosing scope is active but its predicates fail, Apply
// refuses instead of skipping the body.
//
// Scope rule: a requirement, and every when_begin enclosing it, may only use
// host-fact predicates (hostFacts below). Those evaluate identically in every
// privilege chunk and process of one apply and cannot change while ops run,
// which is what makes "refused before any mutation, dry run included" true
// and lets SplitPrivilegeChunks copy a requirement into earlier chunks. A
// path_exists condition (or any unknown fact) can flip during an apply or
// differ between the invoking and the elevated user, so a requirement nested
// under one is refused: at record time by ValidateChunks (via
// ValidateRequirementScopes) and again by Apply's pre-check, which protects
// hand-written or older-recorded plan files.

// hostFacts are the when_begin facts a requirement scope may use.
var hostFacts = map[string]bool{"goos": true, "profile": true, "hostname_contains": true}

// nonHostFactCondition returns a description of the first predicate of op
// that is not a host fact, or "" when every predicate is one.
func nonHostFactCondition(op Op) string {
	for _, p := range op.All {
		switch {
		case p.PathExists != "":
			return fmt.Sprintf("path_exists %s", p.PathExists)
		case !hostFacts[p.Fact]:
			return fmt.Sprintf("fact %q", p.Fact)
		}
	}
	return ""
}

// ValidateRequirementScopes refuses any requirement block whose own
// predicates or enclosing when_begin chain use a condition other than a host
// fact (see the scope rule above). chunks holds one op body per privilege
// chunk; when-blocks never span chunks, so each body is walked on its own.
// Plans without requirements always pass.
func ValidateRequirementScopes(chunks [][]Op) error {
	for _, chunk := range chunks {
		var open []Op
		for _, op := range chunk {
			switch op.Op {
			case KindWhenBegin:
				open = append(open, op)
				if op.Require != "" {
					if err := requirementScopeError(open); err != nil {
						return err
					}
				}
			case KindWhenEnd:
				if len(open) > 0 {
					open = open[:len(open)-1]
				}
			}
		}
	}
	return nil
}

// requirementScopeError checks the chain of open when_begin ops ending in a
// requirement and names the requirement and the offending condition.
func requirementScopeError(chain []Op) error {
	req := chain[len(chain)-1]
	for _, op := range chain {
		if cond := nonHostFactCondition(op); cond != "" {
			where := "is nested under " + op.ID
			if op.ID == req.ID {
				where = "itself uses"
			}
			return &refusal{reason: fmt.Sprintf(
				"requirement %s %s (%s); requirements may only use and be nested under host-fact conditions "+
					"(goos, profile, hostname_contains), because other conditions can change during an apply "+
					"or differ between privilege chunks", req.ID, where, cond)}
		}
	}
	return nil
}

// checkRequirements is Apply's pre-check, run before the first mutation: it
// refuses a requirement block with a non-host-fact scope, then walks the
// when-structure of body with applyLine's scoping rules and refuses the first
// requirement whose enclosing scope is active but whose predicates do not
// hold. Plans without requirements skip it, so their behaviour, including
// when predicate evaluation, is unchanged.
func checkRequirements(body []planLine, facts Facts) error {
	if !slices.ContainsFunc(body, func(l planLine) bool { return l.op.Require != "" }) {
		return nil
	}
	ops := make([]Op, len(body))
	for i, l := range body {
		ops[i] = l.op
	}
	if err := ValidateRequirementScopes([][]Op{ops}); err != nil {
		return err
	}
	var stack []bool
	for _, l := range body {
		switch l.op.Op {
		case KindWhenBegin:
			active := whenActive(stack)
			ok := false
			if active {
				var err error
				if ok, err = evalAll(l.op.All, facts); err != nil {
					return fmt.Errorf("plan: apply line %d: %w", l.line, err)
				}
				if !ok && l.op.Require != "" {
					return fmt.Errorf("plan: apply line %d: %w; nothing was applied",
						l.line, requirementError(l.op, facts))
				}
			}
			stack = append(stack, active && ok)
		case KindWhenEnd:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return nil
}

// requirementError names the unmet requirement and the destination GOOS it
// was evaluated against, so a refusal says which platform was rejected.
func requirementError(op Op, facts Facts) error {
	return fmt.Errorf("%s: requirement not met on this host (goos=%s): %s", op.ID, facts.GOOS, op.Require)
}

// hoistRequirements copies every requirement block of a later chunk, as an
// empty stub inside its enclosing when-openers, to the front of each earlier
// chunk. Chunks apply as separate processes in order, so without the stubs a
// user-level chunk could mutate before the elevated chunk holding the
// requirement refused. Because requirement scopes are host-fact only, a stub
// evaluates exactly as the original does in its own chunk. Empty blocks
// change nothing when the requirement holds; plans without requirements are
// returned unchanged.
func hoistRequirements(chunks []Chunk) []Chunk {
	var pending []Op
	for k := len(chunks) - 1; k >= 0; k-- {
		own := requirementStubs(chunks[k].Ops)
		if len(pending) > 0 {
			ops := make([]Op, 0, len(chunks[k].Ops)+len(pending))
			ops = append(ops, chunks[k].Ops[0]) // header
			ops = append(ops, pending...)
			chunks[k].Ops = append(ops, chunks[k].Ops[1:]...)
		}
		pending = append(own, pending...)
	}
	return chunks
}

// requirementStubs returns, for each requirement block in ops, its enclosing
// when_begin openers, the requirement itself, and the matching when_ends.
// when-blocks are never split across chunks, so the chunk-local stack is the
// complete enclosing context. A requirement whose scope breaks the host-fact
// rule is not copied: such a stub would not evaluate like the original, and
// the plan is refused anyway — by ValidateChunks, which every caller runs on
// SplitPrivilegeChunks' result, and by Apply's pre-check on the chunk that
// holds it.
func requirementStubs(ops []Op) []Op {
	var stubs, open []Op
	for _, op := range ops {
		switch op.Op {
		case KindWhenBegin:
			open = append(open, op)
			if op.Require != "" && requirementScopeError(open) == nil {
				stubs = append(stubs, open...)
				for range len(open) {
					stubs = append(stubs, Op{Op: KindWhenEnd})
				}
			}
		case KindWhenEnd:
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
		}
	}
	return stubs
}
