package plan

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/link"
)

// Facts are live host values used to evaluate when_begin fact predicates.
type Facts struct {
	GOOS     string
	Profile  string
	Hostname string
}

// Apply interprets ops against live host facts and the local filesystem.
// ops[0] must be a plan header that passes ValidateHeader. Stackable
// when_begin/when_end blocks skip inactive bodies without mutation.
// Resource ops other than ensure_dir and link_if_exists are not wired yet.
func Apply(ops []Op, facts Facts) error {
	if len(ops) == 0 {
		return fmt.Errorf("plan: apply: empty plan")
	}
	if err := ValidateHeader(ops[0]); err != nil {
		return err
	}

	var stack []bool
	for i, op := range ops[1:] {
		lineNo := i + 2
		if err := applyLine(op, facts, &stack); err != nil {
			return fmt.Errorf("plan: apply line %d: %w", lineNo, err)
		}
	}
	if len(stack) != 0 {
		return fmt.Errorf("plan: apply: %d unclosed when_begin", len(stack))
	}
	return nil
}

func applyLine(op Op, facts Facts, stack *[]bool) error {
	active := whenActive(*stack)

	switch op.Op {
	case KindWhenBegin:
		ok := false
		if active {
			var err error
			ok, err = evalAll(op.All, facts)
			if err != nil {
				return err
			}
		}
		*stack = append(*stack, active && ok)
		return nil

	case KindWhenEnd:
		if len(*stack) == 0 {
			return fmt.Errorf("when_end without matching when_begin")
		}
		*stack = (*stack)[:len(*stack)-1]
		return nil

	case KindPlan:
		return fmt.Errorf("duplicate plan header")
	}

	if !active {
		return nil
	}
	return applyActive(op)
}

func whenActive(stack []bool) bool {
	if len(stack) == 0 {
		return true
	}
	return stack[len(stack)-1]
}

func applyActive(op Op) error {
	switch op.Op {
	case KindEnsureDir:
		return applyEnsureDir(op)
	case KindLinkIfExists:
		return applyLinkIfExists(op)
	case KindLink, KindFile, KindDir, KindPackage, KindCommand, KindSyncDir:
		return fmt.Errorf("op %q apply not implemented", op.Op)
	default:
		return fmt.Errorf("unknown op %q", op.Op)
	}
}

func applyEnsureDir(op Op) error {
	path, err := ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("ensure_dir: missing path")
	}
	var opts []opt.Option
	if op.Mode != "" {
		mode, err := parseMode(op.Mode)
		if err != nil {
			return fmt.Errorf("ensure_dir: %w", err)
		}
		opts = append(opts, opt.WithMode(mode))
	}
	return dir.Ensure(path, opts...)
}

func applyLinkIfExists(op Op) error {
	path, err := ExpandPath(op.Path)
	if err != nil {
		return err
	}
	target, err := ExpandPath(op.Target)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("link_if_exists: missing path")
	}
	if target == "" {
		return fmt.Errorf("link_if_exists: missing target")
	}
	_, err = os.Stat(target)
	switch {
	case err == nil:
		return link.Ensure(path, opt.WithSymlink(target))
	case os.IsNotExist(err):
		return link.Ensure(path, opt.IsAbsent)
	default:
		return fmt.Errorf("link_if_exists: stat target %s: %w", target, err)
	}
}

func evalAll(preds []Predicate, facts Facts) (bool, error) {
	for _, p := range preds {
		ok, err := evalPredicate(p, facts)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func evalPredicate(p Predicate, facts Facts) (bool, error) {
	switch {
	case p.PathExists != "":
		path, err := ExpandPath(p.PathExists)
		if err != nil {
			return false, err
		}
		return pathExists(path)
	case p.Fact != "":
		return evalFact(p.Fact, p.Eq, facts)
	default:
		return false, fmt.Errorf("empty predicate")
	}
}

func evalFact(name, eq string, facts Facts) (bool, error) {
	switch name {
	case "goos":
		return facts.GOOS == eq, nil
	case "profile":
		return facts.Profile == eq, nil
	case "hostname_contains":
		host := strings.ToLower(facts.Hostname)
		want := strings.ToLower(eq)
		return strings.Contains(host, want), nil
	default:
		return false, fmt.Errorf("unknown fact %q", name)
	}
}

func pathExists(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("path_exists: empty path")
	}
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func parseMode(s string) (os.FileMode, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid mode %q: %w", s, err)
	}
	return os.FileMode(v), nil
}
