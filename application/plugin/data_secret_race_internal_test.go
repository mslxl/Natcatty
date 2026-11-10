//go:build race

package plugin

// dataSecretRaceEnabled reports whether the race detector is active. Elapsed
// time guard rails in the internal guard tests are asserted only outside race
// mode, where the detector's instrumentation dominates the measured interval.
const dataSecretRaceEnabled = true
