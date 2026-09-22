package testutil

import (
	"slices"
	"testing"
)

// TestSharedRefs pins what SharedRefs reports: overlapping backing arrays
// (including disjoint-start sub-slices and pointers into an array), shared
// maps and pointees, and nothing for independent copies or empty slices.
func TestSharedRefs(t *testing.T) {
	type holder struct {
		S []int
		M map[int]int
		P *int
	}
	arr := []int{1, 2, 3, 4}
	n := 7
	m := map[int]int{1: 1}
	tests := []struct {
		name string
		a, b any
		want []string
	}{
		{"same slice", holder{S: arr}, holder{S: arr}, []string{"S"}},
		{"sub-slice with a later start", holder{S: arr[:1]}, holder{S: arr[2:]}, []string{"S"}},
		{"pointer into the other's array", holder{S: arr}, holder{P: &arr[3]}, []string{"P"}},
		{"shared map and pointee", holder{M: m, P: &n}, holder{M: m, P: &n}, []string{"M", "P"}},
		{"independent copies", holder{S: slices.Clone(arr), M: map[int]int{1: 1}, P: new(int)}, holder{S: arr, M: m, P: &n}, nil},
		{"empty slices", holder{S: arr[:0:0]}, holder{S: arr[:0:0]}, nil},
		{"disjoint sub-slices capped", holder{S: arr[:2:2]}, holder{S: arr[2:]}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SharedRefs(tt.a, tt.b); !slices.Equal(got, tt.want) {
				t.Fatalf("SharedRefs = %v, want %v", got, tt.want)
			}
		})
	}
}

// node lets an interface field close a cycle.
type node struct {
	Name string
	Next any
}

// TestSharedRefsAndScribbleTerminateOnCycles pins the visited sets: a
// pointer reachable from itself through an interface is walked once.
func TestSharedRefsAndScribbleTerminateOnCycles(t *testing.T) {
	n := &node{Name: "a"}
	n.Next = n
	if got := SharedRefs(n, n); !slices.Equal(got, []string{""}) {
		t.Fatalf("SharedRefs(cycle, cycle) = %v, want the root", got)
	}
	Scribble(n)
	if n.Name != "a~scribbled" {
		t.Fatalf("Name = %q, want scribbled exactly once", n.Name)
	}
}

// TestScribbleReachesInterfacesAndNonStringMaps pins that storage behind an
// interface and maps with non-string keys are mutated.
func TestScribbleReachesInterfacesAndNonStringMaps(t *testing.T) {
	backing := []string{"x"}
	ints := map[int]string{1: "one"}
	v := struct {
		Any  any
		Ints map[int]string
		Nums map[int]int
	}{Any: backing, Ints: ints, Nums: map[int]int{}}
	Scribble(&v)
	if backing[0] != "x~scribbled" {
		t.Errorf("slice behind an interface not mutated: %v", backing)
	}
	if ints[1] != "one~scribbled" || len(ints) != 2 {
		t.Errorf("int-keyed map not mutated/extended: %v", ints)
	}
	if len(v.Nums) != 1 {
		t.Errorf("empty int-keyed map not extended: %v", v.Nums)
	}
}
