package api

// And returns a predicate that is true only when every pred is true.
// With zero preds it is always true.
func And(preds ...func(Facts) bool) func(Facts) bool {
	return func(f Facts) bool {
		for _, p := range preds {
			if p == nil {
				continue
			}
			if !p(f) {
				return false
			}
		}
		return true
	}
}

// Or returns a predicate that is true when any pred is true.
// With zero preds it is always false.
func Or(preds ...func(Facts) bool) func(Facts) bool {
	return func(f Facts) bool {
		for _, p := range preds {
			if p == nil {
				continue
			}
			if p(f) {
				return true
			}
		}
		return false
	}
}
