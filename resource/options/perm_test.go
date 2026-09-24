package options

import (
	"os"
	"os/user"
	"reflect"
	"runtime"
	"testing"
)

// TestOwnerSpecSetters pins which setters each owner spec reaches, for Perm
// and for WithOwner (which parses the same spec once it holds a colon): the
// group is only set when the spec names one, the owner only when it names
// one, and Root is root plus the numeric root gid. A misuse would show up
// as an unexpected ReportMisuse call.
func TestOwnerSpecSetters(t *testing.T) {
	cases := []struct {
		spec      string
		wantOwner []setterCall
	}{
		{"svc", []setterCall{{"SetOwner", "svc"}}},
		{"svc:staff", []setterCall{{"SetOwner", "svc"}, {"SetGroup", "staff"}}},
		{":staff", []setterCall{{"SetGroup", "staff"}}},
		{":0", []setterCall{{"SetGroup", "0"}}},
		{Root, []setterCall{{"SetOwner", "root"}, {"SetGroup", "0"}}},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			perm := &recorder{}
			Perm(0o640, tc.spec).Apply(perm)
			want := append([]setterCall{{"SetMode", os.FileMode(0o640)}}, tc.wantOwner...)
			if !reflect.DeepEqual(perm.calls, want) {
				t.Errorf("Perm(0o640, %q) calls = %v, want %v", tc.spec, perm.calls, want)
			}

			owner := &recorder{}
			WithOwner(tc.spec).Apply(owner)
			if !reflect.DeepEqual(owner.calls, tc.wantOwner) {
				t.Errorf("WithOwner(%q) calls = %v, want %v", tc.spec, owner.calls, tc.wantOwner)
			}
		})
	}
}

// TestPermSetsNothingOnMisuse pins that a misused Perm leaves no half-applied
// mode or ownership behind: a bad owner stops the valid mode and a bad mode
// stops the valid owner.
func TestPermSetsNothingOnMisuse(t *testing.T) {
	for name, o := range map[string]fileDirOption{
		"bad owner": Perm(0o644, "a:b:c"),
		"bad mode":  Perm(os.ModeDir|0o755, Root),
	} {
		r := &recorder{}
		o.Apply(r)
		if len(r.calls) != 1 || r.calls[0].method != "ReportMisuse" {
			t.Errorf("%s: calls = %v, want exactly one ReportMisuse", name, r.calls)
		}
	}
}

// rootGroupByGOOS is the name each supported destination gives gid 0, the
// group Root records. Root depends on nothing but the gid, so this table is
// the whole per-OS resolution: the kernel, not a name lookup, maps the
// recorded "0" to root:root on Linux and root:wheel on the BSDs and darwin.
var rootGroupByGOOS = map[string]string{
	"linux":   "root",
	"openbsd": "wheel",
	"freebsd": "wheel",
	"netbsd":  "wheel",
	"darwin":  "wheel",
}

// TestRootGroupIsGIDZeroOnThisHost checks the table against the host's group
// database: gid 0 must carry the root-group name the table promises for this
// GOOS. The other GOOS rows are checked when the suite runs on those hosts.
func TestRootGroupIsGIDZeroOnThisHost(t *testing.T) {
	want, ok := rootGroupByGOOS[runtime.GOOS]
	if !ok {
		t.Skipf("GOOS %s is not a supported destination", runtime.GOOS)
	}
	if rootGID != "0" {
		t.Fatalf("Root records gid %q, want 0", rootGID)
	}
	g, err := user.LookupGroupId(rootGID)
	if err != nil {
		t.Skipf("no group database entry for gid 0 here: %v", err)
	}
	if g.Name != want {
		t.Errorf("gid 0 on %s is %q, want %q", runtime.GOOS, g.Name, want)
	}
}
