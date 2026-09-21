package api

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
)

var orderHdr = plan.Op{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "apply"}

// orderOp builds a command op for the ordering tests.
func orderOp(id string, elevate bool, deps ...string) plan.Op {
	return plan.Op{Op: plan.KindCommand, Bin: "true", ID: id, Elevate: elevate, Deps: deps}
}

// bodyIDs lists the op IDs after the plan header.
func bodyIDs(t *testing.T, ops []plan.Op) string {
	t.Helper()
	if ops[0].Op != plan.KindPlan {
		t.Fatalf("header moved: %+v", ops[0])
	}
	ids := make([]string, 0, len(ops)-1)
	for _, o := range ops[1:] {
		ids = append(ids, o.ID)
	}
	return strings.Join(ids, ",")
}

// TestOrderForPrivilegeSplit pins the ordering Apply splits: deps first,
// privilege classes kept together, the fewer-chunk order of the two starting
// classes, and ties in incoming order.
func TestOrderForPrivilegeSplit(t *testing.T) {
	op := orderOp
	cases := []struct {
		name string
		ops  []plan.Op
		want string
	}{
		{"dependency before dependent", []plan.Op{orderHdr, op("a", false, "c"), op("b", true, "c"), op("c", false)}, "c,a,b"},
		{"classes grouped", []plan.Op{orderHdr, op("a", true), op("b", false), op("c", true), op("d", false)}, "a,c,b,d"},
		// Starting with a's class gives a|b|c (3 chunks); starting with the
		// elevated class gives b|a,c (2 chunks), which must win.
		{"fewest chunks over both starting classes", []plan.Op{orderHdr, op("a", false), op("b", true), op("c", false, "b")}, "b,a,c"},
		{"equal chunk counts keep the lowest-index start", []plan.Op{orderHdr, op("a", false), op("b", true)}, "a,b"},
		{"dangling dep ignored", []plan.Op{orderHdr, op("a", true, "typo"), op("b", false)}, "a,b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := orderForPrivilegeSplit(tc.ops)
			if err != nil {
				t.Fatalf("orderForPrivilegeSplit() error = %v", err)
			}
			if ids := bodyIDs(t, got); ids != tc.want {
				t.Fatalf("order = %s, want %s", ids, tc.want)
			}
		})
	}
}

// TestOrderForPrivilegeSplitRefusesCycles pins that every dependency cycle is
// refused with exactly the loop named: a self-dependency (the smallest
// cycle) and a lower-indexed op that depends on a cycle without being on it
// (z -> x with x <-> y), which must not appear in the named loop.
func TestOrderForPrivilegeSplitRefusesCycles(t *testing.T) {
	op := orderOp
	cases := []struct {
		name string
		ops  []plan.Op
		want string
	}{
		{"two-op cycle", []plan.Op{orderHdr, op("a", true, "b"), op("b", false, "a")}, "a -> b -> a"},
		{"cycle behind an acyclic prefix", []plan.Op{orderHdr, op("e", true), op("x", false, "y"), op("y", false, "x"), op("z", false, "x")}, "x -> y -> x"},
		{"lead-in op not on the cycle", []plan.Op{orderHdr, op("e", true), op("z", false, "x"), op("x", false, "y"), op("y", false, "x")}, "x -> y -> x"},
		{"self-dependency after elevated op", []plan.Op{orderHdr, op("a", true), op("b", false, "b")}, "b -> b"},
		{"self-dependent elevated op", []plan.Op{orderHdr, op("a", false), op("b", true, "b")}, "b -> b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := orderForPrivilegeSplit(tc.ops)
			want := "Apply: circular dependency: " + tc.want + " (each depends on the next)"
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("orderForPrivilegeSplit() error = %v, want prefix %q", err, want)
			}
		})
	}
}

// TestSameClassKahnStaysWithSwitchedClass pins that after a forced class
// switch the sort keeps taking the class it switched TO: with a(F),
// b(T), c(F, needs b), d(T) and a start in F, the order is a | b d | c. A
// sort that kept preferring the starting class after the switch would emit
// a | b | c | d (four chunks).
func TestSameClassKahnStaysWithSwitchedClass(t *testing.T) {
	body := []plan.Op{orderOp("a", false), orderOp("b", true), orderOp("c", false, "b"), orderOp("d", true)}
	order, ok := sameClassKahn(body, newDepGraph(body), false)
	if !ok {
		t.Fatal("sameClassKahn reported a cycle in an acyclic body")
	}
	var ids []string
	for _, i := range order {
		ids = append(ids, body[i].ID)
	}
	if got := strings.Join(ids, ","); got != "a,b,d,c" {
		t.Fatalf("order = %s, want a,b,d,c", got)
	}
}
