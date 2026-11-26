package terminal

import (
	"errors"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/ui/terminal/mobile"
)

// TestMismatchedLibraryVersionFailsLoad proves the startup assertion Load
// makes on a phone: a linked library that reports any version but
// mobile.Version fails the same function Load calls, so it cannot start the
// app and misread a cell later.
func TestMismatchedLibraryVersionFailsLoad(t *testing.T) {
	if err := loadLinked(func() (string, error) { return mobile.Version, nil }); err != nil {
		t.Fatalf("loadLinked(%q) = %v, want nil", mobile.Version, err)
	}
	err := loadLinked(func() (string, error) { return "9.9.9-not-this-one", nil })
	if err == nil {
		t.Fatal("loadLinked accepted a mismatched library version")
	}
	for _, want := range []string{"9.9.9-not-this-one", mobile.Version} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}

	// A library that cannot report a version is a load failure too, not a
	// mismatch with an empty string.
	boom := errors.New("no type manifest")
	if err := loadLinked(func() (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Fatalf("loadLinked(resolution error) = %v, want %v", err, boom)
	}
}
