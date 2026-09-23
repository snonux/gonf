package api

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// TaskInfo is an activated task's name and description for listing.
type TaskInfo struct {
	Name        string
	Description string
	// AliasOf is the target task name when Name was registered with Alias,
	// and empty for ordinary tasks and aggregates.
	AliasOf string
}

// task is one activated entry of the public task map. Aliases are activated
// too (they are public names) but carry no body: aliasOf names the task they
// record instead.
type task struct {
	name        string
	description string
	fn          func()
	aliasOf     string
}

type taskCandidate struct {
	name        string
	description string
	fn          func()
	when        []func(Facts) bool
	// planWhen is the AND list of serializable predicates for RecordPlan.
	planWhen []plan.Predicate
	// opaqueWhen is true when a custom When(func) was used and cannot be
	// lowered into plan recipes.
	opaqueWhen bool
	// privileged tags recorded ops with elevate=true for split apply.
	privileged bool
	// cluster is the inventory cluster name for ClusterHosts() (WithCluster).
	cluster string
	// operational marks an explicit operational action (Operational) that
	// pattern aggregates must never pick up automatically.
	operational bool
	// aliasOf is set only for Alias registrations: the candidate has no fn
	// and records the named target task instead (see resolveAlias).
	aliasOf string
	// aggregate is set for Aggregate and AggregateTasks registrations; their
	// bodies share the aggregate dedupe scope (enterAggregateScope).
	aggregate bool
	// members is AggregateTasks' explicit member list (nil for a pattern
	// Aggregate), used by the registration checks and containsOperational.
	members []string
}

// TaskOption configures a deferred task candidate.
type TaskOption func(*taskCandidate)

// TaskOptions is a list of task options; an alias for []TaskOption so
// signatures read concisely (e.g. OptsHelix() TaskOptions in the
// RegisterMethods companion convention). Being an alias, it is the
// identical type — []TaskOption remains fully interchangeable everywhere.
type TaskOptions = []TaskOption

// candidates/tasks/activated are deliberately package-level global state,
// unlike the exec/network runner seams in internal/remote (SSHRunner,
// SCPRunner, GoBuildRunner, Pusher's fields): this is the DSL task registry
// a user's gonf config populates by calling api.Task(...) (and friends) at
// plain top-level init-time, before any controller/CLI wiring exists to
// inject a struct into. There is exactly one task registry per process, by
// design — a gonf config IS the process's task set, the same way inventory's
// hosts/clusters/fleets registry (internal/inventory) is process-wide — and
// every read/write already goes through the tasksMu mutex below, so
// concurrent registration/activation and lookup are safe. This is not an
// oversight left over from the runner-seam refactor; it is the intentional
// DSL/config half of the codebase, left alone on purpose (see task p5's
// annotations).
var (
	tasksMu    sync.Mutex
	candidates []taskCandidate
	tasks      = map[string]task{}
	activated  bool
)

// Privileged marks the task so recorded plan ops get elevate=true.
// Controllers split apply into a sudo/doas gonf invocation for those ops.
func Privileged() TaskOption {
	return func(c *taskCandidate) { c.privileged = true }
}

// WithTaskCluster associates an inventory fleet with this task so ClusterHosts()
// returns that fleet's hosts while the body runs. Prefer RegisterMethods'
// WithCluster so every method on a struct shares one fleet.
func WithTaskCluster(name string) TaskOption {
	return func(c *taskCandidate) { c.cluster = name }
}

// Operational marks the task as an explicit operational action — for example
// requesting certificates, a one-shot invocation, or a diagnostic — rather
// than part of converging a host's configuration. A pattern Aggregate never
// includes an operational task, an Alias of one, or an AggregateTasks that
// (transitively) lists one, so a broad pattern such as "^frontends_" cannot
// pick such an action up by name. The check covers registrations only: task
// bodies are Go code and are not inspected, so an ordinary task whose body
// calls Run("op") still records op wherever that task is recorded, pattern
// aggregates included — do not wrap an operational action in a plain task
// that a setup pattern matches. The task stays callable by its own name, and
// AggregateTasks may still list it: explicit membership is a deliberate
// decision, not an accident of naming.
func Operational() TaskOption {
	return func(c *taskCandidate) { c.operational = true }
}

// When skips activating the task unless pred(facts) is true.
// Custom predicates are not serializable for remote plans; prefer WhenLinux,
// WhenProfile, or WhenHostnameContains when recording plans. A When(func)
// combined with a serializable guard (WhenLinux etc.) still ships that
// guard — the opaque predicate is only an extra controller-side filter, it
// never suppresses the serializable one. A task whose When is opaque ONLY
// (no serializable guard at all) records fine for local Run/gonf plan, but
// PushTo/PushClusterRun/PushFleetRun refuse it: shipping such a plan would
// silently drop the guard and apply the task unconditionally on the
// destination.
func When(pred func(Facts) bool) TaskOption {
	return func(c *taskCandidate) {
		if pred != nil {
			c.when = append(c.when, pred)
			c.opaqueWhen = true
		}
	}
}

// WhenLinux is When(func(f Facts) bool { return f.GOOS == "linux" }).
func WhenLinux() TaskOption {
	return func(c *taskCandidate) {
		c.when = append(c.when, func(f Facts) bool { return f.GOOS == "linux" })
		c.planWhen = append(c.planWhen, plan.Predicate{Fact: "goos", Eq: "linux"})
	}
}

// WhenProfile activates only when Facts.Profile is one of profiles.
// A single profile lowers to a plan fact predicate with Eq; multiple
// profiles lower to the same predicate with In (OR-of-values) — both forms
// are fully serializable, so WhenProfile never marks a task opaque.
func WhenProfile(profiles ...string) TaskOption {
	return func(c *taskCandidate) {
		c.when = append(c.when, ProfileIs(profiles...))
		switch len(profiles) {
		case 0:
			return
		case 1:
			c.planWhen = append(c.planWhen, plan.Predicate{Fact: "profile", Eq: profiles[0]})
		default:
			c.planWhen = append(c.planWhen, plan.Predicate{
				Fact: "profile",
				In:   append([]string(nil), profiles...),
			})
		}
	}
}

// WhenHostnameContains activates when Facts.Hostname contains substr.
func WhenHostnameContains(substr string) TaskOption {
	return func(c *taskCandidate) {
		c.when = append(c.when, func(f Facts) bool {
			return strings.Contains(strings.ToLower(f.Hostname), strings.ToLower(substr))
		})
		c.planWhen = append(c.planWhen, plan.Predicate{Fact: "hostname_contains", Eq: substr})
	}
}

// Task queues a named unit of work for activation. Call from init() or
// RegisterMethods. An empty name, a nil fn or a duplicate name — tasks,
// aggregates and aliases share one namespace — is registration-time misuse,
// always a recipe bug: it is reported as a declaration error
// (internal/declerr), the task is not queued, and RecordPlan, Run, Apply and
// the CLI refuse to run with the error. Activation (When filtering) happens in
// Activate / CLI / Run.
func Task(name, description string, fn func(), opts ...TaskOption) {
	if name == "" {
		declerr.Reportf("Task: name must not be empty")
		return
	}
	if fn == nil {
		declerr.Reportf("Task %q: fn must not be nil", name)
		return
	}

	c := taskCandidate{name: name, description: description, fn: fn}
	for _, o := range opts {
		o(&c)
	}
	if c.cluster != "" {
		clusterName := c.cluster
		inner := c.fn
		c.fn = func() {
			pushTaskCluster(clusterName)
			defer popTaskCluster()
			inner()
		}
	}
	queueCandidate(c)
}

// queueCandidate appends c to the candidate list after the duplicate-name
// check shared by Task and Alias, and marks the registry for re-activation. A
// duplicate is reported as a declaration error and not queued, so the first
// registration keeps the name.
func queueCandidate(c taskCandidate) {
	tasksMu.Lock()
	defer tasksMu.Unlock()

	for _, existing := range candidates {
		if existing.name == c.name {
			declerr.Reportf("Task %q already queued", c.name)
			return
		}
	}
	if activated {
		if _, exists := tasks[c.name]; exists {
			declerr.Reportf("Task %q already registered", c.name)
			return
		}
	}
	candidates = append(candidates, c)
	activated = false // new candidates require re-activation
}

// Activate commits queued candidates whose When predicates pass for facts.
// It is safe to call multiple times; each call rebuilds the active task map
// from the full candidate list.
func Activate(facts Facts) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	activateLocked(facts)
}

// Matching returns activated task names matching pattern (sorted). An invalid
// pattern is recipe misuse: it is reported as a declaration error
// (internal/declerr) and Matching returns nil.
func Matching(pattern string) []string {
	ensureActivated()

	re, err := regexp.Compile(pattern)
	if err != nil {
		declerr.Reportf("Matching: invalid pattern %q: %w", pattern, err)
		return nil
	}

	tasksMu.Lock()
	defer tasksMu.Unlock()

	names := make([]string, 0)
	for name := range tasks {
		if re.MatchString(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Tasks returns all activated tasks sorted by name.
func Tasks() []TaskInfo {
	ensureActivated()

	tasksMu.Lock()
	defer tasksMu.Unlock()

	out := make([]TaskInfo, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, TaskInfo{Name: t.name, Description: t.description, AliasOf: t.aliasOf})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run records the named tasks into a plan and applies it locally in one shot.
// This is the same engine as remote plan→JSONL→apply; local just skips shipping.
//
// Nested Run while a RecordPlan session is active (a task body that runs
// other tasks) only appends child task ops into the current plan — it does
// not apply mid-flight. A nested failure (unknown task, a child's record
// error, a cycle) is returned AND fails the enclosing record, so ignoring the
// returned error (`_ = Run("x")`) cannot silently drop the child's ops. To
// expose a task under a second public name, prefer Alias over a body that
// only calls Run: aggregates then record the target once.
//
// Run itself is not context-aware (equivalent to RunContext(context.Background(),
// ...)): task bodies call Run directly (e.g. a task that fans out to other
// tasks), so its signature is kept exactly as-is for API stability. The CLI
// entry point uses RunContext instead so SIGINT/SIGTERM can cancel an
// in-flight elevated re-exec; see ApplyChunks/ApplyChunksContext's doc
// comment for the same tradeoff one layer down.
func Run(names ...string) error {
	return RunContext(context.Background(), names...)
}

// RunContext is Run bounded/cancelable by ctx.
func RunContext(ctx context.Context, names ...string) error {
	if len(names) == 0 {
		return fmt.Errorf("Run: no tasks specified")
	}

	if plan.Recording() || resource.PlanDraftRecording() {
		// Nested session: bodies append into the current plan. Packaging
		// failures are shared through recordingPackErr, so the nested Run
		// surfaces the real error (not a misleading secondary one), and
		// recordNestedRun stashes any other failure for the enclosing body.
		return recordNestedRun(names)
	}

	planDir, removePlanDir, err := tempPlanDir("gonf-plan-*")
	if err != nil {
		return fmt.Errorf("Run: temp plan dir: %w", err)
	}
	defer removePlanDir()

	// Record straight into the private temp dir (no RecordPlan staging): the
	// directory is removed on return whether the record succeeds or not (DSL
	// misuse in a task body is a returned record error, not a process exit),
	// so nothing survives a refusal and staging would only copy large blobs
	// twice.
	// The plan applies on this machine, so ForHosts only resolves the hosts
	// whose destination guard this hostname satisfies (localHostSelection).
	ops, err := recordPlanForHosts(localHostSelection(), "local", plan.NewStore(planDir), names...)
	if err != nil {
		return err
	}
	// The record already ran the dependency/change-gate pre-flight; the one in
	// ApplyChunksContext repeats it on purpose (see validateRecordedPlan) and
	// cannot fail here because both run the same plan.ValidateChunks.
	return ApplyChunksContext(ctx, ops, planDir, processPrivilege)
}

// ResetTasks clears candidates and activated tasks. It is part of the
// canonical api.ResetForTest seam, which also resets the profile override
// (SetProfileOverride), the plan recording session, and the resource
// package state; ResetTasks itself only owns the task registry.
func ResetTasks() {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	candidates = nil
	tasks = map[string]task{}
	activated = false
	resetTaskCluster()
}

// activateLocked rebuilds the active task map. Real tasks are activated by
// their own When predicates. An alias has none of its own: it is active
// exactly when its target is an active real task, so -list and Matching never
// offer an alias that could not record. A broken alias (unknown target, or an
// alias of an alias) is therefore absent from the list, and naming it
// explicitly fails the record with the reason (resolveAlias).
func activateLocked(facts Facts) {
	tasks = map[string]task{}
	var aliases []taskCandidate
	for _, c := range candidates {
		if c.aliasOf != "" {
			aliases = append(aliases, c)
			continue
		}
		if !whenPasses(c.when, facts) {
			continue
		}
		tasks[c.name] = task{name: c.name, description: c.description, fn: c.fn}
	}
	// Aliases go in only after every real task so registration order does
	// not matter. They are inserted as this loop runs, so the aliasOf check
	// is what keeps an alias of an (earlier) alias from being activated.
	for _, c := range aliases {
		target, ok := tasks[c.aliasOf]
		if !ok || target.aliasOf != "" {
			continue
		}
		tasks[c.name] = task{name: c.name, description: c.description, aliasOf: c.aliasOf}
	}
	activated = true
}

func whenPasses(preds []func(Facts) bool, facts Facts) bool {
	for _, p := range preds {
		if !p(facts) {
			return false
		}
	}
	return true
}

func ensureActivated() {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	if !activated {
		activateLocked(DetectFacts())
	}
}

func findCandidate(name string) (taskCandidate, bool) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	for _, c := range candidates {
		if c.name == name {
			return c, true
		}
	}
	return taskCandidate{}, false
}
