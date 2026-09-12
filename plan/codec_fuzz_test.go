package plan

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func FuzzDecodeOp(f *testing.F) {
	for _, op := range sampleOps() {
		b, err := EncodeOp(op)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Add([]byte(`{"op":"plan","version":1}`))
	f.Add([]byte(`{`))
	f.Add([]byte(``))
	f.Add([]byte(`{"op":""}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	// Plan-required edge seeds: unicode paths, large content, deep when nesting.
	f.Add([]byte(`{"op":"link","path":"${HOME}/文档/файл","symlink":"/tmp/目标"}`))
	f.Add([]byte(`{"op":"file","path":"/tmp/x","content_b64":"` + strings.Repeat("QQ==", 256) + `"}`))
	deepWhen, _ := EncodePlan([]Op{
		{Op: KindPlan, Version: 1},
		{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "linux"}}},
		{Op: KindWhenBegin, All: []Predicate{{Fact: "profile", Eq: "fedora"}}},
		{Op: KindWhenBegin, All: []Predicate{{PathExists: "${HOME}/.config"}}},
		{Op: KindFile, Path: "${HOME}/a", ContentB64: "YQ=="},
		{Op: KindWhenEnd},
		{Op: KindWhenEnd},
		{Op: KindWhenEnd},
	})
	f.Add(deepWhen)

	f.Fuzz(func(t *testing.T, data []byte) {
		op, err := DecodeOp(data)
		if err != nil {
			return
		}
		b, err := EncodeOp(op)
		if err != nil {
			t.Fatalf("re-encode after successful decode: %v (op=%#v)", err, op)
		}
		op2, err := DecodeOp(b)
		if err != nil {
			t.Fatalf("re-decode: %v", err)
		}
		if !reflect.DeepEqual(op, op2) {
			t.Fatalf("round-trip mismatch\nfirst %#v\nsecond %#v", op, op2)
		}
	})
}

func FuzzDecodePlan(f *testing.F) {
	raw, err := EncodePlan(sampleOps())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`{"op":"plan","version":1}` + "\n"))
	f.Add([]byte(`{"op":"link","path":"/x"}` + "\n"))
	f.Add([]byte(`{"op":"plan","version":2}` + "\n"))
	f.Add([]byte("\n\n"))
	f.Add([]byte(`{"op":"plan","version":1}` + "\n{"))
	f.Add([]byte(`{"op":"plan","version":1}` + "\n" + `{"op":"link","path":"${HOME}/ユニコード","symlink":"/tmp/ä"}` + "\n"))
	deep, err := EncodePlan([]Op{
		{Op: KindPlan, Version: 1},
		{Op: KindWhenBegin, All: []Predicate{{Fact: "goos", Eq: "linux"}}},
		{Op: KindWhenBegin, All: []Predicate{{PathExists: "${HOME}/x"}}},
		{Op: KindWhenEnd},
		{Op: KindWhenEnd},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(deep)

	f.Fuzz(func(t *testing.T, data []byte) {
		ops, err := DecodePlanBytes(data)
		if err != nil {
			return
		}
		if len(ops) == 0 {
			t.Fatal("decoded empty plan without error")
		}
		if err := ValidateHeader(ops[0]); err != nil {
			t.Fatalf("DecodePlan returned ops failing ValidateHeader: %v", err)
		}
		raw2, err := EncodePlan(ops)
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		ops2, err := DecodePlanBytes(raw2)
		if err != nil {
			t.Fatalf("re-decode: %v", err)
		}
		if !reflect.DeepEqual(ops, ops2) {
			t.Fatalf("plan round-trip mismatch")
		}
		// Remarshal must stay valid JSONL with no panic on large inputs.
		if !bytes.Contains(raw2, []byte(`"op":"plan"`)) {
			t.Fatalf("re-encoded plan missing header: %s", raw2)
		}
	})
}

func FuzzRoundTripOpJSON(f *testing.F) {
	// Seed with marshaled maps that may partially decode into Op.
	for _, op := range sampleOps() {
		b, _ := json.Marshal(op)
		f.Add(string(b))
	}
	f.Add(`{"op":"command","bin":"true","unless":{"bin":"false","args":["x"],"expect_stdout":"y"}}`)

	f.Fuzz(func(t *testing.T, s string) {
		var op Op
		if err := json.Unmarshal([]byte(s), &op); err != nil {
			return
		}
		if op.Op == "" {
			return
		}
		b, err := EncodeOp(op)
		if err != nil {
			return
		}
		op2, err := DecodeOp(b)
		if err != nil {
			t.Fatal(err)
		}
		b2, err := EncodeOp(op2)
		if err != nil {
			t.Fatal(err)
		}
		op3, err := DecodeOp(b2)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(op2, op3) {
			t.Fatalf("unstable round-trip")
		}
	})
}
