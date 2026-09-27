package api

import (
	"errors"
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
// Markers may implement StructTaskOptions with a value or a pointer
// receiver, and must be embedded (or added as) EXPORTED types: unexported
// embedded markers are skipped here and fall through to their promoted
// method on the outer type (single marker only; multiple same-depth
// markers are an ambiguous selector and are not in the method set).
//
// A struct-level companion with the wrong signature, a nil marker,
// or a marker or companion returning a nil TaskOption, is returned as an
// error: RegisterMethods then registers no task of the struct, because
// silently ignoring the companion (or the nil option) could drop
// Privileged() and lower every task's privileges.
func collectStructOptions(rv reflect.Value, rt reflect.Type) (TaskOptions, error) {
	opts, foundMarkerField, err := markerStructOptions(rv, rt)
	if err != nil {
		return nil, err
	}
	if o := rv.MethodByName("Opts"); o.IsValid() {
		companion, err := callTaskOptionsCompanion(o, "Opts", "Opts must be func() TaskOptions (the struct-level default companion)")
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
			companion, err := callTaskOptionsCompanion(m, "StructTaskOptions", "StructTaskOptions must be func() TaskOptions")
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
// struct has any such field. A field is a marker when its type, or a
// pointer to it, implements StructOption (isMarkerField), so a marker with
// a pointer-receiver StructTaskOptions counts too: it is called through the
// field's address (rv is a pointer, so the field is addressable). A nil
// marker field (isNilMarker), or a nil TaskOption in a marker's result, is
// returned as an error.
func markerStructOptions(rv reflect.Value, rt reflect.Type) (TaskOptions, bool, error) {
	var opts TaskOptions
	found := false
	st := rt
	if st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() != reflect.Struct {
		return nil, false, nil
	}
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.IsExported() {
			continue
		}
		if !isMarkerField(f.Type) {
			continue
		}
		found = true
		// Exported marker field: the field value is accessible.
		fv := rv.Elem().Field(i)
		if isNilMarker(fv) {
			// Calling the marker through a nil pointer or interface would
			// panic, and skipping it would drop its options (e.g.
			// Privileged()).
			return nil, true, fmt.Errorf("marker %s is nil", f.Name)
		}
		if !fv.Type().Implements(structOptionType) {
			fv = fv.Addr() // pointer-receiver StructTaskOptions
		}
		markerOpts := fv.Interface().(StructOption).StructTaskOptions()
		if bad := nilOptionIndex(markerOpts); bad != 0 {
			return nil, true, fmt.Errorf("marker %s: StructTaskOptions returned a nil TaskOption (option %d)", f.Name, bad)
		}
		opts = append(opts, markerOpts...)
	}
	return opts, found, nil
}

// isMarkerField reports whether a field of type ft is a StructOption
// marker: ft implements it (a value-receiver marker, a pointer to any
// marker, or the StructOption interface itself), or *ft does (a value field
// of a pointer-receiver marker).
func isMarkerField(ft reflect.Type) bool {
	if ft.Implements(structOptionType) {
		return true
	}
	return ft.Kind() != reflect.Pointer && ft.Kind() != reflect.Interface &&
		reflect.PointerTo(ft).Implements(structOptionType)
}

// isNilMarker reports whether marker field fv is a nil pointer, a nil
// interface (an embedded StructOption left unset), or an interface holding
// a nil pointer (StructOption((*RequiresRoot)(nil))).
func isNilMarker(fv reflect.Value) bool {
	switch fv.Kind() {
	case reflect.Pointer:
		return fv.IsNil()
	case reflect.Interface:
		if fv.IsNil() {
			return true
		}
		e := fv.Elem()
		return e.Kind() == reflect.Pointer && e.IsNil()
	}
	return false
}

// callTaskOptionsCompanion calls m, the companion named name, which must be
// func() TaskOptions, and returns its result. Any other signature is
// registration-time misuse, returned as the error <want>; so is a nil
// TaskOption in the result, which Task would refuse. registerMethodTasks
// prefixes both with "RegisterMethods(<struct type>): ".
func callTaskOptionsCompanion(m reflect.Value, name, want string) (TaskOptions, error) {
	mt := m.Type()
	if mt.NumIn() != 0 || mt.NumOut() != 1 || mt.Out(0) != reflect.TypeOf(TaskOptions(nil)) {
		return nil, errors.New(want)
	}
	opts := m.Call(nil)[0].Interface().(TaskOptions)
	if bad := nilOptionIndex(opts); bad != 0 {
		return nil, fmt.Errorf("%s returned a nil TaskOption (option %d)", name, bad)
	}
	return opts, nil
}
