package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/api"
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
// -for (per-host recipient targeting, phase 2) is task 4b2 and is not
// implemented here: planSealed always records every ForHosts member, the
// same "whole plan" selection the plaintext -o and -stdout paths use.

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
// resolves recipients once (the union of -recipient flags and the default
// recipients file, docs/plan-encryption.md "Keys") and refuses up front
// with zero of them, before any task body runs, so a plan that could never
// be sealed is never even recorded — no silent plaintext fallback.
func planSealed(outDir, planID string, tasks []string, toStdout bool, recipientFlags []string) int {
	recipients, err := resolvePlanRecipients(recipientFlags)
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
	fmt.Printf("wrote %s (%d ops, %d recipients)\n", filepath.Join(outDir, "plan.age"), len(ops), len(recipients))
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
	eprintf("wrote stdout (%d ops, %d recipients, sealed)\n", len(ops), len(recipients))
	return 0
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

// readRecipientsFile reads the default recipients file's lines for
// seal.ParseRecipients (one age1pq recipient per line, "#" comments
// allowed — ParseRecipients' own convention). A missing file is not an
// error: an operator who seals only with -recipient flags need never
// create one.
func readRecipientsFile() ([]string, error) {
	path, err := defaultRecipientsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read recipients file %s: %w", path, err)
	}
	return strings.Split(string(data), "\n"), nil
}

// resolvePlanRecipients unions -recipient flag values (in the order given)
// with the default recipients file's lines, then validates the whole list
// through plan/seal.ParseRecipients, which enforces the age1pq-only policy
// (docs/plan-encryption.md "Recipient policy") and names any refused
// line's class, never its content. It does not itself refuse an empty
// result (ParseRecipients' own doc comment): planSealed does that, at the
// point sealing would otherwise produce an unreadable artifact.
func resolvePlanRecipients(flagRecipients []string) ([]seal.Recipient, error) {
	lines := append([]string{}, flagRecipients...)
	fileLines, err := readRecipientsFile()
	if err != nil {
		return nil, err
	}
	lines = append(lines, fileLines...)
	recipients, err := seal.ParseRecipients(lines)
	if err != nil {
		return nil, err
	}
	return recipients, nil
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
