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
}

// WithPrefix prepends prefix to each CamelCase→snake_case method name.
func WithPrefix(prefix string) RegisterOption {
	return func(c *registerConfig) { c.prefix = prefix }
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
// Companions (optional):
//   - DescHelix() string — description (else empty)
//   - WhenHelix(Facts) bool — per-task When predicate
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
		desc := ""
		if d := rv.MethodByName("Desc" + name); d.IsValid() {
			dt := d.Type()
			if dt.NumIn() == 0 && dt.NumOut() == 1 && dt.Out(0).Kind() == reflect.String {
				desc = d.Call(nil)[0].String()
			}
		}

		fn := method.Interface().(func())

		var taskOpts TaskOptions
		taskOpts = append(taskOpts, cfg.groupWhen...)
		if o := rv.MethodByName("Opts" + name); o.IsValid() {
			ot := o.Type()
			if ot.NumIn() != 0 || ot.NumOut() != 1 || ot.Out(0) != reflect.TypeOf(TaskOptions(nil)) {
				panic(fmt.Sprintf("RegisterMethods: Opts%s must be func() TaskOptions", name))
			}
			taskOpts = append(taskOpts, o.Call(nil)[0].Interface().([]TaskOption)...)
		}
		if w := rv.MethodByName("When" + name); w.IsValid() {
			wt := w.Type()
			if wt.NumIn() == 1 && wt.In(0) == reflect.TypeOf(Facts{}) &&
				wt.NumOut() == 1 && wt.Out(0).Kind() == reflect.Bool {
				wMethod := w
				taskOpts = append(taskOpts, When(func(f Facts) bool {
					return wMethod.Call([]reflect.Value{reflect.ValueOf(f)})[0].Bool()
				}))
			}
		}

		Task(taskName, desc, fn, taskOpts...)
	}
}

func isCompanionName(name string) bool {
	return (len(name) > 4 && name[:4] == "Desc") ||
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
