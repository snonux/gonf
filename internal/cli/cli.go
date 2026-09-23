package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/internal/applyproto"
	"github.com/snonux/gonf/internal/clihost"
	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/dnszone"
)

// geteuid is os.Geteuid, swappable in tests: loadSealedIdentities' root
// check must be exercisable without actually running the test suite as
// root, and internal/privilege's geteuid (internal/privilege/privilege.go)
// is the established pattern for this in the module (a package-level var
// only a same-package test ever reassigns, never exported).
var geteuid = os.Geteuid

// cliOptions contains the process-wide flags consumed by CLI.
type cliOptions struct {
	version              bool
	planVersion          bool
	strictPreviewVersion bool
	sealedVersion        bool
	list                 bool
	profile              string
	verbose              bool
	quiet                bool
	dryRun               bool
	privilege            string
	cmdTimeout           time.Duration
	args                 []string
}

// cleanupRemoteBuilds is remote.CleanupBuilds; a variable only so a test can
// prove CLI calls it on return (TestCLICleansUpRemoteBuilds).
var cleanupRemoteBuilds = remote.CleanupBuilds

// CLI parses flags and runs or lists tasks. Returns a process exit code.
//
//	gonf -version
//	gonf -plan-version
//	gonf -strict-preview-version
//	gonf -sealed-version
//	gonf -list
//	gonf -profile=fedora
//	gonf -verbose | -quiet
//	gonf -dry-run | -n
//	gonf plan [-o dir|-stdout [-with-secrets]|-redacted] [-seal [-recipient r]... [-recipients-file f] [-no-default-recipients] [-for host|cluster|fleet]] [-id name] <task>...  # emit plan.jsonl (or stdout), or seal to plan.age (or plan-<host>.age per host with -for)
//	gonf apply [-n] [-identity file]... <plan.jsonl|plan.age|->  # apply file/sealed file or stdin (GONF-PUSH/1, sealed, or bare JSONL)
//	gonf <task> [task...]                            # RecordPlan + Apply locally
func CLI() int {
	// Registration-time misuse in the recipe's main (an empty Task name, a
	// duplicate Host, ...) was reported as a declaration error instead of
	// ending the process; refuse every invocation with it, before any flag
	// or subcommand runs, exactly as the process used to die before CLI().
	if err := declerr.First(); err != nil {
		reportDeclarationError(err)
		return 1
	}
	// This binary's main hands its arguments to the CLI, so the local
	// elevated re-exec (`<this binary> apply <chunk>`) is safe while the CLI
	// runs; api refuses it in any process (or phase of a process) that is not
	// inside CLI() (see internal/clihost). The marker counts running CLI()
	// calls and this call's mark is released on return, so a main that calls
	// CLI() and then api.Apply itself cannot re-exec its own main as root.
	defer clihost.MarkActive()()
	// Remove the private dir the gonf binary was cross-compiled into for
	// remote hosts (if any push needed one) once this run is over.
	defer func() { _ = cleanupRemoteBuilds() }()

	options, err := parseCLIFlags(os.Args[0], os.Args[1:])
	if err != nil {
		return 2
	}
	if err := configureCLI(options); err != nil {
		eprintf("%v\n", err)
		return 2
	}
	// Signal-derived context (signalContext): SIGINT/SIGTERM (and SIGHUP
	// unless ignored) cancel in-flight work. It reaches local task runs
	// (api.RunContext), `gonf apply` (api.ApplyPlanContext), which stop the
	// backend command in flight (SIGTERM, SIGKILL after a grace) and, for an
	// elevated sudo/doas re-exec, close that child's cancel pipe instead of
	// (or, for sudo only, in addition to) signalling the wrapper — see
	// api.runElevatedCmd's doc comment for why a signal to the wrapper alone
	// is not trusted — single-host push (PushToContext) and the
	// cluster/fleet fan-out, which kill their local ssh on cancel (the
	// remote gonf is not signalled; see cliApply). Installed after flag
	// parsing only because whether a repeated signal force-exits depends on
	// the subcommand (forceExitOnRepeat).
	ctx, stop := signalContext(forceExitOnRepeat(options))
	defer stop()
	api.Activate(api.DetectFacts())
	return runCLI(ctx, options)
}

// signalContext returns the CLI's signal context: canceled by the first
// SIGINT, SIGTERM or SIGHUP.
//
// SIGHUP is included because the elevated child under sudo's use_pty gets
// it when its pty goes away (sudo killed), and would otherwise die mid-op
// instead of stopping between ops; but only when it is not ignored: Notify
// un-ignores a signal, which would make `nohup gonf ...` stop on logout, so
// signal.Ignored is checked first, before this process calls Notify for it.
//
// With forceOnRepeat the handler is removed after the first signal
// (context.AfterFunc(ctx, stop)), so a second Ctrl-C or SIGTERM gets the
// default action and force-exits a gonf that is still waiting for a
// graceful stop, skipping deferred cleanup. Without it every later signal
// is swallowed, so the graceful stop and its cleanup always complete. The
// returned stop must be called (deferred) once the CLI returns.
func signalContext(forceOnRepeat bool) (context.Context, context.CancelFunc) {
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		sigs = append(sigs, syscall.SIGHUP)
	}
	ctx, stop := signal.NotifyContext(baseContext(), sigs...)
	if forceOnRepeat {
		context.AfterFunc(ctx, stop)
	}
	return ctx, stop
}

// testBaseContext, set only through setTestBaseContext below (this
// package's own in-package tests, cli_test.go, are its only caller),
// replaces context.Background() as signalContext's parent for the duration
// of one test: internal/cli's `gonf apply` path (cliApplyFile/cliApplyStdin)
// already threads ctx down to api.ApplyPlanContext and so to
// plan.ApplyWithContext, so wrapping this base context with
// internal/runners.WithSet lets a test inject a *runners.Set for one CLI()
// call in place of a process-global internal/testseam fake — the same
// narrow, module-internal test hook internal/clihost.SetForTest already is
// for a different piece of state (AGENTS.md, "Test seams"). It is nil in
// every real invocation (CLI() never sets it), so production always gets
// context.Background() here, unchanged from before.
var testBaseContext context.Context

// baseContext returns testBaseContext when a test set one, else
// context.Background().
func baseContext() context.Context {
	if testBaseContext != nil {
		return testBaseContext
	}
	return context.Background()
}

// setTestBaseContext installs ctx as testBaseContext for the duration of one
// test and restores the previous value (nil, in practice) on c's cleanup.
// Test-only, like testBaseContext itself: cli_test.go is its only caller.
//
// It mirrors internal/testseam's Fake* helpers (internal/testseam.go) on the
// one property that matters here: it sets the same parallel guard they do
// (testseam.ParallelGuardEnv, through c.Setenv) itself, rather than leaning
// on the caller to remember it by hand. Before this helper existed, the one
// caller that used testBaseContext set that guard manually right next to the
// assignment — correct today, but nothing tied the two together, so a later
// test could assign testBaseContext directly, forget the guard, and race
// silently against another parallel test instead of panicking loudly the way
// every testseam.Fake* call already does. Routing the assignment through
// this helper closes that gap the same way testseam's own slot.push does.
func setTestBaseContext(c testseam.Cleaner, ctx context.Context) {
	c.Setenv(testseam.ParallelGuardEnv, "1")
	old := testBaseContext
	testBaseContext = ctx
	c.Cleanup(func() { testBaseContext = old })
}

// forceExitOnRepeat reports whether a second signal may force-exit this
// invocation: yes for the interactive outer process, no for `gonf apply`.
// That covers the elevated child (sudo relays both the terminal's SIGINT and
// the outer gonf's SIGTERM to it, so it routinely gets two signals) and the
// destination side of a push (`gonf apply -`). Killing either mid-stop
// would skip deferred cleanup: ConfigSet lock release, staging and run-dir
// removal.
func forceExitOnRepeat(options cliOptions) bool {
	return len(options.args) == 0 || options.args[0] != "apply"
}

func parseCLIFlags(program string, args []string) (cliOptions, error) {
	fs := flag.NewFlagSet(program, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	version := fs.Bool("version", false, "Print version")
	planVersion := fs.Bool("plan-version", false, "Print plan schema version this binary can emit/apply")
	strictPreviewVersion := fs.Bool("strict-preview-version", false, "Print strict remote preview capability version")
	sealedVersion := fs.Bool("sealed-version", false, "Print sealed-plan (plan.age) capability version this binary can decrypt/apply")
	list := fs.Bool("list", false, "List registered tasks")
	profile := fs.String("profile", "", "Override detected profile (fedora, rocky, ...)")
	verbose := fs.Bool("verbose", false, "Debug logging")
	quiet := fs.Bool("quiet", false, "Only warnings and errors (summary still printed)")
	dryRun := fs.Bool("dry-run", false, "Preview changes without applying them")
	dryRunShort := fs.Bool("n", false, "Alias for -dry-run")
	privFlag := fs.String("privilege", "none", "Privilege helper for Privileged() tasks: none|sudo|doas")
	cmdTimeout := fs.Duration("cmd-timeout", exec.DefaultTimeout(), fmt.Sprintf("default per-command timeout for backend execs "+
		"(package manager, systemctl, crontab, ...) and File/ConfigSet validators; a backend exec that outlives it "+
		"gets SIGTERM and, %v later, SIGKILL; 0 or negative keeps the current default; a non-default value is "+
		"forwarded to the elevated re-exec and to a remote gonf apply that accepts it", exec.CancelGrace))
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	return cliOptions{
		version:              *version,
		planVersion:          *planVersion,
		strictPreviewVersion: *strictPreviewVersion,
		sealedVersion:        *sealedVersion,
		list:                 *list,
		profile:              *profile,
		verbose:              *verbose,
		quiet:                *quiet,
		dryRun:               *dryRun || *dryRunShort,
		privilege:            *privFlag,
		cmdTimeout:           *cmdTimeout,
		args:                 fs.Args(),
	}, nil
}

func configureCLI(options cliOptions) error {
	if options.cmdTimeout > 0 {
		api.SetCommandTimeout(options.cmdTimeout)
	}
	switch {
	case options.verbose:
		logger.SetLevel(logger.LevelDebug)
	case options.quiet:
		logger.SetLevel(logger.LevelWarn)
	default:
		logger.SetLevel(logger.LevelInfo)
	}
	// Unconditional: CLI() is the sole real process entry point (and the
	// single point every test re-enters per invocation), so it must always
	// reflect this invocation's own top-level flags exactly — including
	// resetting to false — rather than only ever escalating to true. That
	// makes dry-run deterministic per call instead of sticky across
	// repeated CLI() calls in the same process (e.g. under `go test
	// -shuffle`). Subcommand handlers (cliApply/cliPush/cliCluster/
	// cliFleet) escalate-only, so a top-level "gonf -n <subcmd> ..." set
	// here survives their own flag parsing.
	resource.SetDryRun(options.dryRun)
	m, err := privilege.ParseMode(options.privilege)
	if err != nil {
		return fmt.Errorf("privilege: %w", err)
	}
	api.SetPrivilege(m)
	if options.profile != "" {
		api.SetProfileOverride(options.profile)
	}
	return nil
}

// reportDeclarationError logs a declaration error found before CLI started
// at error level (the message is exactly what logger.Fatal used to print
// there) and, when known, the recipe line it was reported at.
func reportDeclarationError(err error) {
	logger.Error("%v", err)
	if loc := declerr.Location(err); loc != "" {
		logger.Error("declared at %s", loc)
	}
}

// runCLI dispatches one configured invocation, in precedence order: an
// informational version flag, -list, a named subcommand, and finally the
// positional arguments as task names to record and apply locally. It
// returns the process exit code (2 for missing arguments/usage).
func runCLI(ctx context.Context, options cliOptions) int {
	if printVersionInfo(options) {
		return 0
	}
	if options.list {
		return cliList()
	}

	names := options.args
	if len(names) == 0 {
		printUsage()
		return 2
	}
	if code, ok := runSubcommand(ctx, names[0], names[1:]); ok {
		return code
	}
	return runTasks(ctx, names)
}

// printVersionInfo prints the value of the first set informational flag
// (-version, then -plan-version, then -strict-preview-version, then
// -sealed-version) to stdout and reports whether one was set. Those flags
// short-circuit everything else and always exit 0.
func printVersionInfo(options cliOptions) bool {
	switch {
	case options.version:
		fmt.Println(internal.Version)
	case options.planVersion:
		fmt.Println(plan.CurrentVersion)
	case options.strictPreviewVersion:
		fmt.Println(internal.StrictPreviewVersion)
	case options.sealedVersion:
		fmt.Println(internal.SealedVersion)
	default:
		return false
	}
	return true
}

// runSubcommand runs the subcommand called name with its remaining args and
// returns its exit code. ok is false when name is not a subcommand, so the
// caller treats it as a task name instead.
func runSubcommand(ctx context.Context, name string, args []string) (code int, ok bool) {
	switch name {
	case "dns-zone-equivalent":
		return cliDNSZoneEquivalent(args), true
	case "dns-zone-serial":
		return cliDNSZoneSerial(args), true
	case "plan":
		return cliPlan(args), true
	case "apply":
		return cliApply(ctx, args), true
	case "push":
		return cliPush(ctx, args), true
	case "cluster":
		return cliCluster(ctx, args), true
	case "fleet":
		return cliFleet(ctx, args), true
	case "hosts":
		return cliHosts(), true
	case "clusters":
		return cliClusters(), true
	case "fleets":
		return cliFleets(), true
	}
	return 0, false
}

// runTasks records and applies the named tasks on this host: exit 0 on
// success, 1 (with "error: ..." on stderr) on any failure.
//
// RunContext (not Run): this is the CLI process entry point, so a local
// apply's elevated sudo/doas re-exec and the backend command of an
// in-process chunk are stopped by SIGINT/SIGTERM like the fleet fan-out
// already is, instead of only ever timing out via ApplyChunksContext's
// DefaultChunkTimeout or the command timeout. A failure caused by such a
// signal is reported as interrupted (interrupted).
func runTasks(ctx context.Context, names []string) int {
	if err := api.RunContext(ctx, names...); err != nil {
		if interrupted(err) {
			eprintf("error: interrupted: %v\n", err)
			return 1
		}
		eprintErr("error", err)
		return 1
	}
	return 0
}

// interrupted reports whether err was caused by canceling the CLI's signal
// context (SIGINT/SIGTERM): the cause travels in the error chain, so a
// failure that merely happened to end after a signal (or a timeout, which
// is context.DeadlineExceeded) is not mistaken for one.
func interrupted(err error) bool {
	return errors.Is(err, context.Canceled)
}

func cliDNSZoneEquivalent(args []string) int {
	if len(args) != 3 {
		eprintln("usage: gonf dns-zone-equivalent <origin> <candidate.zone> <committed.zone>")
		return 2
	}
	candidate, err := os.ReadFile(args[1])
	if err != nil {
		eprintf("dns-zone-equivalent: read candidate: %v\n", err)
		return 2
	}
	committed, err := os.ReadFile(args[2])
	if err != nil {
		eprintf("dns-zone-equivalent: read committed: %v\n", err)
		return 2
	}
	equal, err := dnszone.Equivalent(candidate, committed, args[0])
	if err != nil {
		eprintf("dns-zone-equivalent: %v\n", err)
		return 2
	}
	if equal {
		return 0
	}
	return 1
}

func cliDNSZoneSerial(args []string) int {
	if len(args) != 2 {
		eprintln("usage: gonf dns-zone-serial <origin> <zone>")
		return 2
	}
	zone, err := os.ReadFile(args[1])
	if err != nil {
		eprintf("dns-zone-serial: read zone: %v\n", err)
		return 2
	}
	serial, err := dnszone.Serial(zone, args[0])
	if err != nil {
		eprintf("dns-zone-serial: %v\n", err)
		return 2
	}
	fmt.Println(serial)
	return 0
}

func cliList() int {
	infos := api.Tasks()
	if len(infos) == 0 {
		eprintln("no tasks registered")
		return 1
	}
	for _, t := range infos {
		fmt.Println(listLine(t))
	}
	return 0
}

// listLine formats one -list row: "name<TAB>description", or just the name
// when there is no description. An alias keeps its own description verbatim
// (a migrated legacy alias lists exactly as the task it replaces did); only
// an alias without one gets "alias of <target>", so it never shows as an
// unexplained bare name.
func listLine(t api.TaskInfo) string {
	desc := t.Description
	if desc == "" && t.AliasOf != "" {
		desc = "alias of " + t.AliasOf
	}
	if desc == "" {
		return t.Name
	}
	return t.Name + "\t" + desc
}

func cliPlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	outDir := fs.String("o", ".", "output directory for plan.jsonl and blobs/ (\".\" is the current directory); "+
		"created 0700 when missing; an existing one is left as it is but must be yours, not world-writable "+
		"and not group-writable except by your private group")
	stdout := fs.Bool("stdout", false, "print plan JSONL to stdout instead of writing plan.jsonl "+
		"(refused for a plan carrying secret material unless -with-secrets is given)")
	withSecrets := fs.Bool("with-secrets", false, "with -stdout: print a plan carrying secret material anyway, "+
		"as an executable secret artifact")
	redacted := fs.Bool("redacted", false, "print a redacted, non-replayable human preview to stdout instead of a plan")
	planID := fs.String("id", "plan", "plan id written into the header")
	sealFlag := fs.Bool("seal", false, "age-encrypt the plan (docs/plan-encryption.md) instead of writing it in the "+
		"clear: writes dir/plan.age (or, with -stdout, the sealed bytes to stdout) to the union of -recipient flags "+
		"and the recipients file; refused with zero recipients (never falls back to plaintext), and cannot "+
		"combine with -redacted or -with-secrets")
	var recipientFlags stringSliceFlag
	fs.Var(&recipientFlags, "recipient", "an age1pq recipient to seal to with -seal (repeatable); unioned with the "+
		"recipients file (${XDG_CONFIG_HOME:-$HOME/.config}/gonf/recipients by default, one age1pq recipient per "+
		"line, # comments allowed, hardened the same way as -identity: must be a regular file, owned by you, "+
		"not writable by group or other) unless -no-default-recipients is given")
	recipientsFile := fs.String("recipients-file", "", "read the recipients file from this path instead of the "+
		"ambient default; still unioned with -recipient flags; a missing file here is an error, not silently empty")
	noDefaultRecipients := fs.Bool("no-default-recipients", false, "do not read the ambient default recipients "+
		"file; seal only to -recipient flags (and -recipients-file, if also given)")
	forTarget := fs.String("for", "", "with -seal: seal one artifact per destination host instead of one whole-plan "+
		"artifact — name a registered host, cluster or fleet. Records once per host (a ForHosts body for another "+
		"host is never resolved) and writes dir/plan-<host>.age per host, sealed to that host's "+
		"api.WithPlanRecipient plus the union of -recipient/recipients-file; refuses before writing anything if "+
		"any resolved host lacks a recipient; with -stdout, resolves to exactly one host or is refused")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	tasks := fs.Args()
	if len(tasks) == 0 {
		eprintln("usage: gonf plan [-o dir|-stdout [-with-secrets]|-redacted] " +
			"[-seal [-recipient r]... [-recipients-file f] [-no-default-recipients] [-for host|cluster|fleet]] [-id name] <task> [task...]")
		return 2
	}
	if msg := planOutputConflict(fs, *stdout, *withSecrets, *redacted, *sealFlag); msg != "" {
		eprintln("plan: " + msg)
		return 2
	}
	if *forTarget != "" && !*sealFlag {
		eprintln("plan: -for only applies to -seal")
		return 2
	}

	switch {
	case *redacted:
		return planPreview(*planID, tasks)
	case *sealFlag && *forTarget != "":
		return planSealedFor(*outDir, *planID, tasks, *stdout, *forTarget, recipientFlags, *recipientsFile, *noDefaultRecipients)
	case *sealFlag:
		return planSealed(*outDir, *planID, tasks, *stdout, recipientFlags, *recipientsFile, *noDefaultRecipients)
	case *stdout:
		return planToStdout(*planID, tasks, *withSecrets)
	}
	return planToDir(*outDir, *planID, tasks)
}

// planOutputConflict returns why the plan output flags contradict each
// other, or "" when they do not: -with-secrets only modifies -stdout,
// -redacted is an output of its own so combining it with -stdout,
// -with-secrets, -o or -seal is refused instead of silently picking one,
// and -seal is refused together with -with-secrets (a sealed export needs
// no plaintext override; task 2b2, docs/plan-encryption.md "Operator UX")
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

// planPreview records into memory (as push does) and prints the redacted
// human preview (api.EncodeRedactedPreview): secret material is replaced and
// the header is a plan_preview line that no gonf applies. Blob-backed ops
// print their blob references only; their blobs are discarded.
func planPreview(planID string, tasks []string) int {
	ops, err := api.RecordPlanTo(planID, plan.NewMemoryStore(), tasks...)
	if err != nil {
		eprintErr("plan", err)
		return 1
	}
	raw, err := api.EncodeRedactedPreview(ops)
	if err != nil {
		eprintf("plan: %v\n", err)
		return 1
	}
	if _, err := os.Stdout.Write(raw); err != nil {
		eprintf("plan: write stdout: %v\n", err)
		return 1
	}
	eprintf("wrote redacted preview to stdout (%d ops, %d secret-bearing; not a plan, cannot be applied)\n",
		len(ops), len(plan.SensitiveIDs(ops)))
	return 0
}

// planToStdout records into memory (as push does) and prints the plan JSONL.
// A plan that needs blobs cannot be printed, so nothing needs to reach the disk:
// no temp directory is created, and blobs a task packages are simply discarded
// with the refusal. A plan carrying secret material (sensitive ops) is refused
// unless withSecrets: stdout is easily logged, piped or scrolled back, so
// printing secrets must be asked for explicitly. The refusal names the ops
// through api.SensitiveOpNames, like warnSensitivePlan.
func planToStdout(planID string, tasks []string, withSecrets bool) int {
	ops, err := api.RecordPlanTo(planID, plan.NewMemoryStore(), tasks...)
	if err != nil {
		eprintErr("plan", err)
		return 1
	}
	if names := api.SensitiveOpNames(ops); len(names) != 0 && !withSecrets {
		eprintf("plan: -stdout refused: the plan carries secret material in %s; "+
			"use -o <dir> (plan.jsonl is written 0600), -redacted for a human preview, or -stdout -with-secrets to print it anyway\n",
			strings.Join(names, ", "))
		return 1
	}
	for _, op := range ops {
		if op.Blob != "" {
			eprintln("plan: -stdout cannot emit plans that need blobs/; use -o <dir>")
			return 1
		}
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		eprintf("plan: encode: %v\n", err)
		return 1
	}
	if _, err := os.Stdout.Write(raw); err != nil {
		eprintf("plan: write stdout: %v\n", err)
		return 1
	}
	eprintf("wrote stdout (%d ops)\n", len(ops))
	return 0
}

// planToDir records into outDir. RecordPlan makes a best-effort check that
// outDir is usable (a symlink or symlinked ancestor, a file, another user's
// directory, a world- or shared-group-writable directory and an unwritable location are
// refused before any task body runs, creating and changing nothing) and writes
// outDir's blobs only after the whole record succeeded, so a refused plan
// leaves an existing outDir exactly as it was and does not create an absent
// one. What the check misses is still refused when the blobs are committed,
// after the task bodies ran but before anything is written to outDir.
//
// outDir is the operator's directory, not gonf's: a missing one is created
// 0700, but an existing one (the default "-o ." is the recipe checkout) keeps
// its mode - plan.SecureDir verifies it instead of chmod'ing it, and refuses
// one that is not ours or that others (or a group other than our private
// group: on user-private-group systems a fresh 0775 checkout is fine) can
// write, because plan.jsonl carries secret material and a later apply trusts
// it. The refusal says why and what to do (chmod go-w it, or pass -o <private
// dir>). plan.jsonl is always 0600 and a blobs/ directory gonf creates is 0700,
// whatever the mode of outDir; an existing blobs/ that passes the directory
// rule keeps its own mode.
//
// Which prefixes the error carries depends on where it came from: pre-flight
// refusals and a few packaging errors start with "RecordPlan: ", while an
// unknown task ("unknown task ...") or a cycle/body error does not; this
// command only adds its own "plan: " in front of whatever it got.
func planToDir(outDir, planID string, tasks []string) int {
	// An empty -o means the current directory, like the default ".". It must
	// be spelled "." for RecordPlan: its empty planDir means "no plan
	// directory" and would skip the output-directory checks and staging.
	if outDir == "" {
		outDir = "."
	}
	ops, err := api.RecordPlan(planID, outDir, tasks...)
	if err != nil {
		eprintErr("plan", err)
		return 1
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		eprintf("plan: encode: %v\n", err)
		return 1
	}
	if err := plan.SecureDir(outDir); err != nil {
		eprintf("plan: secure output directory: %v\n", err)
		return 1
	}
	if err := plan.WritePrivateFile(outDir, "plan.jsonl", raw); err != nil {
		eprintf("plan: write %s: %v\n", filepath.Join(outDir, "plan.jsonl"), err)
		return 1
	}
	outPath := filepath.Join(outDir, "plan.jsonl")
	fmt.Printf("wrote %s (%d ops)\n", outPath, len(ops))
	warnSensitivePlan(outPath, outDir, ops)
	return 0
}

// warnSensitivePlan tells the operator on stderr that the written plan is a
// secret artifact: it names the secret-bearing ops, never their content.
// The names come from api.SensitiveOpNames, which redacts every resolved
// secret, including a short one an identity equals (recording refuses only
// strong secrets in identities). Nothing is printed for a plan without
// secret material. When there is secret material, it also runs the 0b2
// git-worktree check (warnIfPlanUnignoredInGitWorktree, git_worktree_warn.go)
// on outDir: this is the plaintext -o path (no -seal, which planSealed
// handles separately and writes only an encrypted plan.age — task 2b2), so
// a sensitive plan.jsonl written here is plaintext, and if outDir sits
// inside a git worktree that does not already ignore plan.jsonl, a later
// `git add`/`git commit` by the operator could put it into history
// (docs/plan-encryption.md, threat T2). That warning's own fix advice now
// includes -seal as an alternative to a .gitignore entry or a private -o
// directory.
func warnSensitivePlan(outPath, outDir string, ops []plan.Op) {
	names := api.SensitiveOpNames(ops)
	if len(names) == 0 {
		return
	}
	eprintf("plan: %s carries secret material in clear text (%s); "+
		"it is an executable secret artifact (mode 0600, base64 is not encryption): delete it once applied\n",
		outPath, strings.Join(names, ", "))
	warnIfPlanUnignoredInGitWorktree(outDir)
}

// cliApply runs `gonf apply [flags] <plan.jsonl|->` under ctx, the CLI's
// SIGINT/SIGTERM context: a signal stops the backend command in flight
// (api.ApplyPlanContext; SIGTERM, SIGKILL after a grace) and the apply fails
// with exit 1. This is also the receiving end of a push, so a signal
// reaching it stops its apply the same way. With "-cancel-pipe" (set only by
// the local sudo/doas elevated re-exec; see api.elevatedApplyArgv), this is
// also the elevated child: watchCancelPipe below wires stdin into the same
// ctx cancellation, canceling only once it actually reads the parent's
// one-byte cancel signal (a bare close/EOF, with no byte ever read — e.g.
// the parent process itself dying — does NOT cancel, task 6d2), so the
// parent (api.runElevatedCmd) can trigger this child's graceful stop
// directly, out-of-band from process signalling — which the parent no
// longer trusts alone, since it behaves inconsistently across sudo/doas
// implementations (see runElevatedCmd's doc comment). Canceling a push on
// the controller, however, only kills the local ssh: nothing signals the
// remote gonf, which keeps applying until its next write to the closed
// stdout fails; there is no end-to-end remote cancel.
//
// cliApply is reached for every "gonf apply" invocation alike — the local
// elevated re-exec, the receiving end of a push/preview over ssh, AND an
// ordinary operator command such as `gonf apply plan.jsonl` or a manually
// piped `gonf apply -` — so it does NOT ignore SIGPIPE unconditionally (task
// 7d2 fixed this: it used to, on the wrong assumption that this function is
// reached only as a relayed child's entry point, which broke the SIGPIPE
// disposition, and hence any downstream backend command relying on it, for
// every ordinary local apply too). It ignores SIGPIPE only when this really
// is a relayed child: "-cancel-pipe" (the local elevated re-exec) or
// "-relayed" (set only by internal/remote's remote apply command, for the
// push/preview destination) — see ignoreSIGPIPEForRelayedChild.
func cliApply(ctx context.Context, args []string) int {
	f, rest, err := parseApplyFlags(args)
	if err != nil {
		return 2
	}
	// Ignore SIGPIPE only for a genuine relayed child (see
	// ignoreSIGPIPEForRelayedChild's doc comment and cliApply's own doc
	// comment above, task 7d2): neither marker is set for an ordinary local
	// "gonf apply <plan.jsonl>" or a manually piped-plan "gonf apply -",
	// which both keep the default SIGPIPE disposition.
	if f.cancelPipe || f.relayed {
		ignoreSIGPIPEForRelayedChild()
	}
	// Escalate-only: a top-level "gonf -n apply ..." already set this via
	// CLI()'s unconditional call before dispatch; don't stomp it back to
	// false just because this subcommand's own flags didn't repeat -n.
	if f.dryRun || f.strictPreview {
		resource.SetDryRun(true)
	}
	if len(rest) != 1 {
		eprintln("usage: gonf apply [-n|-dry-run] [-strict-preview] [-apply-dir dir] [-identity file]... <plan.jsonl|plan.age|->")
		return 2
	}
	if rest[0] == "-" {
		if f.cancelPipe {
			// "-cancel-pipe" dedicates stdin to the cancel channel; "-"
			// already dedicates it to the push payload (plan.DecodePush).
			// Nothing sets both together today (elevatedApplyArgv always
			// passes a plan path, never "-"), but refuse the combination
			// explicitly instead of silently letting one of them win.
			eprintln("apply: -cancel-pipe cannot be combined with stdin plan input \"-\": " +
				"stdin already carries the push payload")
			return 2
		}
		return cliApplyStdin(ctx, f.applyDir, f.strictPreview, f.identityPaths)
	}
	if f.cancelPipe {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		go watchCancelPipe(os.Stdin, cancel)
	}
	return cliApplyFile(ctx, rest[0], f.identityPaths)
}

// applyFlags is cliApply's own "apply" subcommand flags, parsed by
// parseApplyFlags. Grouping them here (task xd2) keeps cliApply itself
// short and gives internal/cli's fitness test
// (TestParseApplyFlagsAcceptsProducerArgv) a parse entry point it can feed
// argv built the same way the real producers (api's elevatedApplyArgv,
// internal/remote's remoteApplyCmd) build it, without going through
// cliApply's other side effects (SIGPIPE handling, context wiring, actually
// applying a plan). identityPaths (task 3b2) is not part of that shared
// producer/consumer wire contract — neither producer ever sets -identity,
// since sealed apply is never reached through the elevated re-exec or a
// push/preview session (docs/plan-encryption.md: sealed apply is always a
// direct, interactive/scripted `gonf apply` of a file or manually piped
// stdin) — so it is not registered through internal/applyproto.
type applyFlags struct {
	dryRun        bool
	strictPreview bool
	applyDir      string
	cancelPipe    bool
	relayed       bool
	identityPaths []string
}

// parseApplyFlags parses cliApply's "apply" subcommand flags from args and
// returns the parsed flags plus the remaining positional args (the
// plan.jsonl/plan.age path or "-"). The two wire-contract flags are
// registered through applyproto.RegisterFlags under applyproto.CancelPipeFlag/
// RelayedFlag — the single source of truth also used by the producers that
// build the argv this flag set must accept, instead of this function
// spelling "cancel-pipe"/"relayed" as its own literals (see
// internal/applyproto's doc comment for the failure that let a one-sided
// rename slip past the whole test suite). -identity (task 3b2, repeatable:
// only used when the positional argument sniffs as a sealed plan.age) is
// this function's own flag, not part of that shared contract.
func parseApplyFlags(args []string) (applyFlags, []string, error) {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "Preview changes without applying them")
	dryRunShort := fs.Bool("n", false, "Alias for -dry-run")
	strictPreview := fs.Bool("strict-preview", false, "Require a no-staging remote preview")
	applyDir := fs.String("apply-dir", "", "sticky staging dir for multi-chunk push (skip wipe)")
	cancelPipe, relayed := applyproto.RegisterFlags(fs,
		"internal: treat stdin as an out-of-band cancel channel (a byte written to it, then "+
			"closed, cancels this apply's context; a bare close/EOF with no byte ever read does "+
			"not, task 6d2); set only by the local sudo/doas elevated re-exec "+
			"(api.runElevatedCmd), never for interactive or piped-plan (\"-\") use",
		"internal: this apply's stdout/stderr are being relayed by another gonf process (the "+
			"local elevated sudo/doas re-exec, which sets -cancel-pipe instead, or the "+
			"receiving end of a push/preview over ssh) — ignore SIGPIPE for the whole run so "+
			"that relaying process dying mid-apply (crash, OOM-kill) does not SIGPIPE-kill "+
			"this one too; set only by internal/remote's remote apply command, never for an "+
			"interactive or manually piped-plan (\"-\") local apply")
	var identityPaths []string
	fs.Func("identity", "path to an age1pq identity file (repeatable; only used when <plan.jsonl|-> "+
		"sniffs as a sealed plan.age); default for a non-root invocation: "+
		"${XDG_CONFIG_HOME:-$HOME/.config}/gonf/identity; as root (sudo/doas), -identity is "+
		"required, since HOME/XDG_CONFIG_HOME are ambiguous there", func(v string) error {
		identityPaths = append(identityPaths, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return applyFlags{}, nil, err
	}
	return applyFlags{
		dryRun:        *dryRun || *dryRunShort,
		strictPreview: *strictPreview,
		applyDir:      *applyDir,
		cancelPipe:    *cancelPipe,
		relayed:       *relayed,
		identityPaths: identityPaths,
	}, fs.Args(), nil
}

// ignoreSIGPIPEForRelayedChild makes this process ignore SIGPIPE for the
// rest of its run. Call it only when this process genuinely IS a relayed
// child: a local elevated sudo/doas re-exec (api.runElevatedCmd, marked by
// "-cancel-pipe") or the receiving end of a push/preview over ssh
// (internal/remote's remoteApplyCmd, marked by "-relayed") — either way
// logger.RunRelayed owns the pipe (or, for push, the ssh channel feeding
// one) this child's stdout and stderr are wired to, and the controller is
// its only reader.
//
// cliApply itself is NOT that signal: it is also reached for an ordinary,
// non-relayed local apply (`gonf apply <plan.jsonl>`, ALSO a documented
// operator command, see docs/plan-encryption.md's "What travels where
// today" table and docs/plan.md's plan/apply split) and for a plan piped in
// manually (`gonf apply -`, likewise documented). Calling this
// unconditionally from cliApply used to ignore SIGPIPE for every one of
// those too (task lb2's own "scoped to the relayed process tree" claim was
// wrong — measured directly: `gonf apply plan.jsonl` showed SIGPIPE ignored
// in /proc/self/status when it should not have), which silently changed
// how every backend command such an apply runs behaves (see below) and
// broke early-exit behaviour for an operator piping this process's own
// output, e.g. `gonf apply plan.jsonl | head` no longer stopping early when
// head closed its end. cliApply's caller now gates this call on the
// -cancel-pipe/-relayed markers instead (task 7d2), so only a genuine
// relayed child reaches here.
//
// RunRelayed's detached-cat hand-off already keeps a descendant alive once
// it outlives its own gonf's normal exit or a context-cancelled kill (the
// hand-off has time to run because gonf is still the one closing the pipe,
// on its own schedule). It does nothing, however, when the CONTROLLER
// ITSELF dies out from under a still-running child — SIGKILL, the OOM
// killer, a crash: the read end simply vanishes with no hand-off to catch
// it. Go's runtime already treats a SIGPIPE received while writing to file
// descriptor 1 or 2 specially (see the os/signal package docs, "SIGPIPE"):
// unless the signal is otherwise handled, such a write kills the process
// outright, exactly like a plain C program's default disposition. Before
// gonf relayed a child's output at all (task 062), fd 1/2 were the
// terminal, so this never mattered; once they became the write end of a
// pipe with a single, now-dead reader, the very next log line
// (internal/logger) or CLI print killed this child mid-apply (task lb2) —
// confirmed by a probe that SIGKILLed only the controller and found the
// child's own marker write never ran.
//
// Ignoring SIGPIPE here makes such a write return a plain EPIPE error
// instead, the same as it already would on any file descriptor other than
// 1 or 2. internal/logger's logf discards that error (as it already
// discards any other write failure), so the apply keeps going and finishes
// its work even though nothing is listening to the pipe any more; a caller
// piping this child's own stdout/stderr further (rare — it is normally
// exec'd with both wired to a pipe or an ssh channel, never a terminal)
// would see the same "keep going and drop it" behaviour rather than being
// killed. The disposition is process-wide and, being SIG_IGN rather than a
// caught signal, POSIX preserves it across a later exec — including every
// backend command this apply goes on to run (systemctl, a package manager,
// a Command/Cron script), and a shell such as sh/bash does NOT reset it for
// the commands it execs (verified: `sh -c 'yes | head -1'` under an
// ignoring parent leaves `yes` seeing EPIPE, not being killed, exactly as
// gonf's own writes now do). gonf's own backends (internal/exec) capture a
// command's stdout/stderr into buffers rather than wiring them to a live
// pipe another process reads concurrently, so none of them write into a
// pipe that could go away mid-command; a well-behaved program that does
// (GNU coreutils, verified above with `yes`) checks write()'s result and
// exits on EPIPE same as it would have exited on SIGPIPE. A recipe's own
// Command/Cron script that builds a shell pipeline and assumes an
// unresponsive reader kills its producer via signal, without checking
// write() itself, would instead see that producer keep running until
// canceled some other way — a behavior change worth knowing about, not
// one this fix can rule out for every possible script.
func ignoreSIGPIPEForRelayedChild() {
	signal.Ignore(syscall.SIGPIPE)
}

// watchCancelPipe reads r (the elevated child's stdin, wired by
// api.runElevatedCmd to the read end of its cancel pipe) and calls cancel
// only once it has actually read the parent's one-byte cancel signal
// (api.wireElevatedCancelPipe writes exactly one byte, then closes the
// write end). A bare EOF — the read returning 0 bytes with no byte ever
// having arrived — means the parent's write end closed WITHOUT sending that
// byte, i.e. the controller process itself vanished (SIGKILLed, OOM-killed,
// crashed) rather than choosing to cancel; watchCancelPipe treats that as
// "keep applying, do nothing" and returns without ever calling cancel (task
// 6d2). Before this fix, any close — deliberate cancel or the controller
// simply dying — was read as a cancel (io.Copy until EOF, "only the
// close/EOF itself matters"), which defeated task lb2's whole point: the
// elevated child must keep applying even if the controller dies, not treat
// that death as a cancel and abort an in-flight privileged command.
//
// This works the same way regardless of whether the sudo/doas wrapper in
// between would have relayed (or withheld) a process signal correctly, so
// cancellation works the same way under sudo, OpenDoas and native OpenBSD
// doas. cancel is a context.CancelFunc, safe to call more than once (e.g.
// this fires while a real SIGTERM also reached the child directly). A
// normal, uncanceled apply leaves this goroutine parked on the read until
// the process exits (cmd.Stdin's fd is then closed by the OS, delivering a
// bare EOF here that is correctly ignored), which is harmless.
func watchCancelPipe(r io.Reader, cancel context.CancelFunc) {
	var b [1]byte
	for {
		n, err := r.Read(b[:])
		if n > 0 {
			cancel()
			return
		}
		if err != nil {
			// Bare EOF (or any other read error): the parent's write end
			// closed without ever sending the cancel byte. Do nothing.
			return
		}
		// n == 0, err == nil: a spurious empty read some io.Reader
		// implementations can return; retry rather than treating it as
		// either a cancel or an EOF.
	}
}

// cliApplyFile applies the plan file planPath, with its blobs/ sidecars next
// to it. planPath may also be a sealed plan.age (docs/plan-encryption.md,
// task 3b2): the first line is sniffed against the age format's own
// cleartext version banner (isSealedPlanBytes) before anything is decoded
// either way, so an older plan.jsonl and a sealed plan.age take completely
// separate paths from the first line on.
func cliApplyFile(ctx context.Context, planPath string, identityPaths []string) int {
	// Read the plan file the way planToDir/2b2's sealed write wrote it:
	// plan.ReadPrivateFilePath refuses a path that names a directory
	// ("out/"), and a plan file that is a symlink or not a regular file (a
	// FIFO), instead of following it as os.ReadFile would. The directory is
	// reached the normal way: the elevated re-exec reads a chunk below
	// $TMPDIR, which may be a symlinked path. Reading the whole file here
	// also happens to satisfy, for the sealed case, the "read to EOF before
	// applying anything" requirement for free: os.ReadFile-style reads
	// already load every byte before this function does anything else with
	// them.
	raw, err := plan.ReadPrivateFilePath(planPath)
	if err != nil {
		eprintf("apply: read %s: %v\n", planPath, err)
		return 1
	}
	if isSealedPlanBytes(raw) {
		return cliApplySealedFile(ctx, planPath, raw, identityPaths)
	}
	ops, err := plan.DecodePlanBytes(raw)
	if err != nil {
		eprintf("apply: %v\n", err)
		return 1
	}
	if err := applyPlanOps(ctx, ops, filepath.Dir(planPath)); err != nil {
		return 1
	}
	fmt.Printf("applied %s (%d ops)\n", planPath, len(ops))
	return 0
}

// cliApplySealedFile decrypts raw (planPath's full, already-read bytes, a
// sealed plan.age) and applies it, printing the "decrypted and applied ..."
// wording docs/plan-encryption.md's "Provenance" section requires (never
// "verified"/"authenticated": sealing here is confidentiality only).
func cliApplySealedFile(ctx context.Context, planPath string, raw []byte, identityPaths []string) int {
	payload, cleanup, err := decryptAndDecodeSealedPush(bytes.NewReader(raw), identityPaths)
	defer cleanup()
	if err != nil {
		eprintf("apply: %v\n", err)
		return 1
	}
	if err := applyPlanOps(ctx, payload.Ops, payload.PlanDir); err != nil {
		return 1
	}
	fmt.Printf("decrypted and applied %s (%d ops)\n", planPath, len(payload.Ops))
	return 0
}

// applyPlanOps applies ops under ctx and reports a failure on stderr. A
// failure caused by canceling ctx (SIGINT/SIGTERM, see interrupted) is named
// as such, so the operator can tell an interrupted apply from a failing
// resource; the returned error is only a signal for the caller to exit 1.
func applyPlanOps(ctx context.Context, ops []plan.Op, planDir string) error {
	err := api.ApplyPlanContext(ctx, ops, planDir)
	switch {
	case err == nil:
		return nil
	case interrupted(err):
		eprintf("apply: interrupted: %v\n", err)
	default:
		eprintf("apply: %v\n", err)
	}
	return err
}

// cliApplyStdin applies a push payload (plan plus blobs), a sealed plan.age
// stream, or bare JSONL read from stdin, or only previews it with
// strictPreview. The sealed sniff (isSealedPlanBytes) runs on a peek of
// stdin before any of the three decode paths is chosen — see peekSealed —
// so -strict-preview and -apply-dir, which only make sense for the
// unsealed push/preview paths, are refused up front for a sealed stream
// instead of being silently ignored (docs/plan-encryption.md: a sealed
// apply is single-process file-apply semantics, never the strict-preview
// remote-capability check or the multi-chunk sticky dir).
func cliApplyStdin(ctx context.Context, applyDir string, strictPreview bool, identityPaths []string) int {
	br := bufio.NewReader(os.Stdin)
	if isSealedPlanBytes(sealedPeek(br)) {
		if strictPreview {
			eprintln("apply: -strict-preview cannot be combined with a sealed plan.age stream " +
				"(sealed apply is always a real, single-process apply, never a remote-capability preview)")
			return 2
		}
		if applyDir != "" {
			eprintln("apply: -apply-dir cannot be combined with a sealed plan.age stream " +
				"(a sealed apply with blobs stages into its own sealed-run-* directory, never the " +
				"multi-chunk sticky dir)")
			return 2
		}
		return cliApplySealedStdin(ctx, br, identityPaths)
	}
	if strictPreview {
		return cliPreviewStdin(ctx, applyDir, br)
	}
	runDir, cleanup, err := prepareApplyRunDir(applyDir)
	if err != nil {
		eprintln(err)
		return 1
	}
	defer cleanup()

	payload, err := plan.DecodePush(br, runDir)
	if err != nil {
		eprintf("apply: %v\n", err)
		return 1
	}
	planDir := payload.PlanDir
	if planDir == "" && applyDir != "" {
		planDir = applyDir
	}
	if err := applyPlanOps(ctx, payload.Ops, planDir); err != nil {
		return 1
	}
	src := "stdin"
	if planDir != "" {
		src = "stdin+blobs"
	}
	eprintf("applied %s (%d ops)\n", src, len(payload.Ops))
	return 0
}

// cliApplySealedStdin decrypts and applies a sealed plan.age stream whose
// first bytes were already peeked (not consumed) from br by cliApplyStdin.
// See decryptAndDecodeSealedPush for the read-to-EOF-before-applying and
// run-dir logic shared with the file path (cliApplySealedFile); only the
// source name in the printed summary differs here, matching the "stdin" /
// "stdin+blobs" distinction the unsealed stdin path already makes.
func cliApplySealedStdin(ctx context.Context, br *bufio.Reader, identityPaths []string) int {
	payload, cleanup, err := decryptAndDecodeSealedPush(br, identityPaths)
	defer cleanup()
	if err != nil {
		eprintf("apply: %v\n", err)
		return 1
	}
	if err := applyPlanOps(ctx, payload.Ops, payload.PlanDir); err != nil {
		return 1
	}
	src := "stdin"
	if payload.PlanDir != "" {
		src = "stdin+blobs"
	}
	eprintf("decrypted and applied %s (%d ops)\n", src, len(payload.Ops))
	return 0
}

// cliPreviewStdin is the -strict-preview form of cliApplyStdin: it previews
// (dry-runs, see cliApply) a blob-free push payload from r (stdin, already
// wrapped by cliApplyStdin's own peek so no byte the sealed sniff looked at
// is lost).
func cliPreviewStdin(ctx context.Context, applyDir string, r io.Reader) int {
	if applyDir != "" {
		eprintln("apply: -strict-preview cannot use -apply-dir")
		return 2
	}
	// DecodePush refuses blobs without a plan directory. This deliberately
	// avoids NewApplyRunDir, so strict remote preview does not create a
	// staging directory merely to inspect a plan.
	payload, err := plan.DecodePush(r, "")
	if err != nil {
		eprintf("apply: %v\n", err)
		return 1
	}
	if err := applyPlanOps(ctx, payload.Ops, ""); err != nil {
		return 1
	}
	eprintf("previewed stdin (%d ops)\n", len(payload.Ops))
	return 0
}

// ageMagicLine is the first line of every age-encrypted stream (armored or
// binary alike): the format's own cleartext version banner, always ahead of
// any recipient stanza (it has to be, so a decryptor can find a matching
// identity before decrypting anything). cliApplyFile/cliApplyStdin sniff
// this exact line to tell a sealed plan.age from a plaintext
// plan.jsonl/push frame/bare JSONL before deciding how to read the rest of
// the input at all (docs/plan-encryption.md, "Recommended design": `gonf
// apply` "sniffs the first line").
const ageMagicLine = "age-encryption.org/v1"

// maxSealedFrameBytes bounds the fully-decrypted GONF-PUSH/1 frame
// decryptAndDecodeSealedPush reads into memory before trusting any of it:
// age's AEAD authenticates the final segment only once the whole stream has
// been read (see that function's own doc comment), so the whole frame has
// to land in memory before anything can be decoded from it at all — there
// is no way to stream-decode a sealed apply. Without a cap, a crafted or
// corrupted plan.age drives an unbounded io.ReadAll: task be2 measured
// ~10.5 GB peak RSS from a 3.0 MB plan.age whose plan section was a gzip
// bomb (docs/plan-encryption.md, threat T10: recipients are public, so
// anyone can produce a plan.age that decrypts). 512 MiB comfortably covers
// a legitimate sealed frame, including one carrying tar+gzip blobs — this
// cap bounds them in their still-compressed, on-the-wire form, not their
// unpacked size, since readBlobsGzipTar streams extraction straight to disk
// rather than buffering it — while staying far below what would
// meaningfully threaten a typical machine's RAM. Named here, not inlined,
// so it is easy to find and raise if a legitimate sealed plan ever needs
// more. See also plan.MaxDecompressedPushPlan, the separate cap on the
// plan section's OWN gzip decompression inside the frame this bounds.
const maxSealedFrameBytes = 512 << 20 // 512 MiB

// errSealedFrameTooLarge is returned when the decrypted sealed frame would
// exceed maxSealedFrameBytes. The refusal is loud and immediate: nothing
// past the cap is ever decoded or applied.
var errSealedFrameTooLarge = errors.New("sealed plan: decrypted frame exceeds size limit")

// isSealedPlanBytes reports whether data's first line is exactly
// ageMagicLine. data may be a full file's bytes (cliApplyFile, which has
// already read the whole plan file) or only a short peek of a stream
// (cliApplyStdin's sealedPeek); either way only the bytes up to the first
// newline (or all of data, when it is shorter than the magic line and
// carries no newline yet) are compared.
func isSealedPlanBytes(data []byte) bool {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	return string(line) == ageMagicLine
}

// sealedPeek returns up to len(ageMagicLine) bytes from br without
// consuming them (bufio.Reader.Peek), for isSealedPlanBytes to sniff before
// cliApplyStdin decides which of the sealed/push/bare-JSONL paths to read
// the rest of br through. A short read (less data currently buffered than
// that — including none at all, e.g. genuinely empty stdin) simply yields
// fewer bytes, which can never equal ageMagicLine: isSealedPlanBytes then
// correctly reports "not sealed", and the ordinary (unsealed) decode path
// goes on to produce its own read/EOF error for such input, exactly as it
// already did before this sniff existed.
func sealedPeek(br *bufio.Reader) []byte {
	peek, _ := br.Peek(len(ageMagicLine))
	return peek
}

// decryptAndDecodeSealedPush is the decrypt-then-decode core shared by
// cliApplySealedFile and cliApplySealedStdin. It loads identities, opens r
// as a sealed age stream and reads the WHOLE decrypted frame into memory
// before looking at any of it: age authenticates only its final 64 KiB
// segment once that segment has actually been read
// (docs/plan-encryption.md, "Failure handling"), so io.ReadAll-ing the
// decrypt reader to completion — succeeding only once the whole stream has
// authenticated — is what makes "a truncated, bit-flipped, or
// no-matching-identity input applies nothing" true: no run directory is
// ever created, and ApplyPlanContext is never reached, before that full
// read has already succeeded.
//
// Once the decrypted frame is fully in hand, plan.PushHasBlobs decides
// whether it needs a run directory for the blob-unpacking side effect of
// plan.DecodePush: without blobs (the common case: every conf secret today
// is inline content, docs/plan-encryption.md "Plaintext after decryption"),
// DecodePush decodes straight from memory and no plaintext ever touches
// disk on the destination beyond what the ops themselves go on to write.
// With blobs, plan.NewSealedApplyRunDir provides a dedicated
// sealed-run-<pid>-* directory (its own dead-owner-PID sweep, distinct from
// an ordinary push's run dir) — a DecodePush failure after it was created
// (a malformed frame despite a genuine, authenticated decrypt) still
// removes it via the returned cleanup before decryptAndDecodeSealedPush
// returns its error, so nothing partial is left behind either way.
//
// The returned cleanup is always safe to call (a no-op when no run
// directory was created) and the caller must defer it unconditionally,
// including on error, exactly as it would for plan.NewApplyRunDir's own
// cleanup.
//
// The decrypted frame is read through readSealedFrame, capped at
// maxSealedFrameBytes (see that constant's doc comment for why an unbounded
// read here is a real memory-amplification DoS, task be2).
func decryptAndDecodeSealedPush(r io.Reader, identityPaths []string) (*plan.PushPayload, func(), error) {
	noop := func() {}
	identities, err := loadSealedIdentities(identityPaths)
	if err != nil {
		return nil, noop, err
	}
	dr, err := seal.Open(r, identities)
	if err != nil {
		return nil, noop, err
	}
	data, err := readSealedFrame(dr, maxSealedFrameBytes)
	if err != nil {
		return nil, noop, err
	}
	if !plan.PushHasBlobs(data) {
		payload, err := plan.DecodePush(bytes.NewReader(data), "")
		return payload, noop, err
	}
	runDir, cleanup, err := plan.NewSealedApplyRunDir()
	if err != nil {
		return nil, noop, fmt.Errorf("sealed plan: run dir: %w", err)
	}
	payload, err := plan.DecodePush(bytes.NewReader(data), runDir)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	return payload, cleanup, nil
}

// readSealedFrame reads dr (the reader seal.Open returns) fully into
// memory, refusing loudly instead of continuing once more than max bytes
// have come back. max is a parameter (not a direct read of
// maxSealedFrameBytes) so this package's own tests can exercise the cap
// mechanism against a small, fast frame instead of a genuinely huge one.
//
// dr is given io.LimitReader(dr, max+1): reading one byte past max is what
// lets the check below tell "the stream ended exactly at the limit" (fewer
// than max+1 bytes came back: genuine EOF, no error, matching age's normal
// full-stream read) apart from "the stream had more" (max+1 bytes came
// back) without first reading arbitrarily far past it to find out. A
// stream that overflows the cap is never fully read, so age's final-segment
// authentication may not have run for it — that is fine here, since the
// frame is refused either way and nothing from it is ever decoded or
// applied; a frame within the cap is read to genuine EOF exactly as before,
// so this changes nothing about the pre-existing authentication behavior
// for any legitimate-sized input.
func readSealedFrame(dr io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(dr, max+1))
	if err != nil {
		return nil, fmt.Errorf("sealed plan: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w (%d byte limit)", errSealedFrameTooLarge, max)
	}
	return data, nil
}

// loadSealedIdentities resolves the identity files to load for a sealed
// apply: every repeated -identity path, or, when none was given, the
// default identity for a non-root invocation. Root (euid 0) gets no default
// at all and must pass -identity explicitly: under sudo/doas, whether HOME
// and XDG_CONFIG_HOME are the invoking user's or root's own depends on
// env_reset, always_set_home and the doas keepenv/setenv rules, so a
// default here could silently read an unexpected file — or none — instead
// of the operator's actual identity (docs/plan-encryption.md, "Keys", "No
// default identity for root").
//
// None of the errors below carry their own "apply: " prefix: every caller
// of loadSealedIdentities (directly, or through decryptAndDecodeSealedPush)
// already adds exactly one via its own "apply: %v" eprintf, and a second,
// inner prefix here used to double it into a confusing "apply: apply: ..."
// (100 Go Mistakes #52) — e.g. "apply: apply: plan/seal: identity file
// ...: identity file is readable or writable by group or other" (task
// de2). seal.LoadIdentities' own error already names the file and failure
// class, so the loop below returns it unwrapped.
func loadSealedIdentities(paths []string) ([]seal.Identity, error) {
	if len(paths) == 0 {
		if geteuid() == 0 {
			return nil, fmt.Errorf("sealed plan.age: running as root (euid 0); " +
				"-identity is required (sudo/doas leaves HOME/XDG_CONFIG_HOME ambiguous, so no " +
				"default identity path is guessed) — pass -identity <file>")
		}
		def, err := defaultIdentityPath()
		if err != nil {
			return nil, fmt.Errorf("sealed plan.age: %w", err)
		}
		paths = []string{def}
	}
	var identities []seal.Identity
	for _, p := range paths {
		loaded, err := seal.LoadIdentities(p)
		if err != nil {
			return nil, err
		}
		identities = append(identities, loaded...)
	}
	return identities, nil
}

// defaultIdentityPath returns the non-root default identity file path,
// ${XDG_CONFIG_HOME:-$HOME/.config}/gonf/identity (docs/plan-encryption.md,
// "Keys"), the same default gonf plan -seal (task 2b2) uses for its
// recipients file's own directory.
func defaultIdentityPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve $HOME: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "gonf", "identity"), nil
}

func prepareApplyRunDir(applyDir string) (string, func(), error) {
	if applyDir == "" {
		runDir, cleanup, err := plan.NewApplyRunDir()
		if err != nil {
			return "", nil, fmt.Errorf("apply: run dir: %w", err)
		}
		return runDir, cleanup, nil
	}
	if err := prepareStickyApplyDir(applyDir); err != nil {
		return "", nil, err
	}
	return applyDir, func() {}, nil
}

func prepareStickyApplyDir(applyDir string) error {
	if err := os.MkdirAll(applyDir, 0o700); err != nil {
		return fmt.Errorf("apply: apply-dir: %w", err)
	}
	// Loud: the sticky path under /tmp is predictable, so a local attacker on
	// the remote host can pre-create it foreign-owned. Failing Chmod or the
	// ownership check prevents staging blobs into an attacker-writable dir.
	if err := os.Chmod(applyDir, 0o700); err != nil {
		return fmt.Errorf("apply: apply-dir %s: %w", applyDir, err)
	}
	if err := verifyStickyDirOwned(applyDir); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	// The directory slot is reused across pushes, but its contents must not
	// be reused: otherwise stale blobs could resurrect files removed from the
	// source tree. Ownership is verified before wiping the contents.
	if err := wipeDirContents(applyDir); err != nil {
		return fmt.Errorf("apply: apply-dir %s: wipe stale contents: %w", applyDir, err)
	}
	return nil
}

// wipeDirContents removes every entry directly inside dir, leaving dir itself
// (and its already-verified ownership and 0700 mode) untouched. Called on the
// reused sticky -apply-dir, after verifyStickyDirOwned and before extraction,
// so a prior run's leftovers (from a push that never reached
// remote.pushRemoveSticky) cannot leak into the new extraction — see the
// wipe-before-extract comment in cliApplyStdin.
func wipeDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("remove %s: %w", filepath.Join(dir, e.Name()), err)
		}
	}
	return nil
}

// verifyStickyDirOwned refuses to stage into a sticky -apply-dir that a third
// party could have planted: the controller derives the path as
// /tmp/gonf-apply-sticky-<sanitized plan id> (internal/remote), so a local
// attacker on the remote host can pre-create it before the push arrives.
// After MkdirAll + Chmod the path must be a real directory owned by the
// current user. A pre-planted foreign-owned dir already fails the Chmod above
// (EPERM, now loud); a planted symlink survives the Chmod (chmod follows the
// link) and is refused by the IsDir check here.
//
// Root sessions skip the uid check: an elevated apply chunk (sudo -n / doas)
// legitimately reads a sticky dir owned by the SSH login user, and there is no
// in-process way for root to tell that login user apart from another local
// account. This is safe because the blob-upload session always runs first and
// is never privilege-wrapped (internal/remote pushBlobs): as the unprivileged
// login user it either owns the dir (verified below) or fails the Chmod
// loudly — a non-root pre-plant is therefore refused before any chunk runs,
// and only a root attacker could plant or chown a dir past it. Platforms
// without syscall.Stat_t (none of gonf's targets) cannot verify and pass.
func verifyStickyDirOwned(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("apply-dir %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("apply-dir %s is not a directory (pre-planted?)", path)
	}
	if os.Geteuid() == 0 {
		return nil
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("apply-dir %s is not owned by the current user (pre-planted?)", path)
	}
	return nil
}

func printUsage() {
	eprintln("usage: gonf [-list] [-version] [-plan-version] [-strict-preview-version] [-sealed-version] [-profile=...] [-verbose|-quiet] [-dry-run|-n] [-privilege=none|sudo|doas] [-cmd-timeout 5m] <task> [task...]")
	eprintln("       gonf plan [-o dir|-stdout [-with-secrets]|-redacted] " +
		"[-seal [-recipient r]... [-recipients-file f] [-no-default-recipients] [-for host|cluster|fleet]] [-id name] <task> [task...]")
	eprintln("       gonf apply [-n|-dry-run|-strict-preview] [-apply-dir dir] [-identity file]... <plan.jsonl|plan.age|->")
	eprintln("       gonf push [-n|-dry-run|-preview] [-id name] [-privilege=...] [-- ssh-args...] user@host <task> [task...]")
	eprintln("       gonf cluster [-n|-dry-run|-preview] [-j N] [-id name] [-host-timeout 10m] <cluster> <task> [task...]")
	eprintln("       gonf fleet [-n|-dry-run|-preview] [-j N] [-id name] [-host-timeout 10m] <fleet> <task> [task...]")
	eprintln("       gonf hosts | clusters | fleets")
}
