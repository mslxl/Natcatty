package plugindir_test

import (
	"path/filepath"
	"testing"

	"github.com/mslxl/natcatty/sdk/internal/plugindir"
)

func TestResolveKeepsPathsInsideTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin")
	for _, relative := range []string{"plugin.wasm", "sub/plugin.wasm", "./plugin.wasm"} {
		got, err := plugindir.Resolve(dir, relative)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", relative, err)
		}
		if got != filepath.Join(dir, filepath.FromSlash(relative)) {
			t.Fatalf("Resolve(%q) = %q", relative, got)
		}
	}
}

func TestResolveRejectsEscapes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin")
	for _, relative := range []string{
		"", ".", "..", "../plugin.wasm", "sub/../../plugin.wasm",
		"/etc/passwd", "plugin\x00.wasm",
	} {
		if _, err := plugindir.Resolve(dir, relative); err == nil {
			t.Fatalf("Resolve(%q) succeeded", relative)
		}
	}
}
