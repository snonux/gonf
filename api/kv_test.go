package api

import (
	"reflect"
	"testing"
)

func TestParseKV(t *testing.T) {
	got, err := ParseKV([]string{"a", "1", "b", "2"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"a", "1"}, {"b", "2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	if _, err := ParseKV([]string{"a"}); err == nil {
		t.Fatal("expected odd-length error")
	}
}

func TestEachKV(t *testing.T) {
	var keys, vals []string
	EachKV(List("k1", "v1", "k2", "v2"), func(k, v string) {
		keys = append(keys, k)
		vals = append(vals, v)
	})
	if !reflect.DeepEqual(keys, []string{"k1", "k2"}) {
		t.Fatalf("keys = %v", keys)
	}
	if !reflect.DeepEqual(vals, []string{"v1", "v2"}) {
		t.Fatalf("vals = %v", vals)
	}
}
