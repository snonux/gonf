package api

import (
	"errors"
	"reflect"
	"strings"
	"testing"

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
