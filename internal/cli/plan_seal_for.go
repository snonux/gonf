package cli

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// This file is task 4b2 (w82 phase 2, docs/design/plan-encryption.md "Phased
// implementation"): `gonf plan -o dir -seal -for host|cluster|fleet` and
// `gonf plan -seal -stdout -for host`. Unlike planSealed (plan_seal.go),
// which records the whole plan once with no host selection, -for records
// ONCE PER TARGET HOST (api.RecordPlanForHost) so a ForHosts body written
// for an unrelated host's secrets does not end up in this host's artifact —
// see docs/design/plan-encryption.md "Operator UX", the `-for` row. Every host's
// plan is recorded, sealed and staged (a hidden, 0600, sealed staging file,
// see plan_seal_output.go) before any final plan-<host>.age is published,
// so a failure partway through (a bad task body, a missing recipient, a
// filename collision) leaves nothing behind for the hosts already
// processed, while only one host's sealed frame is held in memory at a
// time (task qg2; an earlier version held every host's frame until the
// end).
//
// The per-host isolation is bounded by the SAME substring-based host
// selection `gonf push` itself uses (api.RecordPlanForHost ->
// inventory.SelectionForHosts, see internal/inventory/destination.go): a
// host whose name or SSHHost is a substring of the target's (or vice versa)
// is included in the recording too, so its ForHosts body — and any secret
// it reads — can physically land in the target's sealed artifact (task ng2;
// see docs/design/plan-encryption.md's "Runbook" for the operator-facing caveat).
// This is not a new exposure relative to a plain `gonf push` to the same
// target; it means -for's isolation is a best-effort superset, not an exact
// single-host guarantee.

// forFilenameUnsafe matches every byte sanitizeHostFilename replaces with
// "_": everything except a conservative filename alphabet. It is this
// file's only package-level variable, kept at the top with the file's type
// (task rg2) so the state the -for path carries is visible at a glance.
var forFilenameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// sealedHostPlan is one target host's recorded-and-sealed (and with -sign
// signed) plan (sealHostPlan), held in memory only until it is staged to
// disk (sealForDir) or written to stdout (sealForStdout).
type sealedHostPlan struct {
	report artifactReport
	sealed []byte
}

// planSealedFor is gonf plan -seal -for's entry point: resolve forTarget to
// its host names, refuse up front if any lacks a plan recipient, if the
// base recipients (the -recipient flags and recipients file, the same
// resolvePlanRecipients call planSealed makes) are empty, or if the
// -stdout/-for combination cannot resolve to exactly one file, then record
// and seal each host's plan and write it out (a file per host through
// sealForDir's stage-then-commit, or the one host's bytes on stdout).
//
// The zero-base-recipients refusal (task mg2) mirrors planSealed's own
// (plan_seal.go): without it, a first-time operator with no
// ~/.config/gonf/recipients file and no -recipient flags got a SILENT
// "success" that sealed each artifact to its destination host's recipient
// ONLY, never the operator's own — an artifact the operator who just
// created it cannot open, contradicting plan/seal.ErrNoRecipients' own
// invariant ("a caller can never silently produce an artifact nobody, not
// even the operator, can open") and docs/design/plan-encryption.md's "Keys"
// section ("A sealed write with zero recipients is refused, never degraded
// to plaintext"). Host-only sealing was never a documented, deliberate
// posture: nothing in the design doc describes it as a stronger-security
// option, and the Runbook's own worked example never creates a recipients
// file before running -for, so a first-time operator following it verbatim
// hit this silently. -for now refuses the identical way plain -seal always
// has, before -for existed.
//
// With -sign (task 7g2) each host's artifact is signed on its own, right
// after it is sealed (sealAndSign): the signature covers that host's exact
// ciphertext, so no two hosts' artifacts share one.
func planSealedFor(req sealRequest) int {
	hosts, err := api.PlanRecipientTargetHosts(req.forTarget)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	if !forTargetsSealable(hosts, req.toStdout, req.forTarget) {
		return 1
	}
	baseRecipients, err := resolvePlanRecipients(req.recipientFlags, req.recipientsFile, req.noDefaultRecipients)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	if len(baseRecipients) == 0 {
		// Mirrors planSealed's own zero-recipient refusal (plan_seal.go)
		// verbatim in message style: without this, each host's artifact
		// would be sealed to that host's own recipient ONLY, which the
		// operator who just ran this command cannot open (task mg2).
		eprintf("plan: -for refused: no recipients; pass -recipient age1pq..., "+
			"or create %s with one age1pq recipient per line (docs/design/plan-encryption.md)\n",
			recipientsFileLabel())
		return 1
	}
	files, err := sanitizeHostFilenames(hosts)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	if req.toStdout {
		return sealForStdout(hosts[0], req, baseRecipients)
	}
	return sealForDir(newSealedOutput(req.outDir), hosts, files, req, baseRecipients)
}

// forTargetsSealable runs -for's two host-level refusals before anything
// is recorded or written, printing the refusal itself: a target host
// without a plan recipient, and -stdout on a target that does not resolve
// to exactly one host.
func forTargetsSealable(hosts []string, toStdout bool, forTarget string) bool {
	if missing := hostsMissingPlanRecipient(hosts); len(missing) > 0 {
		eprintf("plan: -for refused: %s %s a plan recipient "+
			"(Host(%q, api.WithPlanRecipient(\"age1pq...\"))); nothing written\n",
			strings.Join(missing, ", "), lackVerb(missing), missing[0])
		return false
	}
	if toStdout && len(hosts) != 1 {
		eprintf("plan: -for -stdout refused: %q resolves to %d hosts, not exactly one\n", forTarget, len(hosts))
		return false
	}
	return true
}

// lackVerb agrees "lacks"/"lack" with a one- or many-host missing list, so
// the refusal reads naturally either way.
func lackVerb(missing []string) string {
	if len(missing) == 1 {
		return "lacks"
	}
	return "lack"
}

// hostsMissingPlanRecipient returns, in order, every host of hosts that has
// no api.WithPlanRecipient set — the check planSealedFor runs before
// recording or sealing anything (docs/design/plan-encryption.md "Operator UX":
// "refuse ... before anything is written").
func hostsMissingPlanRecipient(hosts []string) []string {
	var missing []string
	for _, h := range hosts {
		if _, ok := api.HostPlanRecipient(h); !ok {
			missing = append(missing, h)
		}
	}
	return missing
}

// hostRecipients is host's full recipient list for sealing: base (the
// -recipient flags and recipients file, resolved once for the whole -for
// run) plus host's own api.WithPlanRecipient, re-validated here with
// seal.ParseRecipients even though WithPlanRecipient already validated it
// at registration — cheap, and it keeps this function's only source of
// truth for "is this a valid age1pq recipient" the same one Seal itself
// uses, rather than trusting the registry blindly.
func hostRecipients(host string, base []seal.Recipient) ([]seal.Recipient, error) {
	recipientLine, ok := api.HostPlanRecipient(host)
	if !ok {
		// Unreachable in practice: planSealedFor already refused any host
		// missing a recipient before calling this. Kept as a named error
		// rather than a panic in case the two ever disagree (e.g. a future
		// caller of sealHostPlan that skips the pre-check).
		return nil, fmt.Errorf("host %s has no plan recipient", host)
	}
	own, err := seal.ParseRecipients([]string{recipientLine})
	if err != nil {
		return nil, fmt.Errorf("host %s: %w", host, err)
	}
	out := make([]seal.Recipient, 0, len(base)+len(own))
	out = append(out, base...)
	return append(out, own...), nil
}

// sealHostPlan records (api.RecordPlanForHost) and seals, and with -sign
// signs (sealAndSign, plan_seal.go), one host's own plan in memory. Its
// error wraps
// api.RecordPlanForHost's, which can be a declaration error from inside a
// per-host ForHosts task body (e.g. a MustSecret lookup) -- exactly the new
// failure mode -for introduces -- so callers print it with eprintErr (not
// eprintf), which appends the recipe's declared-at location; a plain
// eprintf silently dropped it (task og2).
func sealHostPlan(host string, req sealRequest, base []seal.Recipient) (sealedHostPlan, error) {
	recipients, err := hostRecipients(host, base)
	if err != nil {
		return sealedHostPlan{}, err
	}
	mem := plan.NewMemoryStore()
	ops, err := api.RecordPlanForHost(host, req.planID, mem, req.tasks...)
	if err != nil {
		return sealedHostPlan{}, fmt.Errorf("host %s: %w", host, err)
	}
	data, report, err := sealAndSign(ops, mem, recipients, req.signer)
	if err != nil {
		return sealedHostPlan{}, fmt.Errorf("host %s: %w", host, err)
	}
	return sealedHostPlan{report: report, sealed: data}, nil
}

// sanitizeHostFilename turns a registered host name into dir/plan-<name>.age's
// <name> fragment. An inventory host name is recipe-authored, not attacker
// input, but a stray "/" or ".." in one would still either escape outDir or
// collide with another host's file, so anything outside [A-Za-z0-9._-] is
// replaced with "_", and a name that sanitizes to "", "." or ".." is
// refused outright (naming the host) instead of silently mapping to
// something else.
func sanitizeHostFilename(host string) (string, error) {
	safe := forFilenameUnsafe.ReplaceAllString(host, "_")
	if safe == "" || safe == "." || safe == ".." {
		return "", fmt.Errorf("host %q has no safe characters for a plan-<host>.age filename; nothing written", host)
	}
	return safe, nil
}

// sanitizeHostFilenames sanitizes every host of hosts and refuses up front,
// before anything is recorded or written, when two hosts would sanitize to
// the same dir/plan-<name>.age (which would otherwise make one host's
// sealed plan silently overwrite another's on disk — a correctness and
// confidentiality bug, since the whole point of -for is that a host's
// artifact holds only its own secrets).
func sanitizeHostFilenames(hosts []string) (map[string]string, error) {
	files := make(map[string]string, len(hosts))
	owners := make(map[string][]string, len(hosts))
	for _, h := range hosts {
		safe, err := sanitizeHostFilename(h)
		if err != nil {
			return nil, err
		}
		files[h] = safe
		owners[safe] = append(owners[safe], h)
	}
	for safe, hs := range owners {
		if len(hs) > 1 {
			sort.Strings(hs)
			return nil, fmt.Errorf("hosts %s all sanitize to the same filename plan-%s.age; nothing written",
				strings.Join(hs, ", "), safe)
		}
	}
	return files, nil
}

// sealForDir records, seals and stages each host's dir/plan-<host>.age in
// hosts' order, then commits them all (task qg2; see plan_seal_output.go
// for the write path, shared with plain -seal's planToSealedDir). Each
// host's sealed frame is staged to disk and released before the next host
// is recorded, so peak memory is one host's frame rather than all of them;
// and since nothing is committed until every host succeeded, a failure
// partway through (a task body error on one host's ForHosts branch, an
// unwritable staging file, ...) discards what was staged and leaves
// nothing written, the same promise every earlier -for refusal makes. Only
// the final commit can fail partway, and its refusal then names what is
// and is not written.
func sealForDir(out *sealedOutput, hosts []string, files map[string]string, req sealRequest, base []seal.Recipient) int {
	for _, host := range hosts {
		r, err := sealHostPlan(host, req, base)
		if err != nil {
			out.discard()
			eprintErr("plan", err)
			return 1
		}
		if err := out.stage("plan-"+files[host]+".age", r.sealed, r.report); err != nil {
			out.discard()
			eprintf("plan: %v; nothing written\n", err)
			return 1
		}
	}
	if err := out.commit(); err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	return 0
}

// sealForStdout is -for -stdout's single-host path: record and seal host's
// plan, then write the bytes to stdout through writeSealedStdout
// (plan_seal.go), the same write and wording plain -seal -stdout uses,
// with "sealed for host <host>".
func sealForStdout(host string, req sealRequest, base []seal.Recipient) int {
	r, err := sealHostPlan(host, req, base)
	if err != nil {
		eprintErr("plan", err)
		return 1
	}
	return writeSealedStdout(r.sealed, r.report, host)
}
