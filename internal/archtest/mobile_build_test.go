package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/internal/testutil"
)

// firstLine returns the first non-empty line of a source file.
func firstLine(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testutil.ModuleRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	t.Fatalf("%s is empty", rel)
	return ""
}

// TestMobileVersionCheckStaysOnTheHost pins the build shape ticket 23 fixed.
//
// ui/terminal/load_linked.go is untagged so TestMismatchedLibraryVersionFailsLoad
// can run on a developer's host; it imports ui/terminal/mobile for the
// version constant and mobile.Version must stay importable there. The cgo
// parts of that package are phone-only, and the go command applies build
// constraints to non-Go files as well: without the same constraint on
// addr.c, a host build with CGO_ENABLED=1 sees a C file while mobile.go is
// excluded and fails with "C source files not allowed when not using cgo or
// SWIG". Any of these lines being removed makes a host build or the host
// test silently stop existing, so it is a visible edit here instead.
func TestMobileVersionCheckStaysOnTheHost(t *testing.T) {
	const phoneOnly = "//go:build ios || android"

	// Untagged: load_linked.go and version.go are what a host build and the
	// version test link. A constraint here would drop the test, not fix a
	// build.
	for _, rel := range []string{
		"ui/terminal/load_linked.go",
		"ui/terminal/mobile/version.go",
	} {
		if got := firstLine(t, rel); strings.HasPrefix(got, "//go:build") {
			t.Errorf("%s starts with %q; it must stay in the host build", rel, got)
		}
	}

	// The cgo Go file and the C files it needs carry the same constraint.
	for _, rel := range []string{
		"ui/terminal/mobile/mobile.go",
		"ui/terminal/mobile/addr.c",
		"ui/terminal/mobile/addr.h",
	} {
		if got := firstLine(t, rel); got != phoneOnly {
			t.Errorf("%s starts with %q, want %q so a host CGO_ENABLED=1 build ignores it",
				rel, got, phoneOnly)
		}
	}
}
