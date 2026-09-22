package api

import "github.com/snonux/gonf/internal/clihost"

// The api tests exercise the elevated apply path with a fake
// elevatedApplyRunner, never with a real sudo/doas re-exec, so the test
// binary stands in for a process running gonf's CLI. Without this marker
// preflightElevation would refuse every elevated chunk (errNoCLIHost) before
// the fake runner is reached. TestApplyRefusesElevationWithoutCLIHost clears it
// temporarily to pin that refusal.
func init() { _ = clihost.SetForTest(true) } // kept for the whole test binary
