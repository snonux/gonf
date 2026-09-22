package testutil

import (
	"fmt"
	"reflect"
	"sort"
)

// memRef is one piece of mutable storage reachable from a value: the
// address range [start, end) of a slice's backing array (up to its
// capacity), of a pointee, or the header of a map.
type memRef struct {
	start, end uintptr
	path       string
}

// visitKey identifies a reference already walked, so cyclic structures
// (a pointer or map reachable from itself, e.g. through an interface)
// terminate. The type is part of the key because a struct and its first
// field share an address.
type visitKey struct {
	addr uintptr
	typ  reflect.Type
}

// SharedRefs reports, sorted, the paths of b (e.g. "Args" or
// "Unless.Args") whose mutable storage overlaps storage reachable from a:
// a slice backing array (compared as address ranges up to capacity, so a
// sub-slice of the other side's array counts), a map, or a pointee
// (including a pointer into the other side's array). An empty result
// means a and b share no mutable storage, so mutating one can never change
// the other. a and b may be of different types (a draft and the op lowered
// from it). Zero-capacity slices and zero-size pointees are ignored:
// nothing can be written through them, and they may all point at the
// same runtime sentinel.
func SharedRefs(a, b any) []string {
	inA := collectRefs(a)
	inB := collectRefs(b)
	seen := map[string]bool{}
	var shared []string
	for _, rb := range inB {
		for _, ra := range inA {
			if rb.start < ra.end && ra.start < rb.end && !seen[rb.path] {
				seen[rb.path] = true
				shared = append(shared, rb.path)
			}
		}
	}
	sort.Strings(shared)
	return shared
}

// Scribble mutates, in place, every value reachable from ptr (which must be
// a non-nil pointer): strings get a suffix, numbers are incremented, bools
// flipped, and every map gains an extra entry. Tests call it on one of two
// values that should be independent and then check the other is
// unchanged, which proves no mutable storage is shared.
//
// Limits: unexported struct fields are skipped (not settable through
// reflection); a value held in an interface is replaced by a scribbled
// copy, which still mutates any storage that copy references; floats,
// complex numbers, channels and funcs are left as they are; a map's extra
// entry uses a scribbled copy of an existing key (a zero key for an empty
// map), so for key types scribble cannot change it overwrites that entry's
// value instead of adding one.
func Scribble(ptr any) {
	v := reflect.ValueOf(ptr)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		panic(fmt.Sprintf("testutil.Scribble: need a non-nil pointer, got %T", ptr))
	}
	s := scribbler{visited: map[visitKey]bool{}}
	s.scribble(v) // through the pointer case, so the root is marked visited
}

// collectRefs returns every memRef reachable from v.
func collectRefs(v any) []memRef {
	w := refWalker{visited: map[visitKey]bool{}}
	w.walk(reflect.ValueOf(v), "")
	return w.refs
}

// refWalker accumulates the memRefs reachable from a value.
type refWalker struct {
	refs    []memRef
	visited map[visitKey]bool
}

// walk records the storage reachable from v under path.
func (w *refWalker) walk(v reflect.Value, path string) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || !w.enter(v) {
			return
		}
		w.add(v.Pointer(), v.Type().Elem().Size(), path)
		w.walk(v.Elem(), path)
	case reflect.Map:
		if v.IsNil() || !w.enter(v) {
			return
		}
		w.add(v.Pointer(), 1, path)
		iter := v.MapRange()
		for iter.Next() {
			w.walk(iter.Value(), fmt.Sprintf("%s[%v]", path, iter.Key()))
		}
	case reflect.Slice:
		if v.IsNil() || v.Cap() == 0 || !w.enter(v) {
			return
		}
		w.add(v.Pointer(), uintptr(v.Cap())*v.Type().Elem().Size(), path)
		for i := range v.Len() {
			w.walk(v.Index(i), fmt.Sprintf("%s[%d]", path, i))
		}
	case reflect.Array:
		for i := range v.Len() {
			w.walk(v.Index(i), fmt.Sprintf("%s[%d]", path, i))
		}
	case reflect.Struct:
		for i := range v.NumField() {
			w.walk(v.Field(i), joinPath(path, v.Type().Field(i).Name))
		}
	case reflect.Interface:
		if !v.IsNil() {
			w.walk(v.Elem(), path)
		}
	}
}

// enter reports whether the reference v has not been walked yet, and
// marks it walked.
func (w *refWalker) enter(v reflect.Value) bool {
	key := visitKey{addr: v.Pointer(), typ: v.Type()}
	if w.visited[key] {
		return false
	}
	w.visited[key] = true
	return true
}

// add records the range [start, start+size) unless it is empty.
func (w *refWalker) add(start, size uintptr, path string) {
	if size == 0 {
		return
	}
	w.refs = append(w.refs, memRef{start: start, end: start + size, path: path})
}

// scribbler mutates reachable values, visiting each reference once.
type scribbler struct {
	visited map[visitKey]bool
}

// scribble is Scribble's recursive worker; v must be addressable, so every
// exported field, slice element and pointee reached from it is settable.
func (s scribbler) scribble(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() && s.enter(v) {
			s.scribble(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				s.scribble(v.Field(i))
			}
		}
	case reflect.Slice:
		if v.IsNil() || !s.enter(v) {
			return
		}
		for i := range v.Len() {
			s.scribble(v.Index(i))
		}
	case reflect.Array:
		for i := range v.Len() {
			s.scribble(v.Index(i))
		}
	case reflect.Map:
		if !v.IsNil() && s.enter(v) {
			s.scribbleMap(v)
		}
	case reflect.Interface:
		s.scribbleInterface(v)
	default:
		scribbleScalar(v)
	}
}

// scribbleInterface replaces the value held in a settable interface with a
// scribbled copy (the held value itself is not addressable). The copy
// shares whatever storage the held value referenced, so that storage is
// mutated too.
func (s scribbler) scribbleInterface(v reflect.Value) {
	if v.IsNil() || !v.CanSet() {
		return
	}
	held := v.Elem()
	cp := reflect.New(held.Type()).Elem()
	cp.Set(held)
	s.scribble(cp)
	v.Set(cp)
}

// scribbleMap mutates every entry of a map (map entries are not
// addressable, so each is copied, scribbled and stored back) and adds one
// entry keyed by a scribbled copy of an existing key, or by the zero key
// when the map is empty.
func (s scribbler) scribbleMap(v reflect.Value) {
	keys := v.MapKeys()
	for _, k := range keys {
		val := reflect.New(v.Type().Elem()).Elem()
		val.Set(v.MapIndex(k))
		s.scribble(val)
		v.SetMapIndex(k, val)
	}
	extra := reflect.New(v.Type().Key()).Elem()
	if len(keys) > 0 {
		extra.Set(keys[0])
		s.scribble(extra)
	}
	v.SetMapIndex(extra, reflect.New(v.Type().Elem()).Elem())
}

// enter reports whether the reference v has not been scribbled yet, and
// marks it scribbled, so shared or cyclic storage is mutated exactly once.
func (s scribbler) enter(v reflect.Value) bool {
	key := visitKey{addr: v.Pointer(), typ: v.Type()}
	if s.visited[key] {
		return false
	}
	s.visited[key] = true
	return true
}

// scribbleScalar changes a settable string, integer or bool.
func scribbleScalar(v reflect.Value) {
	if !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "~scribbled")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		v.SetUint(v.Uint() + 1)
	case reflect.Bool:
		v.SetBool(!v.Bool())
	}
}

// joinPath appends a field name to a dotted path.
func joinPath(path, field string) string {
	if path == "" {
		return field
	}
	return path + "." + field
}
