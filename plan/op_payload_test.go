package plan

import (
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
