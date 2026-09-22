package options

import (
	"os"
	"reflect"
	"testing"

	"github.com/snonux/gonf/resource"
)

// fakeTarget implements the Dependable capability so the DependsOn option can
// be tested in isolation from the concrete resource packages.
type fakeTarget struct {
	deps []string
}

func (f *fakeTarget) AddDependency(id string) { f.deps = append(f.deps, id) }

// capTarget implements every capability interface in this package so each
// option can be applied to it and its setter invocation asserted. Options
// must stay resource-agnostic: this fake stands in for the concrete resource
// packages in tests.
type capTarget struct {
	calls []capCall
}

// capCall records one setter invocation: the method name and the value it
// received (nil for value-less setters).
type capCall struct {
	method string
	value  any
}

func (c *capTarget) record(method string, value any) {
	c.calls = append(c.calls, capCall{method: method, value: value})
}

func (c *capTarget) SetOwner(v string)      { c.record("SetOwner", v) }
func (c *capTarget) SetGroup(v string)      { c.record("SetGroup", v) }
func (c *capTarget) SetMode(v os.FileMode)  { c.record("SetMode", v) }
func (c *capTarget) SetSource(v string)     { c.record("SetSource", v) }
func (c *capTarget) SetSourceGlob(v string) { c.record("SetSourceGlob", v) }
func (c *capTarget) SetSourceBase(v string) { c.record("SetSourceBase", v) }
func (c *capTarget) SetParam(v string)      { c.record("SetParam", v) }
func (c *capTarget) SetTemplate()           { c.record("SetTemplate", nil) }
func (c *capTarget) SetTemplateData(v any)  { c.record("SetTemplateData", v) }
func (c *capTarget) SetValidation(bin string, args []string) {
	c.record("SetValidation", []any{bin, args})
}
func (c *capTarget) SetContent(v string)        { c.record("SetContent", v) }
func (c *capTarget) SetAddLine(v string)        { c.record("SetAddLine", v) }
func (c *capTarget) SetRemoveLine(v string)     { c.record("SetRemoveLine", v) }
func (c *capTarget) AddLines(v ...string)       { c.record("AddLines", v) }
func (c *capTarget) RemoveLines(v ...string)    { c.record("RemoveLines", v) }
func (c *capTarget) SetFileMode(v os.FileMode)  { c.record("SetFileMode", v) }
func (c *capTarget) SetPrune()                  { c.record("SetPrune", nil) }
func (c *capTarget) SetAbsent()                 { c.record("SetAbsent", nil) }
func (c *capTarget) SetLatest()                 { c.record("SetLatest", nil) }
func (c *capTarget) SetRestart()                { c.record("SetRestart", nil) }
func (c *capTarget) SetReload()                 { c.record("SetReload", nil) }
func (c *capTarget) SetUser()                   { c.record("SetUser", nil) }
func (c *capTarget) SetElevate()                { c.record("SetElevate", nil) }
func (c *capTarget) SetEnableOnly()             { c.record("SetEnableOnly", nil) }
func (c *capTarget) SetChangeWatch(v []string)  { c.record("SetChangeWatch", v) }
func (c *capTarget) AddDependency(v string)     { c.record("AddDependency", v) }
func (c *capTarget) SetCronUser(v string)       { c.record("SetCronUser", v) }
func (c *capTarget) SetLegacyCommand(v string)  { c.record("SetLegacyCommand", v) }
func (c *capTarget) SetCommand(v string)        { c.record("SetCommand", v) }
func (c *capTarget) SetMinute(v string)         { c.record("SetMinute", v) }
func (c *capTarget) SetHour(v string)           { c.record("SetHour", v) }
func (c *capTarget) SetMonthday(v string)       { c.record("SetMonthday", v) }
func (c *capTarget) SetMonth(v string)          { c.record("SetMonth", v) }
func (c *capTarget) SetWeekday(v string)        { c.record("SetWeekday", v) }
func (c *capTarget) AddCronEnv(v string)        { c.record("AddCronEnv", v) }
func (c *capTarget) SetSymlink(v string)        { c.record("SetSymlink", v) }
func (c *capTarget) SetHardlink(v string)       { c.record("SetHardlink", v) }
func (c *capTarget) SetName(v string)           { c.record("SetName", v) }
func (c *capTarget) SetDir(v string)            { c.record("SetDir", v) }
func (c *capTarget) SetEnv(v map[string]string) { c.record("SetEnv", v) }
func (c *capTarget) SetCreates(v string)        { c.record("SetCreates", v) }
func (c *capTarget) SetUnless(v *Guard)         { c.record("SetUnless", v) }
func (c *capTarget) SetOnlyIf(v *Guard)         { c.record("SetOnlyIf", v) }
func (c *capTarget) SetHome(v string)           { c.record("SetHome", v) }
func (c *capTarget) SetCreateHome()             { c.record("SetCreateHome", nil) }
func (c *capTarget) SetShell(v string)          { c.record("SetShell", v) }
func (c *capTarget) SetLoginClass(v string)     { c.record("SetLoginClass", v) }
func (c *capTarget) SetSystem()                 { c.record("SetSystem", nil) }
func (c *capTarget) AddSupplementaryGroups(v ...string) {
	c.record("AddSupplementaryGroups", v)
}

func TestDependsOnSingle(t *testing.T) {
	target := &fakeTarget{}

	dep := resource.Resource{Type: "File", Name: "a"} // ID: File[a]
	DependsOn(dep)(target)

	want := []string{"File[a]"}
	if !reflect.DeepEqual(target.deps, want) {
		t.Errorf("deps = %v, want %v", target.deps, want)
	}
}

// TestDependsOnMultiExpands ensures a Multi dependency is expanded so each of
// its members is recorded individually.
func TestDependsOnMultiExpands(t *testing.T) {
	target := &fakeTarget{}

	multi := resource.Multi{
		resource.Resource{Type: "File", Name: "a"},
		resource.Resource{Type: "File", Name: "b"},
	}
	DependsOn(multi)(target)

	want := []string{"File[a]", "File[b]"}
	if !reflect.DeepEqual(target.deps, want) {
		t.Errorf("deps = %v, want %v", target.deps, want)
	}
}

// TestDependsOnMixed ensures multiple arguments (single + multi) are all
// recorded, preserving order.
func TestDependsOnMixed(t *testing.T) {
	target := &fakeTarget{}

	single := resource.Resource{Type: "File", Name: "a"}
	multi := resource.Multi{
		resource.Resource{Type: "File", Name: "b"},
		resource.Resource{Type: "File", Name: "c"},
	}
	DependsOn(single, multi)(target)

	want := []string{"File[a]", "File[b]", "File[c]"}
	if !reflect.DeepEqual(target.deps, want) {
		t.Errorf("deps = %v, want %v", target.deps, want)
	}
}

func TestOnChangeArmsGateAndAddsDependencyEdges(t *testing.T) {
	target := &capTarget{}
	OnChange(
		resource.Resource{Type: "File", Name: "a"},
		resource.Multi{
			resource.Resource{Type: "File", Name: "b"},
			resource.Resource{Type: "File", Name: "c"},
		},
	).Apply(target)

	want := []capCall{
		{method: "SetChangeWatch", value: []string{"File[a]", "File[b]", "File[c]"}},
		{method: "AddDependency", value: "File[a]"},
		{method: "AddDependency", value: "File[b]"},
		{method: "AddDependency", value: "File[c]"},
	}
	if !reflect.DeepEqual(target.calls, want) {
		t.Fatalf("calls = %#v, want %#v", target.calls, want)
	}
}

// TestOptionsReachTheirSetters applies every option to a target implementing
// all capability interfaces and asserts the option forwarded its value to the
// matching setter exactly once. A missing capability would abort via
// logger.Fatal inside requires, failing the test loudly.
func TestOptionsReachTheirSetters(t *testing.T) {
	tests := []struct {
		name   string
		opt    Option
		method string
		want   any
	}{
		{"WithOwner", WithOwner("paul"), "SetOwner", "paul"},
		{"WithGroup", WithGroup("wheel"), "SetGroup", "wheel"},
		{"WithUserGroup", WithUserGroup("wheel"), "AddSupplementaryGroups", []string{"wheel"}},
		{"WithPrimaryGroup", WithPrimaryGroup("svc"), "SetGroup", "svc"},
		{"WithSupplementaryGroups", WithSupplementaryGroups("audio", "wheel"), "AddSupplementaryGroups", []string{"audio", "wheel"}},
		{"WithLoginClass", WithLoginClass("daemon"), "SetLoginClass", "daemon"},
		{"WithHome", WithHome("/var/lib/svc"), "SetHome", "/var/lib/svc"},
		{"WithCreateHome", WithCreateHome, "SetCreateHome", nil},
		{"WithShell", WithShell("/sbin/nologin"), "SetShell", "/sbin/nologin"},
		{"WithSystem", WithSystem, "SetSystem", nil},
		{"WithMode", WithMode(0o644), "SetMode", os.FileMode(0o644)},
		{"WithSource", WithSource("/srv/src"), "SetSource", "/srv/src"},
		{"WithSourceGlob", WithSourceGlob("*.conf"), "SetSourceGlob", "*.conf"},
		{"WithSourceBase", WithSourceBase("assets/testfiles"), "SetSourceBase", "assets/testfiles"},
		{"WithParam", WithParam("stable"), "SetParam", "stable"},
		{"WithTemplate", WithTemplate, "SetTemplate", nil},
		{"WithTemplateData", WithTemplateData(map[string]any{"name": "relay"}), "SetTemplateData", map[string]any{"name": "relay"}},
		{"WithValidation", WithValidation("validator", []string{CandidatePath}), "SetValidation", []any{"validator", []string{CandidatePath}}},
		{"WithContent", WithContent("hello"), "SetContent", "hello"},
		{"WithLines", WithLines("one", "two"), "AddLines", []string{"one", "two"}},
		{"WithoutLines", WithoutLines("old", "stale"), "RemoveLines", []string{"old", "stale"}},
		{"WithLine", WithLine("line"), "SetAddLine", "line"},
		{"WithoutLine", WithoutLine("gone"), "SetRemoveLine", "gone"},
		{"WithFileMode", WithFileMode(0o600), "SetFileMode", os.FileMode(0o600)},
		{"WithPrune", WithPrune, "SetPrune", nil},
		{"IsAbsent", IsAbsent, "SetAbsent", nil},
		{"IsLatest", IsLatest, "SetLatest", nil},
		{"WithRestart", WithRestart, "SetRestart", nil},
		{"WithReload", WithReload, "SetReload", nil},
		{"WithUser", WithUser, "SetUser", nil},
		{"WithElevate", WithElevate, "SetElevate", nil},
		{"WithEnableOnly", WithEnableOnly, "SetEnableOnly", nil},
		{"IfChanged", IfChanged, "SetChangeWatch", []string(nil)},
		{"WithWatch", WithWatch("a", "b"), "SetChangeWatch", []string{"a", "b"}},
		{"WithCronUser", WithCronUser("root"), "SetCronUser", "root"},
		{"WithLegacyCommand", WithLegacyCommand("/usr/local/bin/old"), "SetLegacyCommand", "/usr/local/bin/old"},
		{"WithCommand", WithCommand("true"), "SetCommand", "true"},
		{"WithMinute", WithMinute("5"), "SetMinute", "5"},
		{"WithHour", WithHour("6"), "SetHour", "6"},
		{"WithMonthday", WithMonthday("7"), "SetMonthday", "7"},
		{"WithMonth", WithMonth("8"), "SetMonth", "8"},
		{"WithWeekday", WithWeekday("9"), "SetWeekday", "9"},
		{"WithCronEnv", WithCronEnv("K=V"), "AddCronEnv", "K=V"},
		{"WithSymlink", WithSymlink("/target"), "SetSymlink", "/target"},
		{"WithHardlink", WithHardlink("/target"), "SetHardlink", "/target"},
		{"WithName", WithName("renamed"), "SetName", "renamed"},
		{"WithDir", WithDir("/work"), "SetDir", "/work"},
		{"WithEnv", WithEnv(map[string]string{"A": "B"}), "SetEnv", map[string]string{"A": "B"}},
		{"Creates", Creates("/marker"), "SetCreates", "/marker"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &capTarget{}
			tt.opt(f)

			if len(f.calls) != 1 {
				t.Fatalf("option made %d setter calls, want 1: %v", len(f.calls), f.calls)
			}
			got := f.calls[0]
			if got.method != tt.method {
				t.Errorf("called %q, want %q", got.method, tt.method)
			}
			if !reflect.DeepEqual(got.value, tt.want) {
				t.Errorf("value = %v, want %v", got.value, tt.want)
			}
		})
	}
}

// TestGuardOptionsBuildAndForwardGuards pins the guard construction behind
// Unless/OnlyIf, including the GuardOption modifiers and the ExpectExit=0
// default when no options are given.
func TestGuardOptionsBuildAndForwardGuards(t *testing.T) {
	tests := []struct {
		name   string
		opt    Option
		method string
		want   *Guard
	}{
		{
			name:   "Unless with default expectations",
			opt:    Unless("cmd", []string{"-x"}),
			method: "SetUnless",
			want:   &Guard{Name: "cmd", Args: []string{"-x"}, ExpectExit: 0},
		},
		{
			name:   "OnlyIf with exit and stdout expectations",
			opt:    OnlyIf("check", nil, ExpectExit(3), ExpectStdout("ready")),
			method: "SetOnlyIf",
			want:   &Guard{Name: "check", ExpectExit: 3, ExpectStdout: "ready"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &capTarget{}
			tt.opt(f)

			if len(f.calls) != 1 {
				t.Fatalf("option made %d setter calls, want 1: %v", len(f.calls), f.calls)
			}
			got := f.calls[0]
			if got.method != tt.method {
				t.Errorf("method = %q, want %q", got.method, tt.method)
			}
			guard, ok := got.value.(*Guard)
			if !ok {
				t.Fatalf("value = %T, want *Guard", got.value)
			}
			if !reflect.DeepEqual(guard, tt.want) {
				t.Errorf("guard = %+v, want %+v", guard, tt.want)
			}
		})
	}
}

// TestGuardOptionModifiers covers the GuardOption helpers directly: the
// default guard succeeds on exit 0, and both modifiers set their field.
func TestGuardOptionModifiers(t *testing.T) {
	g := &Guard{Name: "n", ExpectExit: 0}
	if g.ExpectExit != 0 {
		t.Errorf("default ExpectExit = %d, want 0", g.ExpectExit)
	}

	ExpectExit(7)(g)
	ExpectStdout("out")(g)
	if g.ExpectExit != 7 || g.ExpectStdout != "out" {
		t.Errorf("guard = %+v, want ExpectExit 7 and ExpectStdout out", g)
	}
}
