package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIDNSZoneEquivalent(t *testing.T) {
	dir := t.TempDir()
	committed := filepath.Join(dir, "committed.zone")
	candidate := filepath.Join(dir, "candidate.zone")
	if err := os.WriteFile(committed, []byte(`$ORIGIN example.test.
@ IN SOA ns.example.test. hostmaster.example.test. ( 1 1h 30m 7d 1h )
@ IN NS ns.example.test.
www IN A 192.0.2.1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte(`$ORIGIN example.test.
@ IN SOA ns.example.test. hostmaster.example.test. ( 2 1h 30m 7d 1h )
@ IN NS ns.example.test.
www IN A 192.0.2.1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := cliDNSZoneEquivalent([]string{"example.test", candidate, committed}); got != 0 {
		t.Fatalf("equivalent exit = %d, want 0", got)
	}
	if got := cliDNSZoneSerial([]string{"example.test", committed}); got != 0 {
		t.Fatalf("serial exit = %d, want 0", got)
	}
	if err := os.WriteFile(candidate, []byte(`$ORIGIN example.test.
@ IN SOA ns.example.test. hostmaster.example.test. ( 2 1h 30m 7d 1h )
@ IN NS ns.example.test.
www IN A 192.0.2.2
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := cliDNSZoneEquivalent([]string{"example.test", candidate, committed}); got != 1 {
		t.Fatalf("different exit = %d, want 1", got)
	}
	if got := cliDNSZoneEquivalent([]string{"example.test", filepath.Join(dir, "missing"), committed}); got != 2 {
		t.Fatalf("invalid input exit = %d, want 2", got)
	}
}
