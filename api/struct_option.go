package api

import (
	"fmt"
	"reflect"
)

// StructOption can be embedded in (or added as a field to) a struct that is
// registered with RegisterMethods to contribute DEFAULT TaskOptions for
// every task of that struct — the execution contract declared on the struct
// itself. The engine composes all embedded StructOption fields (in
// declaration order) plus the Opts() companion if present; a method's own
// OptsX companion adds to the combined struct-level set.
//
// gonf ships RequiresRoot as the ready-made marker for the common case.
// Custom markers: define any type implementing this interface and embed it.
// Markers must be embedded exported types so reflection can reach them.
type StructOption interface {
	StructTaskOptions() TaskOptions
}

var structOptionType = reflect.TypeOf((*StructOption)(nil)).Elem()

// RequiresRoot is an embeddable marker: a struct embedding it declares that
// every task registered from it needs root.
//
//	type Unattended struct {
//	    RequiresRoot
//	}
type RequiresRoot struct{}

// StructTaskOptions implements StructOption.
func (RequiresRoot) StructTaskOptions() TaskOptions { return TaskOptions{Privileged()} }

// collectStructOptions gathers the struct-level default TaskOptions of a
// registered struct: embedded StructOption markers first (declaration
// order), then the Opts() companion if the struct defines one. A method's
// own OptsX companion is appended to the whole combined struct-level set.
//
// Markers must be embedded (or added as) EXPORTED types: unexported
// embedded markers are skipped here and fall through to their promoted
// method on the outer type (single marker only; multiple same-depth
// markers are an ambiguous selector and are not in the method set).
//
// A struct-level companion with the wrong signature is returned as an error:
// RegisterMethods then registers no task of the struct, because silently
// ignoring the companion could drop Privileged() and lower every task's
// privileges.
func collectStructOptions(rv reflect.Value, rt reflect.Type) (TaskOptions, error) {
	opts, foundMarkerField := markerStructOptions(rv, rt)
	if o := rv.MethodByName("Opts"); o.IsValid() {
		companion, err := callTaskOptionsCompanion(o, "Opts must be func() TaskOptions (the struct-level default companion)")
		if err != nil {
			return nil, err
		}
		opts = append(opts, companion...)
	}
	// Direct StructTaskOptions methods (no marker field): reachable for
	// structs defining it directly or via unexported embedded markers with
	// a pointer receiver. Same signature contract as Opts(). The Opts()
	// companion composes last, so it is collected before this fallback.
	if !foundMarkerField {
		if m := rv.MethodByName("StructTaskOptions"); m.IsValid() {
			companion, err := callTaskOptionsCompanion(m, "StructTaskOptions must be func() TaskOptions")
			if err != nil {
				return nil, err
			}
			opts = append(opts, companion...)
		}
	}
	return opts, nil
}

// markerStructOptions collects the TaskOptions of the exported embedded
// StructOption marker fields, in declaration order, and reports whether the
// struct has any such field. Markers implement StructOption, so their
// StructTaskOptions signature is guaranteed by the compiler.
func markerStructOptions(rv reflect.Value, rt reflect.Type) (TaskOptions, bool) {
	var opts TaskOptions
	found := false
	st := rt
	if st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() != reflect.Struct {
		return nil, false
	}
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.IsExported() {
			continue
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if !ft.Implements(structOptionType) {
			continue
		}
		found = true
		// Exported embedded marker: the field value is accessible.
		if m := rv.Elem().Field(i).MethodByName("StructTaskOptions"); m.IsValid() {
			opts = append(opts, m.Call(nil)[0].Interface().([]TaskOption)...)
		}
	}
	return opts, found
}

// callTaskOptionsCompanion calls m, which must be func() TaskOptions, and
// returns its result; any other signature is registration-time misuse,
// returned as "RegisterMethods: <want>".
func callTaskOptionsCompanion(m reflect.Value, want string) (TaskOptions, error) {
	mt := m.Type()
	if mt.NumIn() != 0 || mt.NumOut() != 1 || mt.Out(0) != reflect.TypeOf(TaskOptions(nil)) {
		return nil, fmt.Errorf("RegisterMethods: %s", want)
	}
	return m.Call(nil)[0].Interface().(TaskOptions), nil
}
