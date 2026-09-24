package cli

import (
	"flag"
	"os"
)

// This file is `gonf plan`'s flag parsing and dispatch (cliPlan). It used
// to be one long function in cli.go; task 7g2 split it into the flag
// definitions (planFlags), the cross-flag refusals (planFlagConflict) and
// the dispatch, when -sign added one more flag and one more refusal.

// planUsage is `gonf plan`'s usage line, shared by cliPlan's own refusal
// and printUsage so the two cannot drift apart.
const planUsage = "gonf plan [-o dir [-plaintext]|-stdout [-with-secrets]|-redacted] " +
	"[-seal [-recipient r]... [-recipients-file f] [-no-default-recipients] [-for host|cluster|fleet] [-sign signer-file]] " +
	"[-id name] <task> [task...]"

// planFlags holds `gonf plan`'s flag set and the values it parses into.
type planFlags struct {
	fs                                                        *flag.FlagSet
	outDir, planID, recipientsFile, forTarget, sign           *string
	stdout, withSecrets, redacted, seal, noDefault, plaintext *bool
	recipients                                                stringSliceFlag
}

// newPlanFlags defines every `gonf plan` flag on a fresh flag set.
func newPlanFlags() *planFlags {
	f := &planFlags{fs: flag.NewFlagSet("plan", flag.ContinueOnError)}
	f.fs.SetOutput(os.Stderr)
	f.defineOutputFlags()
	f.defineSealFlags()
	return f
}

// defineOutputFlags defines the flags that choose what `gonf plan` writes
// and where.
func (f *planFlags) defineOutputFlags() {
	f.outDir = f.fs.String("o", ".", "output directory for plan.jsonl and blobs/ (\".\" is the current directory); "+
		"created 0700 when missing; an existing one is left as it is but must be yours, not world-writable "+
		"and not group-writable except by your private group")
	f.stdout = f.fs.Bool("stdout", false, "print plan JSONL to stdout instead of writing plan.jsonl "+
		"(refused for a plan carrying secret material unless -with-secrets is given)")
	f.withSecrets = f.fs.Bool("with-secrets", false, "with -stdout: print a plan carrying secret material anyway, "+
		"as an executable secret artifact")
	f.redacted = f.fs.Bool("redacted", false, "print a redacted, non-replayable human preview to stdout instead of a plan")
	f.planID = f.fs.String("id", "plan", "plan id written into the header")
	f.plaintext = f.fs.Bool("plaintext", false, "with -o: write a plaintext plan.jsonl even for a plan carrying "+
		"secret material while an operator recipients file exists (without it, such a plan is sealed by default "+
		"to dir/plan.age, task 5b2); cannot combine with -seal, -stdout or -redacted")
}

// defineSealFlags defines -seal and the flags that only apply with it.
func (f *planFlags) defineSealFlags() {
	f.seal = f.fs.Bool("seal", false, "age-encrypt the plan (docs/design/plan-encryption.md) instead of writing it in the "+
		"clear: writes dir/plan.age (or, with -stdout, the sealed bytes to stdout) to the union of -recipient flags "+
		"and the recipients file; refused with zero recipients (never falls back to plaintext), and cannot "+
		"combine with -redacted or -with-secrets")
	f.fs.Var(&f.recipients, "recipient", "an age1pq recipient to seal to with -seal, or when -o seals a plan "+
		"carrying secret material by default because a recipients file exists (repeatable); unioned with the "+
		"recipients file (${XDG_CONFIG_HOME:-$HOME/.config}/gonf/recipients by default, one age1pq recipient per "+
		"line, # comments allowed, hardened the same way as -identity: must be a regular file, owned by you, "+
		"not writable by group or other) unless -no-default-recipients is given")
	f.recipientsFile = f.fs.String("recipients-file", "", "read the recipients file from this path instead of the "+
		"ambient default; still unioned with -recipient flags; a missing file here is an error, not silently empty "+
		"(without -seal, naming it opts -o into sealing a plan carrying secret material by default)")
	f.noDefault = f.fs.Bool("no-default-recipients", false, "do not read the ambient default recipients "+
		"file; seal only to -recipient flags (and -recipients-file, if also given); without -seal, the ambient "+
		"file then does not make -o seal by default")
	f.forTarget = f.fs.String("for", "", "with -seal: seal one artifact per destination host instead of one whole-plan "+
		"artifact — name a registered host, cluster or fleet. Records once per host (a ForHosts body for another "+
		"host is never resolved) and writes dir/plan-<host>.age per host, sealed to that host's "+
		"api.WithPlanRecipient plus the union of -recipient/recipients-file; refuses before writing anything if "+
		"any resolved host lacks a recipient, or if that union is empty (same zero-recipient refusal as plain "+
		"-seal, so the operator can always open what they just sealed); with -stdout, resolves to exactly one "+
		"host or is refused")
	f.sign = f.fs.String("sign", "", "with -seal: sign each sealed artifact (every host's, with -for) with the "+
		"Ed25519 signer in this file, wrapping it in a GONF-SIGNED-PLAN/1 envelope with a signed-at time "+
		"(docs/design/plan-signing.md); make one with gonf plan-signer-keygen. The file must be a regular file owned "+
		"by you with no group or other permission bit; no default path")
}

// cliPlan is `gonf plan`: parse the flags, refuse contradicting ones (exit
// 2), then dispatch to the redacted preview, a sealed write (-seal, with
// -for and -sign handled by planSealedEntry), plaintext stdout or the
// output directory (planToDirDefault, plan_autoseal.go: plaintext, or
// sealed by default for a sensitive plan when an operator recipients file
// exists and -plaintext is not given; task 5b2).
func cliPlan(args []string) int {
	f := newPlanFlags()
	if err := f.fs.Parse(args); err != nil {
		return 2
	}
	tasks := f.fs.Args()
	if len(tasks) == 0 {
		eprintln("usage: " + planUsage)
		return 2
	}
	if msg := f.conflict(); msg != "" {
		eprintln("plan: " + msg)
		return 2
	}
	switch {
	case *f.redacted:
		return planPreview(*f.planID, tasks)
	case *f.seal:
		return planSealedEntry(f.sealRequest(tasks), *f.sign)
	case *f.stdout:
		return planToStdout(*f.planID, tasks, *f.withSecrets)
	}
	return planToDirDefault(f.sealRequest(tasks), *f.plaintext)
}

// sealRequest is the parsed flags as planSealedEntry's request (the signer
// is loaded there, from -sign), and as planToDirDefault's, which may seal
// by default with the same recipient sources (never signed: -sign needs an
// explicit -seal).
func (f *planFlags) sealRequest(tasks []string) sealRequest {
	return sealRequest{
		outDir: *f.outDir, planID: *f.planID, tasks: tasks, toStdout: *f.stdout, forTarget: *f.forTarget,
		recipientFlags: f.recipients, recipientsFile: *f.recipientsFile, noDefaultRecipients: *f.noDefault,
	}
}

// conflict returns why the parsed flags contradict each other, or "": the
// output-flag rules (planOutputConflict), -plaintext's (plaintextConflict),
// -for and -sign only with -seal, and -sign with an empty path (given but
// naming no file, which must not silently mean "unsigned"). -for and -sign
// stay refused without an explicit -seal even though a sensitive plan may
// be sealed by default (task 5b2): sensitivity is known only after
// recording, -for changes how the plan is recorded, and -sign on a run
// that turns out plaintext would be silently ignored.
func (f *planFlags) conflict() string {
	if msg := planOutputConflict(f.fs, *f.stdout, *f.withSecrets, *f.redacted, *f.seal); msg != "" {
		return msg
	}
	if msg := plaintextConflict(*f.plaintext, *f.seal, *f.stdout, *f.redacted); msg != "" {
		return msg
	}
	signSet := false
	f.fs.Visit(func(fl *flag.Flag) { signSet = signSet || fl.Name == "sign" })
	switch {
	case *f.forTarget != "" && !*f.seal:
		return "-for only applies to -seal"
	case signSet && !*f.seal:
		return "-sign only applies to -seal: only a sealed plan.age is signed"
	case signSet && *f.sign == "":
		return "-sign needs a signer file path (make one with gonf plan-signer-keygen)"
	}
	return ""
}

// plaintextConflict returns why -plaintext contradicts the other output
// flags, or "". -plaintext only opts `-o dir` out of sealing by default
// (task 5b2): with -seal it contradicts the explicit request; -stdout
// never seals by default (it refuses a sensitive plan unless -with-secrets)
// and -redacted is a preview, so there it would silently mean nothing.
func plaintextConflict(plaintext, sealed, stdout, redacted bool) string {
	switch {
	case !plaintext:
		return ""
	case sealed:
		return "-plaintext cannot combine with -seal: choose a sealed plan.age or a plaintext plan.jsonl"
	case stdout || redacted:
		return "-plaintext only applies to -o dir: -stdout and -redacted never seal by default " +
			"(-stdout -with-secrets prints a plan carrying secret material)"
	}
	return ""
}

// planOutputConflict returns why the plan output flags contradict each
// other, or "" when they do not: -with-secrets only modifies -stdout,
// -redacted is an output of its own so combining it with -stdout,
// -with-secrets, -o or -seal is refused instead of silently picking one,
// and -seal is refused together with -with-secrets (a sealed export needs
// no plaintext override; task 2b2, docs/design/plan-encryption.md "Operator UX")
// regardless of -stdout. (-stdout with an explicit -o keeps its
// long-standing meaning: -o is ignored; -seal -stdout is allowed — the
// natural CI/pipe form for a sealed plan.)
func planOutputConflict(fs *flag.FlagSet, stdout, withSecrets, redacted, sealed bool) string {
	outSet := false
	fs.Visit(func(f *flag.Flag) { outSet = outSet || f.Name == "o" })
	switch {
	case redacted && (stdout || withSecrets || outSet || sealed):
		return "-redacted prints a preview instead of a plan; it cannot combine with -stdout, -with-secrets, -o or -seal"
	case sealed && withSecrets:
		return "-seal cannot combine with -with-secrets: a sealed plan needs no plaintext override"
	case withSecrets && !stdout:
		return "-with-secrets only applies to -stdout"
	}
	return ""
}
