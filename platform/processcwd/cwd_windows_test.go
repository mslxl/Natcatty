//go:build windows

package processcwd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOfReadsThisProcessCWD exercises the PEB lookup against a live process
// the test owns.
func TestOfReadsThisProcessCWD(t *testing.T) {
	want, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	want = filepath.Clean(want)
	got, err := Of(os.Getpid())
	if err != nil {
		t.Fatalf("Of(%d): %v", os.Getpid(), err)
	}
	if filepath.Clean(got) != want {
		t.Fatalf("Of(%d) = %q, want %q", os.Getpid(), got, want)
	}
}
