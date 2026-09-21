package api

import (
	"reflect"
	"testing"
)

// TestAggregateMemberInProgressIsCycle: a member still being recorded must be
// reported as a cycle, not skipped as "already recorded" (the recorded mark
// is set only after a member's recording finished).
func TestAggregateMemberInProgressIsCycle(t *testing.T) {
	resetAliasTest(t)
	AggregateTasks("outer", "", "A")
	AggregateTasks("A", "", "B")
	AggregateTasks("B", "", "A")

	wantErrContains(t, recordErr(t, "outer"), "task recursion cycle detected: A -> B -> A")
}

// TestOperationalContainmentIgnoresActivation: an Operational member that is
// inactive under the current profile still makes its AggregateTasks
// operational work, so pattern aggregates skip it on every controller.
func TestOperationalContainmentIgnoresActivation(t *testing.T) {
	dir := resetAliasTest(t)
	setup := fileTask(dir, "p_setup")
	fileTask(dir, "op_fedora", Operational(), WhenProfile("fedora"))
	fileTask(dir, "other")
	AggregateTasks("p_ops", "", "op_fedora", "other")
	Aggregate("p", "", "^p_")

	SetProfileOverride("rocky")
	Activate(DetectFacts())
	if got := recordedFilePaths(t, "p"); !reflect.DeepEqual(got, []string{setup}) {
		t.Fatalf("pattern aggregate ops = %v, want only %v", got, setup)
	}
}

// badForHostsTwice misuses ForHosts twice; each misuse stashes an error.
func badForHostsTwice() {
	ForHosts("", func(string, int) {})
	ForHosts[int]("k", nil)
}

// TestBodyErrorFirstOneWins pins the first-error rule of every stash path:
// stashBodyError keeps the first error, stashAggregateError only wraps the
// stashed error it propagates (never replaces it with an unrelated one), and
// propagateNestedRunError does not overwrite an earlier stash.
func TestBodyErrorFirstOneWins(t *testing.T) {
	resetAliasTest(t)
	const first = "ForHosts: key must not be empty"
	Task("bad", "", badForHostsTwice)
	AggregateTasks("agg", "", "bad")
	AggregateTasks("typo_agg", "", "no_such_member")
	Task("then_agg", "", func() {
		ForHosts("", func(string, int) {})
		_ = Run("typo_agg")
	})
	Task("then_run", "", func() {
		ForHosts("", func(string, int) {})
		_ = Run("no_such_task")
	})

	cases := map[string]string{
		"bad":      first,
		"agg":      "aggregate agg: " + first,
		"then_agg": first,
		"then_run": first,
	}
	for name, want := range cases {
		err := recordErr(t, name)
		if err.Error() != want {
			t.Errorf("%s: error = %q, want %q", name, err, want)
		}
	}
}
