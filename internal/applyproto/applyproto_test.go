package applyproto

import (
	"flag"
	"testing"
)

// TestWireContractSpelling pins the exact flag names and cancel byte this
// package is the sole source of truth for: a change here is a deliberate,
// single-place rename of the apply wire contract, not an accidental drift
// that only one of the producers (api's elevatedApplyArgv,
// internal/remote's remoteApplyCmd) or the consumer (internal/cli's
// cliApply) picks up.
func TestWireContractSpelling(t *testing.T) {
	if CancelPipeFlag != "cancel-pipe" {
		t.Fatalf("CancelPipeFlag = %q, want %q", CancelPipeFlag, "cancel-pipe")
	}
	if RelayedFlag != "relayed" {
		t.Fatalf("RelayedFlag = %q, want %q", RelayedFlag, "relayed")
	}
	if CancelByte != 1 {
		t.Fatalf("CancelByte = %d, want %d", CancelByte, 1)
	}
}

// TestElevatedArgs pins ElevatedArgs's shape: "-cancel-pipe" always, "-n"
// appended only for dryRun. api's elevatedApplyArgv appends this slice
// verbatim right after "apply".
func TestElevatedArgs(t *testing.T) {
	if got, want := ElevatedArgs(false), []string{"-cancel-pipe"}; !equalStrings(got, want) {
		t.Fatalf("ElevatedArgs(false) = %v, want %v", got, want)
	}
	if got, want := ElevatedArgs(true), []string{"-cancel-pipe", "-n"}; !equalStrings(got, want) {
		t.Fatalf("ElevatedArgs(true) = %v, want %v", got, want)
	}
}

// TestRelayedArgs pins RelayedArgs's shape: "-relayed " plus, when applyDir
// is set, "-apply-dir <dir> ", followed by stdinArg. internal/remote's
// remoteApplyCmd appends this string verbatim after "apply ".
func TestRelayedArgs(t *testing.T) {
	tests := []struct {
		name     string
		applyDir string
		stdinArg string
		want     string
	}{
		{"no apply-dir", "", "-", "-relayed -"},
		{"no apply-dir, dry-run stdin", "", "-n -", "-relayed -n -"},
		{"apply-dir", "/tmp/gonf-apply-sticky-plan", "-", "-relayed -apply-dir /tmp/gonf-apply-sticky-plan -"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RelayedArgs(tt.applyDir, tt.stdinArg); got != tt.want {
				t.Fatalf("RelayedArgs(%q, %q) = %q, want %q", tt.applyDir, tt.stdinArg, got, tt.want)
			}
		})
	}
}

// TestRegisterFlags proves RegisterFlags wires exactly CancelPipeFlag and
// RelayedFlag onto fs, under those names, as bool flags defaulting false.
func TestRegisterFlags(t *testing.T) {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	cancelPipe, relayed := RegisterFlags(fs, "cancel usage", "relayed usage")
	if *cancelPipe || *relayed {
		t.Fatalf("defaults: cancelPipe=%v relayed=%v, want both false", *cancelPipe, *relayed)
	}
	if err := fs.Parse([]string{"-" + CancelPipeFlag, "-" + RelayedFlag}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !*cancelPipe || !*relayed {
		t.Fatalf("after parse: cancelPipe=%v relayed=%v, want both true", *cancelPipe, *relayed)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
