package testutil

import (
	"fmt"
	"reflect"
	"sort"
)

// SharedRefs reports, sorted, the paths of b (e.g. "Args" or
// "Unless.Args") whose backing storage is also reachable from a: a slice
// array, a map or a pointee both values could write through. An empty
// result means a and b share no mutable storage, so mutating one can never
// change the other. a and b may be of different types (a draft and the op
// lowered from it). Zero-capacity slices are ignored: nothing can be
// written through them, and every empty slice may point at the same
// runtime sentinel.
func SharedRefs(a, b any) []string {
	inA := map[uintptr]string{}
	walkRefs(reflect.ValueOf(a), "", inA)
	inB := map[uintptr]string{}
	walkRefs(reflect.ValueOf(b), "", inB)
	var shared []string
	for addr, path := range inB {
		if _, ok := inA[addr]; ok {
			shared = append(shared, path)
		}
	}
	sort.Strings(shared)
	return shared
}

// Scribble mutates, in place, every value reachable from ptr (which must be
// a non-nil pointer): strings get a suffix, numbers are incremented, bools
// flipped, and every map gains an extra entry. Values held in interfaces
// are skipped (they are not settable through reflection). Tests call it on
// one of two values that should be independent and then check the other is
// unchanged, which proves no mutable storage is shared.
func Scribble(ptr any) {
	v := reflect.ValueOf(ptr)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		panic(fmt.Sprintf("testutil.Scribble: need a non-nil pointer, got %T", ptr))
	}
	scribble(v.Elem())
}

// walkRefs records into out the address of every slice array, map and
// pointee reachable from v, keyed to the first path that reached it.
func walkRefs(v reflect.Value, path string, out map[uintptr]string) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		addRef(out, v.Pointer(), path)
		walkRefs(v.Elem(), path, out)
	case reflect.Map:
		if v.IsNil() {
			return
		}
		addRef(out, v.Pointer(), path)
		iter := v.MapRange()
		for iter.Next() {
			walkRefs(iter.Value(), fmt.Sprintf("%s[%v]", path, iter.Key()), out)
		}
	case reflect.Slice:
		if v.IsNil() || v.Cap() == 0 {
			return
		}
		addRef(out, v.Pointer(), path)
		for i := range v.Len() {
			walkRefs(v.Index(i), fmt.Sprintf("%s[%d]", path, i), out)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			walkRefs(v.Field(i), joinPath(path, v.Type().Field(i).Name), out)
		}
	case reflect.Interface:
		if !v.IsNil() {
			walkRefs(v.Elem(), path, out)
		}
	}
}

// scribble is Scribble's recursive worker; v must be addressable, so every
// exported field, slice element and pointee reached from it is settable.
func scribble(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			scribble(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				scribble(v.Field(i))
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			scribble(v.Index(i))
		}
	case reflect.Map:
		scribbleMap(v)
	default:
		scribbleScalar(v)
	}
}

// scribbleMap mutates every entry of a non-nil map (map entries are not
// addressable, so each is copied, scribbled and stored back) and adds one
// entry when the key type allows it.
func scribbleMap(v reflect.Value) {
	if v.IsNil() {
		return
	}
	for _, k := range v.MapKeys() {
		val := reflect.New(v.Type().Elem()).Elem()
		val.Set(v.MapIndex(k))
		scribble(val)
		v.SetMapIndex(k, val)
	}
	if v.Type().Key().Kind() == reflect.String {
		extra := reflect.New(v.Type().Key()).Elem()
		extra.SetString("SCRIBBLED")
		v.SetMapIndex(extra, reflect.New(v.Type().Elem()).Elem())
	}
}

// scribbleScalar changes a settable string, number or bool.
func scribbleScalar(v reflect.Value) {
	if !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "~scribbled")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(v.Uint() + 1)
	case reflect.Bool:
		v.SetBool(!v.Bool())
	}
}

// addRef records addr under path unless an earlier path already claimed it.
func addRef(out map[uintptr]string, addr uintptr, path string) {
	if _, ok := out[addr]; !ok {
		out[addr] = path
	}
}

// joinPath appends a field name to a dotted path.
func joinPath(path, field string) string {
	if path == "" {
		return field
	}
	return path + "." + field
}
