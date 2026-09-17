package api

import (
	"fmt"
	"reflect"
	"unicode"
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
//     registration-time misuse and panics (a silently ignored companion
//     could drop the guard and run the task on every host).
//   - OptsHelix() TaskOptions — per-task TaskOptions, e.g. Privileged() or
//     the serializable WhenHostnameContains()/WhenProfile() predicates;
//     appended after any WithGroupWhen options of the same call. A wrong
//     signature is registration-time misuse and panics (a silently ignored
//     companion could drop Privileged() and lower a task's privileges).
//
// Methods named Desc*, When*, or Opts* are not registered as tasks.
func RegisterMethods(v any, opts ...RegisterOption) {
	cfg := registerConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			panic("RegisterMethods: nil pointer")
		}
	} else if rv.Kind() != reflect.Struct {
		panic(fmt.Sprintf("RegisterMethods: want struct or *struct, got %T", v))
	}

	// reflect.ValueOf always yields a non-addressable value (CanAddr is
	// false unless the value came from a pointer deref), so a struct
	// argument is wrapped in a fresh pointer to reach pointer-receiver
	// methods; pointer arguments stay as-is.
	rt := rv.Type()
	if rv.Kind() == reflect.Struct {
		ptr := reflect.New(rv.Type())
		ptr.Elem().Set(rv)
		rv = ptr
		rt = rv.Type()
	}

	// Struct-level default TaskOptions: embedded StructOption markers
	// (e.g. RequiresRoot) and/or the Opts() companion. A method's own
	// OptsX companion replaces the combined default for that method (an
	// empty TaskOptions opts out — e.g. an unprivileged smoke-test task on
	// an otherwise-privileged struct).
	structOpts := collectStructOptions(rv, rt)

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

		taskName := cfg.prefix + camelToSnake(name)
		fn := method.Interface().(func())

		var taskOpts TaskOptions
		taskOpts = append(taskOpts, cfg.groupWhen...)
		if cfg.cluster != "" {
			taskOpts = append(taskOpts, WithTaskCluster(cfg.cluster))
		}
		taskOpts = append(taskOpts, resolveOpts(rv, name, structOpts)...)
		if whenOpt := resolveWhen(rv, name); whenOpt != nil {
			taskOpts = append(taskOpts, whenOpt)
		}

		Task(taskName, resolveDesc(rv, name), fn, taskOpts...)
	}
}

// resolveDesc returns the DescX companion's description, or "" if the
// companion is absent or has the wrong signature. Unlike OptsX/WhenX, a
// missing description has no safety consequence, so a mismatched signature
// degrades gracefully instead of panicking.
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
// opt-out), or structOpts otherwise. A wrong OptsX signature panics: a
// silently ignored companion could drop Privileged() and lower a task's
// privileges.
func resolveOpts(rv reflect.Value, name string, structOpts TaskOptions) TaskOptions {
	o := rv.MethodByName("Opts" + name)
	if !o.IsValid() {
		return structOpts
	}
	ot := o.Type()
	if ot.NumIn() != 0 || ot.NumOut() != 1 || ot.Out(0) != reflect.TypeOf(TaskOptions(nil)) {
		panic(fmt.Sprintf("RegisterMethods: Opts%s must be func() TaskOptions", name))
	}
	return o.Call(nil)[0].Interface().(TaskOptions)
}

// resolveWhen returns the TaskOption wrapping the WhenX companion's guard
// predicate, or nil if the companion is absent. A wrong WhenX signature
// panics: a silently ignored companion would drop the guard predicate and
// run the task unconditionally on every host instead of only the intended
// ones.
func resolveWhen(rv reflect.Value, name string) TaskOption {
	w := rv.MethodByName("When" + name)
	if !w.IsValid() {
		return nil
	}
	wt := w.Type()
	if wt.NumIn() != 1 || wt.In(0) != reflect.TypeOf(Facts{}) ||
		wt.NumOut() != 1 || wt.Out(0).Kind() != reflect.Bool {
		panic(fmt.Sprintf("RegisterMethods: When%s must be func(Facts) bool", name))
	}
	wMethod := w
	return When(func(f Facts) bool {
		return wMethod.Call([]reflect.Value{reflect.ValueOf(f)})[0].Bool()
	})
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
