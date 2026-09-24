package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// This file is task 2b2 (w82 phase 1, second half, docs/plan-encryption.md
// "Recommended design"/"Phased implementation"): `gonf plan -o dir -seal`
// and `gonf plan -seal -stdout`. It writes ONLY an age-encrypted GONF-PUSH/1
// frame (dir/plan.age, or the same bytes on stdout), never plan.jsonl or
// blobs/: recording goes straight into a plan.MemoryStore (as push already
// does for the same reason — no plaintext blob sidecar ever touches disk),
// the frame is built with plan.EncodePush and sealed with plan/seal.Seal
// (task 1b2), the one package in this module that imports filippo.io/age.
// Sealing gives confidentiality only, never provenance (see plan/seal's
// package doc and docs/plan-encryption.md "Provenance"): every message this
// file prints on success says "wrote", never "verified" or "trusted".
//
// -for (per-host recipient targeting, phase 2, task 4b2) is implemented in
// plan_seal_for.go: planSealed itself, unchanged here, always records every
// ForHosts member, the same "whole plan" selection the plaintext -o and
// -stdout paths use — a -for run never reaches this file's functions.

// stringSliceFlag collects every occurrence of a repeatable flag, in the
// order given, so `-recipient r1 -recipient r2` accumulates both instead of
// the last one winning as a plain flag.String would (flag.Var takes a
// flag.Value, not a *string).
type stringSliceFlag []string

// String renders the flag package's own usage/help text for the flag's
// current value; empty for the common case for -recipient of never being
// set.
func (s *stringSliceFlag) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

// Set implements flag.Value, appending one more occurrence.
func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// planSealed is gonf plan -seal, both output forms (dir and -stdout): it
// resolves recipients once (the union of -recipient flags and the
// recipients file, docs/plan-encryption.md "Keys") and refuses up front
// with zero of them, before any task body runs, so a plan that could never
// be sealed is never even recorded — no silent plaintext fallback.
// recipientsFilePath is -recipients-file (empty for the ambient default);
// noDefaultRecipients is -no-default-recipients, which skips the ambient
// default file entirely (an explicit -recipients-file is still read even
// when it is set — see loadRecipientsFileLines).
func planSealed(outDir, planID string, tasks []string, toStdout bool, recipientFlags []string, recipientsFilePath string, noDefaultRecipients bool) int {
	recipients, err := resolvePlanRecipients(recipientFlags, recipientsFilePath, noDefaultRecipients)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	if len(recipients) == 0 {
		eprintf("plan: -seal refused: no recipients; pass -recipient age1pq..., "+
			"or create %s with one age1pq recipient per line (docs/plan-encryption.md)\n",
			recipientsFileLabel())
		return 1
	}
	if toStdout {
		return planToSealedStdout(planID, tasks, recipients)
	}
	return planToSealedDir(outDir, planID, tasks, recipients)
}

// planToSealedDir records into memory (as planToStdout and push already do:
// no plaintext blob ever lands on disk), builds the GONF-PUSH/1 push frame
// (the same frame push/cluster/fleet stream) and seals it to recipients,
// writing dir/plan.age with the same private-file rules plaintext
// plan.jsonl gets (plan.SecureDir, then plan.WritePrivateFile — 0600, and
// an existing dir is verified and left exactly as it is, never chmod'ed).
// It never creates plan.jsonl or blobs/ in dir. When dir already holds one
// from an earlier, unsealed run of the same recipe, warnPreexistingPlaintextPlan
// warns about it on stderr without touching it: that file is the operator's,
// and sealing does not know whether it is still needed.
func planToSealedDir(outDir, planID string, tasks []string, recipients []seal.Recipient) int {
	if outDir == "" {
		outDir = "."
	}
	mem := plan.NewMemoryStore()
	ops, err := api.RecordPlanTo(planID, mem, tasks...)
	if err != nil {
		eprintErr("plan", err)
		return 1
	}
	sealed, err := sealPushFrame(ops, mem, recipients)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	if err := plan.SecureDir(outDir); err != nil {
		eprintf("plan: secure output directory: %v\n", err)
		return 1
	}
	if err := plan.WritePrivateFile(outDir, "plan.age", sealed); err != nil {
		eprintf("plan: write %s: %v\n", filepath.Join(outDir, "plan.age"), err)
		return 1
	}
	// Wording note (docs/plan-encryption.md "Provenance"): "wrote", never
	// "verified" or "trusted" — a plan.age that decrypts proves only that
	// whoever sealed it knew a recipient's PUBLIC key, not who they were.
	// The resolved recipients are printed, not just a count (task ce2): they
	// are public, safe to echo, and printing them is what actually lets an
	// operator reviewing output notice an unexpected extra recipient. One
	// per line, fingerprinted unless -verbose (formatRecipients, task 4g2).
	fmt.Printf("wrote %s (%d ops, %d recipients)\n%s",
		filepath.Join(outDir, "plan.age"), len(ops), len(recipients), formatRecipients(recipients))
	warnPreexistingPlaintextPlan(outDir)
	return 0
}

// planToSealedStdout is -seal -stdout: records into memory (as
// planToSealedDir does) and writes the sealed bytes straight to stdout,
// binary age with no armor — a sealed stream is as safe on a pipe or in a
// CI log store as in a file (docs/plan-encryption.md "Artifact"), and this
// is the natural form for `gonf plan -seal -stdout | ssh host gonf apply
// -identity ... -`. No plan.jsonl, blobs/ or plan.age ever touches disk on
// this path; an explicit -o is ignored, exactly as it already is for plain
// -stdout (planOutputConflict's own doc comment).
func planToSealedStdout(planID string, tasks []string, recipients []seal.Recipient) int {
	mem := plan.NewMemoryStore()
	ops, err := api.RecordPlanTo(planID, mem, tasks...)
	if err != nil {
		eprintErr("plan", err)
		return 1
	}
	sealed, err := sealPushFrame(ops, mem, recipients)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	if _, err := os.Stdout.Write(sealed); err != nil {
		eprintf("plan: write stdout: %v\n", err)
		return 1
	}
	eprintf("wrote stdout (%d ops, %d recipients, sealed)\n%s", len(ops), len(recipients), formatRecipients(recipients))
	return 0
}

// formatRecipients renders recipients for the "wrote ..." messages (task
// ce2): printing the actual resolved list, not only a count, is what gives
// an operator a real chance of noticing an unexpected extra recipient — a
// bare count is not something anyone realistically checks against an
// expected value.
//
// Each recipient gets a line of its own, "  recipient <display>\n", right
// under the "wrote ..." line (task 4g2). The ce2 version joined the full
// keys onto that one line, but an age1pq key is ~1,800 characters, so two
// recipients made a single ~3.7 KB line: the opposite of reviewable. The
// display is therefore recipientFingerprint's short form, and the full
// public key only under the top-level -verbose flag (debug log level), for
// an operator who wants to compare it byte for byte. Both are public-key
// material only, safe to print.
func formatRecipients(recipients []seal.Recipient) string {
	full := logger.GetLevel() >= logger.LevelDebug
	var b strings.Builder
	for _, r := range recipients {
		key := r.String()
		if !full {
			key = recipientFingerprint(key)
		}
		b.WriteString("  recipient ")
		b.WriteString(key)
		b.WriteString("\n")
	}
	return b.String()
}

// recipientFingerprint shortens an age1pq recipient key to a stable,
// reviewable form: the fixed "age1pq1" prefix, an ellipsis, the key's last
// 8 characters and the first 16 hex digits (64 bits) of the key's SHA-256.
// The tail lets an operator match it against a key file at a glance; the
// hash is what makes two different keys practically never collide, and an
// operator can reproduce it with `printf %s KEY | sha256sum`. A key too
// short to shorten (never the case for a validated age1pq key) is kept
// whole rather than sliced out of range.
func recipientFingerprint(key string) string {
	const prefix, tail = "age1pq1", 8
	if len(key) <= len(prefix)+tail {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return prefix + "…" + key[len(key)-tail:] + " sha256:" + hex.EncodeToString(sum[:])[:16]
}

// sealPushFrame builds the GONF-PUSH/1 frame for ops/mem (plan.EncodePush,
// the same frame push/cluster/fleet stream) straight into the age
// WriteCloser plan/seal.Seal returns, so the plaintext frame is never fully
// materialized before encryption — only the finished ciphertext is
// buffered, for WritePrivateFile or stdout to write out whole. Close's
// error is checked (per Seal's own doc comment): the final authentication
// tag is only computed then, so a Close failure means the sealed bytes are
// incomplete and must not be written anywhere.
func sealPushFrame(ops []plan.Op, mem *plan.MemoryStore, recipients []seal.Recipient) ([]byte, error) {
	var buf bytes.Buffer
	wc, err := seal.Seal(&buf, recipients)
	if err != nil {
		return nil, err
	}
	if err := plan.EncodePush(wc, ops, mem); err != nil {
		_ = wc.Close()
		return nil, fmt.Errorf("seal: encode push frame: %w", err)
	}
	if err := wc.Close(); err != nil {
		return nil, fmt.Errorf("seal: %w", err)
	}
	return buf.Bytes(), nil
}

// defaultRecipientsPath is the operator recipients file docs/plan-encryption.md
// "Keys" names: $XDG_CONFIG_HOME/gonf/recipients, or
// $HOME/.config/gonf/recipients when XDG_CONFIG_HOME is unset (the same XDG
// base-directory convention the operator identity default, task 3b2, will
// use for its own file).
func defaultRecipientsPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for the default recipients file: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "gonf", "recipients"), nil
}

// recipientsFileLabel is defaultRecipientsPath for display in the
// zero-recipients refusal: falls back to the unresolved XDG form on the
// rare failure to resolve a home directory, so the refusal still says
// something actionable instead of swallowing that error.
func recipientsFileLabel() string {
	path, err := defaultRecipientsPath()
	if err != nil {
		return "${XDG_CONFIG_HOME:-$HOME/.config}/gonf/recipients"
	}
	return path
}

// loadRecipientsFileLines resolves which recipients file (if any) to read
// and returns its path and raw lines, hardened through
// plan/seal.LoadRecipientsFile (task ce2 — the pre-fix version of this
// function used a plain os.ReadFile with no ownership, permission or
// symlink check at all, which let a symlinked or world-writable recipients
// file silently inject an extra recipient into every sealed plan). The
// returned path is the one resolvePlanRecipients labels its
// seal.ParseRecipientsFrom errors with (task de2), even on the "no file to
// read" branches below, where it is simply never used because lines is
// empty.
//
//   - noDefaultRecipients (-no-default-recipients) with no explicit
//     recipientsFilePath skips this source entirely: the operator opted
//     out, relying solely on -recipient flags.
//   - recipientsFilePath (-recipients-file) is read even when
//     noDefaultRecipients is also set — it names a file the operator chose
//     deliberately, not the ambient default — and a missing explicit file
//     is an error rather than silently empty, since a typo'd path should
//     not fail open.
//   - With neither flag, the ambient default path (defaultRecipientsPath)
//     is read when present and silently skipped when absent: an operator
//     who seals only with -recipient flags need never create one.
func loadRecipientsFileLines(recipientsFilePath string, noDefaultRecipients bool) (path string, lines []string, err error) {
	explicit := recipientsFilePath != ""
	if noDefaultRecipients && !explicit {
		return "", nil, nil
	}
	path = recipientsFilePath
	if !explicit {
		def, err := defaultRecipientsPath()
		if err != nil {
			return "", nil, err
		}
		path = def
	}
	lines, err = seal.LoadRecipientsFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return path, nil, nil
		}
		// Unwrapped: LoadRecipientsFile's error already reads "plan/seal:
		// recipients file <path>: <reason>", so wrapping it again printed
		// the path twice (task 4g2, the same doubled-wrap class de2 fixed
		// for loadSealedIdentities); planSealed's own "plan: " prefix is
		// the only one it needs.
		return "", nil, err
	}
	return path, lines, nil
}

// resolvePlanRecipients validates -recipient flag values and the resolved
// recipients file's lines (loadRecipientsFileLines) as SEPARATE
// plan/seal.ParseRecipientsFrom calls, each labeled with its own source,
// then concatenates the results (flags first, in the order given, then the
// file's own recipients — the same effective order the pre-de2 version
// got by unioning the two slices before validating). ParseRecipientsFrom
// enforces the age1pq-only policy (docs/plan-encryption.md "Recipient
// policy") and names any refused line's class, never its content.
//
// Validating each source on its own is what lets a refusal name the
// entry's TRUE origin: the previous version concatenated -recipient flags
// and file lines into one slice before validating, so ParseRecipients'
// generic "recipient line %d" numbered over the MERGED slice — two
// -recipient flags ahead of a bad file line 2 produced "recipient line 4",
// and an operator opening the file at line 4 found nothing wrong there
// (task de2).
//
// resolvePlanRecipients does not itself refuse an empty result: planSealed
// and planSealedFor (plan_seal_for.go, task mg2) each do that at their own
// call site, at the point sealing would otherwise produce an unreadable
// artifact.
func resolvePlanRecipients(flagRecipients []string, recipientsFilePath string, noDefaultRecipients bool) ([]seal.Recipient, error) {
	fromFlags, err := seal.ParseRecipientsFrom(flagRecipients, recipientFlagLabel)
	if err != nil {
		return nil, err
	}
	path, fileLines, err := loadRecipientsFileLines(recipientsFilePath, noDefaultRecipients)
	if err != nil {
		return nil, err
	}
	fromFile, err := seal.ParseRecipientsFrom(fileLines, recipientFileLabel(path))
	if err != nil {
		return nil, err
	}
	return append(fromFlags, fromFile...), nil
}

// recipientFlagLabel is a seal.ParseRecipientsFrom label for -recipient
// flag values: i is 0-based, so it is reported as its 1-based
// command-line position. Unlike a recipients file, repeated -recipient
// flags have no file to point at, so "-recipient #2" is the most concrete
// origin available for a refused or malformed one.
func recipientFlagLabel(i int) string {
	return fmt.Sprintf("-recipient #%d", i+1)
}

// recipientFileLabel returns a seal.ParseRecipientsFrom label naming
// path's own 1-based line number for a 0-based index i, independent of how
// many -recipient flags resolvePlanRecipients validated alongside it.
func recipientFileLabel(path string) func(i int) string {
	return func(i int) string {
		return fmt.Sprintf("%s:%d", path, i+1)
	}
}

// warnPreexistingPlaintextPlan tells the operator, without touching
// anything, that outDir still holds an earlier plaintext plan.jsonl and/or
// blobs/ next to the plan.age this sealed run just wrote
// (docs/plan-encryption.md "Operator UX" table: "Warns ... never deletes
// it: the operator's file"). Sealing cannot tell whether that leftover is
// still needed, so this is warn-only, the same fail-safe shape
// warnIfPlanUnignoredInGitWorktree already has for the plaintext path.
// hasBlobDir (git_worktree_warn.go) is reused so the two warnings agree on
// what counts as a written blobs/ directory.
func warnPreexistingPlaintextPlan(outDir string) {
	var found []string
	if info, err := os.Stat(filepath.Join(outDir, "plan.jsonl")); err == nil && !info.IsDir() {
		found = append(found, "plan.jsonl")
	}
	if hasBlobDir(outDir) {
		found = append(found, "blobs/")
	}
	if len(found) == 0 {
		return
	}
	eprintf("plan: %s also holds a plaintext %s from an earlier unsealed run; "+
		"left as it is (not deleted) — remove it yourself once you no longer need it\n",
		outDir, strings.Join(found, " and "))
}
