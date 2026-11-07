package app

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLegalRootsOrderAndOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NATCATTY_RESOURCES", filepath.Join(root, "override"))
	exe := filepath.Join(root, "app", "natcatty.exe")
	roots := legalRoots(filepath.Join(root, "res"), exe, root)
	if len(roots) < 4 {
		t.Fatalf("roots = %#v", roots)
	}
	if roots[0] != filepath.Join(root, "override") {
		t.Fatalf("override is not first: %#v", roots)
	}
	if roots[1] != filepath.Join(root, "res") {
		t.Fatalf("resource dir is not second: %#v", roots)
	}
	found := false
	want := filepath.Join(root, "resources")
	for _, candidate := range roots {
		if strings.EqualFold(filepath.Clean(candidate), want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("working-directory resources missing: %#v", roots)
	}
	seen := map[string]int{}
	for _, candidate := range roots {
		seen[candidate]++
		if seen[candidate] > 1 {
			t.Fatalf("duplicate root %q in %#v", candidate, roots)
		}
	}
}
