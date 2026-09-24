package api

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/inventory"
)

// HostDefaults bundles host options into one, so hosts sharing a setup pass
// a single value to Host:
//
//	freebsd := HostDefaults(WithSSHUser("paul"), WithPrivilege(PrivilegeDoas), WithData(Unattended{Hour: "3"}))
//	Host("f0", freebsd, WithSSHHost("f0.lan"))
//
// Options apply in order, bundles expanded in place, so a later option
// overrides an earlier one: a plain field (WithSSHHost, WithPrivilege, ...)
// is simply set again, and a WithValue key or WithData type that a bundle
// set is replaced. A key or type set twice outside any bundle stays a
// declaration error, as before; so a bundle placed after an explicit
// WithValue of the same key is refused rather than silently replacing it.
// Pass bundles first. Bundles nest. Every option runs; the first error
// refuses the host, like any other rejected HostOption.
func HostDefaults(opts ...HostOption) HostOption {
	bundle := slices.Clone(opts)
	return func(h *inventory.Host) error { return h.ApplyDefaults(bundle) }
}

// WithData stores v on the host keyed by its concrete type, the typed
// alternative to WithValue: no string key, and the reader names the type.
//
//	type Unattended struct{ Hour, Minute string }
//	Host("f0", WithData(Unattended{Hour: "3", Minute: "10"}))
//	EachHost(func(u Unattended) { /* u is this host's value */ })
//
// Store a struct type of your own rather than a string or int, so two
// recipes never collide on one type. A nil v, or a second value of the same
// type on one host (outside HostDefaults' override rule), is a declaration
// error and the host is not registered.
func WithData(v any) HostOption {
	return func(h *inventory.Host) error {
		if err := h.PutData(v); err != nil {
			return fmt.Errorf("WithData: %w", err)
		}
		return nil
	}
}

// EachHost is ForHosts keyed by type: for every host of the current task's
// cluster (WithCluster/OnCluster) it runs fn with that host's WithData value
// of type T, inside a WhenHostname(host, ...) fragment. It has exactly
// ForHosts' semantics otherwise (member order, host selection, the value of
// every member resolved and checked first):
//
//	EachHost(func(u Unattended) { ... })
//
// A member with no T value is an error, as a missing key is for ForHosts:
// the call records nothing and reports a declaration error. So is a nil fn,
// a missing cluster, or an interface T (values are stored by concrete type,
// so an interface type never matches).
func EachHost[T any](fn func(v T)) {
	var named func(string, T)
	if fn != nil {
		named = func(_ string, v T) { fn(v) }
	}
	visitClusterHosts("EachHost", dataTypeError[T](), named, lookupHostData[T])
}

// EachHostNamed is EachHost for the rare body that also needs the host's
// inventory name.
func EachHostNamed[T any](fn func(host string, v T)) {
	visitClusterHosts("EachHostNamed", dataTypeError[T](), fn, lookupHostData[T])
}

// HostData returns host's WithData value of type T, the typed MustHostValue:
// a missing host or value, or an interface T, is reported as a declaration
// error and the zero T is returned.
func HostData[T any](host string) T {
	v, err := lookupHostData[T](host)
	if err != nil {
		declerr.Reportf("HostData: %w", err)
	}
	return v
}

// dataTypeError refuses an interface T: WithData keys values by their
// concrete type, so an interface T could never find one.
func dataTypeError[T any]() error {
	if t := reflect.TypeFor[T](); t.Kind() == reflect.Interface {
		return fmt.Errorf("type parameter %s is an interface; use the concrete type passed to WithData", t)
	}
	return nil
}

// lookupHostData is the error-returning core of HostData and EachHost. The
// error names the host and the type, never the value.
func lookupHostData[T any](host string) (T, error) {
	var zero T
	if err := dataTypeError[T](); err != nil {
		return zero, err
	}
	t := reflect.TypeFor[T]()
	raw, hostFound, found := inventory.HostData(host, t)
	if !hostFound {
		return zero, fmt.Errorf("Host %q is not registered", host)
	}
	if !found {
		return zero, fmt.Errorf("Host %q: no %s value (WithData)", host, t)
	}
	return raw.(T), nil // stored under reflect.TypeOf(raw) == T
}
