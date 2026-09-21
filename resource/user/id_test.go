package user

import (
	"testing"

	internaluser "github.com/snonux/gonf/internal/user"
	"github.com/snonux/gonf/resource"
)

// TestApplyPassesTheRegisteredIDToTheBackend pins task 272: the ID the
// backend reports mutations under is the one Present registers, computed by
// the shared resource.FormatID, so AnyChanged/NoteResult always match it.
func TestApplyPassesTheRegisteredIDToTheBackend(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	registered := Present("svc").ID()
	var got string
	backend := func(id string, _ internaluser.DesiredUser) error {
		got = id
		resource.NoteResult(id, true) // what a backend's Mutate records
		return nil
	}
	if err := newUserWith(fakeBackend(backend), "svc", nil).apply(); err != nil {
		t.Fatalf("apply() = %v", err)
	}
	if got != registered || got != "User[svc]" {
		t.Fatalf("backend id = %q, registered %q, want both User[svc]", got, registered)
	}
	if !resource.AnyChanged(registered) {
		t.Fatal("the backend's change is not visible under the registered ID")
	}
}
