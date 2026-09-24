package plan

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestDecodeRefusesForeignKindFields pins task 2f2's fix: a wire line that
// carries a field exclusive to a DIFFERENT kind's OpPayload than its own
// "op" must be refused at decode, not silently accepted with the foreign
// field dropped. Before this fix, payloadFromWire's kind-dispatch switch
// (op_payload.go) discarded any such field with no error — a genuine
// encode/decode fidelity regression task yd2 introduced (see the task 2f2
// annotation). Both cases here are exactly the probes that annotation
// recorded: a "cron" line carrying SystemdTimerPayload's
// on_calendar/persistent/description/after, and a "file" line carrying
// CronPayload's cron_user/on_calendar.
func TestDecodeRefusesForeignKindFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		line string
		// wantContains are substrings the error must contain: enough to
		// prove the refusal names the actual offending field(s) and the
		// kind that owns them, not just a generic failure.
		wantContains []string
	}{
		{
			name: "cron carries systemd_timer fields",
			line: `{"op":"cron","id":"Cron[root/backup]","name":"backup","cron_user":"root",` +
				`"command":"/usr/local/bin/backup.sh","schedule":"0 2 * * *",` +
				`"on_calendar":"daily","persistent":true,"description":"stray","after":["x.target"]}`,
			wantContains: []string{
				"cron op carries foreign-kind field",
				"on_calendar (systemd_timer-exclusive)",
				"persistent (systemd_timer-exclusive)",
				"description (systemd_timer-exclusive)",
				"after (systemd_timer-exclusive)",
			},
		},
		{
			name: "file carries cron fields",
			line: `{"op":"file","path":"/etc/x.conf","mode":"0640","content_b64":"Li4u",` +
				`"cron_user":"root","on_calendar":"daily"}`,
			wantContains: []string{
				"file op carries foreign-kind field",
				"cron_user (cron-exclusive)",
				"on_calendar (systemd_timer-exclusive)",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeOp([]byte(tc.line))
			if err == nil {
				t.Fatalf("DecodeOp(%s): want a foreign-payload refusal, got nil error", tc.line)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("DecodeOp(%s) error = %q, want it to contain %q", tc.line, err.Error(), want)
				}
			}
		})
	}
}

// TestDecodeNormalLinesUnaffected pins that task 2f2's checkForeignPayload
// is purely additive/corrective: a conforming line — one that carries only
// its own kind's fields, exactly what toWire/applyToWire ever produces —
// decodes exactly as it always did, and still round-trips byte-identically
// through EncodeOp. One line per migrated payload kind (op_payload.go's
// OpPayloadExamples) is covered, plus a payload-less kind (link_if_exists
// has none) to confirm a line with NO payload fields at all is untouched.
func TestDecodeNormalLinesUnaffected(t *testing.T) {
	t.Parallel()

	lines := []string{
		`{"op":"cron","id":"Cron[root/backup]","name":"backup","cron_user":"root","command":"/usr/local/bin/backup.sh","schedule":"0 2 * * *"}`,
		`{"op":"systemd_timer","id":"SystemdTimer[fit-job]","name":"fit-job","command":"/bin/true","on_calendar":"*-*-* *:05:00","persistent":true}`,
		`{"op":"user","id":"User[svc]","primary_group":"svc","home":"/var/lib/svc","create_home":true,"name":"svc"}`,
		`{"op":"link","id":"Symlink[a]","path":"/a","symlink":"/b"}`,
		`{"op":"link_if_exists","id":"LinkIfExists[a]","path":"/a","target":"/b"}`,
		`{"op":"package","id":"Package[helix]","name":"helix","latest":true}`,
		`{"op":"command","bin":"true","creates":"/x"}`,
		`{"op":"config_set","name":"mail","chroot":"/etc"}`,
		`{"op":"config_set_member","name":"mail","member":"aliases"}`,
		`{"op":"sync_dir","path":"/d","blob":"blobs/d","source_dir":"assets","glob":true,"prune":true}`,
		`{"op":"file","path":"/etc/plain.conf","mode":"0644"}`,
	}

	for _, line := range lines {
		t.Run(line, func(t *testing.T) {
			t.Parallel()
			op, err := DecodeOp([]byte(line))
			if err != nil {
				t.Fatalf("DecodeOp(%s): unexpected error: %v", line, err)
			}
			// A round trip is lossless when re-encoding, then decoding again,
			// reproduces the same Op — checked this way (rather than a
			// byte-equality assertion against the hand-typed input literal
			// above) so the test does not have to hand-derive wireOp's exact
			// canonical field order; DecodeOp/EncodeOp already pin that
			// separately (TestOpJSONTagsMatchPlanExamples).
			reencoded, err := EncodeOp(op)
			if err != nil {
				t.Fatalf("EncodeOp: %v", err)
			}
			roundTripped, err := DecodeOp(reencoded)
			if err != nil {
				t.Fatalf("DecodeOp(re-encoded): unexpected error: %v", err)
			}
			if !reflect.DeepEqual(op, roundTripped) {
				t.Fatalf("round-trip mismatch\nfirst:  %#v\nsecond: %#v", op, roundTripped)
			}
			// A second encode of the round-tripped op must reproduce the same
			// bytes as the first: the canonical wire shape is a fixed point.
			reencodedAgain, err := EncodeOp(roundTripped)
			if err != nil {
				t.Fatalf("EncodeOp(round-tripped): %v", err)
			}
			if string(reencodedAgain) != string(reencoded) {
				t.Fatalf("re-encode not idempotent\nfirst:  %s\nsecond: %s", reencoded, reencodedAgain)
			}
		})
	}
}

// TestEncodeRefusesForeignKindPayload pins task cg2's fix: EncodeOp (via
// Op.MarshalJSON's toWire) must refuse an Op whose Payload's concrete type
// does not belong to its own Op.Op kind, instead of silently emitting a
// wire line carrying that OTHER kind's exclusive fields — exactly the shape
// TestDecodeRefusesForeignKindFields above proves DecodeOp already refuses
// on the way back in. Before this fix, toWire ran op.Payload.applyToWire
// for whatever payload op.Payload held, regardless of op.Op, so EncodeOp
// returned a nil error and a syntactically valid but semantically wrong
// line — one that gonf's own DecodeOp of that very output, or
// api.EncodeRedactedPreview, or plan.RequiredVersion's header would then
// treat inconsistently. The first case is the EXACT probe from the task
// cg2 annotation: EncodeOp(Op{Op: KindDir, Payload: FilePayload{KeyedLines:
// ...}}).
func TestEncodeRefusesForeignKindPayload(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		op   Op
		// wantContains are substrings the error must contain: enough to
		// prove the refusal names the offending payload type and both the
		// kind it was wrongly attached to and the kind that actually owns
		// it — the encode-side mirror of checkForeignPayload's own
		// "<field> (<kind>-exclusive)" style.
		wantContains []string
	}{
		{
			// The task cg2 annotation's own probe, verbatim.
			name: "dir op holds a file payload (keyed_lines)",
			op: Op{
				Op:      KindDir,
				ID:      "Directory[/d]",
				Path:    "/d",
				Payload: FilePayload{KeyedLines: []KeyedLine{{Key: "k", Line: "k=v"}}},
			},
			wantContains: []string{
				"dir op holds a foreign-kind payload",
				"FilePayload",
				"exclusive to file",
			},
		},
		{
			name: "cron op holds a systemd_timer payload",
			op: Op{
				Op:      KindCron,
				ID:      "Cron[root/backup]",
				Payload: SystemdTimerPayload{OnCalendar: "daily"},
			},
			wantContains: []string{
				"cron op holds a foreign-kind payload",
				"SystemdTimerPayload",
				"exclusive to systemd_timer",
			},
		},
		{
			name: "package op holds a user payload",
			op: Op{
				Op:      KindPackage,
				ID:      "Package[helix]",
				Payload: UserPayload{Home: "/home/x"},
			},
			wantContains: []string{
				"package op holds a foreign-kind payload",
				"UserPayload",
				"exclusive to user",
			},
		},
		{
			// A kind with no migrated payload of its own at all (KindTimer
			// has no OpPayloadExamples() entry) must be refused exactly the
			// same way a wrong-kind pairing between two migrated kinds is —
			// any non-nil Payload on such a kind is already a mismatch.
			name: "control kind holds any payload at all",
			op: Op{
				Op:      KindTimer,
				ID:      "Timer[x]",
				Payload: LinkPayload{Symlink: "/x"},
			},
			wantContains: []string{
				"timer op holds a foreign-kind payload",
				"LinkPayload",
				"exclusive to link",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := EncodeOp(tc.op)
			if err == nil {
				t.Fatalf("EncodeOp(%+v) = %s, <nil>; want a foreign-payload refusal", tc.op, b)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("EncodeOp(%+v) error = %q, want it to contain %q", tc.op, err.Error(), want)
				}
			}
		})
	}
}

// TestEncodeRefusalPreventsForeignPayloadFromReachingDecode proves the
// task cg2 annotation's full "confusing works-at-record-time,
// fails-at-apply-time split" is closed end to end: the exact malformed Op
// that used to encode successfully and then be refused on its OWN next
// DecodeOp (the probe TestDecodeRefusesForeignKindFields's "keyed lines on
// a dir op" case would refuse, if it were ever handed to Decode) is now
// refused at EncodeOp itself — so DecodeOp is never even reached with it,
// and api.EncodeRedactedPreview and plan.RequiredVersion's header, which
// operate downstream of encode, are equally protected by construction: with
// no wire line ever produced, there is nothing left for them to see.
func TestEncodeRefusalPreventsForeignPayloadFromReachingDecode(t *testing.T) {
	t.Parallel()
	op := Op{Op: KindDir, ID: "Directory[/d]", Path: "/d", Payload: FilePayload{KeyedLines: []KeyedLine{{Key: "k", Line: "k=v"}}}}

	b, encErr := EncodeOp(op)
	if encErr == nil {
		t.Fatalf("EncodeOp(%+v) unexpectedly succeeded: %s", op, b)
	}
	if len(b) != 0 {
		t.Fatalf("EncodeOp(%+v) returned a non-empty line alongside its error: %s", op, b)
	}

	// The line DecodeOp would have refused, had EncodeOp not already
	// refused it first (pre-fix wire shape, reconstructed by hand to show
	// what used to reach the wire — see the task 2f2/cg2 annotations for
	// the actual byte-identical probe output).
	preFixLine := `{"op":"dir","id":"Directory[/d]","path":"/d","keyed_lines":[{"key":"k","line":"k=v"}]}`
	if _, decErr := DecodeOp([]byte(preFixLine)); decErr == nil {
		t.Fatalf("DecodeOp(%s) unexpectedly succeeded; checkForeignPayload should still refuse it", preFixLine)
	} else if !strings.Contains(decErr.Error(), "keyed_lines (file-exclusive)") {
		t.Fatalf("DecodeOp(%s) error = %q, want it to name keyed_lines as file-exclusive", preFixLine, decErr.Error())
	}
}

// checkPayloadOf pins PayloadOf's contract (task rf2) for one concrete
// OpPayload type T: the correctly-typed value round-trips unchanged, and
// both a nil Payload and a different kind's payload degrade to the zero T
// instead of panicking — the exact contract PayloadOf's own doc comment
// promises, previously spelled out by hand in each of the 16 comma-ok
// copies this helper's caller replaces.
func checkPayloadOf[T OpPayload](t *testing.T, kind Kind, want T) {
	t.Helper()
	if got := PayloadOf[T](Op{Op: kind, Payload: want}); !reflect.DeepEqual(got, want) {
		t.Fatalf("PayloadOf[%T] on its own kind = %#v, want %#v", want, got, want)
	}
	var zero T
	if got := PayloadOf[T](Op{Op: kind}); !reflect.DeepEqual(got, zero) {
		t.Fatalf("PayloadOf[%T] on a nil Payload = %#v, want the zero value %#v", want, got, zero)
	}
	if got := PayloadOf[T](Op{Op: kind, Payload: foreignPayload(want)}); !reflect.DeepEqual(got, zero) {
		t.Fatalf("PayloadOf[%T] on a %T Payload = %#v, want the zero value %#v", want, foreignPayload(want), got, zero)
	}
}

// foreignPayload returns some OpPayload value whose concrete type differs
// from avoid's, for checkPayloadOf's mistyped-Payload case.
func foreignPayload(avoid OpPayload) OpPayload {
	if _, ok := avoid.(CronPayload); !ok {
		return CronPayload{CronUser: "someone-else"}
	}
	return SystemdTimerPayload{OnCalendar: "someone-else"}
}

// TestPayloadOfMatchesEveryMigratedKind exercises PayloadOf for every kind
// OpPayloadExamples() lists, driven off that same inventory (this
// codebase's established single source of truth for "which kinds have a
// migrated payload," already relied on by
// TestCheckForeignPayloadCoversEveryMigratedKind just above) so a future
// kind migration that forgets a case in the switch below fails this test
// loudly instead of silently shipping an unverified PayloadOf instantiation.
func TestPayloadOfMatchesEveryMigratedKind(t *testing.T) {
	t.Parallel()
	for kind, example := range OpPayloadExamples() {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			switch example.(type) {
			case CronPayload:
				checkPayloadOf(t, kind, CronPayload{CronUser: "root", Schedule: "* * * * *"})
			case SystemdTimerPayload:
				checkPayloadOf(t, kind, SystemdTimerPayload{OnCalendar: "daily"})
			case UserPayload:
				checkPayloadOf(t, kind, UserPayload{Home: "/home/x", CreateHome: true})
			case LinkPayload:
				checkPayloadOf(t, kind, LinkPayload{Symlink: "/x"})
			case LinkIfExistsPayload:
				checkPayloadOf(t, kind, LinkIfExistsPayload{Target: "/x"})
			case PackagePayload:
				checkPayloadOf(t, kind, PackagePayload{Latest: true})
			case CommandPayload:
				checkPayloadOf(t, kind, CommandPayload{Bin: "true", Args: []string{"-v"}})
			case ConfigSetPayload:
				checkPayloadOf(t, kind, ConfigSetPayload{Chroot: "/etc"})
			case ConfigSetMemberPayload:
				checkPayloadOf(t, kind, ConfigSetMemberPayload{Member: "m"})
			case SyncDirPayload:
				checkPayloadOf(t, kind, SyncDirPayload{Glob: true, SourceDir: "assets"})
			case FilePayload:
				checkPayloadOf(t, kind, FilePayload{HasContent: true, ContentB64: "eA=="})
			default:
				t.Fatalf("OpPayloadExamples()[%s] = %T: add a checkPayloadOf case for it in this test", kind, example)
			}
		})
	}
}

// TestCheckForeignPayloadCoversEveryMigratedKind guards checkForeignPayload
// itself: payloadFieldOwners is built once, by reflecting
// OpPayloadExamples() (buildPayloadFieldOwners, op_payload.go). It must
// never end up empty — a nil/empty map would make checkForeignPayload a
// silent no-op again, the exact bug this task fixes — and must have
// exactly one entry per field declared across every OpPayloadExamples()
// entry, so a future kind migration that extends OpPayloadExamples() is
// automatically covered without touching this test.
func TestCheckForeignPayloadCoversEveryMigratedKind(t *testing.T) {
	t.Parallel()
	want := 0
	for _, example := range OpPayloadExamples() {
		want += reflect.TypeOf(example).NumField()
	}
	if want == 0 {
		t.Fatal("OpPayloadExamples() has no fields to guard; test setup is broken")
	}
	if got := len(payloadFieldOwners); got != want {
		t.Fatalf("len(payloadFieldOwners) = %d, want %d (one entry per OpPayloadExamples field)", got, want)
	}
}

// wireRawMessageType is fillEveryWireField's special case for wireOp's one
// json.RawMessage field (TemplateData): its Kind() is Slice (RawMessage is
// defined as []byte), so it must be matched before the general
// "slice of non-byte elements" case below, exactly like
// api/secret_fields.go's rawMessageType does for the same field reached
// through FilePayload.
var wireRawMessageType = reflect.TypeOf(json.RawMessage(nil))

// fillEveryWireField recursively sets every exported field reachable from
// v to a distinguishable non-zero value, keyed by its struct-field path
// (e.g. ".KeyedLines[].Key"). It is TestWireFieldRoundTripOwnership's
// filler: unlike api/secret_fields_test.go's fillValue (which the scan/
// redact walkers only ever feed strings, so it leaves bool/int fields at
// their zero value on purpose), this one must also set every bool and int
// field, since the totality test below asserts on ALL of wireOp's fields,
// not just the string-bearing ones.
func fillEveryWireField(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	switch {
	case v.Type() == wireRawMessageType:
		v.SetBytes([]byte(`{"k":"v:` + path + `"}`))
	case v.Kind() == reflect.String:
		v.SetString("wire:" + path)
	case v.Kind() == reflect.Bool:
		v.SetBool(true)
	case v.Kind() == reflect.Int:
		v.SetInt(7)
	case v.Kind() == reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillEveryWireField(t, v.Elem(), path)
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillEveryWireField(t, v.Index(0), path+"[]")
	case v.Kind() == reflect.Map:
		m := reflect.MakeMap(v.Type())
		key := reflect.ValueOf("k:" + path).Convert(v.Type().Key())
		val := reflect.ValueOf("v:" + path).Convert(v.Type().Elem())
		m.SetMapIndex(key, val)
		v.Set(m)
	case v.Kind() == reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			fillEveryWireField(t, v.Field(i), path+"."+f.Name)
		}
	default:
		t.Fatalf("wireOp%s has unhandled type %s; teach fillEveryWireField to fill it", path, v.Type())
	}
}

// TestWireFieldRoundTripOwnership is the mechanical totality guard task 5f2
// was opened to add. The Op field inventory is duplicated four ways with no
// compiler or test tying them together: plan/types.go (Op's own core
// struct), wire.go (wireOp, documented as "an exact, frozen, same-order
// copy"), op_payload.go (toWire/fromWire, plus each kind's own
// payloadConstructors entry and applyToWire method), and — one level
// further out — api's opFieldClasses/TestOpFieldClassesAreExhaustive.
// Forgetting one kind's "X: op.X" copy in toWire, or its
// payloadConstructors entry, compiles cleanly and silently drops field X
// from every recorded plan of that kind forever — exactly the regression
// task 2f2 found and fixed (TestDecodeRefusesForeignKindFields above), just
// approached from the opposite direction: 2f2 guards against a FOREIGN
// field surviving decode when it should have been refused; this test
// guards against an OWNED field failing to survive the fromWire/toWire
// round trip in the first place, mechanically, for every wireOp field and
// every Kind — not only the Chroot/Creates fields the pre-existing
// api.opFieldClasses fitness tests happen to exercise today.
//
// For every plan.AllKinds() entry (so a brand-new Kind with no
// OpPayloadExamples() entry yet is covered exactly like every migrated
// one, and KindEnsureFile's deliberate "no payload at all" case —
// FilePayload's own doc comment — is exercised too): fill a wireOp so
// EVERY field, not just the ones some other pass happens to touch, carries
// a distinguishable non-zero value, set its Op to the Kind under test, and
// run it through fromWire then back through toWire. A field must come out
// exactly as it went in when the decoded Kind owns it (every core field,
// plus — only for the matching Kind — that Kind's own OpPayload-exclusive
// fields), and must come out as the field type's zero value for every
// field some OTHER kind owns exclusively.
//
// Ownership is computed purely by reflecting wireOp itself and
// payloadFieldOwners (built, in turn, by reflecting OpPayloadExamples() —
// this codebase's established single source of truth for "which kind owns
// which wire field", already trusted by checkForeignPayload,
// TestWirePayloadTagsMatch and api's TestOpFieldClassesAreExhaustive): no
// second, hand-maintained field list of its own, so this test needs no
// update when OpPayloadExamples() gains an entry for a future kind
// migration — it is covered automatically, the same way
// TestCheckForeignPayloadCoversEveryMigratedKind already is.
// assertWireFieldRoundTrip checks one wireOp field's fromWire/toWire round
// trip against its expected owner: kind's own fields (core, or exclusive to
// kind itself per owner/hasOwner) must survive unchanged; a field exclusive
// to some OTHER kind's payload must come back as its zero value (see
// TestWireFieldRoundTripOwnership's own doc comment for why). Split out as
// its own unit, rather than inlined in that test's loop, per CLAUDE.md's
// refactor-at-50-lines guidance.
func assertWireFieldRoundTrip(t *testing.T, kind Kind, f reflect.StructField, wantVal, gotVal any, owner payloadFieldOwner, hasOwner bool) {
	t.Helper()
	if !hasOwner || owner.kind == kind {
		// A core field (shared by every kind, or not yet migrated onto any
		// payload), or one of THIS kind's own payload-exclusive fields:
		// must survive the round trip unchanged.
		if !reflect.DeepEqual(gotVal, wantVal) {
			t.Errorf("kind %q: field %q must round-trip unchanged (core, or %s's own), got %#v, want %#v",
				kind, f.Name, kind, gotVal, wantVal)
		}
		return
	}

	// Exclusive to some OTHER kind's payload: fromWire must not have kept
	// it (payloadFromWire only reads w.Op's own entry), and toWire must
	// not have written it back (only op.Payload.applyToWire, for w.Op's
	// own concrete payload type, ever runs). A forgotten kind guard
	// anywhere in that chain — or a forgotten copy in toWire/fromWire's
	// own core-field literals — surfaces here as a non-zero value that
	// should be zero.
	zero := reflect.Zero(f.Type).Interface()
	if !reflect.DeepEqual(gotVal, zero) {
		t.Errorf("kind %q: field %q is %s-exclusive but survived fromWire/toWire as %#v (want the zero value %#v): "+
			"a coordinated edit site (toWire, fromWire, payloadConstructors, or applyToWire) is missing its kind guard",
			kind, f.Name, owner.kind, gotVal, zero)
	}
}

func TestWireFieldRoundTripOwnership(t *testing.T) {
	t.Parallel()
	wt := reflect.TypeOf(wireOp{})

	for _, kind := range AllKinds() {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			var w wireOp
			fillEveryWireField(t, reflect.ValueOf(&w).Elem(), "")
			// Fix the kind under test AFTER the generic fill, which would
			// otherwise leave it some unrelated non-empty string: w.Op is
			// what payloadFromWire (via fromWire) and toWire's Payload
			// dispatch both key off.
			w.Op = kind

			got, err := fromWire(w).toWire()
			if err != nil {
				// fromWire always builds a payload matching its own w.Op via
				// payloadFromWire/payloadConstructors, so toWire's ownership
				// check (task cg2) must never refuse round-tripped output.
				t.Fatalf("kind %q: toWire refused a legitimately round-tripped op: %v", kind, err)
			}

			wv := reflect.ValueOf(w)
			gv := reflect.ValueOf(got)
			for i := range wt.NumField() {
				f := wt.Field(i)
				owner, hasOwner := payloadFieldOwners[f.Name]
				assertWireFieldRoundTrip(t, kind, f, wv.Field(i).Interface(), gv.Field(i).Interface(), owner, hasOwner)
			}
		})
	}
}
