package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// This file is task qg2: the ONE write path for sealed plan artifacts in an
// output directory, shared by plain -seal (planToSealedDir: one plan.age)
// and -seal -for (planSealedFor: one plan-<host>.age per host). Before it,
// both paths carried their own copy of the SecureDir -> WritePrivateFile ->
// "wrote ..." -> warnPreexistingPlaintextPlan sequence and its wording, so
// the next wording or permission change had two places to miss (task de2
// was a whole round of fixing exactly that kind of split).
//
// Writing is two-phase so -for can keep both of its promises at once:
//
//   - bounded memory: each host's sealed frame is staged to disk (a hidden,
//     0600 staging file in the verified output directory) as soon as it is
//     sealed and then released, so peak memory is one host's frame, not
//     every host's (an earlier version held all N frames until the end);
//   - all-or-nothing: no final plan-<host>.age appears until every host has
//     been recorded, sealed and staged; a failure before that removes the
//     staging files again (discard), leaving nothing written.
//
// Only the final commit (one rename per artifact) can fail partway, and
// then the refusal names which artifacts are already in place and which are
// not, instead of implying nothing was written. A process killed between
// stage and commit can leave a hidden staging file behind: it holds the
// same age-sealed bytes the final artifact would, never plaintext.

// stagingSuffix marks a staged artifact's hidden file name (stagingName).
const stagingSuffix = ".staging-"

// sealedOutput stages sealed artifacts in one output directory and then
// commits (publishes) or discards them together. Build it with
// newSealedOutput; the directory is secured lazily, by the first stage, so a
// run that fails before sealing anything never creates it.
type sealedOutput struct {
	dir     string
	secured bool // plan.SecureDir already accepted dir
	created bool // dir did not exist before this output secured it
	staged  []stagedArtifact
}

// stagedArtifact is one artifact staged but not yet committed: its final
// name, the hidden staging name it currently has, and what its "wrote"
// report says about it.
type stagedArtifact struct {
	name, staging string
	report        artifactReport
}

// artifactReport is what a "wrote" report says about one sealed artifact:
// its op count, the recipients it was sealed to and, with -sign (task
// 7g2), who signed it and when (nil when unsigned). Every sealed write,
// to a directory or stdout, words its report from this one value, so the
// dir, -stdout and -for paths cannot drift apart.
type artifactReport struct {
	ops        int
	recipients []seal.Recipient
	signature  *artifactSignature
}

// summary is the report's parenthesised part after "wrote <where> (": the
// counts, then extra (e.g. ", sealed"; may be ""), then ", signed <time>"
// for a signed artifact. An unsigned summary is exactly the pre-7g2 one.
func (r artifactReport) summary(extra string) string {
	return fmt.Sprintf("%d ops, %d recipients%s%s", r.ops, len(r.recipients), extra, r.signature.signedNote())
}

// details is the lines under the "wrote" line: one per recipient
// (formatRecipients), then the signer's for a signed artifact.
func (r artifactReport) details() string {
	return formatRecipients(r.recipients) + r.signature.detailLine()
}

// newSealedOutput returns an output for dir ("" meaning the current
// directory, as -o's default).
func newSealedOutput(dir string) *sealedOutput {
	if dir == "" {
		dir = "."
	}
	return &sealedOutput{dir: dir}
}

// stagingName is name's hidden staging name. The leading dot keeps it out
// of the way of every final name (plan.age, plan-<host>.age never start
// with a dot) and the pid keeps two concurrent runs into the same
// directory from sharing one.
func stagingName(name string) string {
	return "." + name + stagingSuffix + strconv.Itoa(os.Getpid())
}

// path is name's path below the output directory, for messages.
func (o *sealedOutput) path(name string) string { return filepath.Join(o.dir, name) }

// stage writes sealed (the sealed, and with -sign signed, artifact bytes)
// as name's hidden staging file (plan.WritePrivateFile: 0600,
// symlink-safe, in the directory plan.SecureDir verified) and records it
// for commit with report, so the caller can drop sealed right away. Its
// errors are worded without the leading "plan: " the caller prints.
func (o *sealedOutput) stage(name string, sealed []byte, report artifactReport) error {
	if err := o.secure(); err != nil {
		return err
	}
	staging := stagingName(name)
	if err := plan.WritePrivateFile(o.dir, staging, sealed); err != nil {
		return fmt.Errorf("write %s: %w", o.path(name), err)
	}
	o.staged = append(o.staged, stagedArtifact{name: name, staging: staging, report: report})
	return nil
}

// secure runs plan.SecureDir on the output directory once, noting whether
// it existed before, so discard can remove a directory this run created.
func (o *sealedOutput) secure() error {
	if o.secured {
		return nil
	}
	if _, err := os.Lstat(o.dir); errors.Is(err, fs.ErrNotExist) {
		o.created = true
	}
	if err := plan.SecureDir(o.dir); err != nil {
		return fmt.Errorf("secure output directory: %w", err)
	}
	o.secured = true
	return nil
}

// commit renames every staged artifact into place in staging order,
// printing each one's "wrote" report as it lands, then warns (never
// touches) about a leftover plaintext plan.jsonl/blobs/ in the directory.
// When one rename fails, the rest are discarded and the error names what
// is already written and what is not (commitError).
func (o *sealedOutput) commit() error {
	for i, a := range o.staged {
		if err := plan.RenamePrivateFile(o.dir, a.staging, a.name); err != nil {
			o.removeStaging(o.staged[i:])
			rest := o.staged
			o.staged = nil
			return o.commitError(rest, i, err)
		}
		reportSealedWrite(o.path(a.name), a.report)
	}
	o.staged = nil
	warnPreexistingPlaintextPlan(o.dir)
	return nil
}

// reportSealedWrite prints the one "wrote" report every sealed artifact
// written to a directory gets. Wording note (docs/design/plan-encryption.md
// "Provenance"): "wrote", never "verified" or "trusted" — an artifact that
// decrypts proves only that whoever sealed it knew a recipient's PUBLIC
// key, not who they were. The resolved recipients are printed, not just a
// count (task ce2): they are public, safe to echo, and printing them is
// what actually lets an operator reviewing output notice an unexpected
// extra recipient. One per line, fingerprinted unless -verbose
// (formatRecipients, task 4g2). A signed artifact (task 7g2) adds its
// signed-at time to the summary and a "  signer ..." line naming the
// public key; "signed" states what was done to the bytes, and still
// nothing here claims the artifact was verified.
func reportSealedWrite(path string, report artifactReport) {
	fmt.Printf("wrote %s (%s)\n%s", path, report.summary(""), report.details())
}

// commitError words a failed commit of staged[failed]. A one-artifact
// output (plain -seal) keeps the plain "write <path>: <err>" wording; a
// multi-artifact one (-for) adds which artifacts are already in place and
// which are not, since unlike every earlier -for refusal this one cannot
// promise "nothing written" once an earlier rename succeeded.
func (o *sealedOutput) commitError(staged []stagedArtifact, failed int, err error) error {
	err = fmt.Errorf("write %s: %w", o.path(staged[failed].name), err)
	if len(staged) == 1 {
		return err
	}
	written := "nothing written"
	if failed > 0 {
		written = "already written: " + o.joinPaths(staged[:failed])
	}
	return fmt.Errorf("%w; %s; not written: %s", err, written, o.joinPaths(staged[failed:]))
}

// joinPaths lists the final paths of staged, comma-separated.
func (o *sealedOutput) joinPaths(staged []stagedArtifact) string {
	paths := make([]string, len(staged))
	for i, a := range staged {
		paths[i] = o.path(a.name)
	}
	return strings.Join(paths, ", ")
}

// discard removes every staged-but-uncommitted artifact and, when this
// output created the directory, the directory too if it is empty again, so
// a run that fails before commit leaves nothing written. Removal is best
// effort (a failure here must not mask the error that caused the discard):
// a staging file that survives holds only sealed bytes.
func (o *sealedOutput) discard() {
	o.removeStaging(o.staged)
	o.staged = nil
	if o.created {
		_ = os.Remove(o.dir) // fails, harmlessly, unless the directory is empty
	}
}

// removeStaging removes the staging files of staged, best effort.
func (o *sealedOutput) removeStaging(staged []stagedArtifact) {
	for _, a := range staged {
		_ = plan.RemovePrivateFile(o.dir, a.staging)
	}
}
