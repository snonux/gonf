package plan

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/cron"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/link"
	"github.com/snonux/gonf/resource/pkg"
	"github.com/snonux/gonf/resource/service"
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
// Resource ops are topologically sorted by their deps within each contiguous
// run between control ops (plan header, when_begin, when_end), mirroring the
// repository path's dependency order; dep-free plans keep recorded order.
// Deps recorded in this body earlier, or applied by an earlier privilege
// chunk or invocation, count as satisfied; a dep recorded later in this body
// (later when-block) is refused before any mutation. A dep recorded nowhere
// in this body is satisfied at chunk level too — chunk boundaries are
// invisible to a chunk-level Apply; the controller-side pre-flight
// ValidateChunkDeps refuses forward cross-chunk and dangling deps before any
// chunk is applied.
// planDir is the directory containing blobs/ sidecars (usually next to the
// plan JSONL). Pass "" when the plan only uses content_b64 and no blobs.
// After applying (or refusing) the ops, the collected resource summary is
// printed to stderr — one summary per Apply invocation, mirroring the legacy
// repository path; chunked applies therefore print one summary per chunk.
func Apply(ops []Op, facts Facts, planDir string) error {
	if len(ops) == 0 {
		return fmt.Errorf("plan: apply: empty plan")
	}
	if err := ValidateHeader(ops[0]); err != nil {
		return err
	}

	resource.ResetReport()
	// The summary is what the report machinery exists for: every apply ends
	// with the collected outcomes, and a refused plan (sort errors) prints an
	// empty summary making clear nothing was applied. Deferred so partial
	// results are reported on error paths too.
	defer resource.PrintSummary(os.Stderr)

	// Sorting (and its dangling/cycle checks) runs before any mutation so a
	// refused plan leaves the destination untouched.
	body, err := sortedApplyOrder(ops[1:])
	if err != nil {
		return err
	}

	var stack []bool
	for _, l := range body {
		if err := applyLine(l.op, facts, planDir, &stack); err != nil {
			return fmt.Errorf("plan: apply line %d: %w", l.line, err)
		}
	}
	if len(stack) != 0 {
		return fmt.Errorf("plan: apply: %d unclosed when_begin", len(stack))
	}
	return nil
}

// planLine pairs an op with its original 1-based JSONL line number so
// apply-time errors name the recorded line even after dependency reordering.
type planLine struct {
	op   Op
	line int
}

// sortedApplyOrder returns the plan body in apply order: contiguous runs of
// resource ops between control ops (plan header, when_begin, when_end) are
// topologically sorted by their dep lists, mirroring the repository path.
// Control ops keep their recorded position, so resource ops are never
// reordered across when_* boundaries. A dep outside the current run is
// classified: recorded earlier in this body → satisfied (placed); recorded
// later in this body (first occurrence after the run) → refused, apply
// cannot reorder across the when_* boundary in between; recorded nowhere in
// this body → satisfied (an earlier privilege chunk or invocation applied
// it, and chunk boundaries are invisible to a chunk-level Apply). The
// controller-side ValidateChunkDeps pre-flight refuses forward cross-chunk
// and dangling deps before any chunk is applied.
func sortedApplyOrder(body []Op) ([]planLine, error) {
	// bodyIDs maps an op ID to its first recorded index in the body, so an
	// unmatched dep can be classified as later-in-body or absent entirely.
	bodyIDs := map[string]int{}
	for i, op := range body {
		if op.ID != "" {
			if _, seen := bodyIDs[op.ID]; !seen {
				bodyIDs[op.ID] = i
			}
		}
	}

	out := make([]planLine, 0, len(body))
	var run []planLine
	runEnd := 0 // body index just past the current run's last op
	// placed holds the op IDs of runs already emitted: their deps are
	// satisfied, because earlier segments and chunks always apply first.
	placed := map[string]bool{}
	flush := func() error {
		if len(run) == 0 {
			return nil
		}
		sorted, err := sortRunByDeps(run, placed, bodyIDs, runEnd)
		if err != nil {
			return err
		}
		out = append(out, sorted...)
		for _, l := range sorted {
			if l.op.ID != "" {
				placed[l.op.ID] = true
			}
		}
		run = run[:0]
		return nil
	}

	for i, op := range body {
		if IsControlKind(op.Op) {
			if err := flush(); err != nil {
				return nil, err
			}
			out = append(out, planLine{op: op, line: i + 2})
			continue
		}
		run = append(run, planLine{op: op, line: i + 2})
		runEnd = i + 1
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// sortRunByDeps topologically orders one contiguous resource-op run by the
// ops' dep lists (Kahn's algorithm, stable: among ready ops the earliest
// recorded one is emitted first). Deps are matched against op IDs. A dep on
// an op applied by an earlier run is satisfied. A dep whose first body
// occurrence is after this run (bodyIDs first index >= runEnd) is refused —
// apply cannot reorder it across the when_* boundary in between. A dep
// recorded nowhere in this body is satisfied: an earlier privilege chunk or
// invocation applied it, and chunk boundaries are invisible to a chunk-level
// Apply (ValidateChunkDeps refuses dangling deps controller-side before any
// chunk is applied).
func sortRunByDeps(run []planLine, placed map[string]bool, bodyIDs map[string]int, runEnd int) ([]planLine, error) {
	// inRun maps an op ID to every run position carrying it (IDs repeat when
	// a diamond include records the same resource twice).
	inRun := map[string][]int{}
	for pos, l := range run {
		if l.op.ID != "" {
			inRun[l.op.ID] = append(inRun[l.op.ID], pos)
		}
	}

	indeg := make([]int, len(run))
	waiters := make([][]int, len(run)) // dep position → dependent positions
	for pos, l := range run {
		for _, dep := range l.op.Deps {
			at, inCurrent := inRun[dep]
			if !inCurrent {
				if placed[dep] {
					continue // satisfied by an earlier run
				}
				if first, inBody := bodyIDs[dep]; inBody && first >= runEnd {
					return nil, fmt.Errorf(
						"plan: op %s depends on %s which is not ordered before it; later when-block dependencies cannot be reordered before it",
						l.op.ID, dep)
				}
				// Recorded nowhere in this body: satisfied by an earlier
				// privilege chunk or invocation (chunks never reorder).
				continue
			}
			for _, p := range at {
				indeg[pos]++
				waiters[p] = append(waiters[p], pos)
			}
		}
	}
	return kahnStable(run, indeg, waiters)
}

// kahnStable emits the run's ops in dependency order, breaking ties by
// recorded position: among the currently ready ops, the earliest recorded one
// always goes first. A leftover indegree at the end means the run has a
// dependency cycle, mirroring the repository path's cycle error.
func kahnStable(run []planLine, indeg []int, waiters [][]int) ([]planLine, error) {
	ready := make([]int, 0, len(run))
	for pos, n := range indeg {
		if n == 0 {
			ready = append(ready, pos) // ascending: pos is appended in order
		}
	}

	sorted := make([]planLine, 0, len(run))
	for len(ready) > 0 {
		pos := ready[0]
		ready = ready[1:]
		sorted = append(sorted, run[pos])
		for _, w := range waiters[pos] {
			indeg[w]--
			if indeg[w] == 0 {
				// Keep ready ascending by recorded position (stable Kahn).
				at := sort.SearchInts(ready, w)
				ready = append(ready, 0)
				copy(ready[at+1:], ready[at:])
				ready[at] = w
			}
		}
	}
	for pos, n := range indeg {
		if n > 0 {
			return nil, fmt.Errorf("plan: circular dependency involving %s", run[pos].op.ID)
		}
	}
	return sorted, nil
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
	case KindCron:
		return applyCron(op)
	case KindService:
		return applyService(op)
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

	// Empty owner/group means "not recorded": leaving them unset keeps the
	// build() defaults (apply-side user) identical to direct resource use.
	ownership := ownerGroupOptions(op)

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
		opts = append(opts, ownership...)
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
	opts = append(opts, ownership...)
	return file.Ensure(path, opts...)
}

// ownerGroupOptions converts an op's recorded owner/group into options. Both
// are only appended when non-empty: an omitted field must leave ownership to
// the apply-side defaults instead of forcing WithOwner("").
func ownerGroupOptions(op Op) []opt.Option {
	var opts []opt.Option
	if op.Owner != "" {
		opts = append(opts, opt.WithOwner(op.Owner))
	}
	if op.Group != "" {
		opts = append(opts, opt.WithGroup(op.Group))
	}
	return opts
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
	opts = append(opts, ownerGroupOptions(op)...)
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
	opts = append(opts, ownerGroupOptions(op)...)
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
	opts = append(opts, ownerGroupOptions(op)...)
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
	if op.Restart {
		opts = append(opts, opt.WithRestart)
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

func applyCron(op Op) error {
	if op.Name == "" {
		return fmt.Errorf("cron: missing name")
	}
	var opts []opt.Option
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	if op.CronUser != "" {
		opts = append(opts, opt.WithCronUser(op.CronUser))
	}
	if op.Command != "" {
		opts = append(opts, opt.WithCommand(op.Command))
	}
	// A present cron job needs a schedule: silently falling back to the
	// resource default (* * * * *, every minute) would run the command far
	// more often than the plan author intended.
	fields := strings.Fields(op.Schedule)
	if !op.Absent && len(fields) != 5 {
		return fmt.Errorf("cron: schedule %q must contain 5 whitespace-separated fields", op.Schedule)
	}
	if len(fields) == 5 {
		opts = append(opts,
			opt.WithMinute(fields[0]),
			opt.WithHour(fields[1]),
			opt.WithMonthday(fields[2]),
			opt.WithMonth(fields[3]),
			opt.WithWeekday(fields[4]),
		)
	}
	for _, kv := range op.CronEnv {
		opts = append(opts, opt.WithCronEnv(kv))
	}
	return cron.Ensure(op.Name, opts...)
}

func applyService(op Op) error {
	if op.Name == "" {
		return fmt.Errorf("service: missing name")
	}
	var opts []opt.Option
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	if op.Restart {
		opts = append(opts, opt.WithRestart)
	}
	if op.Reload {
		opts = append(opts, opt.WithReload)
	}
	if op.User {
		opts = append(opts, opt.WithUser)
	}
	return service.Ensure(op.Name, opts...)
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

// parseMode parses an octal plan-wire mode string such as "0640" or "04755"
// into a Go FileMode. The special bits 0o4000/0o2000/0o1000 (setuid, setgid,
// sticky) are converted to the os.ModeSetuid/ModeSetgid/ModeSticky flag bits,
// because Go only honors them through those flags: a raw os.FileMode(0o4755)
// would have its high bits truncated by os.Chmod and lower to 0755. Bits
// above 0o7777 have no meaning in the plan wire format and are rejected
// loudly instead of being silently dropped.
func parseMode(s string) (os.FileMode, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid mode %q: %w", s, err)
	}
	if v > 0o7777 {
		return 0, fmt.Errorf("invalid mode %q: only setuid/setgid/sticky (0o4000/0o2000/0o1000) plus the nine permission bits (up to 0o7777) are supported", s)
	}
	return opt.ModeToFlags(os.FileMode(v)), nil
}
