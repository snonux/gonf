package plan

import (
	"fmt"

	"github.com/snonux/gonf/internal/platform"
)

// validateNegatedOSChunks refuses malformed negation before applying or shipping
// any chunk, including predicates inside an inactive enclosing guard.
func validateNegatedOSChunks(chunks [][]Op) error {
	for _, ops := range chunks {
		for _, op := range ops {
			for _, p := range op.All {
				if !p.Not {
					continue
				}
				if op.Op != KindWhenBegin {
					return &refusal{reason: "negated OS predicate requires when_begin"}
				}
				if err := validateNegatedOS(p); err != nil {
					return &refusal{reason: err.Error()}
				}
			}
		}
	}
	return nil
}

func validateNegatedOS(p Predicate) error {
	if p.Fact != "goos" || p.PathExists != "" || (p.Eq != "" && len(p.In) > 0) {
		return fmt.Errorf("negated OS predicate requires only goos with either eq or in")
	}
	values := p.In
	if len(values) == 0 {
		values = []string{p.Eq}
	}
	for _, value := range values {
		if err := platform.Check(value); err != nil {
			return fmt.Errorf("negated OS predicate: %w", err)
		}
	}
	return nil
}
