package pluginpackage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/pluginpackage"
	"github.com/mslxl/natcatty/adapters/wasm"
)

// TestInstallValidationRejectsStartSection drives the committed start-section
// fixture through the real package validator. The module is a valid reactor
// apart from the core start section, so only that rule can reject it.
func TestInstallValidationRejectsStartSection(t *testing.T) {
	module, err := os.ReadFile(filepath.Join("..", "wasm", "testdata", "start-section.wasm"))
	if err != nil {
		t.Fatalf("read start-section fixture: %v", err)
	}
	sum := sha256.Sum256(module)
	files := map[string][]byte{
		"plugin.toml": []byte(installManifest(hex.EncodeToString(sum[:]), 0)),
		"plugin.wasm": module,
	}
	_, err = pluginpackage.New(wasm.Validator{}).
		Validate(context.Background(), writeArchive(t, files))
	if err == nil {
		t.Fatal("a package with a core start section was accepted")
	}
	if !strings.Contains(err.Error(), "start section") {
		t.Fatalf("Validate = %v, want the start section rule", err)
	}
}
