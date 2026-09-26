package api

import (
	"fmt"
	"reflect"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/inventory"
)

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

// EachHostWith is EachHost for data only some members carry: a member with
// no WithData value of type T is skipped instead of being an error, so the
// type says which hosts a body applies to:
//
//	EachHostWith(func(c UPSClient) { ... }) // only hosts WithData(UPSClient{...})
//
// Everything else (member order, host selection, a missing cluster, a nil
// fn or an interface T being an error) is EachHost's. Keep EachHost where
// every member must have the value: there a missing one is a recipe bug.
func EachHostWith[T any](fn func(v T)) {
	var named func(string, T)
	if fn != nil {
		named = func(_ string, v T) { fn(v) }
	}
	visitClusterHosts("EachHostWith", dataTypeError[T](), named, lookupOptionalHostData[T])
}

// lookupOptionalHostData is lookupHostData with a missing value reported as
// errSkipHost (EachHostWith). An unregistered host is still an error.
func lookupOptionalHostData[T any](host string) (T, error) {
	var zero T
	if err := dataTypeError[T](); err != nil {
		return zero, err
	}
	raw, hostFound, found := inventory.HostData(host, reflect.TypeFor[T]())
	switch {
	case !hostFound:
		return zero, fmt.Errorf("Host %q is not registered", host)
	case !found:
		return zero, errSkipHost
	}
	return raw.(T), nil
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
