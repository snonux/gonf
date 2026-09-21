package dnszone

import (
	"fmt"
	"testing"
)

func TestSuccessorRFC1982(t *testing.T) {
	tests := []struct {
		serial uint32
		want   uint32
	}{
		{serial: 0, want: 1},
		{serial: 1, want: 2},
		{serial: 4_294_967_294, want: 4_294_967_295},
		{serial: 4_294_967_295, want: 0},
	}
	for _, test := range tests {
		t.Run(fmt.Sprint(test.serial), func(t *testing.T) {
			if got := Successor(test.serial); got != test.want {
				t.Fatalf("Successor(%d) = %d, want %d", test.serial, got, test.want)
			}
		})
	}
}

func TestSerialRequiresOneApexSOA(t *testing.T) {
	zone := []byte(`$ORIGIN example.test.
@ IN SOA ns.example.test. hostmaster.example.test. ( 4294967295 1h 30m 7d 1h )
@ IN NS ns.example.test.
`)
	serial, err := Serial(zone, "example.test")
	if err != nil {
		t.Fatalf("Serial: %v", err)
	}
	if serial != 4_294_967_295 {
		t.Fatalf("Serial = %d, want 4294967295", serial)
	}
	invalidZones := [][]byte{
		[]byte("$ORIGIN example.test.\n@ IN NS ns.example.test.\n"),
		append(zone, zone...),
		[]byte("$ORIGIN example.test.\nother IN SOA ns.example.test. hostmaster.example.test. ( 1 1h 30m 7d 1h )\n"),
	}
	for _, invalid := range invalidZones {
		if _, err := Serial(invalid, "example.test"); err == nil {
			t.Fatal("Serial accepted a missing, duplicate, or non-apex SOA")
		}
	}
}

func TestEquivalentIgnoresRecordOrderAndSOASerial(t *testing.T) {
	committed := []byte(`$ORIGIN example.test.
$TTL 1h
@ IN SOA ns.example.test. hostmaster.example.test. (
  4294967295 ; serial
  1h 30m 7d 1h )
@ IN NS ns.example.test.
www 300 IN A 192.0.2.1
txt IN TXT "Case Sensitive"
`)
	candidate := []byte(`$TTL 3600
$ORIGIN example.test.
txt IN TXT "Case Sensitive" ; comments do not publish
www 300 IN A 192.0.2.1
@ IN NS ns.example.test.
@ IN SOA ns.example.test. hostmaster.example.test. (
  0 ; serial
  3600 1800 604800 3600 )
`)
	equal, err := Equivalent(candidate, committed, "example.test")
	if err != nil {
		t.Fatalf("Equivalent: %v", err)
	}
	if !equal {
		t.Fatal("record ordering and SOA serial differences must not publish")
	}
}

func TestEquivalentTreatsCaseChangesConservatively(t *testing.T) {
	committed := []byte(`$ORIGIN example.test.
@ IN SOA ns.example.test. hostmaster.example.test. ( 1 1h 30m 7d 1h )
@ IN NS ns.example.test.
`)
	candidate := []byte(`$ORIGIN example.test.
@ IN SOA NS.EXAMPLE.TEST. hostmaster.example.test. ( 2 1h 30m 7d 1h )
@ IN NS ns.example.test.
`)
	equal, err := Equivalent(candidate, committed, "example.test")
	if err != nil {
		t.Fatalf("Equivalent: %v", err)
	}
	if equal {
		t.Fatal("case change must publish until every DNS RDATA form is safely canonicalized")
	}
}

func TestEquivalentDetectsEffectiveChange(t *testing.T) {
	oldZone := []byte(`$ORIGIN example.test.
@ 300 IN SOA ns.example.test. hostmaster.example.test. ( 1 1h 30m 7d 1h )
@ IN NS ns.example.test.
www 300 IN A 192.0.2.1
`)
	newZone := []byte(`$ORIGIN example.test.
@ 300 IN SOA ns.example.test. hostmaster.example.test. ( 2 1h 30m 7d 1h )
@ IN NS ns.example.test.
www 300 IN A 192.0.2.2
`)
	equal, err := Equivalent(newZone, oldZone, "example.test")
	if err != nil {
		t.Fatalf("Equivalent: %v", err)
	}
	if equal {
		t.Fatal("changed RDATA must publish")
	}
}

func TestEquivalentRejectsMalformedOrAmbiguousSOA(t *testing.T) {
	valid := []byte(`$ORIGIN example.test.
@ IN SOA ns.example.test. hostmaster.example.test. ( 1 1h 30m 7d 1h )
@ IN NS ns.example.test.
`)
	tests := []struct {
		name string
		zone []byte
	}{
		{name: "missing SOA", zone: []byte("$ORIGIN example.test.\n@ IN NS ns.example.test.\n")},
		{name: "duplicate SOA", zone: append(valid, valid...)},
		{name: "non-apex SOA", zone: []byte("$ORIGIN example.test.\nother IN SOA ns.example.test. hostmaster.example.test. ( 1 1h 30m 7d 1h )\n")},
		{name: "parse failure", zone: []byte("$ORIGIN example.test.\n@ IN A not-an-address\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Equivalent(test.zone, valid, "example.test"); err == nil {
				t.Fatal("Equivalent succeeded for invalid candidate")
			}
		})
	}
}

// TestCanonicalRecordsAreRightSized guards against retaining a 64 KiB pack
// buffer per record: every canonical record must own a small copy of its wire
// bytes (not a slice of a pack buffer), so memory stays proportional to the
// zone's size rather than to 64 KiB times its record count.
func TestCanonicalRecordsAreRightSized(t *testing.T) {
	zone := []byte(`$ORIGIN example.test.
@ IN SOA ns.example.test. hostmaster.example.test. ( 7 1h 30m 7d 1h )
@ IN NS ns.example.test.
www 300 IN A 192.0.2.1
txt IN TXT "hello"
`)
	records, err := canonical(zone, "example.test.")
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("canonical returned %d records, want 4", len(records))
	}
	// The allocator may round capacity up to a size class, so assert that
	// capacity is far below the 64 KiB pack buffer rather than equal to len.
	const maxRecordCap = 1024
	for i, record := range records {
		if len(record) == 0 || cap(record) > maxRecordCap {
			t.Fatalf("record %d: len %d cap %d, want a non-empty copy with cap <= %d", i, len(record), cap(record), maxRecordCap)
		}
	}
	// Records must not alias one another or a shared scratch buffer: mutating
	// one record must leave every other record intact.
	snapshot := make([]string, len(records))
	for i, record := range records {
		snapshot[i] = string(record)
	}
	for i := range records[0] {
		records[0][i] ^= 0xff
	}
	for i := 1; i < len(records); i++ {
		if string(records[i]) != snapshot[i] {
			t.Fatalf("record %d shares a backing array with record 0", i)
		}
	}
}

// TestZoneErrorTextsArePreserved pins the error messages the dns-zone-*
// CLI commands print, so the shared parser keeps them stable for both entry
// points.
func TestZoneErrorTextsArePreserved(t *testing.T) {
	valid := []byte(`$ORIGIN example.test.
@ IN SOA ns.example.test. hostmaster.example.test. ( 1 1h 30m 7d 1h )
`)
	nonApex := []byte("$ORIGIN example.test.\nother IN SOA ns.example.test. hostmaster.example.test. ( 1 1h 30m 7d 1h )\n")
	missing := []byte("$ORIGIN example.test.\n@ IN NS ns.example.test.\n")
	duplicate := append(append([]byte{}, valid...), valid...)
	// malformed has a valid apex SOA, so only the parser error can reject it;
	// without the SOA a "got 0" invariant error would mask a missing
	// parser.Err() check.
	malformed := append(append([]byte{}, valid...), "www IN A bad\n"...)
	const parseErr = `dns: bad A A: "bad" at line: 3:12`
	serialTests := []struct {
		name, origin string
		zone         []byte
		want         string
	}{
		{"invalid origin", "bad..origin", valid, `invalid origin "bad..origin."`},
		{"non-apex SOA", "example.test", nonApex, `SOA owner "other.example.test." is not apex "example.test."`},
		{"missing SOA", "example.test", missing, "want exactly one apex SOA, got 0"},
		{"duplicate SOA", "example.test", duplicate, "want exactly one apex SOA, got 2"},
		{"parse failure after SOA", "example.test", malformed, parseErr},
	}
	for _, test := range serialTests {
		t.Run("Serial/"+test.name, func(t *testing.T) {
			_, err := Serial(test.zone, test.origin)
			if err == nil || err.Error() != test.want {
				t.Fatalf("Serial error = %v, want %q", err, test.want)
			}
		})
	}
	equivalentTests := []struct {
		name, origin         string
		candidate, committed []byte
		want                 string
	}{
		{"invalid origin", "bad..origin", valid, valid, `candidate zone: invalid origin "bad..origin."`},
		{"candidate non-apex", "example.test", nonApex, valid, `candidate zone: SOA owner "other.example.test." is not apex "example.test."`},
		{"committed missing", "example.test", valid, missing, "committed zone: want exactly one apex SOA, got 0"},
		{"committed duplicate", "example.test", valid, duplicate, "committed zone: want exactly one apex SOA, got 2"},
		{"candidate parse failure", "example.test", malformed, valid, "candidate zone: " + parseErr},
		{"committed parse failure", "example.test", valid, malformed, "committed zone: " + parseErr},
	}
	for _, test := range equivalentTests {
		t.Run("Equivalent/"+test.name, func(t *testing.T) {
			_, err := Equivalent(test.candidate, test.committed, test.origin)
			if err == nil || err.Error() != test.want {
				t.Fatalf("Equivalent error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestSerialIgnoresNonSOARecords documents that Serial only inspects the SOA:
// other records are parsed but never packed, and their order relative to the
// SOA does not matter.
func TestSerialIgnoresNonSOARecords(t *testing.T) {
	zone := []byte(`$ORIGIN example.test.
www IN A 192.0.2.1
@ IN SOA ns.example.test. hostmaster.example.test. ( 42 1h 30m 7d 1h )
mail IN MX 10 mx.example.test.
`)
	serial, err := Serial(zone, "example.test.")
	if err != nil || serial != 42 {
		t.Fatalf("Serial = %d, %v; want 42, nil", serial, err)
	}
}
