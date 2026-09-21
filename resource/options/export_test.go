package options

import (
	"fmt"
	"reflect"
)

// MissingSetters is exposed to the external resource_capability_test.go. For
// every option that resource family fam accepts (per optionCases), it applies
// the option to a recorder to learn which setter(s) the option calls, then
// checks via reflection that resource has a method of that name with the
// recorder's exact signature. It returns one message per missing setter.
//
// This derives the required capabilities from the options themselves, so the
// check needs no hand-kept per-resource list: an option newly accepted by a
// family, or a resource losing a setter, shows up here instead of as a
// logger.Fatal ("does not support ...") when a user applies the option.
func MissingSetters(fam string, resource any) []string {
	have := reflect.TypeOf(resource)
	recorderType := reflect.TypeOf(&recorder{})
	var missing []string
	for _, tc := range optionCases {
		if !acceptsFamily(tc, fam) {
			continue
		}
		for _, method := range settersCalledBy(tc) {
			want, _ := recorderType.MethodByName(method)
			got, ok := have.MethodByName(method)
			switch {
			case !ok:
				missing = append(missing, fmt.Sprintf("%s: %v lacks %s (needed by %s)", fam, have, method, tc.name))
			case !sameSignature(got.Type, want.Type):
				missing = append(missing, fmt.Sprintf("%s: %v.%s is %v, want %v (needed by %s)", fam, have, method, got.Type, want.Type, tc.name))
			}
		}
	}
	return missing
}

// acceptsFamily reports whether fam is one of tc's expected families.
func acceptsFamily(tc optionCase, fam string) bool {
	for _, f := range tc.families {
		if f == fam {
			return true
		}
	}
	return false
}

// settersCalledBy returns the distinct setter names tc's option calls.
func settersCalledBy(tc optionCase) []string {
	var methods []string
	seen := map[string]bool{}
	for _, call := range tc.want {
		if !seen[call.method] {
			seen[call.method] = true
			methods = append(methods, call.method)
		}
	}
	return methods
}

// sameSignature compares two method types obtained from reflect.Type's
// MethodByName, ignoring the receiver (the first input).
func sameSignature(a, b reflect.Type) bool {
	if a.NumIn() != b.NumIn() || a.NumOut() != b.NumOut() || a.IsVariadic() != b.IsVariadic() {
		return false
	}
	for i := 1; i < a.NumIn(); i++ {
		if a.In(i) != b.In(i) {
			return false
		}
	}
	for i := 0; i < a.NumOut(); i++ {
		if a.Out(i) != b.Out(i) {
			return false
		}
	}
	return true
}

// Families lists the family names MissingSetters understands, in the order of
// allFamilies, so the external test can assert it covers every family.
func Families() []string {
	names := make([]string, len(allFamilies))
	for i, f := range allFamilies {
		names[i] = f.name
	}
	return names
}
