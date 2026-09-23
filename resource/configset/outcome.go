package configset

import (
	"fmt"
	"sync"

	"github.com/snonux/gonf/resource"
)

// outcomeStore carries each set's per-member publication result from the
// set's apply to its member handles, which apply right after it (they depend
// on the set). It is keyed by set name, then member key. A set forgets its
// previous entry when its apply starts and stores a new one only after it
// succeeded, so a member handle can never report a stale result from an
// earlier apply: it fails instead.
//
// There is no package-level store. Each one is shared only by a set and the
// member handles that read it: Present creates one per set for the set's
// applier and its member appliers, the plan handlers registered in init share
// one between the config_set and config_set_member kinds (newHandlers), and
// Ensure, which has no member handles, uses a throwaway one. This package's
// unit tests create their own, so they neither see each other's results nor
// need to reset anything; tests elsewhere (e.g. api) go through the shared
// store of the registered handlers, where a set forgets its entry when its
// apply starts. The mutex only guards the map; applies are single-goroutine like
// the rest of the resource layer.
type outcomeStore struct {
	mu sync.Mutex
	m  map[string]map[string]bool
}

// newOutcomeStore returns an empty store.
func newOutcomeStore() *outcomeStore {
	return &outcomeStore{m: map[string]map[string]bool{}}
}

// forget drops name's previous result at the start of an apply.
func (o *outcomeStore) forget(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.m, name)
}

// record stores which members of name were published (or would be, under
// dry-run) by the apply that just succeeded.
func (o *outcomeStore) record(name string, changed map[string]bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[name] = changed
}

// member reports whether member key of set name changed during the current
// apply; ok is false when the set has not (successfully) applied yet or has
// no such member.
func (o *outcomeStore) member(name, key string) (changed, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	set, found := o.m[name]
	if !found {
		return false, false
	}
	changed, ok = set[key]
	return changed, ok
}

// applyMember notes the member handle's result, read from outcomes, under its
// own ID. It mutates nothing: the set already published the file. Failing
// when the set has not applied keeps a watch on this handle from silently
// never firing. The plan path (memberHandler.Apply, planwire.go) calls it
// directly; nothing calls it through the repository since task e72 retired
// that path.
func applyMember(outcomes *outcomeStore, name, key string) error {
	changed, ok := outcomes.member(name, key)
	if !ok {
		return fmt.Errorf("config set member %s: set %s has not been applied before its member handle", memberName(name, key), setID(name))
	}
	resource.NoteResult(memberID(name, key), changed)
	return nil
}
