package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/snonux/gonf/plan"
)

// fieldClass says how the sensitivity scan treats one string-bearing field
// of plan.Op (see opFieldClasses).
type fieldClass int

const (
	// classMetadata fields (owners, modes, kinds, shells) mark the op
	// sensitive when they hold a strong secret (secret.Values.ContainsStrong)
	// and otherwise ignore a match: a weak secret such as "root" or
	// "postgres" equals ordinary metadata by chance. They never refuse:
	// resolving a secret must not break every User("postgres").
	classMetadata fieldClass = iota
	// classIdentity fields (IDs, names, paths, binaries, directories) are
	// logged and reported on every host: a strong secret there refuses the
	// record, any other match marks the op sensitive. The only refusing
	// class.
	classIdentity
	// classPayload fields (argv, lines, commands, environment, predicates)
	// mark the op sensitive on any match.
	classPayload
	// classContent fields are base64 content: the decoded bytes are scanned.
	classContent
	// classTemplateData is a string leaf of template_data: it and its
	// base64 decoding (how json.Marshal records a []byte) are scanned.
	classTemplateData
)

// opFieldClasses classifies every string-bearing field of plan.Op by its
// JSON path ("members[].key", "env{}" for map values, "env{key}" for map
// keys, "template_data{}" for template data leaves). The scan and the
// redaction walk all of them by reflection (scanOpStrings and
// redactOpStrings); TestOpFieldClassesAreExhaustive fails when a field is
// added to plan.Op without being classified here. An unclassified path met
// at run time is treated as identity.
var opFieldClasses = map[string]fieldClass{
	// Kind, generated references and host metadata.
	"op": classMetadata, "blob": classMetadata, "mode": classMetadata, "file_mode": classMetadata,
	"owner": classMetadata, "group": classMetadata, "primary_group": classMetadata,
	"supplementary_groups[]": classMetadata, "shell": classMetadata, "login_class": classMetadata,
	"cron_user": classMetadata, "all[].fact": classMetadata,
	"members[].mode": classMetadata, "members[].owner": classMetadata, "members[].group": classMetadata,
	// Identities, paths and binaries: logged and reported unredacted.
	"id": classIdentity, "name": classIdentity, "path": classIdentity, "symlink": classIdentity,
	"target": classIdentity, "hardlink": classIdentity, "member": classIdentity,
	"deps[]": classIdentity, "watch[]": classIdentity, "after[]": classIdentity, "wants[]": classIdentity,
	"bin": classIdentity, "dir": classIdentity, "creates": classIdentity, "home": classIdentity,
	"validation_bin": classIdentity, "validators[].bin": classIdentity,
	"unless.bin": classIdentity, "only_if.bin": classIdentity,
	"source_dir": classIdentity,
	"chroot":     classIdentity, "staging_dir": classIdentity,
	"members[].key": classIdentity, "members[].path": classIdentity,
	"all[].path_exists": classIdentity, "require": classIdentity,
	// Payload: what the op writes, runs or compares.
	"args[]": classPayload, "env{}": classPayload, "env{key}": classPayload,
	"add_lines[]": classPayload, "remove_lines[]": classPayload,
	// A keyed line is written content; its key is a literal prefix of it, so
	// it is payload too (a secret scan must look at both).
	"keyed_lines[].key": classPayload, "keyed_lines[].line": classPayload,
	"add_line": classPayload, "remove_line": classPayload,
	"command": classPayload, "legacy_command": classPayload, "schedule": classPayload,
	"cron_env[]": classPayload, "on_calendar": classPayload, "on_boot_sec": classPayload,
	"description": classPayload, "service_description": classPayload,
	// template_param is the {{.Param}} value: the literal content itself for
	// a template without a source file.
	"template_param":    classPayload,
	"validation_args[]": classPayload, "validators[].args[]": classPayload,
	"unless.args[]": classPayload, "unless.expect_stdout": classPayload,
	"only_if.args[]": classPayload, "only_if.expect_stdout": classPayload,
	"all[].eq": classPayload, "all[].in[]": classPayload,
	// Content.
	"content_b64": classContent, "members[].content_b64": classContent,
	"template_data{}": classTemplateData, "template_data{key}": classPayload,
}

// classOf returns the class of path, identity for an unclassified one.
func classOf(path string) fieldClass {
	if c, ok := opFieldClasses[path]; ok {
		return c
	}
	return classIdentity
}

// copyOp deep-copies op through the wire codec. A legitimately built op
// round-trips losslessly: its Payload (if any) only ever carries fields
// exclusive to its OWN Kind (every concrete OpPayload's applyToWire,
// plan/op_payload.go, writes only its own fields), so encoding it and
// decoding the result back reconstructs the same value. This is narrower
// than an earlier version of this comment claimed ("every op round-trips
// losslessly," unqualified): before task 2f2, a DECODED wireOp carrying an
// extra field exclusive to some OTHER kind (reachable only via a
// hand-edited or forged plan.jsonl line, never via toWire/applyToWire) was
// silently dropped by payloadFromWire's kind-dispatch switch instead of
// refused — see the task 2f2 annotation for the probe that found it.
// checkForeignPayload (op_payload.go) now refuses such a line at decode
// instead, so copyOp itself never had a caller that could observe the old
// silent drop (a plan.Op value copyOp receives has already survived that
// same decode, or was never decoded at all), but the unqualified claim was
// still false about what DecodeOp would do with a forged line, so it is
// corrected here rather than left to mislead the next reader.
func copyOp(op plan.Op) (plan.Op, error) {
	line, err := plan.EncodeOp(op)
	if err != nil {
		return plan.Op{}, err
	}
	return plan.DecodeOp(line)
}

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// isStringMap reports whether t is a map from a string kind to a string
// kind (map[string]string).
func isStringMap(t reflect.Type) bool {
	return t.Kind() == reflect.Map && t.Key().Kind() == reflect.String && t.Elem().Kind() == reflect.String
}

// isScalar reports whether k holds no string: booleans and numbers.
func isScalar(k reflect.Kind) bool {
	switch k {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// ---------------------------------------------------------------------
// Read-only walker (scanOpStrings): used by every pass that may run
// concurrently over an op shared with other goroutines.
// ---------------------------------------------------------------------

// scanOpStrings calls fn for every string in op — every string field, slice
// element, map[string]string key and value, template_data leaf, and (by
// name) the concrete Payload's own fields — with its JSON path. fn may
// return a rewritten string, exactly like redactOpStrings' fn, so a scan
// and a redaction can share one closure shape; scanOpStrings simply never
// looks at what fn returns. That is the whole safety property: it is not a
// flag that gates a write, there is no write anywhere in this call graph
// (scanValue, scanStruct, scanStringMap, scanRawJSON, scanAny) to gate in
// the first place. A caller that must not modify op — scanOp and
// opDisplayName (api/secret_scan.go), because both may run concurrently
// over the SAME []plan.Op (internal/remote/fleet.go's Fanout hands one
// recorded slice to a goroutine per host) — always uses this function, even
// when its fn happens to be an identity closure. redactOp
// (api/secret_plan.go) is the one caller that needs a real rewrite, and it
// calls redactOpStrings instead, always on its own copyOp deep copy, never
// on a caller's shared op.
//
// Before task kf2, there was one walkOpStrings with a `mutate bool`
// parameter instead of this split: mutate=false gated only walkStruct's
// Payload write-back, while the plain reflect.Value.SetString call for
// every OTHER string field (including a string reached through a shared
// *Guard pointer, e.g. CommandPayload.Unless.Bin) was unconditional. That
// left the "read-only" mode read-only only because its two callers
// happened to pass an identity fn; a rewriting fn (the shape a redacted
// preview built from a scan, rather than a full redactOp copy, would use)
// mutated the caller's op in place through the shared Guard pointer while
// simultaneously discarding the (correctly gated) top-level Payload
// field's own rewrite — a half-mutated op, and the exact concurrency
// hazard task 0f2 had just fixed for that Payload field specifically. See
// TestScanOpStringsNeverMutatesEvenWithARewritingFn and
// TestScanOpStringsConcurrentRaceWithRewritingFn for the regression tests.
func scanOpStrings(op *plan.Op, fn func(path, s string) string) {
	scanValue(reflect.ValueOf(op).Elem(), "", fn)
}

// scanValue is scanOpStrings' reflective step. It only ever reads: every
// branch either recurses or calls fn and drops the result, and no branch
// calls a reflect.Value Set*/SetBytes method or rebuilds a map. Compare
// redactValue below, its mutating twin.
func scanValue(v reflect.Value, path string, fn func(path, s string) string) {
	switch {
	case v.Type() == rawMessageType:
		scanRawJSON(v, path, fn)
	case v.Kind() == reflect.String:
		fn(path, v.String())
	case v.Kind() == reflect.Pointer:
		if !v.IsNil() {
			scanValue(v.Elem(), path, fn)
		}
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8:
		for i := range v.Len() {
			scanValue(v.Index(i), path+"[]", fn)
		}
	case isStringMap(v.Type()):
		scanStringMap(v, path, fn)
	case v.Kind() == reflect.Struct:
		scanStruct(v, path, fn)
	case isScalar(v.Kind()):
		// Holds no string.
	default:
		panic(fmt.Sprintf("scanOpStrings: %s has unhandled type %s; teach scanValue and classify it", path, v.Type()))
	}
}

// scanStruct walks every exported field under its JSON name, the read-only
// twin of redactStruct. Op.Payload (task yd2) is special: its json tag is
// "-" because Op never marshals itself by default struct reflection (see
// Op.MarshalJSON), but its concrete value's OWN fields (plan.CronPayload,
// plan.SystemdTimerPayload, ...) still sit at the OP'S top level on the
// wire — that is exactly what Op.MarshalJSON's wireOp merge reproduces. So
// the walk must reach them at the SAME path, not nested under a "Payload"
// segment that never existed on the wire. Unlike redactStruct, there is
// nothing to write back here, so this branch reads the interface's dynamic
// value directly (fv.Elem()) instead of copying it to an addressable local
// first: the shared Payload value is only ever read, never touched by a
// Set call of any kind.
func scanStruct(v reflect.Value, path string, fn func(path, s string) string) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Name == "Payload" && f.Type.Kind() == reflect.Interface {
			fv := v.Field(i)
			if fv.IsNil() {
				continue
			}
			scanValue(fv.Elem(), path, fn)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if path != "" {
			name = path + "." + name
		}
		scanValue(v.Field(i), name, fn)
	}
}

// scanStringMap walks a map[string]string's keys ("{key}") and values
// ("{}"), the read-only twin of redactStringMap: it never rebuilds or
// writes the map.
func scanStringMap(v reflect.Value, path string, fn func(path, s string) string) {
	if v.IsNil() {
		return
	}
	iter := v.MapRange()
	for iter.Next() {
		fn(path+"{key}", iter.Key().String())
		fn(path+"{}", iter.Value().String())
	}
}

// scanRawJSON walks the string leaves ("{}") and object keys ("{key}") of
// encoded JSON (template_data), the read-only twin of redactRawJSON: it
// decodes a throwaway copy and never calls v.SetBytes. Invalid JSON is left
// alone; recording never produces it.
func scanRawJSON(v reflect.Value, path string, fn func(path, s string) string) {
	if v.Len() == 0 {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(v.Bytes()))
	dec.UseNumber()
	var data any
	if err := dec.Decode(&data); err != nil {
		return
	}
	scanAny(data, path, fn)
}

// scanAny is scanRawJSON's recursive step over decoded JSON.
func scanAny(x any, path string, fn func(path, s string) string) {
	switch t := x.(type) {
	case string:
		fn(path+"{}", t)
	case []any:
		for _, e := range t {
			scanAny(e, path, fn)
		}
	case map[string]any:
		for k, e := range t {
			fn(path+"{key}", k)
			scanAny(e, path, fn)
		}
	}
}

// ---------------------------------------------------------------------
// Mutating walker (redactOpStrings): the one real rewriter, used only by
// redactOp (api/secret_plan.go) on its own copyOp deep copy — never on a
// caller's shared op.
// ---------------------------------------------------------------------

// redactOpStrings calls fn for every string in op exactly like
// scanOpStrings does, but writes back whatever fn returns: every string
// field, slice element, map[string]string key/value and template_data leaf
// that fn changed is rewritten in place (map and template_data get rebuilt
// and re-encoded only when something in them changed), and the concrete
// Payload's rewritten copy is written back into the interface field.
// Because it always writes, it must only ever be called on an op no other
// goroutine can observe — redactOp's copyOp deep copy — never on a shared
// []plan.Op such as one internal/remote/fleet.go's Fanout hands to several
// per-host goroutines; see scanOpStrings' doc comment for that hazard and
// task kf2 for the bug this split closes (a `mutate bool` flag on one
// shared function was not a strong enough guarantee: it gated only one of
// several write paths).
func redactOpStrings(op *plan.Op, fn func(path, s string) string) {
	redactValue(reflect.ValueOf(op).Elem(), "", fn)
}

// redactValue is redactOpStrings' reflective step, the mutating twin of
// scanValue.
func redactValue(v reflect.Value, path string, fn func(path, s string) string) {
	switch {
	case v.Type() == rawMessageType:
		redactRawJSON(v, path, fn)
	case v.Kind() == reflect.String:
		if s := fn(path, v.String()); s != v.String() {
			v.SetString(s)
		}
	case v.Kind() == reflect.Pointer:
		if !v.IsNil() {
			redactValue(v.Elem(), path, fn)
		}
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8:
		for i := range v.Len() {
			redactValue(v.Index(i), path+"[]", fn)
		}
	case isStringMap(v.Type()):
		redactStringMap(v, path, fn)
	case v.Kind() == reflect.Struct:
		redactStruct(v, path, fn)
	case isScalar(v.Kind()):
		// Holds no string.
	default:
		panic(fmt.Sprintf("redactOpStrings: %s has unhandled type %s; teach redactValue and classify it", path, v.Type()))
	}
}

// redactStruct walks every exported field under its JSON name, the
// mutating twin of scanStruct. Op.Payload (task yd2) is special the same
// way scanStruct's doc comment explains; the difference here is that its
// concrete value sits behind an interface field and is therefore not
// addressable, so a rewrite (fn returning a different string, via
// SetString) cannot mutate it in place — this copies it to an addressable
// local first, walks and (unconditionally, since redactOpStrings is only
// ever called on an op nothing else can observe) writes the possibly
// rewritten copy back into the interface field.
func redactStruct(v reflect.Value, path string, fn func(path, s string) string) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Name == "Payload" && f.Type.Kind() == reflect.Interface {
			fv := v.Field(i)
			if fv.IsNil() {
				continue
			}
			concrete := reflect.New(fv.Elem().Type()).Elem()
			concrete.Set(fv.Elem())
			redactValue(concrete, path, fn)
			fv.Set(concrete)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if path != "" {
			name = path + "." + name
		}
		redactValue(v.Field(i), name, fn)
	}
}

// redactStringMap walks a map[string]string's keys ("{key}") and values
// ("{}"), rebuilding it when fn changed either — the mutating twin of
// scanStringMap.
func redactStringMap(v reflect.Value, path string, fn func(path, s string) string) {
	if v.IsNil() {
		return
	}
	out := reflect.MakeMapWithSize(v.Type(), v.Len())
	changed := false
	iter := v.MapRange()
	for iter.Next() {
		k, val := iter.Key().String(), iter.Value().String()
		nk, nv := fn(path+"{key}", k), fn(path+"{}", val)
		changed = changed || nk != k || nv != val
		out.SetMapIndex(reflect.ValueOf(nk).Convert(v.Type().Key()), reflect.ValueOf(nv).Convert(v.Type().Elem()))
	}
	if changed {
		v.Set(out)
	}
}

// redactRawJSON walks the string leaves ("{}") and object keys ("{key}") of
// encoded JSON (template_data) and re-encodes it when fn changed any — the
// mutating twin of scanRawJSON. Invalid JSON is left alone; recording never
// produces it.
func redactRawJSON(v reflect.Value, path string, fn func(path, s string) string) {
	if v.Len() == 0 {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(v.Bytes()))
	dec.UseNumber()
	var data any
	if err := dec.Decode(&data); err != nil {
		return
	}
	changed := false
	data = redactAny(data, path, fn, &changed)
	if !changed {
		return
	}
	if raw, err := json.Marshal(data); err == nil {
		v.SetBytes(raw)
	}
}

// redactAny is redactRawJSON's recursive step over decoded JSON.
func redactAny(x any, path string, fn func(path, s string) string, changed *bool) any {
	switch t := x.(type) {
	case string:
		s := fn(path+"{}", t)
		*changed = *changed || s != t
		return s
	case []any:
		for i := range t {
			t[i] = redactAny(t[i], path, fn, changed)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			nk := fn(path+"{key}", k)
			*changed = *changed || nk != k
			out[nk] = redactAny(e, path, fn, changed)
		}
		return out
	}
	return x
}
