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
