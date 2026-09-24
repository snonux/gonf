package resource

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestResetForTestClearsStaleWhenContext pins finding (a) from task re2:
// PushWhenContext's returned pop is exported, and its caller must defer it
// so nested/sibling fragments see a correctly balanced stack; before this
// fix, a caller that dropped the returned pop (never called it) left a
// stale condition on a package-level whenStack that resource.ResetForTest
// never cleared, so it would mislabel a LATER, wholly unrelated collision's
// error message with the wrong condition. whenStack now lives on the
// repository struct itself (next to declaredUnder), so a ResetForTest
// (which swaps in a fresh repository via ResetRepository) discards it along
// with everything else the old scope tracked, just like it already
// discards declaredUnder and every registered resource.
func TestResetForTestClearsStaleWhenContext(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	// Push a condition but deliberately never pop it -- standing in for a
	// caller that forgets to defer the returned pop.
	PushWhenContext(`WhenHostname("stale")`)

	ResetForTest()

	// A fresh collision after the reset, with no When*/WhenPathExists
	// condition active in this new scope, must not be mislabeled with the
	// condition pushed before the reset: currentWhenContext() must report
	// "" again, not the entry a dropped pop left behind on the old
	// repository instance.
	repo := getRepository()
	res, _ := Register("File", "/tmp/collide.txt", noopRegistered)
	err := repo.register(res)
	if err == nil {
		t.Fatal("expected a collision error when registering the same resource twice, got nil")
	}
	if strings.Contains(err.Error(), "stale") {
		t.Fatalf("error = %q, leaked the stale WhenHostname condition pushed before ResetForTest", err)
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("error = %q, want it to mention \"already registered\"", err)
	}
}

// TestPushWhenContextScopedToItsOwnRepositoryInstance pins the mechanism
// behind the fix above: pushWhenContext's returned pop closes over the
// repository instance it was called on, not "whichever repository is
// current now". A push against a since-discarded (post-reset) instance
// therefore cannot resurrect stale state on the live one, and a push
// against the live instance is visible to a later collision on it.
func TestPushWhenContextScopedToItsOwnRepositoryInstance(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	if got := getRepository().currentWhenContext(); got != "" {
		t.Fatalf("currentWhenContext() on a fresh repository = %q, want empty", got)
	}

	pop := PushWhenContext(`WhenPathExists("/tmp")`)
	if got := getRepository().currentWhenContext(); got != `WhenPathExists("/tmp")` {
		t.Fatalf("currentWhenContext() after push = %q, want the pushed condition", got)
	}
	pop()
	if got := getRepository().currentWhenContext(); got != "" {
		t.Fatalf("currentWhenContext() after pop = %q, want empty again", got)
	}
}

// TestWhenStackAccessIsLockedAgainstRegister pins task xf2 finding (b):
// whenStack sits on the repository next to the mutex-protected maps, and
// register() reads it while holding r.mu, so pushWhenContext and its pop
// must take r.mu as well. Concurrent push/register/pop calls on one
// repository are not something the DSL does (it is single-goroutine), and
// their LIFO labelling would be meaningless, but under -race they must at
// least not be a data race; before the fix the unlocked append/reslice
// raced register()'s locked read of the slice.
func TestWhenStackAccessIsLockedAgainstRegister(t *testing.T) {
	r := newRepository()

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				pop := r.pushWhenContext(`WhenHostname("race")`)
				res := Resource{Type: "File", Name: fmt.Sprintf("/tmp/race-%d-%d", g, i)}
				if err := r.register(res); err != nil {
					t.Errorf("register %v: %v", res, err)
				}
				pop()
			}
		}()
	}
	wg.Wait()

	if got := len(r.registeredIDs()); got != 800 {
		t.Fatalf("registered %d resources, want 800", got)
	}
}
