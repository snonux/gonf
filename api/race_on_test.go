//go:build race

package api

// raceEnabled reports whether the test binary runs under the race detector,
// which slows the timing-bound tests roughly tenfold.
const raceEnabled = true
