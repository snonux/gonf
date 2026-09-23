package cli

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/applyproto"
)

// TestParseApplyFlagsAcceptsProducerArgv is task xd2's fitness test: it
// proves cliApply's real flag set (parseApplyFlags, which cliApply itself
// calls) parses the exact "apply" argument tail the two real producers
// build — api's elevatedApplyArgv (the local elevated sudo/doas re-exec)
// and internal/remote's remoteApplyCmd (a push/preview apply session) —
// instead of pinning only a producer's own spelling (as the pre-existing
// api.TestElevatedApplyArgvDryRun does) and leaving a consumer-side rename
// or typo to fail only at RUNTIME, on every privileged apply and every
// push, with a raw "flag provided but not defined" at the far end.
//
// It cannot call elevatedApplyArgv or remoteApplyCmd directly: they are
// unexported in api and internal/remote, and Go's package encapsulation
// means no single test file can reach unexported identifiers of two other
// packages at once (nor should this package export test-only hooks for a
// public package like api — see AGENTS.md's "Test seams"). Instead it
// builds the argv tail through internal/applyproto's ElevatedArgs/
// RelayedArgs — the exact, unmodified building blocks elevatedApplyArgv and
// remoteApplyCmd call to build that same tail (see their doc comments) —
// so this test is exercising the real, shared shape, not a hand-rolled
// approximation of it.
func TestParseApplyFlagsAcceptsProducerArgv(t *testing.T) {
	t.Run("elevated re-exec (api.elevatedApplyArgv)", func(t *testing.T) {
		for _, dryRun := range []bool{false, true} {
			args := append(applyproto.ElevatedArgs(dryRun), "/tmp/plan/chunk-elevated.jsonl")
			f, rest, err := parseApplyFlags(args)
			if err != nil {
				t.Fatalf("dryRun=%v: parseApplyFlags(%v): %v", dryRun, args, err)
			}
			if !f.cancelPipe {
				t.Fatalf("dryRun=%v: -cancel-pipe not recognized from %v", dryRun, args)
			}
			if f.dryRun != dryRun {
				t.Fatalf("dryRun=%v: parsed dryRun = %v", dryRun, f.dryRun)
			}
			if len(rest) != 1 || rest[0] != "/tmp/plan/chunk-elevated.jsonl" {
				t.Fatalf("dryRun=%v: rest = %v, want [plan path]", dryRun, rest)
			}
		}
	})

	t.Run("push/preview session (internal/remote.remoteApplyCmd)", func(t *testing.T) {
		tests := []struct {
			name     string
			applyDir string
			stdinArg string
		}{
			{"plain push", "", "-"},
			{"dry-run push", "", "-n -"},
			{"strict preview", "", "-n -strict-preview -"},
			{"sticky apply-dir", "/tmp/gonf-apply-sticky-plan", "-"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				// remoteApplyCmd appends this string, split on spaces, as
				// argv the same way a shell would (none of these arguments
				// contain spaces themselves).
				args := strings.Fields(applyproto.RelayedArgs(tt.applyDir, tt.stdinArg))
				f, rest, err := parseApplyFlags(args)
				if err != nil {
					t.Fatalf("parseApplyFlags(%v): %v", args, err)
				}
				if !f.relayed {
					t.Fatalf("-relayed not recognized from %v", args)
				}
				if f.applyDir != tt.applyDir {
					t.Fatalf("applyDir = %q, want %q", f.applyDir, tt.applyDir)
				}
				if len(rest) != 1 || rest[0] != "-" {
					t.Fatalf("rest = %v, want [\"-\"]", rest)
				}
			})
		}
	})
}
