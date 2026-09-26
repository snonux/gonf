package api

import (
	"fmt"
	"go/token"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/logger"
)

// Needs declares that the task requires other tasks: wherever the task is
// recorded (Run, the CLI, gonf plan, push/cluster/fleet, an aggregate), its
// needed tasks are recorded right before it, unless the same Run list or
// aggregate tree already recorded them:
//
//	Task("web", "", web, Needs("pf", "base"))
//	Run("web")        // records pf, base, web
//	Run("base", "web") // records base, pf, web
//
// Resolution: in RegisterMethods(..., WithPrefix("frontends_")) a name is
// tried relative to the prefix first ("pf" → "frontends_pf"), then as a full
// name; outside RegisterMethods it is a full name. Aliases resolve to their
// target. Names are resolved when a plan is recorded, so the needed task may
// be registered after its dependent.
//
// Semantics (the simple, safe design; auto-inclusion instead of refusal):
//   - Each needed task records with its own guards and privilege, in the
//     envelope the dependent records in, never inside the dependent's own
//     when_begin: Needs never widens or narrows where the needed task
//     applies.
//   - Deduplication is by scope, exactly like an aggregate: a need already
//     recorded earlier in the same Run(...) list or aggregate tree is not
//     recorded again, and a later explicit name in the same scope that a
//     Needs already recorded is skipped. A task body running Run(...) starts
//     a scope of its own, since its envelope may differ.
//   - A plan without Needs records exactly as before.
//   - A task that needs Operational work counts as operational for pattern
//     aggregates (containsOperational), so a pattern cannot pull it in.
//
// A need can also be a method expression of a RegisterMethods struct
// instead of a string, which an editor can jump to and rename, and a typo in
// which is a compile error:
//
//	func (Unattended) OptsCron() TaskOptions {
//		return TaskOptions{Needs(Unattended.Script, Unattended.StampDir)}
//	}
//
// It names the task RegisterMethods registered for that method, whatever
// prefix it got. A struct registered more than once (under two prefixes)
// resolves to the registration sharing the dependent's prefix, else the
// need is ambiguous and fails the record like an unknown name. A method
// value (u.Script) works the same way, with one catch: a method value of a
// method promoted from an embedded struct names the embedded type's method
// (Outer{}.Base is Inner.Base), so write the method expression Outer.Base
// for those.
//
// The methods of a generic struct work too, with two limits. The runtime
// does not tell instantiations apart, so G[int].Script and G[string].Script
// name the same method: with one instantiation registered, either names
// its task. And inside a generic method, a method expression or method
// value over the type parameter (G[X].Script, g.Script) compiles to a
// closure the runtime cannot name: write a concrete instantiation
// (G[int].Script) or the task name there.
//
// Errors: an empty name, a task that needs itself, or a Needs cycle
// (a → b → a, aliases followed) is a declaration error at registration and
// the task is not queued. A func that does not name an exported method of
// a struct (a package function, an unexported method, a function literal,
// a closure such as G[X].Script above), or an argument that is neither a
// string nor a func, is a declaration error when Needs is called. A need naming no
// registered task fails the record that reaches it, like an unknown
// AggregateTasks member; so does a need whose opaque When fails on the
// controller.
func Needs(tasks ...any) TaskOption {
	names := make([]string, 0, len(tasks))
	for _, t := range tasks {
		name, err := needName(t)
		if err != nil {
			declerr.Report(err)
			continue
		}
		names = append(names, name)
	}
	return func(c *taskCandidate) { c.needs = append(c.needs, names...) }
}

// needsPrefix sets the RegisterMethods prefix relative Needs names resolve
// against (resolveNeedName). Internal: RegisterMethods adds it.
func needsPrefix(prefix string) TaskOption {
	return func(c *taskCandidate) { c.needsPrefix = prefix }
}

// resolveNeedName returns the task a Needs entry n names: prefix+n when that
// exists, else n itself when that exists.
func resolveNeedName(prefix, n string, exists func(string) bool) (string, bool) {
	if key, ok := strings.CutPrefix(n, methodNeedPrefix); ok {
		name, ok := methodTaskName(key, prefix)
		return name, ok && exists(name)
	}
	if prefix != "" && exists(prefix+n) {
		return prefix + n, true
	}
	if exists(n) {
		return n, true
	}
	return "", false
}

// candidateExists reports whether a task, aggregate or alias named name is
// queued.
func candidateExists(name string) bool {
	_, ok := findCandidate(name)
	return ok
}

// resolvedNeeds returns c's needs resolved against the registry, skipping
// the ones that name nothing yet (recording reports those).
func (c taskCandidate) resolvedNeeds() []string {
	return c.needEdges(candidateExists)
}

// needEdges is the Needs graph edge list of c for a registry view exists:
// an alias leads to its target, any other task to its resolved needs.
func (c taskCandidate) needEdges(exists func(string) bool) []string {
	if c.aliasOf != "" {
		return []string{c.aliasOf}
	}
	var out []string
	for _, n := range c.needs {
		if name, ok := resolveNeedName(c.needsPrefix, n, exists); ok {
			out = append(out, name)
		}
	}
	return out
}

// checkNeeds enforces Needs' registration-time contract on the candidate c
// that is about to be queued: no empty name, no self need, no cycle.
func checkNeeds(c taskCandidate) error {
	if len(c.needs) == 0 {
		return nil
	}
	exists := func(name string) bool { return name == c.name || candidateExists(name) }
	for _, n := range c.needs {
		if n == "" {
			return fmt.Errorf("Task %q: Needs: task name must not be empty", c.name)
		}
		if name, ok := resolveNeedName(c.needsPrefix, n, exists); ok && name == c.name {
			return fmt.Errorf("Task %q: Needs: a task must not need itself", c.name)
		}
	}
	if chain := needsCycle(c, exists); chain != nil {
		return fmt.Errorf("Task %q: Needs cycle: %s", c.name, strings.Join(chain, " -> "))
	}
	return nil
}

// needsCycle returns the chain c -> ... -> c when following Needs (and
// alias) edges from c leads back to c, or nil. Checking at every
// registration catches every cycle: the task that closes one is registered
// last, when all the others are already queued.
func needsCycle(c taskCandidate, exists func(string) bool) []string {
	lookup := func(name string) (taskCandidate, bool) {
		if name == c.name {
			return c, true
		}
		return findCandidate(name)
	}
	visited := map[string]bool{}
	var walk func(cur taskCandidate, path []string) []string
	walk = func(cur taskCandidate, path []string) []string {
		for _, next := range cur.needEdges(exists) {
			if next == c.name {
				return append(slices.Clone(path), next)
			}
			nc, ok := lookup(next)
			if !ok || visited[next] {
				continue
			}
			visited[next] = true
			if chain := walk(nc, append(slices.Clone(path), next)); chain != nil {
				return chain
			}
		}
		return nil
	}
	return walk(c, []string{c.name})
}

// needsScope is the Needs dedupe scope of one Run(...) list
// (recSession.needs): recorded holds every task (alias target) the list or
// an aggregate tree under it recorded, viaNeeds the ones only a Needs
// recorded, which a later explicit name in the list then skips.
type needsScope struct {
	recorded map[string]bool
	viaNeeds map[string]bool
}

// enterNeedsScope installs a fresh Needs scope for one Run(...) list and
// returns the function restoring the previous one.
func enterNeedsScope() (restore func()) {
	saved := recSession.needs
	recSession.needs = &needsScope{recorded: map[string]bool{}, viaNeeds: map[string]bool{}}
	return func() { recSession.needs = saved }
}

// recordNeeds records the needs of task name that its scope has not
// recorded yet, in declaration order, each through recordTaskName (so a
// need's own needs, alias, guard, privilege and cycle check all apply).
func recordNeeds(name string) error {
	c, ok := findCandidate(name)
	if !ok || len(c.needs) == 0 {
		return nil
	}
	for _, n := range c.needs {
		need, ok := resolveNeedName(c.needsPrefix, n, candidateExists)
		if !ok {
			return fmt.Errorf("task %q needs unknown task %q", name, needLabel(n))
		}
		target, _, err := resolveAlias(need)
		if err != nil {
			return fmt.Errorf("task %q needs %q: %w", name, need, err)
		}
		if needRecorded(target) {
			continue
		}
		if err := recordTaskName(need); err != nil {
			return err
		}
		noteNeedRecorded(target)
	}
	return nil
}

// needRecorded reports whether target was already recorded in the current
// scope: the aggregate tree, or the Run(...) list.
func needRecorded(target string) bool {
	if recSession.aggregateSeen[target] {
		return true
	}
	return recSession.needs != nil && recSession.needs.recorded[target]
}

// noteRecorded marks name's target as recorded in the current Run(...)
// list, so a later Needs of it is satisfied. It never causes a skip on its
// own (only viaNeeds does), so a plan without Needs is unchanged.
func noteRecorded(name string) {
	if recSession.needs == nil {
		return
	}
	if target, _, err := resolveAlias(name); err == nil {
		recSession.needs.recorded[target] = true
	}
}

// noteNeedRecorded marks target as recorded by a Needs, in the aggregate
// tree (so the aggregate skips it as a later member) and in the list.
func noteNeedRecorded(target string) {
	if recSession.aggregateSeen != nil {
		recSession.aggregateSeen[target] = true
	}
	if recSession.needs != nil {
		recSession.needs.recorded[target] = true
		recSession.needs.viaNeeds[target] = true
	}
}

// skipNeeded reports whether an explicit name of the current Run(...) list
// must be skipped because a Needs earlier in the list already recorded it.
func skipNeeded(name string) bool {
	if recSession.needs == nil || len(recSession.needs.viaNeeds) == 0 {
		return false
	}
	target, _, err := resolveAlias(name)
	if err != nil || !recSession.needs.viaNeeds[target] {
		return false
	}
	logger.Debug("Run: skipping %q: a Needs earlier in the list already recorded it", name)
	return true
}

// methodNeedPrefix marks a Needs entry that is a method reference (the
// normalized runtime name of a method expression) rather than a task name.
// A NUL byte never occurs in a task name, so the two cannot collide.
const methodNeedPrefix = "\x00method:"

// methodTasks maps a registered method (methodKey) to the task names
// RegisterMethods gave it, in registration order.
var (
	methodTasksMu sync.Mutex
	methodTasks   = map[string][]string{}
)

// resetMethodTasks forgets every registered method (ResetTasks).
func resetMethodTasks() {
	methodTasksMu.Lock()
	defer methodTasksMu.Unlock()
	methodTasks = map[string][]string{}
}

// methodKey is the key of method name of the struct type t in methodTasks,
// in the form a method expression's runtime name normalizes to. A generic
// struct's type arguments are dropped (G[int] → G), as the runtime name
// carries none: every instantiation of a generic struct shares one key, so
// two registered instantiations resolve like one struct registered twice.
func methodKey(t reflect.Type, name string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return runtimePkgPath(t.PkgPath()) + "." + genericBaseName(t.Name()) + "." + name
}

// runtimePkgPath is import path p escaped the way the Go toolchain writes
// it into runtime function names (cmd/internal/objabi.PathToPrefix): a '.'
// in the last element (gopkg.in/yaml.v3 → yaml%2ev3), and '%', '"', control
// characters, space and non-ASCII bytes anywhere, become %xx. Keeping the
// escaped form leaves the first '.' after the last '/' the separator
// between package and type, which namesExportedMethod and needLabel rely on.
func runtimePkgPath(p string) string {
	const hex = "0123456789abcdef"
	slash := strings.LastIndexByte(p, '/')
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if c := p[i]; c <= ' ' || (c == '.' && i > slash) || c == '%' || c == '"' || c >= 0x7F {
			b.Write([]byte{'%', hex[c>>4], hex[c&0xF]})
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// genericBaseName is a reflect type name without a generic instantiation's
// type arguments: "G[int]" and "G[map[string]pkg/path.T]" become "G".
func genericBaseName(name string) string {
	if i := strings.IndexByte(name, '['); i >= 0 {
		return name[:i]
	}
	return name
}

// noteMethodTask records that RegisterMethods registered task for method
// name of t, for Needs(T.Method).
func noteMethodTask(t reflect.Type, name, task string) {
	key := methodKey(t, name)
	methodTasksMu.Lock()
	defer methodTasksMu.Unlock()
	methodTasks[key] = append(methodTasks[key], task)
}

// methodTaskName returns the task registered for the method key: the only
// one, or the one under prefix when the struct was registered more than
// once.
func methodTaskName(key, prefix string) (string, bool) {
	methodTasksMu.Lock()
	tasks := methodTasks[key]
	methodTasksMu.Unlock()
	if len(tasks) == 1 {
		return tasks[0], true
	}
	if prefix == "" {
		return "", false
	}
	// Match the exact name, not a prefix: with registrations under "a_" and
	// "a_b_", "a_b_base" also starts with "a_".
	want := prefix + camelToSnake(key[strings.LastIndexByte(key, '.')+1:])
	for _, t := range tasks {
		if t == want {
			return t, true
		}
	}
	return "", false
}

// needName returns the Needs entry of t: a string as written, or the
// method reference of a method expression (T.Method, (*T).Method) or a
// method value (v.Method).
func needName(t any) (string, error) {
	switch v := t.(type) {
	case string:
		return v, nil
	case nil:
		return "", fmt.Errorf("Needs: nil task")
	}
	rv := reflect.ValueOf(t)
	if rv.Kind() != reflect.Func || rv.IsNil() {
		return "", fmt.Errorf("Needs: want a task name or a method expression such as Unattended.Script, got %T", t)
	}
	fn := runtime.FuncForPC(rv.Pointer())
	if fn == nil {
		return "", fmt.Errorf("Needs: cannot resolve %T to a method", t)
	}
	raw := fn.Name()
	key := normalizeMethodName(raw)
	if !namesExportedMethod(key) {
		return "", notMethodError(raw, key)
	}
	return methodNeedPrefix + key, nil
}

// namesExportedMethod reports whether the normalized runtime name key is
// "pkgpath.Type.Method" with an exported Method, the only shape
// RegisterMethods registers a task for. A package function ("pkg.Setup"),
// an unexported method ("pkg.U.helper") and a closure ("pkg.T.M.func1",
// "pkg.init.func1") are not.
func namesExportedMethod(key string) bool {
	_, rest, ok := strings.Cut(key[strings.LastIndexByte(key, '/')+1:], ".")
	if !ok {
		return false
	}
	typ, method, ok := strings.Cut(rest, ".")
	return ok && token.IsIdentifier(typ) && token.IsIdentifier(method) && token.IsExported(method)
}

// notMethodError is the declaration error of a Needs func that names no
// exported method (namesExportedMethod). A closure compiled inside a
// generic method gets a hint: there, a method expression or method value
// over the type parameter (G[X].M, g.M) is such a closure, whose method the
// runtime cannot name.
func notMethodError(raw, key string) error {
	err := fmt.Errorf("Needs: %s does not name an exported method of a struct", needLabel(methodNeedPrefix+key))
	if inGenericMethodClosure(raw) {
		return fmt.Errorf("%w (inside a generic method, G[X].M and g.M over the type parameter are closures: "+
			"name a concrete instantiation such as G[int].M, or the task)", err)
	}
	return err
}

// inGenericMethodClosure reports whether the runtime name raw is a closure
// compiled directly in a method of a generic type: "pkg.G[...].M.funcN" or
// "pkg.(*G[...]).M.funcN". A closure in a generic function
// ("pkg.F[...].funcN", "pkg.F[...].funcN.funcM") is not.
func inGenericMethodClosure(raw string) bool {
	i := strings.Index(raw, "[...]")
	if i < 0 {
		return false
	}
	// After the type arguments (and a pointer receiver's ")"): ".M.funcN".
	rest := strings.TrimPrefix(raw[i+len("[...]"):], ")")
	segs := strings.Split(rest, ".")
	if len(segs) < 3 || segs[0] != "" {
		return false
	}
	return token.IsIdentifier(segs[1]) && !isClosureSegment(segs[1]) && isClosureSegment(segs[2])
}

// isClosureSegment reports whether seg is a closure's "funcN".
func isClosureSegment(seg string) bool {
	num, ok := strings.CutPrefix(seg, "func")
	return ok && num != "" && strings.Trim(num, "0123456789") == ""
}

// normalizeMethodName turns a method's runtime name into methodKey's form:
// "pkg.(*T).M" and the method value wrapper "pkg.T.M-fm" both become
// "pkg.T.M", and so does a generic struct's "pkg.T[...].M" (the runtime
// writes every instantiation's type arguments as "[...]").
func normalizeMethodName(n string) string {
	n = strings.TrimSuffix(n, "-fm")
	n = strings.ReplaceAll(n, "[...]", "")
	if i := strings.Index(n, "(*"); i >= 0 {
		if j := strings.IndexByte(n[i:], ')'); j >= 0 {
			n = n[:i] + n[i+2:i+j] + n[i+j+1:]
		}
	}
	return n
}

// needLabel is how an error names the Needs entry n: a task name as
// written, a method reference as "T.Method".
func needLabel(n string) string {
	key, ok := strings.CutPrefix(n, methodNeedPrefix)
	if !ok {
		return n
	}
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		key = key[i+1:]
	}
	if _, rest, ok := strings.Cut(key, "."); ok {
		return rest
	}
	return key
}
