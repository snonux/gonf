package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// CLI parses flags and runs or lists tasks. Returns a process exit code.
//
//	gonf -version
//	gonf -plan-version
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

	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	version := fs.Bool("version", false, "Print version")
	planVersion := fs.Bool("plan-version", false, "Print plan schema version this binary can emit/apply")
	list := fs.Bool("list", false, "List registered tasks")
	profile := fs.String("profile", "", "Override detected profile (fedora, rocky, ...)")
	verbose := fs.Bool("verbose", false, "Debug logging")
	quiet := fs.Bool("quiet", false, "Only warnings and errors (summary still printed)")
	dryRun := fs.Bool("dry-run", false, "Preview changes without applying them")
	dryRunShort := fs.Bool("n", false, "Alias for -dry-run")
	privFlag := fs.String("privilege", "none", "Privilege helper for Privileged() tasks: none|sudo|doas")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}

	switch {
	case *verbose:
		logger.SetLevel(logger.LevelDebug)
	case *quiet:
		logger.SetLevel(logger.LevelWarn)
	default:
		logger.SetLevel(logger.LevelInfo)
	}

	if *dryRun || *dryRunShort {
		resource.SetDryRun(true)
	}
	if m, err := privilege.ParseMode(*privFlag); err != nil {
		fmt.Fprintf(os.Stderr, "privilege: %v\n", err)
		return 2
	} else {
		api.SetPrivilege(m)
	}

	if *profile != "" {
		api.SetProfileOverride(*profile)
	}

	// Resolve When guards after -profile so init()-queued tasks see CLI facts.
	api.Activate(api.DetectFacts())

	if *version {
		fmt.Println(internal.Version)
		return 0
	}
	if *planVersion {
		fmt.Println(plan.CurrentVersion)
		return 0
	}

	if *list {
		return cliList()
	}

	names := fs.Args()
	if len(names) == 0 {
		printUsage()
		return 2
	}

	switch names[0] {
	case "plan":
		return cliPlan(names[1:])
	case "apply":
		return cliApply(names[1:])
	case "push":
		return cliPush(names[1:])
	case "fleet":
		return cliFleet(ctx, names[1:])
	case "hosts":
		return cliHosts()
	case "fleets":
		return cliFleets()
	}

	if err := api.Run(names...); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func cliList() int {
	infos := api.Tasks()
	if len(infos) == 0 {
		fmt.Fprintln(os.Stderr, "no tasks registered")
		return 1
	}
	for _, t := range infos {
		if t.Description != "" {
			fmt.Printf("%s\t%s\n", t.Name, t.Description)
		} else {
			fmt.Println(t.Name)
		}
	}
	return 0
}

func cliPlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	outDir := fs.String("o", ".", "output directory for plan.jsonl and blobs/")
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

	planDir := *outDir
	if *stdout {
		dir, err := os.MkdirTemp("", "gonf-plan-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "plan: temp dir: %v\n", err)
			return 1
		}
		defer func() { _ = os.RemoveAll(dir) }()
		planDir = dir
	}

	ops, err := api.RecordPlan(*planID, planDir, tasks...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan: %v\n", err)
		return 1
	}
	if *stdout {
		for _, op := range ops {
			if op.Blob != "" {
				fmt.Fprintln(os.Stderr, "plan: -stdout cannot emit plans that need blobs/; use -o <dir>")
				return 1
			}
		}
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plan: encode: %v\n", err)
		return 1
	}
	if *stdout {
		if _, err := os.Stdout.Write(raw); err != nil {
			fmt.Fprintf(os.Stderr, "plan: write stdout: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "wrote stdout (%d ops)\n", len(ops))
		return 0
	}
	if err := os.MkdirAll(*outDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "plan: %v\n", err)
		return 1
	}
	outPath := filepath.Join(*outDir, "plan.jsonl")
	if err := os.WriteFile(outPath, raw, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "plan: write %s: %v\n", outPath, err)
		return 1
	}
	fmt.Printf("wrote %s (%d ops)\n", outPath, len(ops))
	return 0
}

func cliApply(args []string) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "Preview changes without applying them")
	dryRunShort := fs.Bool("n", false, "Alias for -dry-run")
	applyDir := fs.String("apply-dir", "", "sticky staging dir for multi-chunk push (skip wipe)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dryRun || *dryRunShort {
		resource.SetDryRun(true)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: gonf apply [-n|-dry-run] [-apply-dir dir] <plan.jsonl|->")
		return 2
	}
	planPath := rest[0]
	if planPath == "-" {
		return cliApplyStdin(*applyDir)
	}
	raw, err := os.ReadFile(planPath)
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

func cliApplyStdin(applyDir string) int {
	var (
		runDir  string
		cleanup func()
		err     error
	)
	if applyDir != "" {
		if err := os.MkdirAll(applyDir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "apply: apply-dir: %v\n", err)
			return 1
		}
		// Loud: the sticky path under /tmp is predictable, so a local
		// attacker on the remote host can pre-create it foreign-owned
		// (0777). That Chmod fails EPERM — discarding the error (as this
		// path once did) would let the pushed blobs land in an
		// attacker-writable dir, ready for substitution or theft.
		if err := os.Chmod(applyDir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "apply: apply-dir %s: %v\n", applyDir, err)
			return 1
		}
		if err := verifyStickyDirOwned(applyDir); err != nil {
			fmt.Fprintf(os.Stderr, "apply: %v\n", err)
			return 1
		}
		runDir = applyDir
		cleanup = func() {} // sticky — caller owns lifecycle
	} else {
		runDir, cleanup, err = plan.NewApplyRunDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "apply: run dir: %v\n", err)
			return 1
		}
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
	fmt.Fprintln(os.Stderr, "usage: gonf [-list] [-version] [-plan-version] [-profile=...] [-verbose|-quiet] [-dry-run|-n] [-privilege=none|sudo|doas] <task> [task...]")
	fmt.Fprintln(os.Stderr, "       gonf plan [-o dir|-stdout] [-id name] <task> [task...]")
	fmt.Fprintln(os.Stderr, "       gonf apply [-n|-dry-run] [-apply-dir dir] <plan.jsonl|->")
	fmt.Fprintln(os.Stderr, "       gonf push [-n] [-id name] [-privilege=...] [-- ssh-args...] user@host <task> [task...]")
	fmt.Fprintln(os.Stderr, "       gonf fleet [-n] [-j N] [-id name] [-host-timeout 10m] <fleet> <task> [task...]")
	fmt.Fprintln(os.Stderr, "       gonf hosts | fleets")
}
