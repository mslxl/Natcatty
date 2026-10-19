// Package testutil holds helpers shared by the repository's tests.
package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ModuleRoot returns the directory holding go.mod.
func ModuleRoot(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above internal/testutil")
		}
		dir = parent
	}
}
