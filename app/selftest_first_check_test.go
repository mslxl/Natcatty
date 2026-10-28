//go:build selftest

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// freshResultDir returns a per-process result directory path that does not
// exist yet, inside a private test temp root. It mirrors the first launch of
// the selftest binary, where %TEMP%\natcatty-selftest-<pid> has never been
// created.
func freshResultDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), fmt.Sprintf("natcatty-selftest-%d", os.Getpid()))
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the fixture result directory %q already exists: %v", dir, err)
	}
	return dir
}

// TestTicket48FirstCheckInAFreshProcess is the ticket 61 rework regression for
// the first keyboard check of a brand-new process. Ticket 48 creates a working
// directory under the process result directory in its first step, before the
// harness writes the result; run 1 used to fail in 46 ms with "The system
// cannot find the file specified" because nothing had created the parent yet.
func TestTicket48FirstCheckInAFreshProcess(t *testing.T) {
	dir := freshResultDir(t)
	tester, _ := newTicket16TesterInDir(t, dir)

	// F11, 4, 8 as the very first shortcut: no other check has run, so no
	// earlier write could have created the directory.
	result := runTicket48(t, tester, true)
	if !result.Pass || len(result.Errors) != 0 {
		t.Fatalf("the first ticket 48 check failed: pass=%v errors=%v steps=%+v",
			result.Pass, result.Errors, result.Steps)
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-48.json")); err != nil {
		t.Fatalf("the first check did not write its result below %q: %v", dir, err)
	}
	shots := filepath.Join(dir, "natcatty-selftest-48-shots")
	if entries, err := os.ReadDir(shots); err != nil || len(entries) < 14 {
		t.Fatalf("the first check captured %d screenshots in %q, want at least 14: %v",
			len(entries), shots, err)
	}
}

// TestTicket15FirstCheckInAFreshProcess covers ticket 15 as the first keyboard
// check in a fresh harness with a missing result directory. Ticket 15 keeps
// its working files outside the result directory, so this is coverage of the
// first-check path rather than the ticket 48 ordering regression.
func TestTicket15FirstCheckInAFreshProcess(t *testing.T) {
	dir := freshResultDir(t)
	tester, _ := newTicket15TesterInDir(t, dir)
	result := runTicket15(t, tester, true)
	if !result.Pass || len(result.Errors) != 0 {
		t.Fatalf("the first ticket 15 check failed: pass=%v errors=%v",
			result.Pass, result.Errors)
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-15.json")); err != nil {
		t.Fatalf("the first check did not write its result below %q: %v", dir, err)
	}
}

// TestTicket16FirstCheckInAFreshProcess covers ticket 16 as the first keyboard
// check in a fresh harness with a missing result directory. Ticket 16 keeps
// its marker files outside the result directory, so this is coverage of the
// first-check path rather than the ticket 48 ordering regression.
func TestTicket16FirstCheckInAFreshProcess(t *testing.T) {
	dir := freshResultDir(t)
	tester, _ := newTicket16TesterInDir(t, dir)
	result := runTicket16(t, tester, true)
	if !result.Pass || len(result.Errors) != 0 {
		t.Fatalf("the first ticket 16 check failed: pass=%v errors=%v",
			result.Pass, result.Errors)
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-16.json")); err != nil {
		t.Fatalf("the first check did not write its result below %q: %v", dir, err)
	}
}
