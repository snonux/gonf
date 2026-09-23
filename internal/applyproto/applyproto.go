// Package applyproto is the one authoritative definition of the "gonf
// apply" wire contract shared by an elevated (sudo/doas) local re-exec and
// a remote push/preview apply session: the two internal apply-subcommand
// flag names, the cancel-pipe protocol's one byte, and the argv shape each
// carries.
//
// Before this package existed, the contract was spelled independently on
// each side: the producers (api's elevatedApplyArgv, for the local
// elevated re-exec; internal/remote's remoteApplyCmd, for a push/preview
// apply session) each built "-cancel-pipe"/"-relayed" as a literal string,
// and the consumer (internal/cli's cliApply) registered
// fs.Bool("cancel-pipe", ...) / fs.Bool("relayed", ...) as separate
// literals of its own. Renaming or mis-spelling either flag on ONE side
// only failed at RUNTIME — "flag provided but not defined" at the far end
// of every privileged apply and every push — and the pre-existing
// elevatedApplyArgv unit test pinned only the producer's spelling, so a
// rename inside internal/cli alone left the whole suite green. Every
// producer and the consumer now import this package's constants/builders
// instead of spelling their own, so a rename requires touching exactly one
// place, and internal/cli's fitness test (TestParseApplyFlagsAcceptsProducerArgv)
// parses the exact argv shape these builders produce.
package applyproto

import "flag"

// CancelPipeFlag is the apply-subcommand flag name (no leading dash) set
// only on the local elevated (sudo/doas) re-exec child: it tells that
// child to treat its stdin as an out-of-band cancel channel (see
// CancelByte). Producer: api's elevatedApplyArgv (via ElevatedArgs below).
// Consumer: internal/cli's cliApply flag set (via RegisterFlags below).
const CancelPipeFlag = "cancel-pipe"

// RelayedFlag is the apply-subcommand flag name (no leading dash) set only
// on a remote apply session (the receiving end of a push or a strict
// preview): it tells that child its stdout/stderr are being relayed back
// to the controller over ssh, so it should ignore SIGPIPE for its whole
// run rather than die mid-apply if the controller vanishes. Producer:
// internal/remote's remoteApplyCmd (via RelayedArgs below). Consumer:
// internal/cli's cliApply flag set (via RegisterFlags below).
const RelayedFlag = "relayed"

// CancelByte is the one byte the controller writes to the cancel pipe to
// signal a deliberate cancel of the local elevated re-exec (task 6d2); its
// value carries no meaning, only its presence does. Producer:
// api.wireElevatedCancelPipe. Consumer: internal/cli's watchCancelPipe,
// which (unchanged by this package) cancels on any successful read rather
// than checking the byte against this constant — a bare EOF with no byte
// ever read is what it distinguishes as "not a cancel", not the byte's
// value; see watchCancelPipe's own doc comment.
const CancelByte byte = 1

// ElevatedArgs returns the apply-subcommand argument tail that follows
// "apply" in the local elevated re-exec's argv (api's elevatedApplyArgv):
// "-cancel-pipe" always, plus "-n" when dryRun. Building it here, instead
// of at the call site, is what makes elevatedApplyArgv's use of
// CancelPipeFlag structural rather than a second literal that could drift
// from this package's spelling.
func ElevatedArgs(dryRun bool) []string {
	args := []string{"-" + CancelPipeFlag}
	if dryRun {
		args = append(args, "-n")
	}
	return args
}

// RelayedArgs returns the apply-subcommand argument tail that follows
// "apply" in a remote apply session's command line (internal/remote's
// remoteApplyCmd): "-relayed [-apply-dir <dir>] <stdinArg>". applyDir ""
// omits "-apply-dir". Building it here (rather than remoteApplyCmd
// constructing the "-relayed ..." string itself, twice, once per branch)
// also removes that duplication.
func RelayedArgs(applyDir, stdinArg string) string {
	args := "-" + RelayedFlag + " "
	if applyDir != "" {
		args += "-apply-dir " + applyDir + " "
	}
	return args + stdinArg
}

// RegisterFlags registers the two wire-contract flags on fs (internal/cli's
// "apply" subcommand flag set) under CancelPipeFlag/RelayedFlag, so the
// consumer side can never drift from this package's spelling by a
// hand-typed literal. usage strings are supplied by the caller (cliApply
// carries the full operator-facing explanation; this package only owns the
// name and the wire shape).
func RegisterFlags(fs *flag.FlagSet, cancelPipeUsage, relayedUsage string) (cancelPipe, relayed *bool) {
	return fs.Bool(CancelPipeFlag, false, cancelPipeUsage), fs.Bool(RelayedFlag, false, relayedUsage)
}
