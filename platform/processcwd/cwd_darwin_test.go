//go:build darwin

package processcwd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOfReadsThisProcessCWD exercises the lsof lookup against a live process
// the test owns. A machine without lsof skips instead of failing: the fallback
// is allowed to report no cwd.
func TestOfReadsThisProcessCWD(t *testing.T) {
	want, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(want); err == nil {
		want = resolved
	}
	got, err := Of(os.Getpid())
	if err != nil {
		t.Skipf("lsof cwd lookup unavailable: %v", err)
	}
	if got != want {
		t.Fatalf("Of(%d) = %q, want %q", os.Getpid(), got, want)
	}
}
