package api

import (
	"fmt"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/plan"
)

// supportedGOOS are the operating systems gonf manages, in the order the
// declaration error lists them. WhenOS accepts only these names.
var supportedGOOS = []string{"linux", "darwin", "freebsd", "openbsd", "netbsd"}

// bsdGOOS are the BSDs WhenBSD matches.
var bsdGOOS = []string{"freebsd", "openbsd", "netbsd"}

// WhenOS guards the task with the destination's GOOS being one of goos,
// e.g. WhenOS("linux", "darwin"). Like every serializable guard it travels
// in the plan as a when_begin (a goos fact predicate: Eq for one name, In
// for several) and is evaluated on each destination at apply time; it does
// not hide the task on a controller of another OS (see
// TaskInfo.DestinationGuard).
//
// A name gonf does not manage (see supportedGOOS), or no name at all, is
// recipe misuse: it is reported as a declaration error (internal/declerr),
// and the task gets a controller-side predicate that never holds, so it is
// never activated rather than applied unguarded.
func WhenOS(goos ...string) TaskOption {
	return func(c *taskCandidate) {
		if err := checkGOOS(goos); err != nil {
			declerr.Report(err)
			c.opaque = append(c.opaque, func(Facts) bool { return false })
			return
		}
		c.planWhen = append(c.planWhen, factPredicate("goos", goos))
	}
}

// WhenLinux guards the task with goos == linux (see WhenOS).
func WhenLinux() TaskOption { return WhenOS("linux") }

// WhenDarwin guards the task with goos == darwin, i.e. macOS (see WhenOS).
func WhenDarwin() TaskOption { return WhenOS("darwin") }

// WhenFreeBSD guards the task with goos == freebsd (see WhenOS).
func WhenFreeBSD() TaskOption { return WhenOS("freebsd") }

// WhenOpenBSD guards the task with goos == openbsd (see WhenOS).
func WhenOpenBSD() TaskOption { return WhenOS("openbsd") }

// WhenNetBSD guards the task with goos == netbsd (see WhenOS).
func WhenNetBSD() TaskOption { return WhenOS("netbsd") }

// WhenBSD guards the task with goos being freebsd, openbsd or netbsd (see
// WhenOS).
func WhenBSD() TaskOption { return WhenOS(bsdGOOS...) }

// checkGOOS refuses an empty list and any name outside supportedGOOS.
func checkGOOS(goos []string) error {
	if len(goos) == 0 {
		return fmt.Errorf("WhenOS: needs at least one GOOS (%s)", strings.Join(supportedGOOS, ", "))
	}
	for _, g := range goos {
		if !slices.Contains(supportedGOOS, g) {
			return fmt.Errorf("WhenOS: unknown GOOS %q (want one of %s)", g, strings.Join(supportedGOOS, ", "))
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
