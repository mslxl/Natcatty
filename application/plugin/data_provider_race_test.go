//go:build race

package plugin_test

// dataTestRaceEnabled reports whether the race detector is active. Elapsed-time
// guard rails in the suite are asserted only outside race mode, where the
// detector's instrumentation dominates the measured interval.
const dataTestRaceEnabled = true
