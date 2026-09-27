package api

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/inventory"
)

// RegisterOption configures RegisterMethods: WithPrefix, OnCluster,
// WithCluster, WithGroupWhen, or any TaskOption, which applies to every
// method of the call exactly like WithGroupWhen(opt):
//
//	RegisterMethods(pkg.Pkg{}, WhenProfile("fedora"))
type RegisterOption interface {
	applyRegister(c *registerConfig)
}

// registerOptionFunc is the RegisterOption of a plain config function.
type registerOptionFunc func(*registerConfig)

func (f registerOptionFunc) applyRegister(c *registerConfig) { f(c) }

// applyRegister makes a TaskOption a RegisterOption: WithGroupWhen(o).
// applyRegisterOptions refuses a nil o before it gets here.
func (o TaskOption) applyRegister(c *registerConfig) {
	c.groupWhen = append(c.groupWhen, o)
}

type registerConfig struct {
	prefix string
	// prefixSet is true once WithPrefix ran, so WithPrefix("") (bare method
	// names) is told apart from no WithPrefix (the derived default prefix).
	prefixSet bool
	groupWhen TaskOptions
	cluster   string
	// guardCluster is OnCluster's cluster: besides setting cluster, every
	// task of the call gets that cluster's hostname destination guard
	// (clusterGuard). Empty for WithCluster, which only sets cluster.
	guardCluster string
}

// WithPrefix prepends prefix to each CamelCase→snake_case method name,
// replacing the default derived from the struct (see DefaultPrefix). Use it
// when several structs share one namespace (WithPrefix("frontends_") on
// frontends.Web and openbsd.Unattended); WithPrefix("") registers the bare
// method names.
func WithPrefix(prefix string) RegisterOption {
	return registerOptionFunc(func(c *registerConfig) {
		c.prefix = prefix
		c.prefixSet = true
	})
}

// DefaultPrefix returns the task-name prefix RegisterMethods uses for v when
// the call has no WithPrefix: the struct's package name and type name in
// snake_case, each followed by "_". A trailing "Tasks" is dropped from the
// type name; a type named like its package (garage.Garage), or just Tasks,
// contributes nothing, and neither does package main:
//
//	freebsd.Unattended → "freebsd_unattended_"
//	garage.Garage      → "garage_"
//	home.HomeTasks     → "home_"
//	tasks.HomeTasks    → "tasks_home_"
//	main.Backup        → "backup_"
//	rnodes.WireGuard   → "rnodes_wireguard_"
//
// The package name keeps same-named structs of different packages
// (openbsd.Unattended, freebsd.Unattended) apart.
func DefaultPrefix(v any) string {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return ""
	}
	pkg, typ := "", genericBaseName(t.Name())
	if s := t.String(); strings.Contains(s, ".") {
		pkg = s[:strings.IndexByte(s, '.')]
	}
	return prefixFor(pkg, typ)
}

// prefixWords are product names spelled in CamelCase that stay one word in
// a derived prefix: WireGuard → "wireguard_", not "wire_guard_".
var prefixWords = strings.NewReplacer("WireGuard", "Wireguard")

// prefixFor is DefaultPrefix over a package name and a type name.
func prefixFor(pkg, typ string) string {
	if pkg == "main" {
		pkg = ""
	}
	typ = camelToSnake(prefixWords.Replace(strings.TrimSuffix(typ, "Tasks")))
	if typ == pkg {
		typ = ""
	}
	var b strings.Builder
	for _, part := range []string{pkg, typ} {
		if part != "" {
			b.WriteString(part)
			b.WriteByte('_')
		}
	}
	return b.String()
}

// WithCluster associates an inventory cluster with every method registered in
// this call so recipes can use ClusterHosts() / MustHostValue without naming
// the cluster again. The cluster must already be registered.
func WithCluster(name string) RegisterOption {
	return registerOptionFunc(func(c *registerConfig) { c.cluster = name })
}

// OnCluster is WithCluster plus a destination guard: every method registered
// in this call applies only on the cluster's hosts. The guard is the
// serializable hostname_contains predicate over the cluster's member names
// (case-insensitive substring, OR over the members: the same test
// WhenHostname(ClusterHosts(), ...) applies to each of its fragments),
// recorded as the task's own when_begin and evaluated on each destination:
//
//	RegisterMethods(freebsd.Unattended{}, WithPrefix("freebsd_"), OnCluster("freebsd"))
//
// so the bodies drop their WhenHostname(ClusterHosts(), func() { ... })
// wrapper. ForHosts/EachHost inside such a body keep working: their
// per-host fragments nest inside the task guard.
//
// Plan shape (a deliberate choice): one task-level when_begin whose
// predicate lists every member (In; Eq for a one-host cluster), not the N
// per-host fragments of the body wrapper. The body runs once instead of
// once per member, a ForHosts body is not multiplied N times, -list marks
// the task "[destination-guarded: hostname_contains=h1|h2]" off-cluster,
// and a local Run's pattern aggregate skips it on a non-member host like
// any other destination-guarded member. The ops therefore differ from the
// wrapper's, but every destination converges to the same state.
//
// The cluster must be registered before RegisterMethods runs (its members
// are read then). An unknown cluster, or OnCluster combined with a
// WithCluster naming another cluster, is a declaration error and registers
// nothing of v.
func OnCluster(name string) RegisterOption {
	return registerOptionFunc(func(c *registerConfig) {
		c.cluster = name
		c.guardCluster = name
	})
}

// WithGroupWhen applies TaskOptions (typically When*) to every method
// registered in the call. Prefer WhenProfile / WhenLinux so plan recording
// can emit when_begin recipes.
func WithGroupWhen(opts ...TaskOption) RegisterOption {
	return registerOptionFunc(func(c *registerConfig) {
		c.groupWhen = append(c.groupWhen, opts...)
	})
}

// RegisterMethods queues tasks from exported methods on v (struct or pointer).
//
// Naming: method Helix of home.HomeTasks → "home_helix". Without WithPrefix the
// prefix is derived from the package and type name (DefaultPrefix);
// WithPrefix("x_") replaces it and WithPrefix("") drops it. Do not repeat
// the type name in methods (Unattended.Newsyslog, not
// Unattended.UnattendedNewsyslog) — the prefix already namespaces.
// Companions (optional):
//   - Opts() TaskOptions — struct-level DEFAULT TaskOptions for every
//     method registered from this struct (e.g. a single Privileged() for
//     an all-privileged struct); a method's own OptsX companion adds to
//     the default (Unprivileged() opts a single method out of Privileged).
//   - DescHelix() string — description (else empty)
//   - WhenHelix() TaskOption — per-task guard such as WhenLinux(), which
//     is serializable and so works with push; or WhenHelix(Facts) bool, an
//     opaque controller-only predicate. Either is appended after any
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
// wrong signature, a nil option (a nil RegisterOption or TaskOption among
// opts, or inside WithGroupWhen), a nil TaskOption returned by a companion
// or StructOption marker, a nil marker (pointer or interface), a companion
// promoted through a nil embedded pointer or interface (for a task method
// or WhenX(Facts) predicate, which run later, only a nil embed held in a
// by-value copy of v: see checkDeferred), an unknown
// OnCluster cluster — is reported as a declaration error (internal/declerr,
// which RecordPlan, Run, Apply and the CLI refuse to run with). A bad
// receiver, option, OnCluster, marker or struct-level companion registers
// nothing of v; a bad per-method companion or task method skips only that
// method's task.
//
// A Needs("x") in an OptsX companion (or WithGroupWhen) is resolved relative
// to this call's WithPrefix first: see Needs.
func RegisterMethods(v any, opts ...RegisterOption) {
	cfg, err := registerConfigFor(opts)
	if err != nil {
		declerr.Report(err)
		return
	}
	rv, rt, err := registerReceiver(v)
	if err != nil {
		declerr.Report(err)
		return
	}
	if !cfg.prefixSet {
		cfg.prefix = DefaultPrefix(v)
	}
	// A struct passed by value is registered as a private copy that can
	// never change; one passed by pointer may still be initialised later.
	byValue := reflect.ValueOf(v).Kind() == reflect.Struct
	registerMethodTasks(rv, rt, cfg, byValue)
}

// RegisterOnCluster registers several structs on one cluster: it is
// RegisterMethods(v, OnCluster(cluster)) for every struct v, each under its
// own DefaultPrefix, so a cluster's recipe groups read as one list:
//
//	RegisterOnCluster(cluster.NameFreeBSD,
//		freebsd.Carp{}, freebsd.NFS{}, freebsd.Relayd{}, freebsd.Zrepl{})
//
// An item that is a RegisterOption (WithPrefix aside) or a TaskOption
// applies to every struct of the call, wherever it appears in the list:
//
//	RegisterOnCluster(cluster.NameFreeBSD, freebsd.Debug{}, Operational())
//
// WithPrefix is refused (a declaration error, nothing registered): a prefix
// shared by several structs belongs on each RegisterMethods call, and one
// per struct is the default already. So is a nil item (untyped, or a nil
// struct pointer) or a nil TaskOption (directly or inside WithGroupWhen):
// no struct of the call is registered. Any other item is a struct or
// pointer to one, checked as RegisterMethods checks it.
func RegisterOnCluster(cluster string, items ...any) {
	var opts []RegisterOption
	var optItems []int // optItems[i] is opts[i]'s 1-based position in items
	var structs []any
	for i, item := range items {
		if isNilItem(item) {
			// Refuse up front: RegisterMethods would refuse only this item
			// while registering the others.
			declerr.Reportf("RegisterOnCluster(%q): item %d is nil", cluster, i+1)
			return
		}
		if o, ok := item.(RegisterOption); ok {
			opts = append(opts, o)
			optItems = append(optItems, i+1)
			continue
		}
		structs = append(structs, item)
	}
	var probe registerConfig
	if bad, why := applyRegisterOptions(&probe, opts); bad != 0 {
		declerr.Reportf("RegisterOnCluster(%q): item %d %s", cluster, optItems[bad-1], why)
		return
	}
	if probe.prefixSet {
		declerr.Report(fmt.Errorf("RegisterOnCluster(%q): WithPrefix is not allowed; use RegisterMethods for a shared prefix", cluster))
		return
	}
	opts = append(opts, OnCluster(cluster))
	for _, v := range structs {
		RegisterMethods(v, opts...)
	}
}

// isNilItem reports whether a RegisterOnCluster item is nil: untyped nil (a
// nil RegisterOption passed as any) or a typed nil pointer such as
// (*Foo)(nil). A nil TaskOption is left to applyRegisterOptions, which names
// it.
func isNilItem(item any) bool {
	if item == nil {
		return true
	}
	v := reflect.ValueOf(item)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// applyRegisterOptions applies opts to cfg in order. It stops at the first
// nil option and returns its 1-based position with what is wrong with it,
// or 0 and "" when every option applied. A nil option is refused rather
// than skipped, since it may stand for a guard such as OnCluster the recipe
// failed to build: a nil RegisterOption, a TaskOption(nil) passed as one, or
// a WithGroupWhen holding a nil TaskOption (which Task would refuse).
func applyRegisterOptions(cfg *registerConfig, opts []RegisterOption) (int, string) {
	for i, o := range opts {
		if o == nil {
			return i + 1, "is a nil RegisterOption"
		}
		if to, ok := o.(TaskOption); ok && to == nil {
			return i + 1, "is a nil TaskOption"
		}
		n := len(cfg.groupWhen)
		o.applyRegister(cfg)
		if j := nilOptionIndex(cfg.groupWhen[n:]); j != 0 {
			return i + 1, fmt.Sprintf("is WithGroupWhen with a nil TaskOption (option %d)", j)
		}
	}
	return 0, ""
}

// registerConfigFor applies opts and resolves OnCluster's guard into
// groupWhen, so every method of the call carries it. A nil option (see
// applyRegisterOptions), an unknown OnCluster cluster, or one contradicting
// WithCluster, is returned as an error.
func registerConfigFor(opts []RegisterOption) (registerConfig, error) {
	cfg := registerConfig{}
	if bad, why := applyRegisterOptions(&cfg, opts); bad != 0 {
		return cfg, fmt.Errorf("RegisterMethods: option %d %s", bad, why)
	}
	if cfg.guardCluster == "" {
		return cfg, nil
	}
	if cfg.cluster != cfg.guardCluster {
		return cfg, fmt.Errorf("RegisterMethods: OnCluster(%q) and WithCluster(%q) name different clusters",
			cfg.guardCluster, cfg.cluster)
	}
	guard, err := clusterGuard(cfg.guardCluster)
	if err != nil {
		return cfg, fmt.Errorf("RegisterMethods: OnCluster: %w", err)
	}
	cfg.groupWhen = append(cfg.groupWhen, guard)
	return cfg, nil
}

// clusterGuard returns the TaskOption guarding a task with cluster name's
// hostname_contains predicate: Eq for a one-host cluster (the exact
// predicate WhenHostname(host) records), In over the members otherwise
// (plan.EvalPredicates matches In as "contains any entry", case
// insensitive, like Eq).
func clusterGuard(name string) (TaskOption, error) {
	rec, ok := inventory.LookupCluster(name)
	if !ok {
		return nil, fmt.Errorf("Cluster %q is not registered", name)
	}
	matches := make([]string, len(rec.Hosts))
	for i, h := range rec.Hosts {
		matches[i] = hostnameMatch(h)
	}
	return WhenHostnameIn(matches...), nil
}

// hostnameMatch is the hostname fragment selecting inventory host name on a
// destination (WithHostnameMatch, else the name).
func hostnameMatch(name string) string {
	return inventory.HostnameMatchFor(name)
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
// companion misuse is reported as a declaration error naming the struct
// type ("RegisterMethods(pkg.Type): ..."): a struct-level one registers
// nothing, a per-method one skips that method. byValue is true when v was
// passed by value (see checkDeferred).
func registerMethodTasks(rv reflect.Value, rt reflect.Type, cfg registerConfig, byValue bool) {
	// rt is always a pointer type (registerReceiver); name its struct.
	misuse := func(err error) {
		declerr.Report(fmt.Errorf("RegisterMethods(%s): %w", rt.Elem(), err))
	}
	// Struct-level default TaskOptions: embedded StructOption markers
	// (e.g. RequiresRoot) and/or the Opts() companion. A method's own
	// OptsX companion is appended after the combined default for that
	// method (Unprivileged() opts out of Privileged, e.g. an unprivileged
	// smoke-test task on an otherwise-privileged struct).
	structOpts, err := collectStructOptions(rv, rt)
	if err != nil {
		misuse(err)
		return
	}

	// names is built from rt.Method(i) in index order rather than a map:
	// reflect.Type.Method documents that methods are sorted in lexicographic
	// order by name, so iterating this slice (instead of ranging a map, whose
	// iteration order Go randomizes per range) registers tasks in a fixed,
	// deterministic order and — mirroring the SystemdUnits precedent
	// (api/systemd_units.go's validate) — a recipe with several broken
	// companions always reports the same first one (alphabetically first by
	// method name) instead of a different one each run.
	names := make([]string, 0, rt.NumMethod())
	for i := 0; i < rt.NumMethod(); i++ {
		name := rt.Method(i).Name
		if isCompanionName(name) {
			continue
		}
		names = append(names, name)
	}

	for _, name := range names {
		method := rv.MethodByName(name)
		if !method.IsValid() {
			continue
		}
		mt := method.Type()
		if mt.NumIn() != 0 || mt.NumOut() != 0 {
			continue // only func()
		}

		// A task method promoted through a nil embedded field would panic
		// when its task runs (checkDeferred).
		if err := checkDeferred(rv, name, byValue); err != nil {
			misuse(err)
			continue
		}
		taskOpts, err := methodTaskOptions(rv, name, cfg, structOpts, byValue)
		if err != nil {
			misuse(err)
			continue
		}
		desc, err := resolveDesc(rv, name)
		if err != nil {
			misuse(err)
			continue
		}
		task := cfg.prefix + camelToSnake(name)
		noteMethodTask(rt, name, task)
		Task(task, desc, method.Interface().(func()), taskOpts...)
	}
}

// methodTaskOptions assembles the TaskOptions of method name: the call's
// WithGroupWhen (which carries OnCluster's guard) and WithCluster options,
// the call's prefix for resolving relative Needs names, then its OptsX
// companion (or the struct-level default) and its WhenX guard. A companion
// with the wrong signature is returned as an error.
func methodTaskOptions(rv reflect.Value, name string, cfg registerConfig, structOpts TaskOptions, byValue bool) (TaskOptions, error) {
	var taskOpts TaskOptions
	taskOpts = append(taskOpts, cfg.groupWhen...)
	if cfg.cluster != "" {
		taskOpts = append(taskOpts, WithTaskCluster(cfg.cluster))
	}
	if cfg.prefix != "" {
		taskOpts = append(taskOpts, needsPrefix(cfg.prefix))
	}
	methodOpts, err := resolveOpts(rv, name, structOpts)
	if err != nil {
		return nil, err
	}
	taskOpts = append(taskOpts, methodOpts...)
	whenOpt, err := resolveWhen(rv, name, byValue)
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
// degrades gracefully instead of refusing the task. A companion promoted
// through a nil embedded field is returned as an error (callPromoted), as
// for every other companion: it is a nil declaration, and calling it
// would panic.
func resolveDesc(rv reflect.Value, name string) (string, error) {
	d := rv.MethodByName("Desc" + name)
	if !d.IsValid() {
		return "", nil
	}
	dt := d.Type()
	if dt.NumIn() != 0 || dt.NumOut() != 1 || dt.Out(0).Kind() != reflect.String {
		return "", nil
	}
	return callPromoted(rv, "Desc"+name, func() (string, error) {
		return d.Call(nil)[0].String(), nil
	})
}

// resolveOpts returns the struct-level default followed by the OptsX
// companion's TaskOptions, if present. The companion adds to the default
// rather than replacing it, so a RequiresRoot struct's OptsX that only adds
// Needs keeps Privileged; Unprivileged() is the explicit opt-out. A wrong
// OptsX signature is returned as an error: a silently ignored companion
// could drop Unprivileged() or a guard.
func resolveOpts(rv reflect.Value, name string, structOpts TaskOptions) (TaskOptions, error) {
	merged := append(TaskOptions(nil), structOpts...)
	o := rv.MethodByName("Opts" + name)
	if !o.IsValid() {
		return merged, nil
	}
	methodOpts, err := callPromoted(rv, "Opts"+name, func() (TaskOptions, error) {
		return callTaskOptionsCompanion(o, "Opts"+name, "Opts"+name+" must be func() TaskOptions")
	})
	if err != nil {
		return nil, err
	}
	return append(merged, methodOpts...), nil
}

// resolveWhen returns the TaskOption of the WhenX companion, or nil if the
// companion is absent. Two signatures are accepted:
//
//   - WhenX() TaskOption: a guard such as WhenLinux() or WhenProfile("x").
//     A serializable guard travels in the plan and is evaluated on the
//     destination, so the task still works with push, cluster and fleet.
//   - WhenX(Facts) bool: an opaque predicate, evaluated on the controller
//     only (When). Push, cluster and fleet refuse a task guarded this way.
//
// Any other signature is returned as an error: a silently ignored companion
// would drop the guard and run the task unconditionally on every host.
func resolveWhen(rv reflect.Value, name string, byValue bool) (TaskOption, error) {
	w := rv.MethodByName("When" + name)
	if !w.IsValid() {
		return nil, nil
	}
	wt := w.Type()
	if wt.NumIn() == 0 && wt.NumOut() == 1 && wt.Out(0) == reflect.TypeOf(TaskOption(nil)) {
		opt, err := callPromoted(rv, "When"+name, func() (TaskOption, error) {
			opt, _ := w.Call(nil)[0].Interface().(TaskOption)
			return opt, nil
		})
		if err != nil {
			return nil, err
		}
		if opt == nil {
			//lint:ignore ST1005 the message starts with the Go method name
			return nil, fmt.Errorf("When%s returned a nil TaskOption", name)
		}
		return opt, nil
	}
	if wt.NumIn() != 1 || wt.In(0) != reflect.TypeOf(Facts{}) ||
		wt.NumOut() != 1 || wt.Out(0).Kind() != reflect.Bool {
		//lint:ignore ST1005 the message starts with the Go method name
		return nil, fmt.Errorf("When%s must be func() TaskOption or func(Facts) bool", name)
	}
	// The predicate runs at activation, not now (checkDeferred).
	if err := checkDeferred(rv, "When"+name, byValue); err != nil {
		return nil, err
	}
	wMethod := w
	return When(func(f Facts) bool {
		return wMethod.Call([]reflect.Value{reflect.ValueOf(f)})[0].Bool()
	}), nil
}

// checkDeferred checks a method of rv that runs later, not during
// RegisterMethods (a task method, a WhenX(Facts) predicate), for a nil
// embedded pointer or interface on its promotion chain (checkPromoted).
// Only a struct registered by value is checked, and only the fields held
// in that private copy (directly, in its by-value embedded structs, or in
// its embedded interfaces' values): they can never change, so the call
// would certainly panic. Fields behind a non-nil pointer (embedded or held
// in an interface) are shared with the recipe, and a struct registered by
// pointer reads all its fields when the method runs, so an embed the
// recipe sets after RegisterMethods is fine there and is not refused.
func checkDeferred(rv reflect.Value, name string, byValue bool) error {
	if !byValue {
		return nil
	}
	_, err := checkPromoted(rv, name, true)
	return err
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
