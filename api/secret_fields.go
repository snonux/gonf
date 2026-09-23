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
// redaction walk all of them by reflection (walkOpStrings);
// TestOpFieldClassesAreExhaustive fails when a field is added to plan.Op
// without being classified here. An unclassified path met at run time is
// treated as identity.
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

// walkOpStrings calls fn for every string in op — every string field,
// slice element, map[string]string key and value, and template_data leaf —
// with its JSON path, and stores what fn returns. Callers that must not
// modify op pass a copy (copyOp) or an fn that returns its input. It knows
// exactly the kinds plan.Op is built from (strings, bools, numbers,
// pointers, slices, structs, map[string]string, json.RawMessage) and panics
// on any other (an interface, array, other map or []byte field), a
// programming error that TestOpFieldClassesAreExhaustive catches first. The
// one interface field Op actually has, Payload (task yd2), never reaches
// this generic panic-on-interface path at all: walkStruct special-cases it
// by name and descends into its concrete value's own fields directly, at
// the same path its fields had on the wire before the split.
//
// mutate gates ONLY that Payload field's write-back (walkStruct): every
// other field already writes conditionally, only when fn actually changed
// something (SetString/map rebuild/SetBytes each compare before writing),
// so an fn that never changes a string (scanOp's and opDisplayName's do
// not) already leaves them untouched regardless of mutate. Payload's
// concrete value is different — it sits behind an interface field, so the
// walk works on an addressable local copy and, before task 0f2, wrote that
// copy back UNCONDITIONALLY. redactOp is the only real mutator (it walks
// its own copyOp deep copy and needs the rewritten payload stored back), so
// it alone passes mutate=true. scanOp and opDisplayName scan a caller's
// shared op (an []plan.Op that internal/remote/fleet.go's Fanout may fan
// out to per-host goroutines, exactly the aliasing plan/wire.go's own
// normalizeWire comment warns against) and must pass mutate=false: fn still
// runs for its side effects (the sensitivity flag), but the rewritten local
// copy is discarded instead of being stored into the shared interface
// field, so two goroutines calling the public, read-only
// api.SensitiveOpNames or api.EncodeRedactedPreview over the same ops slice
// never race a concurrent reader of that field.
func walkOpStrings(op *plan.Op, fn func(path, s string) string, mutate bool) {
	walkValue(reflect.ValueOf(op).Elem(), "", fn, mutate)
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

// walkValue is walkOpStrings' reflective step. mutate is threaded through
// unchanged to every recursive call so it reaches whichever struct (in
// practice only the top-level Op) holds the Payload field walkStruct
// special-cases; see walkOpStrings' doc comment.
func walkValue(v reflect.Value, path string, fn func(path, s string) string, mutate bool) {
	switch {
	case v.Type() == rawMessageType:
		walkRawJSON(v, path, fn)
	case v.Kind() == reflect.String:
		if s := fn(path, v.String()); s != v.String() {
			v.SetString(s)
		}
	case v.Kind() == reflect.Pointer:
		if !v.IsNil() {
			walkValue(v.Elem(), path, fn, mutate)
		}
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8:
		for i := range v.Len() {
			walkValue(v.Index(i), path+"[]", fn, mutate)
		}
	case isStringMap(v.Type()):
		walkStringMap(v, path, fn)
	case v.Kind() == reflect.Struct:
		walkStruct(v, path, fn, mutate)
	case isScalar(v.Kind()):
		// Holds no string.
	default:
		panic(fmt.Sprintf("walkOpStrings: %s has unhandled type %s; teach walkValue and classify it", path, v.Type()))
	}
}

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

// walkStruct walks every exported field under its JSON name. Op.Payload
// (task yd2, "Layer 2" of the PlanDraft/Op god-struct split) is special:
// its json tag is "-" because Op never marshals itself by default struct
// reflection (see Op.MarshalJSON), but its concrete value's OWN fields
// (plan.CronPayload, plan.SystemdTimerPayload, ...) still sit at the OP'S
// top level on the wire — that is exactly what Op.MarshalJSON's wireOp
// merge reproduces. So the walk must reach them at the SAME path, not
// nested under a "Payload" segment that never existed on the wire and that
// opFieldClasses knows nothing about; it is named, not type-matched,
// because plan.OpPayload's one method is unexported (only the plan package
// can implement it), so this package cannot spell the interface type to
// compare against.
func walkStruct(v reflect.Value, path string, fn func(path, s string) string, mutate bool) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Name == "Payload" && f.Type.Kind() == reflect.Interface {
			fv := v.Field(i)
			if fv.IsNil() {
				continue
			}
			// fv.Elem() (the interface's dynamic value) is a copy and not
			// addressable, so a redaction pass (fn rewriting a string via
			// SetString) cannot mutate it in place — copy it to an
			// addressable local and walk that. Only write the (possibly
			// rewritten) copy back into the interface field when mutate:
			// fv aliases the SAME Payload value a concurrent reader
			// elsewhere may be reading right now (e.g. another goroutine's
			// api.SensitiveOpNames call over the same shared []plan.Op —
			// internal/remote/fleet.go's Fanout runs one such call per
			// host), so an unconditional fv.Set here raced a concurrent
			// reflect.Value.IsNil read on that field (task 0f2, reproduced
			// with go test -race). scanOp and opDisplayName pass
			// mutate=false for exactly that reason; redactOp — the only
			// real mutator, and only on its own copyOp deep copy, never a
			// caller's shared op — passes mutate=true.
			concrete := reflect.New(fv.Elem().Type()).Elem()
			concrete.Set(fv.Elem())
			walkValue(concrete, path, fn, mutate)
			if mutate {
				fv.Set(concrete)
			}
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
		walkValue(v.Field(i), name, fn, mutate)
	}
}

// walkStringMap walks a map[string]string's keys ("{key}") and values
// ("{}"), rebuilding it when fn changed either.
func walkStringMap(v reflect.Value, path string, fn func(path, s string) string) {
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

// walkRawJSON walks the string leaves ("{}") and object keys ("{key}") of
// encoded JSON (template_data) and re-encodes it when fn changed any.
// Invalid JSON is left alone; recording never produces it.
func walkRawJSON(v reflect.Value, path string, fn func(path, s string) string) {
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
	data = walkAny(data, path, fn, &changed)
	if !changed {
		return
	}
	if raw, err := json.Marshal(data); err == nil {
		v.SetBytes(raw)
	}
}

// walkAny is walkRawJSON's recursive step over decoded JSON.
func walkAny(x any, path string, fn func(path, s string) string, changed *bool) any {
	switch t := x.(type) {
	case string:
		s := fn(path+"{}", t)
		*changed = *changed || s != t
		return s
	case []any:
		for i := range t {
			t[i] = walkAny(t[i], path, fn, changed)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			nk := fn(path+"{key}", k)
			*changed = *changed || nk != k
			out[nk] = walkAny(e, path, fn, changed)
		}
		return out
	}
	return x
}
