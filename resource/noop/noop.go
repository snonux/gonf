// Package noop implements the Noop resource: a named resource that changes
// nothing and always reports ok. It replaces the Command("true", nil,
// Unless("true", nil), WithName(n)) idiom, which ran two processes on the
// destination and reported "skipped", with one that runs nothing.
//
// It is its own plan kind ("noop", schema v25) rather than a disguised
// existing kind: every existing kind either probes or mutates the host and
// reports under its own type (Command[...], File[...]), none reports a
// plain ok under the recipe's own name.
package noop

import (
	"errors"
	"fmt"
	"strings"

	"github.com/snonux/gonf/resource"
)

// noopType is the Noop resource's type label: its ID is Noop[name].
const noopType = "Noop"

// Noop is a resource that does nothing. It takes no options, so it embeds
// neither DependsOn nor Misuse: other resources may still depend on it.
type Noop struct {
	name string
}

// Present registers a Noop named name and records its plan draft. An empty
// or multi-line name is a declaration error (resource.Refuse) and nothing is
// registered.
func Present(name string) resource.Resource {
	if err := validName(name); err != nil {
		return resource.Refuse(noopType, name, err)
	}
	n := &Noop{name: name}
	r, ok := resource.Register(noopType, name, n)
	if ok {
		resource.RecordPlanDraft(n.planDraft(r.ID()))
	}
	return r
}

// Ensure "applies" a Noop named name without registering it: it notes the
// resource ok and changes nothing, in a dry run as in a real one.
func Ensure(name string) error {
	if err := validName(name); err != nil {
		return err
	}
	resource.Note(resource.FormatID(noopType, name), resource.StatusOK)
	return nil
}

// planDraft records n as a "noop" plan draft under id.
func (n *Noop) planDraft(id string) resource.PlanDraft {
	return resource.PlanDraft{Kind: "noop", ID: id, Name: n.name}
}

// validName refuses a name that cannot form a readable Noop[name] ID.
func validName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("noop: name must not be empty")
	}
	if strings.ContainsAny(name, "\n\r") {
		return fmt.Errorf("noop %q: name must be a single line", name)
	}
	return nil
}
