package inventory

import (
	"fmt"
	"reflect"
)

// hostDefaults is AddHost's option scratch state for HostDefaults bundles
// (api.HostDefaults): depth counts the bundles currently applying, and
// values/data record the Values keys and Data types a bundle set. Such an
// entry is a default: any later option may replace it. An entry set outside
// a bundle is final, so setting it twice stays an error, as before bundles
// existed.
type hostDefaults struct {
	depth  int
	values map[string]bool
	data   map[reflect.Type]bool
}

// scratch returns h's defaults state, creating it on first use.
func (h *Host) scratch() *hostDefaults {
	if h.defaults == nil {
		h.defaults = &hostDefaults{values: map[string]bool{}, data: map[reflect.Type]bool{}}
	}
	return h.defaults
}

// ApplyDefaults applies opts to h as one defaults bundle: every option runs
// (like AddHost), the first error is returned, and every value or datum set
// meanwhile stays overridable by later options.
func (h *Host) ApplyDefaults(opts []HostOption) error {
	s := h.scratch()
	s.depth++
	defer func() { s.depth-- }()
	var first error
	for _, o := range opts {
		if err := o(h); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// PutValue stores value under key during option application. A key already
// set is an error unless a defaults bundle set it (then it is replaced).
func (h *Host) PutValue(key string, value any) error {
	if key == "" {
		return fmt.Errorf("key must not be empty")
	}
	s := h.scratch()
	if _, exists := h.Values[key]; exists && !s.values[key] {
		return fmt.Errorf("key %q already set", key)
	}
	if h.Values == nil {
		h.Values = map[string]any{}
	}
	h.Values[key] = value
	s.values[key] = s.depth > 0
	return nil
}

// PutData stores v keyed by its concrete type during option application,
// with PutValue's override rule. A nil v has no type and is an error.
func (h *Host) PutData(v any) error {
	if v == nil {
		return fmt.Errorf("value must not be nil")
	}
	t := reflect.TypeOf(v)
	s := h.scratch()
	if _, exists := h.Data[t]; exists && !s.data[t] {
		return fmt.Errorf("a %s value is already set", t)
	}
	if h.Data == nil {
		h.Data = map[reflect.Type]any{}
	}
	h.Data[t] = v
	s.data[t] = s.depth > 0
	return nil
}

// HostData returns the WithData value of type t stored on host name, looked
// up atomically under the registry lock (see HostValue). hostFound is false
// for an unregistered host, found false when the host has no value of t.
func HostData(name string, t reflect.Type) (value any, hostFound, found bool) {
	mu.Lock()
	defer mu.Unlock()
	rec, hostFound := hosts[name]
	if !hostFound {
		return nil, false, false
	}
	value, found = rec.Data[t]
	return value, true, found
}
