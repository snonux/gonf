package systemd

import (
	"slices"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// JoinRegisteredReload lets a resource that reloads the manager itself, as
// part of one composite op, share the bus's registered daemon-reload
// instead of adding a second reload to the apply. SystemdTimer is the
// caller: its op writes its unit files, reloads privately when they changed
// and only then converges the timer, so the reload cannot move out of the
// op. Instead the registered reload on the same bus (DaemonReload[user]
// for user, DaemonReload[system] otherwise; typically from SystemdUnits) is
// ordered after the joiner by one extra dependency edge on joinerID. The
// joiner then applies first, and its private reload also loads every
// input the registered reload watches that was written before it; the
// registered reload, whose gate only fires on a change noted after the
// bus's last reload (changedSinceLastReload), is then held unless another
// input changed later. Either way the bus reloads once, after all unit
// files and before every activation. Only the dependency list of the
// recorded reload changes, so the plan schema does not.
//
// It returns false, and leaves everything as it was (the joiner then keeps
// its private reload after the registered one, two reloads at worst, as
// before), when:
//   - no daemon-reload is registered on that bus in this recipe scope yet
//     (a SystemdTimer alone, one on the other bus, or one declared before
//     the reload: it then applies before the reload's inputs are written,
//     so its reload could not cover them anyway);
//   - resource.AmendRegistered refuses the edge: the joiner already
//     depends on the reload (DependsOn a SystemdUnits composition), which
//     would close a cycle, or a when-block boundary or privilege change
//     separates the recorded reload from the joiner.
//
// A registered reload that is not change-gated always reloads, so joining
// it only fixes the order; the joiner's own reload still runs on change.
func JoinRegisteredReload(user bool, joinerID string) bool {
	id := busReloadID(user)
	r, d, ok := registeredReload(id)
	if !ok {
		return false
	}
	if slices.Contains(d.DependsOn.IDs, joinerID) {
		return true
	}
	m := *d
	m.DependsOn.IDs = append(slices.Clone(d.DependsOn.IDs), joinerID)
	if err := resource.AmendRegistered(m.planDraft(r.ID()), joinerID); err != nil {
		logger.Debug("%s: %s keeps its own daemon-reload, not ordered before this bus's registered one: %v", id, joinerID, err)
		return false
	}
	*d = m
	logger.Debug("%s: ordered after %s, which shares this bus's reload", id, joinerID)
	return true
}
