package cli

import (
	"time"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/resource"
)

// cliSettings is a snapshot of every process-wide setting a CLI invocation
// changes from its flags (configureCLI and the subcommand handlers):
//
//   - dryRun: resource's dry-run flag (-n / -dry-run, subcommand -n,
//     apply -strict-preview);
//   - logLevel: the logger's minimum level (-verbose / -quiet);
//   - privilege: api's privilege helper for Privileged() tasks (-privilege);
//   - profile: api's Facts.Profile override (-profile);
//   - cmdTimeout: internal/exec's default per-command timeout (-cmd-timeout).
//
// A setting the CLI starts changing later belongs here too, so that
// scopeCLISettings keeps covering all of them.
type cliSettings struct {
	dryRun     bool
	logLevel   logger.Level
	privilege  privilege.Mode
	profile    string
	cmdTimeout time.Duration
}

// captureCLISettings reads the current value of every setting in
// cliSettings.
func captureCLISettings() cliSettings {
	return cliSettings{
		dryRun:     resource.DryRun(),
		logLevel:   logger.GetLevel(),
		privilege:  api.Privilege(),
		profile:    api.ProfileOverride(),
		cmdTimeout: api.CommandTimeout(),
	}
}

// restore puts every setting back to the snapshot's value. cmdTimeout is
// always positive (internal/exec refuses a non-positive default), so
// api.SetCommandTimeout never ignores it.
func (s cliSettings) restore() {
	resource.SetDryRun(s.dryRun)
	logger.SetLevel(s.logLevel)
	api.SetPrivilege(s.privilege)
	api.SetProfileOverride(s.profile)
	api.SetCommandTimeout(s.cmdTimeout)
}

// scopeCLISettings snapshots every cliSettings setting and returns a func
// that puts the snapshot back. CLI() defers it before configureCLI runs, so
// none of an invocation's top-level flags outlives the call.
//
// In the real binary the process exits right after CLI() returns, so the
// restore changes nothing there, and no setting needs to live on for the
// process: an elevated re-exec'd child (`gonf apply <chunk>`) and a remote
// gonf get the settings they need through their own argv
// (api.elevatedApplyArgv, remote's -cmd-timeout forwarding), never through
// this process's globals after CLI() returned. The restore matters for
// in-process callers that run several invocations in turn: above all this
// package's tests, which call CLI() under `go test -shuffle=on`, and a
// library main that calls CLI() and then drives api itself (it gets back
// the settings it had before, not the last invocation's -privilege or
// -profile).
//
// Task vg2 introduced this for dry-run alone after a leaked flag made
// TestCLIApplyFileIgnoresStdinWithoutCancelPipe run in dry-run mode under
// one shuffle order; task xg2 extended it to the log level, privilege mode,
// profile override and command timeout, which configureCLI used to leave
// set the same way (e.g. a "-cmd-timeout 50ms" test left every later
// test's backend commands under a 50ms timeout unless it remembered its own
// t.Cleanup reset).
//
// Scope note: the task activation CLI() performs (api.Activate with the
// detected facts) is not one of these settings. It is part of the task
// registry, which a recipe or test resets with api.ResetTasks, and
// TestCLIProfileActivates relies on reading it after CLI() returned.
func scopeCLISettings() (restore func()) {
	return captureCLISettings().restore
}

// scopeDryRun snapshots resource's process-wide dry-run flag alone and
// returns a func that puts the snapshot back. The subcommand handlers
// (cliApply, cliPush, cliCluster, cliFleet) change only this setting, via
// escalateDryRun, and scope it themselves because a test may call a handler
// directly, without CLI()'s scopeCLISettings around it — exactly how
// TestCLIApplySealedStdinRefusesStrictPreview (cliApply escalates before it
// refuses the sealed stream) used to leak dry-run into the next test before
// task vg2.
func scopeDryRun() (restore func()) {
	prev := resource.DryRun()
	return func() { resource.SetDryRun(prev) }
}

// escalateDryRun is the subcommand handlers' escalate-only dry-run setting:
// it turns the flag on when on is true and never turns it off, so a
// top-level "gonf -n <subcmd> ..." (already set by configureCLI before
// dispatch, or pre-set by an in-process caller) survives a subcommand whose
// own flags did not repeat -n. Like scopeDryRun, the returned func restores
// the value found on entry; the handler defers it.
func escalateDryRun(on bool) (restore func()) {
	restore = scopeDryRun()
	if on {
		resource.SetDryRun(true)
	}
	return restore
}
