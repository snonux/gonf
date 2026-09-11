package api

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"sync"

	"github.com/snonux/gonf/resource"
)

// TaskInfo is a registered task's name and description for listing.
type TaskInfo struct {
	Name        string
	Description string
}

type task struct {
	name        string
	description string
	fn          func()
}

var (
	tasksMu sync.Mutex
	tasks   = map[string]task{}
)

// Task registers a named unit of work. Call from init or an explicit
// Register(). Duplicate names are a fatal error.
func Task(name, description string, fn func()) {
	if name == "" {
		log.Fatal("Task: name must not be empty")
	}
	if fn == nil {
		log.Fatalf("Task %q: fn must not be nil", name)
	}

	tasksMu.Lock()
	defer tasksMu.Unlock()

	if _, exists := tasks[name]; exists {
		log.Fatalf("Task %q already registered", name)
	}
	tasks[name] = task{name: name, description: description, fn: fn}
}

// Matching returns registered task names matching pattern (sorted).
// An invalid regexp is a fatal error.
func Matching(pattern string) []string {
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

// Tasks returns all registered tasks sorted by name.
func Tasks() []TaskInfo {
	tasksMu.Lock()
	defer tasksMu.Unlock()

	out := make([]TaskInfo, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, TaskInfo{Name: t.name, Description: t.description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run runs each named task sequentially: reset the resource repository, call
// the task function (which registers resources), then Apply. Nested Run calls
// (e.g. an aggregate task) are supported.
func Run(names ...string) error {
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

// ResetTasks clears the task registry. Intended for tests.
func ResetTasks() {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	tasks = map[string]task{}
}
