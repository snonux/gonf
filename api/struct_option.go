package api

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
)

// StructOption can be embedded in (or added as a field to) a struct that is
// registered with RegisterMethods to contribute DEFAULT TaskOptions for
// every task of that struct — the execution contract declared on the struct
// itself. The engine composes all embedded StructOption fields (in
// declaration order) plus the Opts() companion if present; a method's own
// OptsX companion adds to the combined struct-level set.
//
// gonf ships RequiresRoot as the ready-made marker for the common case.
// Custom markers: define any type implementing this interface (value or
// pointer receiver) and embed it. Markers must be embedded exported types so
// reflection can reach them. A struct that declares StructTaskOptions itself
// overrides its markers, as in Go method resolution: only its own method is
// called.
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
// order), then the Opts() companion if the struct defines one, then the
// struct's own StructTaskOptions if it has no marker field. A method's own
// OptsX companion is appended to the whole combined struct-level set.
//
// It follows Go method resolution: a struct that declares StructTaskOptions
// itself (on T or *T, not promoted) overrides its embedded markers, so only
// that method is called (it may call the markers it wants itself) and the
// marker fields are not collected. Otherwise every exported marker field is
// collected (markerStructOptions); a struct with none falls back to its
// promoted StructTaskOptions, e.g. through an unexported embedded marker
// (single marker only; multiple same-depth markers are an ambiguous
// selector and are not in the method set).
//
// A struct-level companion with the wrong signature, a nil marker (nilPath),
// or a marker or companion returning a nil TaskOption, is returned as an
// error: RegisterMethods then registers no task of the struct, because
// silently ignoring the companion (or the nil option) could drop
// Privileged() and lower every task's privileges.
func collectStructOptions(rv reflect.Value, rt reflect.Type) (TaskOptions, error) {
	own := declaresStructTaskOptions(rt.Elem())
	var opts TaskOptions
	foundMarkerField := false
	if !own {
		var err error
		opts, foundMarkerField, err = markerStructOptions(rv, rt)
		if err != nil {
			return nil, err
		}
	}
	if o := rv.MethodByName("Opts"); o.IsValid() {
		companion, err := callTaskOptionsCompanion(o, "Opts", "Opts must be func() TaskOptions (the struct-level default companion)")
		if err != nil {
			return nil, err
		}
		opts = append(opts, companion...)
	}
	// The struct's own StructTaskOptions, or with no marker field the one
	// it promotes. Same signature contract as Opts(), and it composes after
	// Opts().
	if !foundMarkerField {
		if m := rv.MethodByName("StructTaskOptions"); m.IsValid() {
			if !own {
				if path := nilPath(rv, ""); path != "" {
					return nil, fmt.Errorf("marker %s is nil", path)
				}
			}
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
// marker (nilPath), or a nil TaskOption in a marker's result, is returned
// as an error.
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
		if path := nilPath(fv, f.Name); path != "" {
			// Calling the marker through a nil pointer or interface would
			// panic, and skipping it would drop its options (e.g.
			// Privileged()).
			return nil, true, fmt.Errorf("marker %s is nil", path)
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

// maxPromotionDepth bounds the embedded-field walks below; real marker
// chains are one or two levels deep, and the bound also stops a recursive
// type (type A struct{ *A }).
const maxPromotionDepth = 16

// nilPath follows the chain of embedded fields that StructTaskOptions is
// promoted through, starting at v (named name, "" for the registered
// struct itself), and returns the dotted field path to the first nil
// pointer or interface on it — which calling the method would dereference
// — or "" when there is none: v is a nil marker field, an interface holding
// a nil pointer, or a struct such as Base in
// type Base struct{ *RequiresRoot } whose promoting field is nil.
func nilPath(v reflect.Value, name string) string {
	path := name
	for range maxPromotionDepth {
		for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
			if v.IsNil() {
				return path
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct || declaresStructTaskOptions(v.Type()) {
			return ""
		}
		i, ok := promotingField(v.Type())
		if !ok {
			return ""
		}
		if path != "" {
			path += "."
		}
		path += v.Type().Field(i).Name
		v = v.Field(i)
	}
	return ""
}

// promotingField returns the index of struct type t's embedded field that
// StructTaskOptions is promoted through: the one reaching a declaration at
// the shallowest depth, as Go's selector rule picks it. It reports false
// when no embedded field provides the method or the shallowest depth is
// ambiguous (the method is then not in t's method set at all).
func promotingField(t reflect.Type) (int, bool) {
	best, bestDepth, tie := -1, 0, false
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.Anonymous {
			continue
		}
		d := promotionDepth(f.Type, maxPromotionDepth)
		switch {
		case d < 0:
		case best < 0 || d < bestDepth:
			best, bestDepth, tie = i, d, false
		case d == bestDepth:
			tie = true
		}
	}
	return best, best >= 0 && !tie
}

// promotionDepth returns how many embedded fields below t (or *t) the
// declaration of StructTaskOptions is (0: t declares it), or -1 when t does
// not provide the method within budget levels.
func promotionDepth(t reflect.Type, budget int) int {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if declaresStructTaskOptions(t) {
		return 0
	}
	if t.Kind() != reflect.Struct || budget == 0 {
		return -1
	}
	best := -1
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.Anonymous {
			continue
		}
		if d := promotionDepth(f.Type, budget-1); d >= 0 && (best < 0 || d+1 < best) {
			best = d + 1
		}
	}
	return best
}

// declaresStructTaskOptions reports whether t itself declares a method
// named StructTaskOptions (any signature, value or pointer receiver), as
// opposed to promoting one from an embedded field; an interface type counts
// when it has the method. A method set cannot tell the two apart, so this
// asks the runtime where the method's code is: a promoted method is a
// compiler-generated wrapper whose file is "<autogenerated>".
func declaresStructTaskOptions(t reflect.Type) bool {
	if t.Kind() == reflect.Interface {
		_, ok := t.MethodByName("StructTaskOptions")
		return ok
	}
	for _, mt := range []reflect.Type{t, reflect.PointerTo(t)} {
		m, ok := mt.MethodByName("StructTaskOptions")
		if !ok {
			continue
		}
		fn := runtime.FuncForPC(m.Func.Pointer())
		if fn == nil {
			return false
		}
		file, _ := fn.FileLine(fn.Entry())
		return file != "<autogenerated>"
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
