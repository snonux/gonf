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
// SOA invariant used by Equivalent prevents a publisher from advancing a
// serial picked from an unrelated record or comment.
func Serial(zone []byte, origin string) (uint32, error) {
	origin = dns.Fqdn(origin)
	if _, ok := dns.IsDomainName(origin); !ok {
		return 0, fmt.Errorf("invalid origin %q", origin)
	}

	parser := dns.NewZoneParser(bytes.NewReader(zone), origin, "")
	var serial uint32
	soaCount := 0
	for rr, ok := parser.Next(); ok; rr, ok = parser.Next() {
		soa, ok := rr.(*dns.SOA)
		if !ok {
			continue
		}
		if !dns.IsSubDomain(origin, soa.Hdr.Name) || !dns.IsSubDomain(soa.Hdr.Name, origin) {
			return 0, fmt.Errorf("SOA owner %q is not apex %q", soa.Hdr.Name, origin)
		}
		soaCount++
		serial = soa.Serial
	}
	if err := parser.Err(); err != nil {
		return 0, err
	}
	if soaCount != 1 {
		return 0, fmt.Errorf("want exactly one apex SOA, got %d", soaCount)
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

func canonical(zone []byte, origin string) ([][]byte, error) {
	origin = dns.Fqdn(origin)
	if _, ok := dns.IsDomainName(origin); !ok {
		return nil, fmt.Errorf("invalid origin %q", origin)
	}

	parser := dns.NewZoneParser(bytes.NewReader(zone), origin, "")
	var records [][]byte
	soaCount := 0
	for rr, ok := parser.Next(); ok; rr, ok = parser.Next() {
		copy := dns.Copy(rr)
		header := copy.Header()
		if header.Rrtype == dns.TypeSOA {
			if !dns.IsSubDomain(origin, header.Name) || !dns.IsSubDomain(header.Name, origin) {
				return nil, fmt.Errorf("SOA owner %q is not apex %q", header.Name, origin)
			}
			soaCount++
			copy.(*dns.SOA).Serial = 0
		}
		wire := make([]byte, 65535)
		n, err := dns.PackRR(copy, wire, 0, nil, false)
		if err != nil {
			return nil, fmt.Errorf("pack %s: %w", header.Name, err)
		}
		records = append(records, wire[:n])
	}
	if err := parser.Err(); err != nil {
		return nil, err
	}
	if soaCount != 1 {
		return nil, fmt.Errorf("want exactly one apex SOA, got %d", soaCount)
	}
	sort.Slice(records, func(i, j int) bool { return bytes.Compare(records[i], records[j]) < 0 })
	return records, nil
}
