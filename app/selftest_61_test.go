//go:build selftest

package app

import "testing"

// TestSelftest61PassesTwiceInOneProcess is the ticket 61 repeatability check.
// The harness keeps one registered set of steps, so a second F11,6,1 in the
// same process must rebuild every fixture after the reset step rather than
// reuse a closed service or a consumed deadline. Running the same step slice
// twice here catches that regression without opening a real window.
func TestSelftest61PassesTwiceInOneProcess(t *testing.T) {
	run := &selftest61{}
	check := run.check()
	for pass := 1; pass <= 2; pass++ {
		for _, step := range check.Steps {
			outcome := step.Run(nil)
			if outcome.Waiting {
				t.Fatalf("pass %d step %q waited; every ticket 61 step is synchronous", pass, step.Name)
			}
			if !outcome.Pass {
				t.Fatalf("pass %d step %q failed: %v", pass, step.Name, outcome.Err)
			}
		}
		if run.run != pass {
			t.Fatalf("after pass %d the run counter = %d", pass, run.run)
		}
	}
}
