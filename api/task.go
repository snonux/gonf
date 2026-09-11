package api

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/snonux/gonf/resource"
)

// TaskInfo is an activated task's name and description for listing.
type TaskInfo struct {
	Name        string
	Description string
}

type task struct {
	name        string
	description string
	fn          func()
}

type taskCandidate struct {
	name        string
	description string
	fn          func()
	when        []func(Facts) bool
}

// TaskOption configures a deferred task candidate.
type TaskOption func(*taskCandidate)

var (
	tasksMu    sync.Mutex
	candidates []taskCandidate
	tasks      = map[string]task{}
	activated  bool
)

// When skips activating the task unless pred(facts) is true.
func When(pred func(Facts) bool) TaskOption {
	return func(c *taskCandidate) {
		if pred != nil {
			c.when = append(c.when, pred)
		}
	}
}

// WhenLinux is When(func(f Facts) bool { return f.GOOS == "linux" }).
func WhenLinux() TaskOption {
	return When(func(f Facts) bool { return f.GOOS == "linux" })
}

// WhenProfile activates only when Facts.Profile is one of profiles.
func WhenProfile(profiles ...string) TaskOption {
	return When(ProfileIs(profiles...))
}

// WhenHostnameContains activates when Facts.Hostname contains substr.
func WhenHostnameContains(substr string) TaskOption {
	return When(func(f Facts) bool {
		return strings.Contains(strings.ToLower(f.Hostname), strings.ToLower(substr))
	})
}

// Task queues a named unit of work for activation. Call from init() or
// RegisterMethods. Duplicate candidate names are a fatal error.
// Activation (When filtering) happens in Activate / CLI / Run.
func Task(name, description string, fn func(), opts ...TaskOption) {
	if name == "" {
		log.Fatal("Task: name must not be empty")
	}
	if fn == nil {
		log.Fatalf("Task %q: fn must not be nil", name)
	}

	c := taskCandidate{name: name, description: description, fn: fn}
	for _, o := range opts {
		o(&c)
	}

	tasksMu.Lock()
	defer tasksMu.Unlock()

	for _, existing := range candidates {
		if existing.name == name {
			log.Fatalf("Task %q already queued", name)
		}
	}
	if activated {
		if _, exists := tasks[name]; exists {
			log.Fatalf("Task %q already registered", name)
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

func activateLocked(facts Facts) {
	tasks = map[string]task{}
	for _, c := range candidates {
		if !whenPasses(c.when, facts) {
			continue
		}
		tasks[c.name] = task{name: c.name, description: c.description, fn: c.fn}
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

// Matching returns activated task names matching pattern (sorted).
func Matching(pattern string) []string {
	ensureActivated()

	re, err := regexp.Compile(pattern)
	if err != nil {
		log.Fatalf("Matching: invalid pattern %q: %v", pattern, err)
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
		out = append(out, TaskInfo{Name: t.name, Description: t.description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run runs each named activated task sequentially.
func Run(names ...string) error {
	ensureActivated()

	if len(names) == 0 {
		return fmt.Errorf("Run: no tasks specified")
	}

	for _, name := range names {
		if err := runOne(name); err != nil {
			return err
		}
	}
	return nil
}

func runOne(name string) error {
	tasksMu.Lock()
	t, ok := tasks[name]
	tasksMu.Unlock()
	if !ok {
		return fmt.Errorf("unknown task %q", name)
	}

	resource.ResetRepository()
	t.fn()
	if err := resource.Apply(); err != nil {
		return fmt.Errorf("task %s: %w", name, err)
	}
	return nil
}

// ResetTasks clears candidates and activated tasks. Intended for tests.
func ResetTasks() {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	candidates = nil
	tasks = map[string]task{}
	activated = false
	profileOverride = ""
}
