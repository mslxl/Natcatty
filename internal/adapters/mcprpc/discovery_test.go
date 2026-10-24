package mcprpc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDiscoveryRoundTripsAndFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "external-mcp", "discovery.json")
	written, err := WriteDiscovery(path, Discovery{
		Port:  45678,
		Token: "secret-token",
		PID:   1234,
	})
	if err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	if written.Host != "127.0.0.1" || written.Version != 1 ||
		written.PermissionMode != "confirm" || written.ChatSessionID != ExternalScope {
		t.Fatalf("written defaults = %+v", written)
	}
	read, err := ReadDiscovery(path)
	if err != nil {
		t.Fatalf("read discovery: %v", err)
	}
	if read.Port != 45678 || read.Token != "secret-token" || read.PID != 1234 {
		t.Fatalf("read = %+v", read)
	}
	if read.Host != "127.0.0.1" || read.ChatSessionID != ExternalScope {
		t.Fatalf("read defaults = %+v", read)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("discovery mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestDiscoveryRejectsMissingPortOrToken(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"no-token": `{"port": 1234}`,
		"no-port":  `{"token": "abc"}`,
		"bad-json": `{`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadDiscovery(path); err == nil {
				t.Fatal("invalid discovery was accepted")
			}
		})
	}
	if _, err := ReadDiscovery(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing discovery was accepted")
	}
}

func TestDiscoveryPathHonorsTheEnvironmentOverride(t *testing.T) {
	t.Setenv(EnvDiscoveryFile, filepath.Join("somewhere", "custom.json"))
	path, err := ResolveDiscoveryPath()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if path != filepath.Join("somewhere", "custom.json") {
		t.Fatalf("path = %q", path)
	}
}

func TestDefaultDiscoveryPathUsesTheAppDataDirectory(t *testing.T) {
	t.Setenv(EnvDiscoveryFile, "")
	path, err := DefaultDiscoveryPath()
	if err != nil {
		t.Fatalf("default path: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(path), "external-mcp/discovery.json") {
		t.Fatalf("path = %q, want an external-mcp/discovery.json suffix", path)
	}
	if !strings.Contains(path, "Natcatty") {
		t.Fatalf("path = %q, want the MyGo application directory name", path)
	}
}

func TestRemoveDiscoveryOnlyRemovesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "discovery.json")
	if _, err := WriteDiscovery(path, Discovery{Port: 1, Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDiscovery(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
	// Removing an already-absent file is not an error.
	if err := RemoveDiscovery(path); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}
