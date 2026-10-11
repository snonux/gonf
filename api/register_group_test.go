package api

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
)

type lodgeGroup struct{}

func (lodgeGroup) Build()                 { File("/tmp/gonfy-group/build", WithContent("lodge")) }
func (lodgeGroup) OptsBuild() TaskOptions { return TaskOptions{Needs(otherRecipe.Base)} }

func TestRegisterGroupMatchesSeparateCalls(t *testing.T) {
	setupForHostsInventory(t)
	opts := []RegisterOption{WithPrefix("lodge_"), OnCluster("all"), WhenLinux()}
	RegisterGroup([]any{lodgeGroup{}, otherRecipe{}}, opts...)
	a := recordWire(t, "lodge_build")
	c, ok := findCandidate("lodge_build")
	if !ok || c.cluster != "all" || !reflect.DeepEqual(c.resolvedNeeds(), []string{"lodge_base"}) {
		t.Fatalf("group candidate = %+v", c)
	}
	found := false
	for _, p := range c.planWhen {
		if p.Fact == "hostname_contains" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing cluster guard: %v", c.planWhen)
	}
	setupForHostsInventory(t)
	RegisterMethods(lodgeGroup{}, opts...)
	RegisterMethods(otherRecipe{}, opts...)
	if b := recordWire(t, "lodge_build"); a != b {
		t.Fatalf("group and singles differ:\n%s\n%s", a, b)
	}
}

func TestRegisterGroupDefaultPrefixes(t *testing.T) {
	resetForHostsState(t)
	RegisterGroup(nil)
	RegisterGroup([]any{WireGuard{}, otherRecipe{}})
	for _, name := range []string{"api_wireguard_keys", "api_other_recipe_base"} {
		if _, ok := findCandidate(name); !ok {
			t.Fatalf("missing %s", name)
		}
	}
	if err := declerr.First(); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterGroupRefusalContinues(t *testing.T) {
	for _, bad := range []any{nil, (*lodgeGroup)(nil), 42} {
		t.Run(badName(bad), func(t *testing.T) {
			resetForHostsState(t)
			RegisterGroup([]any{bad, otherRecipe{}}, WithPrefix("lodge_"))
			if declerr.First() == nil {
				t.Fatal("invalid item accepted")
			}
			if _, ok := findCandidate("lodge_base"); !ok {
				t.Fatal("later valid item not registered")
			}
			if _, err := RecordPlan("group", "", "lodge_base"); err == nil {
				t.Fatal("record accepted declaration error")
			}
		})
	}
}

func badName(v any) string {
	if v == nil {
		return "nil"
	}
	return reflect.TypeOf(v).String()
}

func TestRegisterGroupInvalidOptions(t *testing.T) {
	for _, opts := range [][]RegisterOption{{nil}, {TaskOption(nil)}, {OnCluster("missing")}, {WithGroupWhen(nil)}} {
		t.Run("invalid", func(t *testing.T) {
			resetForHostsState(t)
			RegisterGroup([]any{WireGuard{}, otherRecipe{}}, opts...)
			if declerr.First() == nil {
				t.Fatal("invalid options accepted")
			}
			if len(Tasks()) != 0 {
				t.Fatal("invalid options registered tasks")
			}
		})
	}
}

func TestRegisterGroupCollision(t *testing.T) {
	resetForHostsState(t)
	RegisterGroup([]any{otherRecipe{}, otherRecipe{}}, WithPrefix("lodge_"))
	if _, err := RecordPlan("group", "", "lodge_base"); err == nil {
		t.Fatal("duplicate tasks accepted")
	}
}
