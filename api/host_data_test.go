package api

import (
	"reflect"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/inventory"
)

// testWindow is the typed per-host datum of the WithData tests.
type testWindow struct{ From, To string }

// testLabel is a second datum type, to show types do not collide.
type testLabel string

// lookupRec returns the stored inventory record of host name.
func lookupRec(t *testing.T, name string) inventory.Host {
	t.Helper()
	rec, ok := inventory.LookupHost(name)
	if !ok {
		t.Fatalf("host %q not registered", name)
	}
	return rec
}

// TestHostDefaultsOverrideOrder: options apply in order with bundles
// expanded in place, so later options win over a bundle's fields, values
// and data, and a later bundle wins over an earlier explicit field.
func TestHostDefaultsOverrideOrder(t *testing.T) {
	resetForHostsState(t)
	base := HostDefaults(WithSSHUser("rex"), WithSSHPort(2222),
		WithValue("k", 1), WithData(testWindow{"1", "2"}))

	Host("a", base, WithSSHUser("paul"), WithValue("k", 2), WithData(testWindow{"3", "4"}))
	Host("b", WithSSHUser("paul"), base)
	Host("c", HostDefaults(base, WithValue("k", 3)))

	a := lookupRec(t, "a")
	if a.User != "paul" || a.Port != 2222 || a.Values["k"] != 2 {
		t.Fatalf("a = user %q port %d k %v, want paul 2222 2", a.User, a.Port, a.Values["k"])
	}
	if got := HostData[testWindow]("a"); got != (testWindow{"3", "4"}) {
		t.Fatalf("a data = %v", got)
	}
	if b := lookupRec(t, "b"); b.User != "rex" {
		t.Fatalf("b user = %q, want the later bundle's rex", b.User)
	}
	if c := lookupRec(t, "c"); c.Values["k"] != 3 {
		t.Fatalf("nested bundle: k = %v, want 3", c.Values["k"])
	}
}

// TestHostDefaultsRefusals: a key or type set twice outside a bundle, or a
// bundle after an explicit key, is still a declaration error, and a bad
// option inside a bundle refuses the host.
func TestHostDefaultsRefusals(t *testing.T) {
	requireDeclErr(t, `WithValue: key "k" already set`, func() {
		Host("a", WithValue("k", 1), HostDefaults(WithValue("k", 2)))
	})
	if _, ok := inventory.LookupHost("a"); ok {
		t.Fatal("a refused host must not be registered")
	}
	requireDeclErr(t, `WithData: a api.testWindow value is already set`, func() {
		Host("a", WithData(testWindow{}), WithData(testWindow{}))
	})
	requireDeclErr(t, `WithData: value must not be nil`, func() {
		Host("a", HostDefaults(WithData(nil)))
	})
	// Once registered, an overridden default is final: WithValue's
	// duplicate check is back to normal for SetValue.
	requireDeclErr(t, `value key "k" already set`, func() {
		Host("a", HostDefaults(WithValue("k", 1))).SetValue("k", 2)
	})
}

// registerEachHostTask registers task name on cluster "all" whose body
// visits every host's testWindow with EachHostNamed.
func registerEachHostTask(name string) *[]string {
	visited := &[]string{}
	Task(name, "", func() {
		EachHostNamed(func(host string, w testWindow) {
			*visited = append(*visited, host+"="+w.From+w.To)
			File("/tmp/for-hosts-"+host, options.WithContent(w.From+" "+w.To+"\n"))
		})
	}, WithTaskCluster("all"))
	return visited
}

// TestEachHostMatchesForHosts: typed data records exactly the ops ForHosts
// records for the same per-host values, visiting members in order.
func TestEachHostMatchesForHosts(t *testing.T) {
	resetForHostsState(t)
	var hosts []HostRef
	for i, n := range []string{"h1", "h2", "h3"} {
		w := testWindow{string(rune('1' + i)), "x"}
		hosts = append(hosts, Host(n, WithValue(forHostsKey, [2]string{w.From, w.To}), WithData(w),
			WithData(testLabel(n))))
	}
	Cluster("all", hosts...)
	visited := registerEachHostTask("typed")
	registerForHostsTask("keyed", "all")
	var labels []testLabel
	Task("labels", "", func() {
		EachHost(func(l testLabel) { labels = append(labels, l) })
	}, WithTaskCluster("all"))

	if got, want := recordOps(t, "typed"), recordOps(t, "keyed"); !reflect.DeepEqual(got[1:], want[1:]) {
		t.Fatalf("EachHostNamed ops differ from ForHosts:\n got %#v\nwant %#v", got, want)
	}
	if want := []string{"h1=1x", "h2=2x", "h3=3x"}; !reflect.DeepEqual(*visited, want) {
		t.Fatalf("visited = %v, want %v", *visited, want)
	}
	recordOps(t, "labels")
	if want := []testLabel{"h1", "h2", "h3"}; !reflect.DeepEqual(labels, want) {
		t.Fatalf("EachHost labels = %v, want %v", labels, want)
	}
}

// TestEachHostRefusals mirrors ForHosts: a member without a T value, an
// interface T, a nil fn or a missing cluster records nothing and fails the
// record; HostData reports a missing value as a declaration error.
func TestEachHostRefusals(t *testing.T) {
	resetForHostsState(t)
	Cluster("all", Host("h1", WithData(testWindow{})), Host("h2"))
	Task("missing", "", func() { EachHost(func(testWindow) {}) }, WithTaskCluster("all"))
	Task("iface", "", func() { EachHost(func(any) {}) }, WithTaskCluster("all"))
	Task("nilfn", "", func() { EachHost[testWindow](nil) }, WithTaskCluster("all"))
	Task("nocluster", "", func() { EachHostNamed(func(string, testWindow) {}) })

	wantErrContains(t, recordErr(t, "missing"), `EachHost: Host "h2": no api.testWindow value (WithData)`)
	wantErrContains(t, recordErr(t, "iface"), "EachHost: type parameter interface {} is an interface")
	wantErrContains(t, recordErr(t, "nilfn"), "EachHost: fn must not be nil")
	wantErrContains(t, recordErr(t, "nocluster"), "EachHostNamed: no cluster on the current task")

	requireDeclErr(t, `HostData: Host "h2": no api.testWindow value (WithData)`, func() {
		Host("h2")
		HostData[testWindow]("h2")
	})
}
