package plan

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/link"
	"github.com/snonux/gonf/resource/pkg"
	"github.com/snonux/gonf/resource/systemd"
	"github.com/snonux/gonf/resource/timer"
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
// planDir is the directory containing blobs/ sidecars (usually next to the
// plan JSONL). Pass "" when the plan only uses content_b64 and no blobs.
func Apply(ops []Op, facts Facts, planDir string) error {
	if len(ops) == 0 {
		return fmt.Errorf("plan: apply: empty plan")
	}
	if err := ValidateHeader(ops[0]); err != nil {
		return err
	}

	resource.ResetReport()

	var stack []bool
	for i, op := range ops[1:] {
		lineNo := i + 2
		if err := applyLine(op, facts, planDir, &stack); err != nil {
			return fmt.Errorf("plan: apply line %d: %w", lineNo, err)
		}
	}
	if len(stack) != 0 {
		return fmt.Errorf("plan: apply: %d unclosed when_begin", len(stack))
	}
	return nil
}

func applyLine(op Op, facts Facts, planDir string, stack *[]bool) error {
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
	return applyActive(op, planDir)
}

func whenActive(stack []bool) bool {
	if len(stack) == 0 {
		return true
	}
	return stack[len(stack)-1]
}

func applyActive(op Op, planDir string) error {
	switch op.Op {
	case KindEnsureDir:
		return applyEnsureDir(op)
	case KindLinkIfExists:
		return applyLinkIfExists(op)
	case KindFile:
		return applyFile(op, planDir)
	case KindSyncDir:
		return applySyncDir(op, planDir)
	case KindLink:
		return applyLink(op)
	case KindDir:
		return applyDir(op)
	case KindPackage:
		return applyPackage(op)
	case KindCommand:
		return applyCommand(op)
	case KindTimer:
		return applyTimer(op)
	case KindDaemonReload:
		return applyDaemonReload(op)
	default:
		return fmt.Errorf("unknown op %q", op.Op)
	}
}

func applyFile(op Op, planDir string) error {
	path, err := ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("file: missing path")
	}
	if op.Absent {
		return file.Ensure(path, opt.IsAbsent)
	}

	var opts []opt.Option
	if op.AddLine != "" || op.RemoveLine != "" {
		if op.ContentB64 != "" || op.Blob != "" {
			return fmt.Errorf("file: add_line/remove_line cannot combine with content_b64/blob")
		}
		if op.RemoveLine != "" {
			opts = append(opts, opt.WithoutLine(op.RemoveLine))
		}
		if op.AddLine != "" {
			opts = append(opts, opt.WithLine(op.AddLine))
		}
		if op.Mode != "" {
			mode, err := parseMode(op.Mode)
			if err != nil {
				return fmt.Errorf("file: %w", err)
			}
			opts = append(opts, opt.WithMode(mode))
		}
		return file.Ensure(path, opts...)
	}

	var content []byte
	switch {
	case op.ContentB64 != "":
		data, err := DecodeContentB64(op.ContentB64)
		if err != nil {
			return err
		}
		content = data
	case op.Blob != "":
		data, err := ReadFile(planDir, op.Blob)
		if err != nil {
			return err
		}
		content = data
	default:
		return fmt.Errorf("file: missing content_b64 and blob")
	}

	opts = []opt.Option{opt.WithContent(string(content))}
	if op.Mode != "" {
		mode, err := parseMode(op.Mode)
		if err != nil {
			return fmt.Errorf("file: %w", err)
		}
		opts = append(opts, opt.WithMode(mode))
	}
	return file.Ensure(path, opts...)
}

func applySyncDir(op Op, planDir string) error {
	path, err := ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("sync_dir: missing path")
	}
	if op.Blob == "" {
		return fmt.Errorf("sync_dir: missing blob id")
	}
	src, err := Resolve(planDir, op.Blob)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("sync_dir: blob %q: %w", op.Blob, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("sync_dir: blob %q is not a directory", op.Blob)
	}

	opts := []opt.Option{opt.WithSource(src)}
	if op.Mode != "" {
		mode, err := parseMode(op.Mode)
		if err != nil {
			return fmt.Errorf("sync_dir: %w", err)
		}
		opts = append(opts, opt.WithMode(mode))
	}
	if op.FileMode != "" {
		mode, err := parseMode(op.FileMode)
		if err != nil {
			return fmt.Errorf("sync_dir: file_mode: %w", err)
		}
		opts = append(opts, opt.WithFileMode(mode))
	}
	if op.Prune {
		opts = append(opts, opt.WithPrune)
	}
	return dir.Ensure(path, opts...)
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

func applyDir(op Op) error {
	path, err := ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("dir: missing path")
	}
	var opts []opt.Option
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	if op.Prune {
		opts = append(opts, opt.WithPrune)
	}
	if op.Mode != "" {
		mode, err := parseMode(op.Mode)
		if err != nil {
			return fmt.Errorf("dir: %w", err)
		}
		opts = append(opts, opt.WithMode(mode))
	}
	return dir.Ensure(path, opts...)
}

func applyLink(op Op) error {
	path, err := ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("link: missing path")
	}
	if op.Absent {
		return link.Ensure(path, opt.IsAbsent)
	}
	switch {
	case op.Symlink != "":
		target, err := ExpandPath(op.Symlink)
		if err != nil {
			return err
		}
		return link.Ensure(path, opt.WithSymlink(target))
	case op.Hardlink != "":
		target, err := ExpandPath(op.Hardlink)
		if err != nil {
			return err
		}
		return link.Ensure(path, opt.WithHardlink(target))
	default:
		return fmt.Errorf("link: missing symlink or hardlink target")
	}
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

func applyPackage(op Op) error {
	if op.Name == "" {
		return fmt.Errorf("package: missing name")
	}
	var opts []opt.Option
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	return pkg.Ensure(op.Name, opts...)
}

func applyCommand(op Op) error {
	if op.Bin == "" {
		return fmt.Errorf("command: missing bin")
	}
	var opts []opt.Option
	if op.Name != "" {
		opts = append(opts, opt.WithName(op.Name))
	}
	if op.Dir != "" {
		dirPath, err := ExpandPath(op.Dir)
		if err != nil {
			return err
		}
		opts = append(opts, opt.WithDir(dirPath))
	}
	if op.Creates != "" {
		creates, err := ExpandPath(op.Creates)
		if err != nil {
			return err
		}
		opts = append(opts, opt.Creates(creates))
	}
	if len(op.Env) > 0 {
		opts = append(opts, opt.WithEnv(op.Env))
	}
	if op.Unless != nil {
		opts = append(opts, guardOption(op.Unless, true)...)
	}
	if op.OnlyIf != nil {
		opts = append(opts, guardOption(op.OnlyIf, false)...)
	}
	return cmd.Ensure(op.Bin, append([]string(nil), op.Args...), opts...)
}

func applyTimer(op Op) error {
	if op.Name == "" {
		return fmt.Errorf("timer: missing name")
	}
	var opts []opt.Option
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	if op.User {
		opts = append(opts, opt.WithUser)
	}
	if op.EnableOnly {
		opts = append(opts, opt.WithEnableOnly)
	}
	return timer.Ensure(op.Name, opts...)
}

func applyDaemonReload(op Op) error {
	var opts []opt.Option
	if op.User {
		opts = append(opts, opt.WithUser)
	}
	if op.IfChanged {
		opts = append(opts, opt.IfChanged)
		if len(op.Watch) > 0 {
			opts = append(opts, opt.WithWatch(op.Watch...))
		}
	}
	return systemd.Ensure(opts...)
}

func guardOption(g *Guard, unless bool) []opt.Option {
	var gopts []opt.GuardOption
	if g.ExpectStdout != "" {
		gopts = append(gopts, opt.ExpectStdout(g.ExpectStdout))
	}
	if g.ExpectExit != nil {
		gopts = append(gopts, opt.ExpectExit(*g.ExpectExit))
	}
	if unless {
		return []opt.Option{opt.Unless(g.Bin, append([]string(nil), g.Args...), gopts...)}
	}
	return []opt.Option{opt.OnlyIf(g.Bin, append([]string(nil), g.Args...), gopts...)}
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
