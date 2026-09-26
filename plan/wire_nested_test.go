package plan

import (
	"reflect"
	"testing"
)

// Empty nested slices decode to nil, so decode -> encode -> decode is
// DeepEqual (the invariant FuzzDecodeOp asserts).
func TestNestedEmptySlicesRoundTrip(t *testing.T) {
	for _, in := range []string{
		`{"op":"file","path":"/f","blocks":[{"name":"a","lines":[]}]}`,
		`{"op":"config_set","name":"s","validators":[{"bin":"x","args":[]}]}`,
		`{"op":"when_begin","all":[{"fact":"goos","in":[]}]}`,
	} {
		op, err := DecodeOp([]byte(in))
		if err != nil {
			t.Logf("%s: decode: %v", in, err)
			continue
		}
		b, err := EncodeOp(op)
		if err != nil {
			t.Fatal(err)
		}
		op2, err := DecodeOp(b)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(op, op2) {
			t.Errorf("round trip changed %s", in)
		}
	}
}
