package foostore

import (
	"bytes"
	"fmt"
	"time"

	"github.com/snonux/gonf/secret"
)

// The exit codes of foostore's machine read contract (version 1).
const (
	exitOK        = 0
	exitFailure   = 1 // unexpected failure, its own --timeout, a signal
	exitUsage     = 2
	exitNotFound  = 4 // the only code an optional lookup may suppress
	exitAmbiguous = 5
	exitLocked    = 6
	exitCorrupt   = 7
	exitIO        = 8
)

// usagePrefix starts the usage text `foostore read --help` prints under
// contract version 1; notFoundLine is the exit-code line that defines the
// suppressible code. Both must be present for the binary to pass the
// contract check.
const (
	usagePrefix  = "usage: foostore read "
	notFoundLine = "4 not found (the only suppressible code)"
)

// outcomes maps a contract exit code to its secret kind and a description
// that holds no output of foostore.
var outcomes = map[int]struct {
	kind error
	what string
}{
	exitFailure:   {secret.ErrUnavailable, "foostore read failed or timed out"},
	exitUsage:     {secret.ErrInvalid, "foostore refused the read as a usage error (reference not in exact form, or a field on an attachment)"},
	exitNotFound:  {secret.ErrNotFound, "foostore store has no such item"},
	exitAmbiguous: {secret.ErrInvalid, "foostore reference is ambiguous"},
	exitLocked:    {secret.ErrUnavailable, "foostore store is locked or its credentials are unusable"},
	exitCorrupt:   {secret.ErrUnavailable, "foostore store is corrupt"},
	exitIO:        {secret.ErrUnavailable, "foostore store I/O failed"},
}

// classify turns a finished read into nil (stdout is the secret) or a typed
// error naming ref and item. It never includes stdout or stderr content.
func classify(ref secret.Ref, item Item, res result, cfg Config) error {
	fail := func(kind error, format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		return &secret.Error{Kind: kind, Ref: ref,
			Err: fmt.Errorf("%s: %s%s", describe(item), msg, stderrNote(res))}
	}
	switch {
	case res.startErr != nil:
		return fail(secret.ErrUnavailable, "cannot run %q: %v", cfg.Binary, res.startErr)
	case res.timedOut:
		return fail(secret.ErrUnavailable, "killed after %v", cfg.Timeout+killGrace)
	case res.overflow:
		return fail(secret.ErrInvalid, "value exceeds %d bytes", cfg.MaxBytes)
	case res.exitCode == exitOK && res.waitErr != nil:
		// Exit 0 but the output pipes were not closed in time (a
		// descendant held them): stdout may be incomplete.
		return fail(secret.ErrUnavailable, "output incomplete: %v", res.waitErr)
	case res.exitCode == exitOK:
		return nil
	}
	if o, ok := outcomes[res.exitCode]; ok {
		return fail(o.kind, "%s (exit %d)", o.what, res.exitCode)
	}
	return fail(secret.ErrUnavailable, "unexpected foostore result (%s)", exitNote(res))
}

// probeError reports why the contract check failed, or nil when the binary
// printed contract version 1's usage. The usage text is not secret, but an
// older foostore may run an interactive search for "read" instead and print
// entry names, so the output is never included. timeout is the Provider's
// own probeTimeout, reported as-is so the message matches what actually
// bounded the check (a package test may have shrunk it).
func probeError(res result, binary string, timeout time.Duration) error {
	switch {
	case res.startErr != nil:
		return fmt.Errorf("cannot run %q: %w", binary, res.startErr)
	case res.timedOut:
		return fmt.Errorf("%q did not answer `read --help` within %v", binary, timeout)
	case res.exitCode != exitOK || res.waitErr != nil || res.overflow:
		return fmt.Errorf("%q does not implement the foostore machine read contract v1 (`read --help`: %s)", binary, exitNote(res))
	case !bytes.HasPrefix(res.stdout, []byte(usagePrefix)) || !bytes.Contains(res.stdout, []byte(notFoundLine)):
		return fmt.Errorf("%q does not implement the foostore machine read contract v1 (`read --help` printed no contract usage)", binary)
	}
	return nil
}

// describe names item for error messages: its foostore reference and field
// are logical names, not secrets.
func describe(item Item) string {
	if item.Field == "" {
		return fmt.Sprintf("foostore attachment %q", item.Reference)
	}
	return fmt.Sprintf("foostore entry %q field %q", item.Reference, item.Field)
}

// exitNote describes how the process ended, without its output: the exit
// code, or the Wait error ("signal: killed"), which carries none either.
func exitNote(res result) string {
	if res.exitCode >= 0 {
		return fmt.Sprintf("exit %d", res.exitCode)
	}
	if res.waitErr != nil {
		return res.waitErr.Error()
	}
	return "no exit status"
}

// stderrNote reports the size of foostore's stderr, which is withheld: its
// diagnostics are sanitised by contract, but a wrong binary's need not be.
func stderrNote(res result) string {
	if res.stderrLen == 0 {
		return ""
	}
	return fmt.Sprintf("; stderr withheld (%d bytes)", res.stderrLen)
}
