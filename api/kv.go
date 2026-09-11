package api

import (
	"fmt"
	"log"
)

// ParseKV interprets a flat key/value list (as from Elems) into pairs.
// An odd-length list is an error.
func ParseKV(kv []string) ([][2]string, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("ParseKV: odd number of elements (%d)", len(kv))
	}
	out := make([][2]string, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		out = append(out, [2]string{kv[i], kv[i+1]})
	}
	return out, nil
}

// EachKV calls fn for each consecutive key/value in kv (typically from Elems).
// An odd-length list is a fatal error.
func EachKV(kv []string, fn func(key, val string)) {
	pairs, err := ParseKV(kv)
	if err != nil {
		log.Fatal(err)
	}
	for _, p := range pairs {
		fn(p[0], p[1])
	}
}
