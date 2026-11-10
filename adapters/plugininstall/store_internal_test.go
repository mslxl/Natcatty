package plugininstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteTreeNeverOverwritesAnExistingFile exercises the exclusive-create
// guard directly: a collision must leave the bytes already on disk untouched.
func TestWriteTreeNeverOverwritesAnExistingFile(t *testing.T) {
	stage := t.TempDir()
	target := filepath.Join(stage, "plugin.wasm")
	if err := os.WriteFile(target, []byte("validated"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeTree(stage, map[string][]byte{"plugin.wasm": []byte("replacement")})
	if err == nil {
		t.Fatal("writeTree overwrote an existing file")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("writeTree error = %v, want an overwrite refusal", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "validated" {
		t.Fatalf("existing file changed to %q", got)
	}
}
