//go:build linux

package processcwd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOfReadsThisProcessCWD exercises the /proc lookup against a live process
// the test owns.
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
		t.Fatalf("Of(%d): %v", os.Getpid(), err)
	}
	if got != want {
		t.Fatalf("Of(%d) = %q, want %q", os.Getpid(), got, want)
	}
}
