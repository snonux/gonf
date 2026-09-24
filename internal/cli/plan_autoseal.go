package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/plan/seal"
)

// This file is task 5b2 (w82 phase 3, docs/design/plan-encryption.md "Operator
// UX" and "Phased implementation"), an approved behaviour correction
// (approved by the user 2026-09-24): `gonf plan -o dir` without -seal
// SEALS BY DEFAULT when both
//
//   - the recorded plan is sensitive (api.SensitiveOpNames, the same
//     signal that makes -stdout refuse and -o warn "carries secret
//     material in clear text"), and
//   - an operator recipients file exists: -recipients-file when given,
//     otherwise the ambient default (defaultRecipientsPath) unless
//     -no-default-recipients skips it.
//
// Then it writes dir/plan.age exactly as `-seal` does (sealAndSign and
// writeSealedDir, to the union of -recipient flags and that file) and says
// so on stderr (sealedByDefaultNote). -plaintext opts out and keeps the
// pre-5b2 plaintext plan.jsonl. A non-sensitive plan, or a run with no
// recipients file, writes plaintext exactly as before (the latter through
// the untouched planToDir, so a client without a recipients file is
// byte-for-byte unaffected).
//
// A recipients file that exists but cannot be used (unsafe, malformed,
// holding no recipient) REFUSES a sensitive plan, never falls back to
// plaintext silently (the 1b2 recipient policy; the revised 5b2 scope). A
// non-sensitive plan is still written in plaintext then, as before, with a
// warning that the next sensitive plan would be refused.
//
// Only this -o path seals by default. -stdout keeps its own refusal of a
// sensitive plan (switching a pipe from JSONL to binary age behind the
// consumer's back would break it), -redacted is a preview, and -for and
// -sign stay static usage errors without an explicit -seal: whether the
// plan is sensitive is known only after recording, but -for changes how
// it is recorded and -sign given to a run that turns out plaintext would
// be silently ignored.

// planToDirDefault is `gonf plan -o dir` without -seal: -plaintext, or no
// recipients file to seal to, takes the unchanged plaintext path
// (planToDir); otherwise planAutoSeal decides once the plan is recorded.
func planToDirDefault(req sealRequest, plaintext bool) int {
	if req.outDir == "" {
		req.outDir = "."
	}
	if plaintext {
		return planToDir(req.outDir, req.planID, req.tasks)
	}
	file := autoSealRecipientsFile(req.recipientsFile, req.noDefaultRecipients)
	if file == "" {
		return planToDir(req.outDir, req.planID, req.tasks)
	}
	return planAutoSeal(req, file)
}

// autoSealRecipientsFile returns the recipients file whose presence opts a
// plain -o run into sealing a sensitive plan by default, or "" when there
// is none: the explicit -recipients-file (named deliberately, so it counts
// even when missing, and a sensitive plan is then refused like `-seal`
// refuses it), else the ambient default unless -no-default-recipients. The
// default counts as present unless Lstat says it does not exist: anything
// else (a symlink, a permission error) is left for the load to refuse, so
// an unreadable file never quietly means "no file, write plaintext".
func autoSealRecipientsFile(explicit string, noDefault bool) string {
	if explicit != "" {
		return explicit
	}
	if noDefault {
		return ""
	}
	path, err := defaultRecipientsPath()
	if err != nil {
		return "" // no home directory: no default recipients file can exist
	}
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return path
}

// planAutoSeal records the plan into memory (api.RecordPlanDeferred, after
// the usual up-front output-directory check) and then writes it sealed
// when it is sensitive, or in plaintext when it is not. The recipients are
// resolved before recording, as -seal does, but their error only matters
// for a sensitive plan (autoSealRefusal).
func planAutoSeal(req sealRequest, file string) int {
	recipients, recErr := resolvePlanRecipients(req.recipientFlags, req.recipientsFile, req.noDefaultRecipients)
	d, err := api.RecordPlanDeferred(req.planID, req.outDir, req.tasks...)
	if err != nil {
		eprintErr("plan", err)
		return 1
	}
	names := api.SensitiveOpNames(d.Ops)
	if len(names) == 0 {
		return writeDeferredPlaintext(req.outDir, d, recErr)
	}
	if msg := autoSealRefusal(recErr, recipients, file, names); msg != "" {
		eprintln("plan: " + msg)
		return 1
	}
	data, report, err := sealAndSign(d.Ops, d.Blobs, recipients, nil)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	if code := writeSealedDir(req.outDir, data, report); code != 0 {
		return code
	}
	eprintln(sealedByDefaultNote(names, file, filepath.Join(req.outDir, "plan.age")))
	return 0
}

// writeDeferredPlaintext is the non-sensitive outcome of planAutoSeal: the
// blobs are committed from memory and plan.jsonl written exactly as
// planToDir writes them. recErr, an unusable recipients source, did not
// matter for this plan, but would refuse the next sensitive one, so it is
// reported as a warning after the write.
func writeDeferredPlaintext(outDir string, d *api.DeferredPlan, recErr error) int {
	if err := d.CommitBlobs(); err != nil {
		eprintErr("plan", err)
		return 1
	}
	if code := writePlanJSONL(outDir, d.Ops); code != 0 {
		return code
	}
	if recErr != nil {
		eprintf("plan: warning: %v; this plan carries no secret material and was written in plaintext, "+
			"but a secret-bearing plan would be refused until this is fixed (or -plaintext is passed)\n", recErr)
	}
	return 0
}

// autoSealRefusal returns why a sensitive plan cannot be sealed by
// default, or "" when it can: the recipients did not resolve (an unsafe or
// malformed recipients file, a missing -recipients-file, a bad -recipient)
// or resolved to none (a file of comments only). Both refuse instead of
// writing plaintext: the operator created a recipients file, so a
// plaintext secret artifact is exactly what they asked not to get.
func autoSealRefusal(recErr error, recipients []seal.Recipient, file string, names []string) string {
	const tail = "; not written in plaintext by default — fix the recipients, pass -seal -recipient age1pq..., " +
		"or pass -plaintext to write a plaintext plan.jsonl anyway; nothing written"
	lead := "sealing by default refused (the plan carries secret material in " + strings.Join(names, ", ") + "): "
	switch {
	case recErr != nil:
		return lead + recErr.Error() + tail
	case len(recipients) == 0:
		return lead + "no recipients: " + file + " holds no age1pq recipient" + tail
	}
	return ""
}

// sealedByDefaultNote is the stderr line that follows the "wrote
// dir/plan.age" report of a default seal: it says that sealing happened by
// default, why (which ops are secret-bearing, never their values; which
// recipients file opted in), and how to opt out.
func sealedByDefaultNote(names []string, file, planAge string) string {
	return "plan: sealed by default: the plan carries secret material in " + strings.Join(names, ", ") +
		" and the recipients file " + file + " exists, so " + planAge +
		" was written instead of a plaintext plan.jsonl; decrypt it with gonf apply -identity <file>, " +
		"or pass -plaintext to write plaintext instead"
}
