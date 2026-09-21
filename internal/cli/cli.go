package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/dnszone"
)

// cliOptions contains the process-wide flags consumed by CLI.
type cliOptions struct {
	version              bool
	planVersion          bool
	strictPreviewVersion bool
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
//	gonf -list
//	gonf -profile=fedora
//	gonf -verbose | -quiet
//	gonf -dry-run | -n
//	gonf plan [-o dir|-stdout] [-id name] <task>...  # emit plan.jsonl (or stdout)
//	gonf apply [-n] <plan.jsonl|->               # apply file or GONF-PUSH/1 stdin
//	gonf <task> [task...]                            # RecordPlan + Apply locally
func CLI() int {
	// Signal-derived context for the fleet fan-out: SIGINT/SIGTERM cancel
	// in-flight ssh pushes. Only the fleet path is context-aware (bounded
	// decision); local apply and single-host push are not.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Remove the private dir the gonf binary was cross-compiled into for
	// remote hosts (if any push needed one) once this run is over.
	defer func() { _ = cleanupRemoteBuilds() }()

	options, err := parseCLIFlags(os.Args[0], os.Args[1:])
	if err != nil {
		return 2
	}
	if err := configureCLI(options); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	api.Activate(api.DetectFacts())
	return runCLI(ctx, options)
}

func parseCLIFlags(program string, args []string) (cliOptions, error) {
	fs := flag.NewFlagSet(program, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	version := fs.Bool("version", false, "Print version")
	planVersion := fs.Bool("plan-version", false, "Print plan schema version this binary can emit/apply")
	strictPreviewVersion := fs.Bool("strict-preview-version", false, "Print strict remote preview capability version")
	list := fs.Bool("list", false, "List registered tasks")
	profile := fs.String("profile", "", "Override detected profile (fedora, rocky, ...)")
	verbose := fs.Bool("verbose", false, "Debug logging")
	quiet := fs.Bool("quiet", false, "Only warnings and errors (summary still printed)")
	dryRun := fs.Bool("dry-run", false, "Preview changes without applying them")
	dryRunShort := fs.Bool("n", false, "Alias for -dry-run")
	privFlag := fs.String("privilege", "none", "Privilege helper for Privileged() tasks: none|sudo|doas")
	cmdTimeout := fs.Duration("cmd-timeout", exec.DefaultTimeout(), "default per-command timeout for backend execs (package manager, systemctl, crontab, ...; 0 or negative keeps the current default)")
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	return cliOptions{
		version:              *version,
		planVersion:          *planVersion,
		strictPreviewVersion: *strictPreviewVersion,
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

func runCLI(ctx context.Context, options cliOptions) int {
	if options.version {
		fmt.Println(internal.Version)
		return 0
	}
	if options.planVersion {
		fmt.Println(plan.CurrentVersion)
		return 0
	}
	if options.strictPreviewVersion {
		fmt.Println(internal.StrictPreviewVersion)
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

	switch names[0] {
	case "dns-zone-equivalent":
		return cliDNSZoneEquivalent(names[1:])
	case "dns-zone-serial":
		return cliDNSZoneSerial(names[1:])
	case "plan":
		return cliPlan(names[1:])
	case "apply":
		return cliApply(names[1:])
	case "push":
		return cliPush(ctx, names[1:])
	case "cluster":
		return cliCluster(ctx, names[1:])
	case "fleet":
		return cliFleet(ctx, names[1:])
	case "hosts":
		return cliHosts()
	case "clusters":
		return cliClusters()
	case "fleets":
		return cliFleets()
	}

	// RunContext (not Run): this is the CLI process entry point, so a local
	// apply's elevated sudo/doas re-exec should be killed by SIGINT/SIGTERM
	// like the fleet fan-out already is, instead of only ever timing out via
	// ApplyChunksContext's DefaultChunkTimeout.
	if err := api.RunContext(ctx, names...); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func cliDNSZoneEquivalent(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: gonf dns-zone-equivalent <origin> <candidate.zone> <committed.zone>")
		return 2
	}
	candidate, err := os.ReadFile(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "dns-zone-equivalent: read candidate: %v\n", err)
		return 2
	}
	committed, err := os.ReadFile(args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "dns-zone-equivalent: read committed: %v\n", err)
		return 2
	}
	equal, err := dnszone.Equivalent(candidate, committed, args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "dns-zone-equivalent: %v\n", err)
		return 2
	}
	if equal {
		return 0
	}
	return 1
}

func cliDNSZoneSerial(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gonf dns-zone-serial <origin> <zone>")
		return 2
	}
	zone, err := os.ReadFile(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "dns-zone-serial: read zone: %v\n", err)
		return 2
	}
	serial, err := dnszone.Serial(zone, args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "dns-zone-serial: %v\n", err)
		return 2
	}
	fmt.Println(serial)
	return 0
}

func cliList() int {
	infos := api.Tasks()
	if len(infos) == 0 {
		fmt.Fprintln(os.Stderr, "no tasks registered")
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
	stdout := fs.Bool("stdout", false, "print plan JSONL to stdout instead of writing plan.jsonl")
	planID := fs.String("id", "plan", "plan id written into the header")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	tasks := fs.Args()
	if len(tasks) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gonf plan [-o dir|-stdout] [-id name] <task> [task...]")
		return 2
	}

	if *stdout {
		return planToStdout(*planID, tasks)
	}
	return planToDir(*outDir, *planID, tasks)
}

// planToStdout records into memory (as push does) and prints the plan JSONL.
// A plan that needs blobs cannot be printed, so nothing needs to reach the disk:
// no temp directory is created, and blobs a task packages are simply discarded
// with the refusal.
func planToStdout(planID string, tasks []string) int {
	ops, err := api.RecordPlanTo(planID, plan.NewMemoryStore(), tasks...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan: %v\n", err)
		return 1
	}
	for _, op := range ops {
		if op.Blob != "" {
			fmt.Fprintln(os.Stderr, "plan: -stdout cannot emit plans that need blobs/; use -o <dir>")
			return 1
		}
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan: encode: %v\n", err)
		return 1
	}
	if _, err := os.Stdout.Write(raw); err != nil {
		fmt.Fprintf(os.Stderr, "plan: write stdout: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "wrote stdout (%d ops)\n", len(ops))
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
		fmt.Fprintf(os.Stderr, "plan: %v\n", err)
		return 1
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan: encode: %v\n", err)
		return 1
	}
	if err := plan.SecureDir(outDir); err != nil {
		fmt.Fprintf(os.Stderr, "plan: secure output directory: %v\n", err)
		return 1
	}
	if err := plan.WritePrivateFile(outDir, "plan.jsonl", raw); err != nil {
		fmt.Fprintf(os.Stderr, "plan: write %s: %v\n", filepath.Join(outDir, "plan.jsonl"), err)
		return 1
	}
	outPath := filepath.Join(outDir, "plan.jsonl")
	fmt.Printf("wrote %s (%d ops)\n", outPath, len(ops))
	return 0
}

func cliApply(args []string) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "Preview changes without applying them")
	dryRunShort := fs.Bool("n", false, "Alias for -dry-run")
	strictPreview := fs.Bool("strict-preview", false, "Require a no-staging remote preview")
	applyDir := fs.String("apply-dir", "", "sticky staging dir for multi-chunk push (skip wipe)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Escalate-only: a top-level "gonf -n apply ..." already set this via
	// CLI()'s unconditional call before dispatch; don't stomp it back to
	// false just because this subcommand's own flags didn't repeat -n.
	if *dryRun || *dryRunShort || *strictPreview {
		resource.SetDryRun(true)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: gonf apply [-n|-dry-run] [-strict-preview] [-apply-dir dir] <plan.jsonl|->")
		return 2
	}
	planPath := rest[0]
	if planPath == "-" {
		return cliApplyStdin(*applyDir, *strictPreview)
	}
	// Read the plan file the way planToDir wrote it: plan.ReadPrivateFilePath
	// refuses a path that names a directory ("out/"), and a plan.jsonl that is
	// a symlink or not a regular file (a FIFO), instead of following it as
	// os.ReadFile would. The directory is reached the normal way: the elevated
	// re-exec reads a chunk below $TMPDIR, which may be a symlinked path.
	raw, err := plan.ReadPrivateFilePath(planPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply: read %s: %v\n", planPath, err)
		return 1
	}
	ops, err := plan.DecodePlanBytes(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply: %v\n", err)
		return 1
	}
	planDir := filepath.Dir(planPath)
	if err := api.ApplyPlan(ops, planDir); err != nil {
		fmt.Fprintf(os.Stderr, "apply: %v\n", err)
		return 1
	}
	fmt.Printf("applied %s (%d ops)\n", planPath, len(ops))
	return 0
}

func cliApplyStdin(applyDir string, strictPreview bool) int {
	if strictPreview {
		if applyDir != "" {
			fmt.Fprintln(os.Stderr, "apply: -strict-preview cannot use -apply-dir")
			return 2
		}
		// DecodePush refuses blobs without a plan directory. This deliberately
		// avoids NewApplyRunDir, so strict remote preview does not create a
		// staging directory merely to inspect a plan.
		payload, err := plan.DecodePush(os.Stdin, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "apply: %v\n", err)
			return 1
		}
		if err := api.ApplyPlan(payload.Ops, ""); err != nil {
			fmt.Fprintf(os.Stderr, "apply: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "previewed stdin (%d ops)\n", len(payload.Ops))
		return 0
	}
	runDir, cleanup, err := prepareApplyRunDir(applyDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer cleanup()

	payload, err := plan.DecodePush(os.Stdin, runDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply: %v\n", err)
		return 1
	}
	planDir := payload.PlanDir
	if planDir == "" && applyDir != "" {
		planDir = applyDir
	}
	if err := api.ApplyPlan(payload.Ops, planDir); err != nil {
		fmt.Fprintf(os.Stderr, "apply: %v\n", err)
		return 1
	}
	src := "stdin"
	if planDir != "" {
		src = "stdin+blobs"
	}
	fmt.Fprintf(os.Stderr, "applied %s (%d ops)\n", src, len(payload.Ops))
	return 0
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
	fmt.Fprintln(os.Stderr, "usage: gonf [-list] [-version] [-plan-version] [-strict-preview-version] [-profile=...] [-verbose|-quiet] [-dry-run|-n] [-privilege=none|sudo|doas] [-cmd-timeout 5m] <task> [task...]")
	fmt.Fprintln(os.Stderr, "       gonf plan [-o dir|-stdout] [-id name] <task> [task...]")
	fmt.Fprintln(os.Stderr, "       gonf apply [-n|-dry-run|-strict-preview] [-apply-dir dir] <plan.jsonl|->")
	fmt.Fprintln(os.Stderr, "       gonf push [-n|-dry-run|-preview] [-id name] [-privilege=...] [-- ssh-args...] user@host <task> [task...]")
	fmt.Fprintln(os.Stderr, "       gonf cluster [-n|-dry-run|-preview] [-j N] [-id name] [-host-timeout 10m] <cluster> <task> [task...]")
	fmt.Fprintln(os.Stderr, "       gonf fleet [-n|-dry-run|-preview] [-j N] [-id name] [-host-timeout 10m] <fleet> <task> [task...]")
	fmt.Fprintln(os.Stderr, "       gonf hosts | clusters | fleets")
}
