package api

import (
	"errors"
	"fmt"
	"reflect"
	"unicode"

	"github.com/snonux/gonf/internal/declerr"
)

// RegisterOption configures RegisterMethods.
type RegisterOption func(*registerConfig)

type registerConfig struct {
	prefix    string
	groupWhen TaskOptions
	cluster   string
}

// WithPrefix prepends prefix to each CamelCase→snake_case method name.
func WithPrefix(prefix string) RegisterOption {
	return func(c *registerConfig) { c.prefix = prefix }
}

// WithCluster associates an inventory cluster with every method registered in
// this call so recipes can use ClusterHosts() / MustHostValue without naming
// the cluster again. The cluster must already be registered.
func WithCluster(name string) RegisterOption {
	return func(c *registerConfig) { c.cluster = name }
}

// WithGroupWhen applies TaskOptions (typically When*) to every method
// registered in the call. Prefer WhenProfile / WhenLinux so plan recording
// can emit when_begin recipes.
func WithGroupWhen(opts ...TaskOption) RegisterOption {
	return func(c *registerConfig) {
		c.groupWhen = append(c.groupWhen, opts...)
	}
}

// RegisterMethods queues tasks from exported methods on v (struct or pointer).
//
// Naming: method Helix → "helix", with WithPrefix("home_") → "home_helix".
// Do not repeat the type name in methods (Unattended.Newsyslog, not
// Unattended.UnattendedNewsyslog) — the type and WithPrefix already namespace.
// Companions (optional):
//   - Opts() TaskOptions — struct-level DEFAULT TaskOptions for every
//     method registered from this struct (e.g. a single Privileged() for
//     an all-privileged struct); a method's own OptsX companion replaces
//     the default for that method, so an empty TaskOptions opts out.
//   - DescHelix() string — description (else empty)
//   - WhenHelix(Facts) bool — per-task When predicate; appended after any
//     WithGroupWhen options of the same call. A wrong signature is
//     registration-time misuse: the task is not registered (a silently
//     ignored companion could drop the guard and run the task on every
//     host).
//   - OptsHelix() TaskOptions — per-task TaskOptions, e.g. Privileged() or
//     the serializable WhenHostnameContains()/WhenProfile() predicates;
//     appended after any WithGroupWhen options of the same call. A wrong
//     signature is registration-time misuse: the task is not registered (a
//     silently ignored companion could drop Privileged() and lower a task's
//     privileges).
//
// Methods named Desc*, When*, or Opts* are not registered as tasks.
//
// Misuse — v not a struct or non-nil pointer to one, a companion with the
// wrong signature — is reported as a declaration error (internal/declerr,
// which RecordPlan, Run, Apply and the CLI refuse to run with). A bad receiver
// or struct-level companion registers nothing of v; a bad per-method
// companion skips only that method's task.
func RegisterMethods(v any, opts ...RegisterOption) {
	cfg := registerConfigFor(opts)
	rv, rt, err := registerReceiver(v)
	if err != nil {
		declerr.Report(err)
		return
	}
	registerMethodTasks(rv, rt, cfg)
}

func registerConfigFor(opts []RegisterOption) registerConfig {
	cfg := registerConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// registerReceiver returns the addressable receiver RegisterMethods reads
// methods from, or the misuse error for a nil pointer or a non-struct v.
func registerReceiver(v any) (reflect.Value, reflect.Type, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return reflect.Value{}, nil, errors.New("RegisterMethods: nil pointer")
		}
	} else if rv.Kind() != reflect.Struct {
		return reflect.Value{}, nil, fmt.Errorf("RegisterMethods: want struct or *struct, got %T", v)
	}

	// reflect.ValueOf always yields a non-addressable value (CanAddr is
	// false unless the value came from a pointer deref), so a struct
	// argument is wrapped in a fresh pointer to reach pointer-receiver
	// methods; pointer arguments stay as-is.
	if rv.Kind() == reflect.Struct {
		ptr := reflect.New(rv.Type())
		ptr.Elem().Set(rv)
		rv = ptr
	}
	return rv, rv.Type(), nil
}

// registerMethodTasks queues one Task per exported func() method of rv. A
// companion misuse is reported as a declaration error: a struct-level one
// registers nothing, a per-method one skips that method.
func registerMethodTasks(rv reflect.Value, rt reflect.Type, cfg registerConfig) {
	// Struct-level default TaskOptions: embedded StructOption markers
	// (e.g. RequiresRoot) and/or the Opts() companion. A method's own
	// OptsX companion replaces the combined default for that method (an
	// empty TaskOptions opts out — e.g. an unprivileged smoke-test task on
	// an otherwise-privileged struct).
	structOpts, err := collectStructOptions(rv, rt)
	if err != nil {
		declerr.Report(err)
		return
	}

	typeNames := map[string]struct{}{}
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		name := m.Name
		if isCompanionName(name) {
			continue
		}
		typeNames[name] = struct{}{}
	}

	for name := range typeNames {
		method := rv.MethodByName(name)
		if !method.IsValid() {
			continue
		}
		mt := method.Type()
		if mt.NumIn() != 0 || mt.NumOut() != 0 {
			continue // only func()
		}

		taskOpts, err := methodTaskOptions(rv, name, cfg, structOpts)
		if err != nil {
			declerr.Report(err)
			continue
		}
		Task(cfg.prefix+camelToSnake(name), resolveDesc(rv, name), method.Interface().(func()), taskOpts...)
	}
}

// methodTaskOptions assembles the TaskOptions of method name: the call's
// WithGroupWhen and WithCluster options, then its OptsX companion (or the
// struct-level default) and its WhenX guard. A companion with the wrong
// signature is returned as an error.
func methodTaskOptions(rv reflect.Value, name string, cfg registerConfig, structOpts TaskOptions) (TaskOptions, error) {
	var taskOpts TaskOptions
	taskOpts = append(taskOpts, cfg.groupWhen...)
	if cfg.cluster != "" {
		taskOpts = append(taskOpts, WithTaskCluster(cfg.cluster))
	}
	methodOpts, err := resolveOpts(rv, name, structOpts)
	if err != nil {
		return nil, err
	}
	taskOpts = append(taskOpts, methodOpts...)
	whenOpt, err := resolveWhen(rv, name)
	if err != nil {
		return nil, err
	}
	if whenOpt != nil {
		taskOpts = append(taskOpts, whenOpt)
	}
	return taskOpts, nil
}

// resolveDesc returns the DescX companion's description, or "" if the
// companion is absent or has the wrong signature. Unlike OptsX/WhenX, a
// missing description has no safety consequence, so a mismatched signature
// degrades gracefully instead of refusing the task.
func resolveDesc(rv reflect.Value, name string) string {
	d := rv.MethodByName("Desc" + name)
	if !d.IsValid() {
		return ""
	}
	dt := d.Type()
	if dt.NumIn() == 0 && dt.NumOut() == 1 && dt.Out(0).Kind() == reflect.String {
		return d.Call(nil)[0].String()
	}
	return ""
}

// resolveOpts returns the OptsX companion's TaskOptions if present, which
// REPLACES the struct-level default (an empty TaskOptions is an explicit
// opt-out), or structOpts otherwise. A wrong OptsX signature is returned as
// an error: a silently ignored companion could drop Privileged() and lower a
// task's privileges.
func resolveOpts(rv reflect.Value, name string, structOpts TaskOptions) (TaskOptions, error) {
	o := rv.MethodByName("Opts" + name)
	if !o.IsValid() {
		return structOpts, nil
	}
	return callTaskOptionsCompanion(o, "Opts"+name+" must be func() TaskOptions")
}

// resolveWhen returns the TaskOption wrapping the WhenX companion's guard
// predicate, or nil if the companion is absent. A wrong WhenX signature is
// returned as an error: a silently ignored companion would drop the guard
// predicate and run the task unconditionally on every host instead of only
// the intended ones.
func resolveWhen(rv reflect.Value, name string) (TaskOption, error) {
	w := rv.MethodByName("When" + name)
	if !w.IsValid() {
		return nil, nil
	}
	wt := w.Type()
	if wt.NumIn() != 1 || wt.In(0) != reflect.TypeOf(Facts{}) ||
		wt.NumOut() != 1 || wt.Out(0).Kind() != reflect.Bool {
		return nil, fmt.Errorf("RegisterMethods: When%s must be func(Facts) bool", name)
	}
	wMethod := w
	return When(func(f Facts) bool {
		return wMethod.Call([]reflect.Value{reflect.ValueOf(f)})[0].Bool()
	}), nil
}

func isCompanionName(name string) bool {
	// "Opts" (exactly) is the struct-level default companion; the prefixed
	// forms are per-method companions.
	return name == "Opts" ||
		(len(name) > 4 && name[:4] == "Desc") ||
		(len(name) > 4 && name[:4] == "When") ||
		(len(name) > 4 && name[:4] == "Opts")
}

// camelToSnake converts Helix → helix, TmuxRocky → tmux_rocky.
func camelToSnake(s string) string {
	if s == "" {
		return s
	}
	var b []rune
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || (unicode.IsUpper(prev) && nextLower) {
					b = append(b, '_')
				}
			}
			b = append(b, unicode.ToLower(r))
			continue
		}
		b = append(b, r)
	}
	return string(b)
}
