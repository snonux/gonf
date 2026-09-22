package inventory

import (
	"errors"
	"strings"
	"testing"
)

// mustAddHost registers name with opts, failing the test when the
// registration is refused.
func mustAddHost(t *testing.T, name string, opts ...HostOption) {
	t.Helper()
	if _, err := AddHost(name, opts...); err != nil {
		t.Fatal(err)
	}
}

// TestRegistrationMisuseReturnsErrors pins that the registry returns every
// registration-time misuse as an error (api reports it as a declaration
// error) instead of ending the process, and that a refused record is not
// stored.
func TestRegistrationMisuseReturnsErrors(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	mustAddHost(t, "h")
	if _, err := AddCluster("c", []string{"h"}); err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("WithValue: key must not be empty")
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"empty host", second(AddHost("")), "Host: name must not be empty"},
		{"duplicate host", second(AddHost("h")), `Host "h" already registered`},
		{"rejected option", second(AddHost("x", func(h *Host) { h.RejectOption(rejected) })), rejected.Error()},
		{"value on unknown host", SetHostValue("nope", "k", 1), `SetValue: Host "nope" is not registered`},
		{"empty value key", SetHostValue("h", "", 1), "SetValue: key must not be empty"},
		{"empty cluster", second(AddCluster("c2", nil)), `Cluster "c2": must include at least one Host`},
		{"duplicate cluster", second(AddCluster("c", []string{"h"})), `Cluster "c" already registered`},
		{"cluster of unknown host", second(AddCluster("c3", []string{"ghost"})), `Cluster "c3": Host "ghost" is not registered`},
		{"parallel of unknown cluster", SetClusterParallel("nope", 2), `Cluster "nope" is not registered`},
		{"empty fleet", second(AddFleet("f", nil)), `Fleet "f": must include at least one Cluster`},
		{"fleet of unknown cluster", second(AddFleet("f", []string{"nope"})), `Fleet "f": Cluster "nope" is not registered`},
	}
	for _, tc := range cases {
		if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, tc.err, tc.want)
		}
	}
	for _, name := range []string{"", "x"} {
		if _, ok := LookupHost(name); ok {
			t.Errorf("refused host %q was stored", name)
		}
	}
	if _, ok := LookupCluster("c3"); ok {
		t.Error("refused cluster c3 was stored")
	}
}

// second returns the error of a (record, error) registration result.
func second[T any](_ T, err error) error { return err }
