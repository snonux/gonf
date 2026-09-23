package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// This file is task 4b2 (w82 phase 2, docs/plan-encryption.md "Phased
// implementation"): `gonf plan -o dir -seal -for host|cluster|fleet` and
// `gonf plan -seal -stdout -for host`. Unlike planSealed (plan_seal.go),
// which records the whole plan once with no host selection, -for records
// ONCE PER TARGET HOST (api.RecordPlanForHost) so a ForHosts body written
// for one host's secrets never ends up in another host's artifact — see
// docs/plan-encryption.md "Operator UX", the `-for` row. Every host's plan
// is fully recorded and sealed in memory before anything is written to
// disk, so a failure partway through (a bad task body, a missing recipient,
// a filename collision) leaves nothing behind for the hosts already
// processed.

// sealedHostPlan is one target host's recorded-and-sealed plan, held in
// memory until every host in the -for run has succeeded (see planSealedFor).
type sealedHostPlan struct {
	ops        int
	recipients []seal.Recipient
	sealed     []byte
}

// planSealedFor is gonf plan -seal -for's entry point: resolve forTarget to
// its host names, refuse up front if any lacks a plan recipient or if the
// -stdout/-for combination cannot resolve to exactly one file, seal every
// host's plan in memory, then write the results out (a file per host, or
// the one host's bytes on stdout).
func planSealedFor(outDir, planID string, tasks []string, toStdout bool, forTarget string,
	recipientFlags []string, recipientsFilePath string, noDefaultRecipients bool) int {
	hosts, err := api.PlanRecipientTargetHosts(forTarget)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	if missing := hostsMissingPlanRecipient(hosts); len(missing) > 0 {
		eprintf("plan: -for refused: %s %s a plan recipient "+
			"(Host(%q, api.WithPlanRecipient(\"age1pq...\"))); nothing written\n",
			strings.Join(missing, ", "), lackVerb(missing), missing[0])
		return 1
	}
	if toStdout && len(hosts) != 1 {
		eprintf("plan: -for -stdout refused: %q resolves to %d hosts, not exactly one\n", forTarget, len(hosts))
		return 1
	}
	baseRecipients, err := resolvePlanRecipients(recipientFlags, recipientsFilePath, noDefaultRecipients)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	files, err := sanitizeHostFilenames(hosts)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	results, err := sealPerHostPlans(hosts, planID, tasks, baseRecipients)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	if toStdout {
		return writeSealedForStdout(hosts[0], results[hosts[0]])
	}
	return writeSealedForDir(outDir, hosts, files, results)
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
// recording or sealing anything (docs/plan-encryption.md "Operator UX":
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
		// caller of sealPerHostPlans that skips the pre-check).
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

// sealPerHostPlans records (api.RecordPlanForHost) and seals (sealPushFrame,
// plan_seal.go) every host's own plan, entirely in memory, before
// planSealedFor writes anything out. Returning on the first error, with
// nothing written for any host yet, is what makes "-for refuses before
// writing anything" hold even for a failure this function's caller could
// not have checked up front (an unknown task, a task body error on one
// particular host's ForHosts branch, ...).
func sealPerHostPlans(hosts []string, planID string, tasks []string, base []seal.Recipient) (map[string]sealedHostPlan, error) {
	out := make(map[string]sealedHostPlan, len(hosts))
	for _, host := range hosts {
		recipients, err := hostRecipients(host, base)
		if err != nil {
			return nil, err
		}
		mem := plan.NewMemoryStore()
		ops, err := api.RecordPlanForHost(host, planID, mem, tasks...)
		if err != nil {
			return nil, fmt.Errorf("host %s: %w", host, err)
		}
		sealed, err := sealPushFrame(ops, mem, recipients)
		if err != nil {
			return nil, fmt.Errorf("host %s: %w", host, err)
		}
		out[host] = sealedHostPlan{ops: len(ops), recipients: recipients, sealed: sealed}
	}
	return out, nil
}

// forFilenameUnsafe matches every byte sanitizeHostFilename replaces with
// "_": everything except a conservative filename alphabet.
var forFilenameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

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

// writeSealedForDir writes one dir/plan-<host>.age per host, in hosts'
// order, with the same private-file rules planToSealedDir uses for the
// whole-plan case (plan.SecureDir once, then plan.WritePrivateFile per
// file: 0600, symlink-safe). It warns (never touches) about a leftover
// plaintext plan.jsonl/blobs/ exactly like planToSealedDir does.
func writeSealedForDir(outDir string, hosts []string, files map[string]string, results map[string]sealedHostPlan) int {
	if outDir == "" {
		outDir = "."
	}
	if err := plan.SecureDir(outDir); err != nil {
		eprintf("plan: secure output directory: %v\n", err)
		return 1
	}
	for _, host := range hosts {
		name := "plan-" + files[host] + ".age"
		r := results[host]
		if err := plan.WritePrivateFile(outDir, name, r.sealed); err != nil {
			eprintf("plan: write %s: %v\n", filepath.Join(outDir, name), err)
			return 1
		}
		fmt.Printf("wrote %s (%d ops, %d recipients: %s)\n",
			filepath.Join(outDir, name), r.ops, len(r.recipients), formatRecipients(r.recipients))
	}
	warnPreexistingPlaintextPlan(outDir)
	return 0
}

// writeSealedForStdout is -for -stdout's single-host write, matching
// planToSealedStdout's wording and no-disk-touched behaviour.
func writeSealedForStdout(host string, r sealedHostPlan) int {
	if _, err := os.Stdout.Write(r.sealed); err != nil {
		eprintf("plan: write stdout: %v\n", err)
		return 1
	}
	eprintf("wrote stdout (%d ops, %d recipients, sealed for host %s: %s)\n",
		r.ops, len(r.recipients), host, formatRecipients(r.recipients))
	return 0
}
