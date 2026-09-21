// Package dnszone parses immutable DNS zone candidates for publishers that
// own the surrounding lock and transaction. It deliberately does not write
// files, choose roles, advance state, or reload a name server.
package dnszone

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/miekg/dns"
)

// Successor returns the RFC 1982 successor of serial. uint32 overflow is the
// required wraparound from 4294967295 to 0.
func Successor(serial uint32) uint32 { return serial + 1 }

// Serial returns the serial from the sole apex SOA in zone. The same strict
// SOA invariant used by Equivalent (enforced by the shared parseZone) prevents
// a publisher from advancing a serial picked from an unrelated record or
// comment. Non-SOA records are parsed for syntax but never packed.
func Serial(zone []byte, origin string) (uint32, error) {
	origin, err := normalizeOrigin(origin)
	if err != nil {
		return 0, err
	}
	var serial uint32
	err = parseZone(zone, origin, func(rr dns.RR) error {
		// parseZone guarantees exactly one apex SOA, so the last (and only)
		// SOA seen is the answer.
		if soa, isSOA := rr.(*dns.SOA); isSOA {
			serial = soa.Serial
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return serial, nil
}

// Equivalent reports whether candidate and committed contain the same parsed
// RRs, apart from the apex SOA serial and RR order. Both inputs must be
// complete, parseable zones with exactly one apex SOA and no non-apex SOA.
//
// This deliberately compares the DNS library's wire rendering rather than
// attempting partial DNS canonicalization. A partial canonicalizer can mistake
// opaque fields (such as TXT) for domain names and suppress a real change.
// Callers must therefore render stable owner and RDATA spelling in templates.
func Equivalent(candidate, committed []byte, origin string) (bool, error) {
	// The origin is validated once for both zones. An invalid origin used to
	// surface from the candidate parse first, so it keeps the "candidate
	// zone:" prefix to leave the CLI's error text unchanged.
	origin, err := normalizeOrigin(origin)
	if err != nil {
		return false, fmt.Errorf("candidate zone: %w", err)
	}
	candidateRecords, err := canonical(candidate, origin)
	if err != nil {
		return false, fmt.Errorf("candidate zone: %w", err)
	}
	committedRecords, err := canonical(committed, origin)
	if err != nil {
		return false, fmt.Errorf("committed zone: %w", err)
	}
	if len(candidateRecords) != len(committedRecords) {
		return false, nil
	}
	for i := range candidateRecords {
		if !bytes.Equal(candidateRecords[i], committedRecords[i]) {
			return false, nil
		}
	}
	return true, nil
}

// normalizeOrigin returns origin as a fully qualified domain name, or an error
// when it is not a valid domain name.
func normalizeOrigin(origin string) (string, error) {
	origin = dns.Fqdn(origin)
	if _, ok := dns.IsDomainName(origin); !ok {
		return "", fmt.Errorf("invalid origin %q", origin)
	}
	return origin, nil
}

// parseZone parses zone relative to the already normalized origin and calls
// visit for every RR in file order. It owns the invariants Serial and
// Equivalent share: every SOA must sit at the apex (checked before visit sees
// it), there must be exactly one of them, and any parse error fails the zone.
// A visit error stops parsing and is returned unchanged.
func parseZone(zone []byte, origin string, visit func(dns.RR) error) error {
	parser := dns.NewZoneParser(bytes.NewReader(zone), origin, "")
	soaCount := 0
	for rr, more := parser.Next(); more; rr, more = parser.Next() {
		// The zone parser converts RFC 3597 generic syntax for known types
		// into the concrete type, so every SOA arrives as *dns.SOA.
		if soa, isSOA := rr.(*dns.SOA); isSOA {
			if !isApex(soa.Hdr.Name, origin) {
				return fmt.Errorf("SOA owner %q is not apex %q", soa.Hdr.Name, origin)
			}
			soaCount++
		}
		if err := visit(rr); err != nil {
			return err
		}
	}
	if err := parser.Err(); err != nil {
		return err
	}
	if soaCount != 1 {
		return fmt.Errorf("want exactly one apex SOA, got %d", soaCount)
	}
	return nil
}

// isApex reports whether name and origin are the same domain (case-insensitive,
// as dns.IsSubDomain compares), i.e. each is a subdomain of the other.
func isApex(name, origin string) bool {
	return dns.IsSubDomain(origin, name) && dns.IsSubDomain(name, origin)
}

// canonical returns the sorted wire renderings of every RR in zone, with the
// apex SOA serial zeroed so that serial bumps alone never count as a change.
// origin must already be normalized by normalizeOrigin.
//
// One scratch buffer of the largest possible RR size is reused for packing and
// each record gets a right-sized copy of its bytes. Slicing a per-record 64 KiB
// buffer instead would keep every whole buffer reachable, so a 1000-record zone
// would retain about 64 MB (twice that while Equivalent holds both zones).
func canonical(zone []byte, origin string) ([][]byte, error) {
	scratch := make([]byte, dns.MaxMsgSize)
	var records [][]byte
	err := parseZone(zone, origin, func(rr dns.RR) error {
		rrCopy := dns.Copy(rr)
		if soa, isSOA := rrCopy.(*dns.SOA); isSOA {
			soa.Serial = 0
		}
		n, err := dns.PackRR(rrCopy, scratch, 0, nil, false)
		if err != nil {
			return fmt.Errorf("pack %s: %w", rrCopy.Header().Name, err)
		}
		// Clone rather than keep scratch[:n]: a record must neither alias the
		// scratch buffer (the next PackRR would overwrite it) nor keep the
		// 64 KiB scratch array alive after canonical returns.
		records = append(records, bytes.Clone(scratch[:n]))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool { return bytes.Compare(records[i], records[j]) < 0 })
	return records, nil
}
