package api

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/internal/needsfixture.v1"
	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/plan"
)

// recordWire records task name alone and returns its encoded plan with the
// task name replaced by "T", so two spellings of one body compare equal.
func recordWire(t *testing.T, name string) string {
	t.Helper()
	ops, err := RecordPlan("sugar", "", name)
	if err != nil {
		t.Fatalf("RecordPlan(%s): %v", name, err)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), name, "T")
}

// requireSameOps registers the two bodies as tasks and fails unless they
// record the same plan: a sugar must record exactly its long form.
func requireSameOps(t *testing.T, long, sugar func()) {
	t.Helper()
	resetForHostsState(t)
	Task("long_form", "", long)
	Task("sugar_form", "", sugar)
	if a, b := recordWire(t, "long_form"), recordWire(t, "sugar_form"); a != b {
		t.Fatalf("plans differ:\nlong:  %s\nsugar: %s", a, b)
	}
}

func TestRootPermSugar(t *testing.T) {
	requireSameOps(t,
		func() {
			File("/tmp/s/a", WithContent("x"), Perm(0o644, Root))
			File("/tmp/s/b", WithContent("x"), Perm(0o755, Root))
			File("/tmp/s/c", WithContent("x"), Perm(0o600, Root))
			Dir("/tmp/s/d", Perm(0o755, Root))
			Dir("/tmp/s/e", Perm(0o700, Root))
			EnsureDir("/tmp/s/f", Perm(0o755, Root))
			EnsureDir("/tmp/s/g", Perm(0o700, Root))
			EnsureFile("/tmp/s/h", Perm(0o600, Root))
		},
		func() {
			File("/tmp/s/a", WithContent("x"), RootOwned)
			File("/tmp/s/b", WithContent("x"), RootExec)
			File("/tmp/s/c", WithContent("x"), RootPrivate)
			Dir("/tmp/s/d", RootOwned)
			Dir("/tmp/s/e", RootPrivate)
			EnsureDir("/tmp/s/f", RootOwned)
			EnsureDir("/tmp/s/g", RootPrivate)
			EnsureFile("/tmp/s/h", RootPrivate)
		})
}

func TestWithContentFrom(t *testing.T) {
	requireSameOps(t,
		func() { File("/tmp/s/a", WithContent("rendered")) },
		func() { File("/tmp/s/a", WithContentFrom("rendered", nil)) })

	requireDeclErr(t, "WithContentFrom: template broke", func() {
		File("/tmp/s/a", WithContentFrom("", errors.New("template broke")))
	})
}

func TestWithShellVarAndSymlink(t *testing.T) {
	requireSameOps(t,
		func() {
			File("/tmp/s/rc.conf", WithKeyedLine("vm_enable=", `vm_enable="YES"`),
				WithKeyedLine("kern.x=", `kern.x="a \"b\" \$c"`))
			Link("/tmp/s/l", WithSymlink("/tmp/s/target"))
		},
		func() {
			File("/tmp/s/rc.conf", WithShellVar("vm_enable", "YES"), WithShellVar("kern.x", `a "b" $c`))
			Symlink("/tmp/s/l", "/tmp/s/target")
		})

	requireDeclErr(t, `WithShellVar: "1x" is not a shell variable name`, func() {
		File("/tmp/s/rc.conf", WithShellVar("1x", "y"))
	})
}

// sugarRecipe is a RegisterMethods struct whose Cron needs two siblings by
// method expression, one of them through a pointer receiver.
type sugarRecipe struct{}

func (sugarRecipe) Script()    {}
func (*sugarRecipe) StampDir() {}
func (sugarRecipe) Cron()      {}
func (sugarRecipe) OptsCron() TaskOptions {
	return TaskOptions{Needs(sugarRecipe.Script, (*sugarRecipe).StampDir)}
}
func (sugarRecipe) OptsScript() TaskOptions { return TaskOptions{Needs(otherRecipe{}.Base)} }

type otherRecipe struct{}

func (otherRecipe) Base() {}

func TestNeedsMethodExpressions(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(sugarRecipe{}, WithPrefix("s_"))
	RegisterMethods(otherRecipe{}, WithPrefix("o_"))
	if err := declerr.First(); err != nil {
		t.Fatal(err)
	}
	c, _ := findCandidate("s_cron")
	if got, want := c.resolvedNeeds(), []string{"s_script", "s_stamp_dir"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("s_cron needs = %v, want %v", got, want)
	}
	c, _ = findCandidate("s_script")
	if got, want := c.resolvedNeeds(), []string{"o_base"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("s_script needs = %v, want %v", got, want)
	}
	if _, err := RecordPlan("sugar", "", "s_cron"); err != nil {
		t.Fatal(err)
	}
}

func TestNeedsMethodExpressionErrors(t *testing.T) {
	requireDeclErr(t, "Needs: want a task name or a method expression", func() {
		Task("x", "", func() {}, Needs(42))
	})

	// A struct registered twice resolves within the dependent's prefix, and
	// is ambiguous from anywhere else.
	resetForHostsState(t)
	RegisterMethods(otherRecipe{}, WithPrefix("a_"))
	RegisterMethods(otherRecipe{}, WithPrefix("b_"))
	Task("b_user", "", func() {}, Needs(otherRecipe.Base), needsPrefix("b_"))
	Task("free", "", func() {}, Needs(otherRecipe.Base))
	c, _ := findCandidate("b_user")
	if got := c.resolvedNeeds(); !reflect.DeepEqual(got, []string{"b_base"}) {
		t.Fatalf("b_user needs = %v, want [b_base]", got)
	}
	if _, err := RecordPlan("sugar", "", "free"); err == nil || !strings.Contains(err.Error(), `needs unknown task "otherRecipe.Base"`) {
		t.Fatalf("ambiguous need: err = %v", err)
	}
}

// genericSugar is a generic RegisterMethods struct (task eb): Needs of its
// methods by method expression (value and pointer receiver) and by method
// value must resolve like on a plain struct, although the runtime names a
// generic method G[...].M while reflect names the type G[int]. The
// instantiation in OptsCron is concrete: over the type parameter X it
// would be a closure (see TestNeedsGenericTypeParamClosureIsDeclError).
type genericSugar[X any] struct{}

func (genericSugar[X]) Script()    {}
func (*genericSugar[X]) StampDir() {}
func (genericSugar[X]) Cron()      {}
func (genericSugar[X]) OptsCron() TaskOptions {
	return TaskOptions{Needs(genericSugar[int].Script, (*genericSugar[int]).StampDir)}
}

// TestNeedsMethodExpressionsOnGenericStruct (task eb): method expressions
// and method values of a generic struct name its registered tasks, also
// for an instantiation whose type argument comes from another package.
func TestNeedsMethodExpressionsOnGenericStruct(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(genericSugar[int]{}, WithPrefix("g_"))
	Task("g_free", "", func() {}, Needs(genericSugar[int]{}.Script, (&genericSugar[int]{}).StampDir))
	if err := declerr.First(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"g_cron", "g_free"} {
		c, _ := findCandidate(name)
		if got, want := c.resolvedNeeds(), []string{"g_script", "g_stamp_dir"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s needs = %v, want %v", name, got, want)
		}
	}
	if _, err := RecordPlan("sugar", "", "g_cron"); err != nil {
		t.Fatalf("RecordPlan(g_cron): %v", err)
	}

	// Another instantiation under the derived default prefix shares the
	// method key and resolves within its own prefix.
	RegisterMethods(&genericSugar[map[string]plan.Op]{})
	c, _ := findCandidate("api_generic_sugar_cron")
	if got, want := c.resolvedNeeds(), []string{"api_generic_sugar_script", "api_generic_sugar_stamp_dir"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("api_generic_sugar_cron needs = %v, want %v", got, want)
	}
}

// TestNeedsGenericMethodAmbiguousOutsidePrefix (task eb, negative): the
// runtime cannot tell G[int].M from G[string].M, so two instantiations are
// two registrations of one struct: a dependent under neither prefix gets a
// "needs unknown task" error naming the method rather than a guess.
func TestNeedsGenericMethodAmbiguousOutsidePrefix(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(genericSugar[int]{}, WithPrefix("a_"))
	RegisterMethods(genericSugar[string]{}, WithPrefix("b_"))
	Task("free", "", func() {}, Needs(genericSugar[bool].Script))
	if _, err := RecordPlan("sugar", "", "free"); err == nil ||
		!strings.Contains(err.Error(), `needs unknown task "genericSugar.Script"`) {
		t.Fatalf("ambiguous generic need: err = %v", err)
	}
	c, _ := findCandidate("b_cron")
	if got, want := c.resolvedNeeds(), []string{"b_script", "b_stamp_dir"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("b_cron needs = %v, want %v", got, want)
	}
}

// TestNeedsGenericMethodSingleRegistrationAnyInstantiation (task eb) pins
// the documented limit: with one instantiation registered, a method
// expression of ANY instantiation names its task, since the runtime name
// carries no type arguments.
func TestNeedsGenericMethodSingleRegistrationAnyInstantiation(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(genericSugar[int]{}, WithPrefix("g_"))
	Task("free", "", func() {}, Needs(genericSugar[string].Script))
	c, _ := findCandidate("free")
	if got, want := c.resolvedNeeds(), []string{"g_script"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("free needs = %v, want %v", got, want)
	}
}

// genericClosureNeed needs its own method over the type parameter X, which
// the compiler turns into a closure the runtime cannot name.
type genericClosureNeed[X any] struct{}

func (genericClosureNeed[X]) Base() {}
func (genericClosureNeed[X]) Web()  {}
func (genericClosureNeed[X]) OptsWeb() TaskOptions {
	return TaskOptions{Needs(genericClosureNeed[X].Base)}
}
func (g genericClosureNeed[X]) OptsBase() TaskOptions { return TaskOptions{Needs(g.Web)} }

// genericClosureHint is the hint only a closure compiled inside a generic
// method gets.
const genericClosureHint = "inside a generic method, G[X].M and g.M over the type parameter are closures"

// TestNeedsGenericTypeParamClosureIsDeclError (task eb, negative): a method
// expression or method value over a type parameter is a declaration error
// naming the closure, with the generic-method hint, not a record-time
// "unknown task" nobody can match to the recipe.
func TestNeedsGenericTypeParamClosureIsDeclError(t *testing.T) {
	requireDeclErr(t, "Needs: genericClosureNeed.OptsBase.func1 does not name an exported method of a struct ("+genericClosureHint, func() {
		RegisterMethods(genericClosureNeed[int]{}, WithPrefix("c_"))
	})
	requireDeclErr(t, "Needs: genericClosureNeed.OptsWeb.func1 does not name an exported method of a struct ("+genericClosureHint, func() {
		_ = genericClosureNeed[int]{}.OptsWeb()
	})
}

// genericFuncClosure returns a closure compiled in a generic FUNCTION
// (runtime name F[...].funcN), and a closure nested in it.
func genericFuncClosure[X any]() (func(), func()) {
	outer := func() func() { return func() {} }
	return func() {}, outer()
}

// TestNeedsGenericFunctionClosureHasNoMethodHint (task eb, negative): a
// closure of a generic function is refused without the generic-METHOD
// hint, which would point at a method expression that is not there.
func TestNeedsGenericFunctionClosureHasNoMethodHint(t *testing.T) {
	direct, nested := genericFuncClosure[int]()
	for _, f := range []func(){direct, nested} {
		requireDeclErr(t, "does not name an exported method of a struct", func() {
			Task("x", "", func() {}, Needs(f))
		})
		if err := declerr.First(); strings.Contains(err.Error(), genericClosureHint) {
			t.Fatalf("generic function closure: unexpected generic-method hint: %v", err)
		}
	}
}

// TestInGenericMethodClosure pins which runtime names get the hint: only a
// funcN directly inside a method of a generic type.
func TestInGenericMethodClosure(t *testing.T) {
	for raw, want := range map[string]bool{
		"example.com/p.G[...].OptsCron.func1":       true,
		"example.com/p.(*G[...]).OptsCron.func2":    true,
		"example.com/p.G[...].OptsCron.func1.func2": true,
		"example.com/p%2ev1.G[...].OptsCron.func1":  true,
		"example.com/p.Setup[...].func1":            false,
		"example.com/p.Setup[...].func1.func2":      false,
		"example.com/p.(*G[...]).func1":             false,
		"example.com/p.G[...].Base":                 false,
		"example.com/p.G[...].Base-fm":              false,
		"example.com/p.G[...].OptsCron.funcA":       false,
		"example.com/p.T.OptsCron.func1":            false,
		"example.com/p.TestX.func1":                 false,
	} {
		if got := inGenericMethodClosure(raw); got != want {
			t.Errorf("inGenericMethodClosure(%q) = %v, want %v", raw, got, want)
		}
	}
}

// TestNeedsMethodExpressionDottedPackagePath (task eb): a struct from a
// package whose last path element contains a dot (like gopkg.in/yaml.v3)
// resolves, although the runtime escapes that dot as %2e and reflect's
// PkgPath does not.
func TestNeedsMethodExpressionDottedPackagePath(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(needsfixture.Recipe{}, WithPrefix("y_"))
	Task("y_user", "", func() {}, Needs(needsfixture.Recipe.Base, needsfixture.Recipe{}.Web))
	if err := declerr.First(); err != nil {
		t.Fatal(err)
	}
	c, _ := findCandidate("y_user")
	if got, want := c.resolvedNeeds(), []string{"y_base", "y_web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("y_user needs = %v, want %v", got, want)
	}
	if _, err := RecordPlan("sugar", "", "y_user"); err != nil {
		t.Fatalf("RecordPlan(y_user): %v", err)
	}
	for p, want := range map[string]string{
		"gopkg.in/yaml.v3":      "gopkg.in/yaml%2ev3",
		"example.com/a.b/c":     "example.com/a.b/c",
		"example.com/x%y/\"q\"": "example.com/x%25y/%22q%22",
		"main":                  "main",
	} {
		if got := runtimePkgPath(p); got != want {
			t.Errorf("runtimePkgPath(%q) = %q, want %q", p, got, want)
		}
	}
}

// NeedsPackageFunc is an exported package function, not a method.
func NeedsPackageFunc() {}

// needsHelper has an unexported method, which RegisterMethods never
// registers as a task.
type needsHelper struct{}

func (needsHelper) helper() {}

// TestNeedsNonMethodFuncIsDeclError (task eb, negative): a func that does
// not name an exported method of a struct is refused at declaration, with
// no generic-method hint outside a generic method.
func TestNeedsNonMethodFuncIsDeclError(t *testing.T) {
	for _, c := range []struct {
		label string
		need  any
	}{
		{"NeedsPackageFunc", NeedsPackageFunc},
		{"needsHelper.helper", needsHelper.helper},
		{"TestNeedsNonMethodFuncIsDeclError.func1", func() {}},
	} {
		requireDeclErr(t, "Needs: "+c.label+" does not name an exported method of a struct", func() {
			Task("x", "", func() {}, Needs(c.need))
		})
		if err := declerr.First(); strings.Contains(err.Error(), genericClosureHint) {
			t.Fatalf("Needs(%s): unexpected generic-method hint: %v", c.label, err)
		}
	}
}

// needsInner's Base is promoted into needsOuter.
type needsInner struct{}

func (needsInner) Base() {}

type needsOuter struct{ needsInner }

func (needsOuter) Web() {}

// TestNeedsPromotedMethod (task eb) pins the documented embedding rule: a
// method expression of a promoted method names the outer struct's task,
// while its method value names the embedded type's method.
func TestNeedsPromotedMethod(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(needsOuter{}, WithPrefix("o_"))
	Task("expr", "", func() {}, Needs(needsOuter.Base))
	Task("value", "", func() {}, Needs(needsOuter{}.Base))
	c, _ := findCandidate("expr")
	if got, want := c.resolvedNeeds(), []string{"o_base"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expr needs = %v, want %v", got, want)
	}
	if _, err := RecordPlan("sugar", "", "value"); err == nil ||
		!strings.Contains(err.Error(), `needs unknown task "needsInner.Base"`) {
		t.Fatalf("promoted method value: err = %v", err)
	}
}

func TestRegisterMethodsTakesTaskOptions(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(otherRecipe{}, WithPrefix("a_"), WhenProfile("fedora"))
	RegisterMethods(otherRecipe{}, WithPrefix("b_"), WithGroupWhen(WhenProfile("fedora")))
	if a, b := recordWire(t, "a_base"), recordWire(t, "b_base"); a != b {
		t.Fatalf("TaskOption and WithGroupWhen record differently:\n%s\n%s", a, b)
	}
}

type WireGuard struct{}

func (WireGuard) Keys() {}

func TestRegisterOnClusterAndPrefixWords(t *testing.T) {
	setupForHostsInventory(t)
	RegisterOnCluster("all", WireGuard{}, otherRecipe{}, Operational())
	if err := declerr.First(); err != nil {
		t.Fatal(err)
	}
	want := []plan.Predicate{{Fact: "hostname_contains", In: []string{"h1", "h1-wg", "h2", "h3"}}}
	for _, name := range []string{"api_wireguard_keys", "api_other_recipe_base"} {
		c, ok := findCandidate(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if !c.operational || !reflect.DeepEqual(c.planWhen, want) || c.cluster != "all" {
			t.Fatalf("%s: operational=%v planWhen=%v cluster=%q", name, c.operational, c.planWhen, c.cluster)
		}
	}
	requireDeclErr(t, "RegisterOnCluster(\"all\"): WithPrefix is not allowed", func() {
		RegisterOnCluster("all", otherRecipe{}, WithPrefix("x_"))
	})
}

func TestAggregatePrefix(t *testing.T) {
	resetForHostsState(t)
	RegisterMethods(otherRecipe{}, WithPrefix("grp_"))
	AggregatePrefix("grp")
	AggregatePrefix("grp2", "Custom text")
	c, _ := findCandidate("grp")
	if c.description != "Run all grp_* tasks" {
		t.Fatalf("description = %q", c.description)
	}
	c, _ = findCandidate("grp2")
	if c.description != "Custom text" {
		t.Fatalf("description = %q", c.description)
	}
	ops, err := RecordPlan("sugar", "", "grp")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("aggregate recorded nothing")
	}
}

func TestWhenHostnameInAndHostnameMatch(t *testing.T) {
	resetForHostsState(t)
	f0 := Host("f0", WithHostnameMatch("f0.lan.example"), WithData(upsClient{Server: "f0"}))
	f1 := Host("f1", WithData(upsServer{}))
	Cluster("pair", f0, f1)
	Task("guarded", "", func() {}, WhenHostnameIn("f0", "f1"))
	Task("one", "", func() {}, WhenHostnameIn("f0"))
	var seen []string
	Task("each", "", func() {
		EachHostWith(func(c upsClient) { seen = append(seen, c.Server) })
	}, WithTaskCluster("pair"))
	RegisterMethods(otherRecipe{}, WithPrefix("p_"), OnCluster("pair"))

	c, _ := findCandidate("guarded")
	if want := []plan.Predicate{{Fact: "hostname_contains", In: []string{"f0", "f1"}}}; !reflect.DeepEqual(c.planWhen, want) {
		t.Fatalf("WhenHostnameIn(f0, f1) = %v", c.planWhen)
	}
	c, _ = findCandidate("one")
	if want := []plan.Predicate{{Fact: "hostname_contains", Eq: "f0"}}; !reflect.DeepEqual(c.planWhen, want) {
		t.Fatalf("WhenHostnameIn(f0) = %v", c.planWhen)
	}
	c, _ = findCandidate("p_base")
	if want := []plan.Predicate{{Fact: "hostname_contains", In: []string{"f0.lan.example", "f1"}}}; !reflect.DeepEqual(c.planWhen, want) {
		t.Fatalf("OnCluster guard with WithHostnameMatch = %v", c.planWhen)
	}
	ops, err := RecordPlan("sugar", "", "each")
	if err != nil {
		t.Fatal(err)
	}
	if got := whenHosts(ops); !reflect.DeepEqual(got, []string{"f0.lan.example"}) {
		t.Fatalf("EachHostWith fragments = %v, want only f0's", got)
	}
	if !reflect.DeepEqual(seen, []string{"f0"}) {
		t.Fatalf("EachHostWith visited %v", seen)
	}

	requireDeclErr(t, "WithHostnameMatch: fragment must not be empty", func() { Host("x", WithHostnameMatch(" ")) })
	requireDeclErr(t, "WhenHostnameIn: no hosts", func() { Task("x", "", func() {}, WhenHostnameIn()) })
}

type upsClient struct{ Server string }
type upsServer struct{}
