package remote

import (
	"context"
	"fmt"
	"sync"

	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/plan"
)

// Syncing the gonf binary to a remote host: the Pusher type, its
// constructor, the package-level public API (EnsureRemoteGonf,
// RequireRemoteGonf and the Assume* test seams) and the upgrade decision.
// The steps it drives live in sibling files, one responsibility each:
//
//	sync_buildcache.go  per-platform build cache and build locks
//	sync_gobuild.go     the real "go build" cross-compile (GoBuildRunner)
//	crossbuild.go       the private build directory and cached-binary checks
//	sync_scp.go         scp argv construction and the real SCPRunner
//	sync_probe.go       plan/strict-preview/release version and uname probes
//	sync_staging.go     the remote mktemp staging directory
//	sync_install.go     copying the binary over, installing and verifying it
//
// cmdtimeout.go adds the Pusher's -cmd-timeout capability probe
// (CmdTimeoutProber), which gates forwarding the controller's command
// timeout to the remote apply.

// Pusher bundles the exec/network seams the gonf binary sync (the sync_*.go
// files) needs to build and stage a fresh gonf binary on a remote host:
// cross-compiling it (GoBuildRunner), probing the remote's installed
// plan-schema and strict-preview capability versions (PlanVersionProber and
// StrictPreviewProber), and copying the binary over (SCPRunner) — plus the
// build cache/lock state those seams share (buildGonf). Bundling them in a
// struct instead of separate package-level vars means a test can construct
// its own *Pusher with fake fields and call its methods directly: nothing is
// shared, mutable package state to race on, so such a test is free to run
// with t.Parallel() alongside any other test (see sync_gonf_test.go).
//
// defaultPusher (below) is wired to the real implementations and is what the
// package-level EnsureRemoteGonf/AssumeRemotePlanCurrent functions (the
// sync's public API, in this file) operate on, so every existing external
// caller (api.PushTo, Fanout, and every test that calls
// remote.AssumeRemotePlanCurrent) keeps working completely unchanged.
//
// SSHRunner (remote.go) is deliberately NOT a Pusher field. Unlike these
// seams above — which, when Pusher was introduced, were read only from the
// gonf binary sync and its own test — SSHRunner is stubbed from roughly a
// dozen other test files across api/, internal/cli/ and internal/orchestrate/
// (over a hundred references in total) to fake an end-to-end push without a
// real ssh binary. Folding it into Pusher would require updating every one
// of those call sites in this same change: a materially larger, riskier
// refactor than this one. Left as a documented, well-scoped follow-on (see
// task p5's annotations for the reasoning).
type Pusher struct {
	SCPRunner         func(ctx context.Context, localPath string, t PushTarget, remotePath string) error
	GoBuildRunner     func(ctx context.Context, goos, goarch, out, pkg string) error
	PlanVersionProber func(ctx context.Context, t PushTarget, pc ProbeContext) (int, error)
	// StrictPreviewProber reports the target's strict-preview capability
	// version. It is distinct from the plan schema because strict preview
	// changes transport safety rather than plan encoding.
	StrictPreviewProber func(ctx context.Context, t PushTarget, pc ProbeContext) (int, error)

	// ReleaseVersionProber probes the remote gonf binary's own release
	// version (internal.Version, e.g. "0.12.1", as printed by `gonf
	// -version`) — distinct from PlanVersionProber, which only reports the
	// plan WIRE SCHEMA version. A behavior-only fix (one that changes how
	// gonf applies a plan without changing what a plan encodes — e.g. a
	// template-rendering bug fix) never bumps plan.CurrentVersion, so
	// PlanVersionProber alone can never see it, and a host would keep
	// running the old, buggy binary forever even once the controller has a
	// newer one available to push. EnsureRemoteGonf additionally upgrades
	// when this reports a release older than the controller's own
	// internal.Version, even when the plan schema is unchanged. A nil value
	// (as in a hand-built *Pusher a test doesn't care about this field)
	// simply skips the extra check — the plan-schema check above remains
	// authoritative either way.
	ReleaseVersionProber func(ctx context.Context, t PushTarget, pc ProbeContext) (string, error)

	// CmdTimeoutProber reports whether the target's gonf binary in pc
	// accepts the global flag (e.g. "-cmd-timeout=30s"), so a non-default
	// controller -cmd-timeout is forwarded to the remote apply only where
	// it cannot break the command line (see cmdtimeout.go). Besides the
	// verdict it returns a one-line reason for the CmdTimeoutUnverified
	// case (a sudo/doas refusal, a missing binary), which the warning
	// quotes. A nil value forwards nothing.
	CmdTimeoutProber func(ctx context.Context, t PushTarget, pc ProbeContext, flag string) (CmdTimeoutSupport, string, error)

	// CrossBuildRoot is the parent directory in which this Pusher creates its
	// private build dir (see crossbuild.go). Empty means os.TempDir(). It is
	// a test seam: tests point it at t.TempDir() so nothing lands in the
	// real temp dir.
	CrossBuildRoot string

	// buildMu protects only the fields below (quick reads/writes), never
	// the build itself — see buildKeyLock's doc comment for why a single
	// lock held across the whole struct/package used to serialize every
	// cross-compile.
	buildMu       sync.Mutex
	buildCache    map[string]cachedBuild // "goos/goarch" → built binary (crossbuild.go)
	buildKeyLocks map[string]*sync.Mutex // "goos/goarch" → that key's build lock
	buildDir      *buildDirState         // current private build dir; nil until first build
	retiredDirs   []*buildDirState       // earlier dirs of ours, removed by Close
}

// defaultPusher is the package's production Pusher. EnsureRemoteGonf and
// AssumeRemotePlanCurrent, this file's public API, operate on it, so callers
// outside this package never need to know Pusher exists.
var defaultPusher = NewPusher()

// NewPusher returns a Pusher wired to the real ssh/scp/go-build
// implementations, with a fresh (empty) build cache.
func NewPusher() *Pusher {
	return &Pusher{
		SCPRunner:            defaultSCPRunner,
		GoBuildRunner:        defaultGoBuildRunner,
		PlanVersionProber:    probePlanVersion,
		StrictPreviewProber:  probeStrictPreviewVersion,
		ReleaseVersionProber: probeReleaseVersion,
		CmdTimeoutProber:     probeCmdTimeoutSupport,
		buildCache:           map[string]cachedBuild{},
		buildKeyLocks:        map[string]*sync.Mutex{},
	}
}

// AssumeRemotePlanCurrent is a test seam for callers that fake SSHRunner and
// push: it makes defaultPusher report the controller's plan schema AND
// release version, so EnsureRemoteGonf skips the upgrade path without any
// ssh probe. Restore with the returned func (or t.Cleanup).
//
// Both probes must be faked because EnsureRemoteGonf consults the
// release-version probe whenever the plan schema is current. Faking only the
// plan schema (as this helper once did) left "gonf -version" running over a
// real ssh against the test's fake hosts; the failure was only logged, so
// tests passed while touching the network (task x72). The -cmd-timeout
// capability probe is faked too (as "accepted"): a push reaches it whenever
// a test left a non-default command timeout active. The strict-preview
// probe is deliberately left alone: only preview (RequireRemoteGonf) reads
// it, and preview tests use AssumeRemoteGonfCurrent. A preview reached under
// this helper hits refuseNetworkExecInTests instead of silently probing.
func AssumeRemotePlanCurrent() func() {
	oldPlan := defaultPusher.PlanVersionProber
	oldRelease := defaultPusher.ReleaseVersionProber
	oldCmdTimeout := defaultPusher.CmdTimeoutProber
	defaultPusher.PlanVersionProber = currentPlanVersion
	defaultPusher.ReleaseVersionProber = currentReleaseVersion
	defaultPusher.CmdTimeoutProber = acceptCmdTimeout
	return func() {
		defaultPusher.PlanVersionProber = oldPlan
		defaultPusher.ReleaseVersionProber = oldRelease
		defaultPusher.CmdTimeoutProber = oldCmdTimeout
	}
}

// AssumeRemoteGonfCurrent makes the default pusher report the controller's
// current plan schema, release version and strict-preview capability: every
// probe AssumeRemotePlanCurrent fakes, plus the one strict preview
// (RequireRemoteGonf) needs. It is a test seam for callers that fake SSH
// transport and need to exercise strict preview without a live host.
func AssumeRemoteGonfCurrent() func() {
	restorePush := AssumeRemotePlanCurrent()
	oldStrictPreview := defaultPusher.StrictPreviewProber
	defaultPusher.StrictPreviewProber = currentStrictPreviewVersion
	return func() {
		defaultPusher.StrictPreviewProber = oldStrictPreview
		restorePush()
	}
}

// EnsureRemoteGonf upgrades the remote gonf binary when it cannot apply the
// controller's plan schema (missing gonf, or -plan-version < CurrentVersion),
// OR when the plan schema is fine but the remote binary's own release
// version (gonf -version) is older than the controller's — see
// Pusher.ReleaseVersionProber's doc comment for why that second check
// exists. GOOS/GOARCH come from the target when set, otherwise from remote uname.
// When an upgrade runs, installedPath is the remote binary path to use for
// subsequent apply commands; otherwise it is empty (keep PATH "gonf").
//
// This is a thin wrapper over defaultPusher.EnsureRemoteGonf: its caller,
// the Push-mode Delivery.ToHost (via ensureRuntime, for api.PushTo and
// Fanout), and every test that stubs SCPRunner/GoBuildRunner/
// PlanVersionProber via a Pusher, or calls AssumeRemotePlanCurrent, keeps
// working against this same package-level function.
func EnsureRemoteGonf(ctx context.Context, t PushTarget) (installedPath string, err error) {
	return defaultPusher.EnsureRemoteGonf(ctx, t)
}

// RequireRemoteGonf verifies that the target already has a gonf binary that
// can safely preview this controller's plan. It only runs remote version
// probes; unlike EnsureRemoteGonf, it never cross-compiles, copies, or
// installs a binary. Strict remote preview uses this check so an absent or
// stale runtime fails clearly instead of turning a preview into provisioning.
// pc is the privilege context the probes run in: the one that will run the
// previewed apply chunk (see requireRemoteGonfForChunks), because sudo/doas
// can resolve a different gonf binary than the SSH login's PATH.
func RequireRemoteGonf(ctx context.Context, t PushTarget, pc ProbeContext) error {
	return defaultPusher.RequireRemoteGonf(ctx, t, pc)
}

// RequireRemoteGonf is the Pusher-scoped implementation of
// RequireRemoteGonf. It deliberately treats a missing, unparseable, or older
// release version as a refusal: strict preview must establish that the remote
// runtime has the same behavior as the controller, whereas ordinary push can
// repair a stale runtime through EnsureRemoteGonf.
func (p *Pusher) RequireRemoteGonf(ctx context.Context, t PushTarget, pc ProbeContext) error {
	if t.Host == "" {
		return fmt.Errorf("remote preview: empty host")
	}
	remotePlanVersion, err := p.PlanVersionProber(ctx, t, pc)
	if err != nil {
		return fmt.Errorf("remote preview: probe gonf plan schema: %w", err)
	}
	if remotePlanVersion < plan.CurrentVersion {
		return fmt.Errorf("remote preview: remote gonf plan schema %d is older than controller schema %d; preview does not install or update gonf, run push first", remotePlanVersion, plan.CurrentVersion)
	}
	if p.StrictPreviewProber == nil {
		return fmt.Errorf("remote preview: cannot verify remote strict-preview capability; preview does not install or update gonf, run push first")
	}
	remoteStrictPreviewVersion, err := p.StrictPreviewProber(ctx, t, pc)
	if err != nil {
		return fmt.Errorf("remote preview: probe gonf strict-preview capability: %w", err)
	}
	if remoteStrictPreviewVersion < internal.StrictPreviewVersion {
		return fmt.Errorf("remote preview: remote gonf strict-preview capability %d is older than controller capability %d; preview does not install or update gonf, run push first", remoteStrictPreviewVersion, internal.StrictPreviewVersion)
	}
	if p.ReleaseVersionProber == nil {
		return fmt.Errorf("remote preview: cannot verify remote gonf release version; preview does not install or update gonf, run push first")
	}

	remoteRelease, err := p.ReleaseVersionProber(ctx, t, pc)
	if err != nil {
		return fmt.Errorf("remote preview: probe gonf release version: %w", err)
	}
	if remoteRelease == "" {
		return fmt.Errorf("remote preview: remote gonf did not report a release version; preview does not install or update gonf, run push first")
	}
	remoteVersion, err := parseReleaseVersion(remoteRelease)
	if err != nil {
		return fmt.Errorf("remote preview: remote gonf release version %q: %w", remoteRelease, err)
	}
	controllerVersion, err := parseReleaseVersion(internal.Version)
	if err != nil {
		return fmt.Errorf("remote preview: controller gonf release version %q: %w", internal.Version, err)
	}
	if releaseVersionLess(remoteVersion, controllerVersion) {
		return fmt.Errorf("remote preview: remote gonf release %s is older than controller %s; preview does not install or update gonf, run push first", remoteRelease, internal.Version)
	}
	return nil
}

// EnsureRemoteGonf is the Pusher-scoped implementation of the package-level
// EnsureRemoteGonf above (see its doc comment for the full behavior). It
// reads only p's own fields (SCPRunner, GoBuildRunner, PlanVersionProber,
// build cache) — the sole exception is SSHRunner (remote.go), still a
// package-level var; see Pusher's doc comment for why that one seam is not
// yet folded in here.
func (p *Pusher) EnsureRemoteGonf(ctx context.Context, t PushTarget) (installedPath string, err error) {
	if t.Host == "" {
		return "", fmt.Errorf("ensure gonf: empty host")
	}
	// The bootstrap installs gonf for, and verifies it as, the SSH login
	// user, so it probes in that context (ProbeLogin).
	remoteVer, err := p.PlanVersionProber(ctx, t, ProbeLogin)
	if err != nil {
		return "", fmt.Errorf("ensure gonf: %w", err)
	}

	needUpgrade := remoteVer < plan.CurrentVersion
	switch {
	case needUpgrade:
		logger.Info("push %s: remote plan schema %d < %d — syncing gonf binary",
			t.Destination(), remoteVer, plan.CurrentVersion)
	case p.ReleaseVersionProber != nil:
		if stale, reason := p.remoteReleaseIsStale(ctx, t); stale {
			needUpgrade = true
			logger.Info("push %s: %s — syncing gonf binary", t.Destination(), reason)
		}
	}
	if !needUpgrade {
		return "", nil
	}
	goos, goarch, err := remoteBuildTarget(ctx, t)
	if err != nil {
		return "", fmt.Errorf("ensure gonf: %w", err)
	}
	return p.installRemoteGonf(ctx, t, goos, goarch)
}

// currentPlanVersion, currentReleaseVersion and currentStrictPreviewVersion
// are the fake probes installed by the Assume* test seams: each reports the
// controller's own value, i.e. "the remote gonf is up to date".
func currentPlanVersion(context.Context, PushTarget, ProbeContext) (int, error) {
	return plan.CurrentVersion, nil
}

func currentReleaseVersion(context.Context, PushTarget, ProbeContext) (string, error) {
	return internal.Version, nil
}

func currentStrictPreviewVersion(context.Context, PushTarget, ProbeContext) (int, error) {
	return internal.StrictPreviewVersion, nil
}

func remoteBuildTarget(ctx context.Context, t PushTarget) (string, string, error) {
	goos, goarch := t.GOOS, t.GOARCH
	if goos == "" || goarch == "" {
		detectedOS, detectedArch, err := probeUname(ctx, t)
		if err != nil {
			return "", "", err
		}
		if goos == "" {
			goos = detectedOS
		}
		if goarch == "" {
			goarch = detectedArch
		}
	}
	return goos, goarch, nil
}
