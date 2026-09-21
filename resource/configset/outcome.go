package configset

import (
	"fmt"
	"sync"

	"github.com/snonux/gonf/resource"
)

// outcomes carries each set's per-member publication result from the set's
// apply to its member handles, which apply right after it (they depend on the
// set). It is keyed by set name, then member key. A set forgets its previous
// entry when its apply starts and stores a new one only after it succeeded, so
// a member handle can never report a stale result from an earlier apply in
// the same process: it fails instead. The mutex only guards the map; applies
// are single-goroutine like the rest of the resource layer.
var outcomes = struct {
	sync.Mutex
	m map[string]map[string]bool
}{m: map[string]map[string]bool{}}

// forgetOutcome drops name's previous result at the start of an apply.
func forgetOutcome(name string) {
	outcomes.Lock()
	defer outcomes.Unlock()
	delete(outcomes.m, name)
}

// recordOutcome stores which members of name were published (or would be,
// under dry-run) by the apply that just succeeded.
func recordOutcome(name string, changed map[string]bool) {
	outcomes.Lock()
	defer outcomes.Unlock()
	outcomes.m[name] = changed
}

// memberOutcome reports whether member key of set name changed during the
// current apply; ok is false when the set has not (successfully) applied yet
// or has no such member.
func memberOutcome(name, key string) (changed, ok bool) {
	outcomes.Lock()
	defer outcomes.Unlock()
	set, found := outcomes.m[name]
	if !found {
		return false, false
	}
	changed, ok = set[key]
	return changed, ok
}

// memberApplier is the Applier of a member handle resource (legacy path).
func memberApplier(name, key string) resource.Applier {
	return resource.ApplierFunc(func() error { return applyMember(name, key) })
}

// applyMember notes the member handle's result under its own ID. It mutates
// nothing: the set already published the file. Failing when the set has not
// applied keeps a watch on this handle from silently never firing.
func applyMember(name, key string) error {
	changed, ok := memberOutcome(name, key)
	if !ok {
		return fmt.Errorf("config set member %s: set %s has not been applied before its member handle", memberName(name, key), setID(name))
	}
	resource.NoteResult(memberID(name, key), changed)
	return nil
}
