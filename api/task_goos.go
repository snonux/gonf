package api

import (
	"fmt"
	"slices"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/platform"
	"github.com/snonux/gonf/plan"
)

// WhenOS guards the task with the destination's GOOS being one of goos,
// e.g. WhenOS("linux", "darwin"). Like every serializable guard it travels
// in the plan as a when_begin (a goos fact predicate: Eq for one name, In
// for several) and is evaluated on each destination at apply time; it does
// not hide the task on a controller of another OS (see
// TaskInfo.DestinationGuard).
//
// A name gonf does not manage (see internal/platform), or no name at all, is
// recipe misuse: it is reported as a declaration error (internal/declerr),
// and the task gets a controller-side predicate that never holds, so it is
// never activated rather than applied unguarded.
func WhenOS(goos ...string) TaskOption { return goosGuard("WhenOS", false, goos) }

// WhenNotOS guards the task with the destination's GOOS being none of goos.
// It travels in the plan and adds to other task guards (AND), just like
// WhenOS. Empty lists and unsupported names are declaration errors.
func WhenNotOS(goos ...string) TaskOption { return goosGuard("WhenNotOS", true, goos) }

// WhenLinux guards the task with goos == linux (see WhenOS).
func WhenLinux() TaskOption { return WhenOS(platform.Linux) }

// WhenDarwin guards the task with goos == darwin, i.e. macOS (see WhenOS).
func WhenDarwin() TaskOption { return WhenOS(platform.Darwin) }

// WhenFreeBSD guards the task with goos == freebsd (see WhenOS).
func WhenFreeBSD() TaskOption { return WhenOS(platform.FreeBSD) }

// WhenOpenBSD guards the task with goos == openbsd (see WhenOS).
func WhenOpenBSD() TaskOption { return WhenOS(platform.OpenBSD) }

// WhenNetBSD guards the task with goos == netbsd (see WhenOS).
func WhenNetBSD() TaskOption { return WhenOS(platform.NetBSD) }

// WhenBSD guards the task with goos being freebsd, openbsd or netbsd (see
// WhenOS).
func WhenBSD() TaskOption { return WhenOS(platform.BSD()...) }

// goosGuard shares validation and serializable lowering for both OS guards.
func goosGuard(name string, not bool, goos []string) TaskOption {
	goos = slices.Clone(goos)
	return func(c *taskCandidate) {
		if err := checkGOOS(goos); err != nil {
			declerr.Report(fmt.Errorf("%s: %w", name, err))
			c.opaque = append(c.opaque, func(Facts) bool { return false })
			return
		}
		p := factPredicate("goos", goos)
		p.Not = not
		c.planWhen = append(c.planWhen, p)
	}
}

// checkGOOS refuses an empty list and any name platform.Check refuses.
func checkGOOS(goos []string) error {
	if len(goos) == 0 {
		return fmt.Errorf("needs at least one GOOS (%s)", platform.List())
	}
	for _, g := range goos {
		if err := platform.Check(g); err != nil {
			return err
		}
	}
	return nil
}

// factPredicate lowers "fact is one of values" to one serializable
// predicate: Eq for a single value, In (OR-of-values) for several. values
// must not be empty; it is copied, so the caller may reuse its slice.
func factPredicate(fact string, values []string) plan.Predicate {
	if len(values) == 1 {
		return plan.Predicate{Fact: fact, Eq: values[0]}
	}
	return plan.Predicate{Fact: fact, In: slices.Clone(values)}
}
