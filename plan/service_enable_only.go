package plan

import "fmt"

// ValidateServiceEnableOnly refuses incompatible runtime requests before
// any chunk is applied or sent. The service handler repeats this check for
// callers invoking it directly; both paths preserve runtime ownership.
func ValidateServiceEnableOnly(chunks [][]Op) error {
	for _, ops := range chunks {
		for _, op := range ops {
			if op.Op == KindService && op.EnableOnly && (op.Restart || op.Reload || op.IfChanged) {
				return &refusal{reason: fmt.Sprintf("service %s: enable_only cannot be combined with restart, reload or if_changed", op.Name)}
			}
		}
	}
	return nil
}
