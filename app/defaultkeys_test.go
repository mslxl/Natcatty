package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
)

func TestSSHDefaultKeysPreferStandardNamesAndSkipUnusableFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"id_rsa":          "RSA",
		"id_ed25519":      "ED25519",
		"id_ecdsa":        "ECDSA",
		"id_custom":       "CUSTOM",
		"id_rsa.pub":      "PUBLIC",
		"config":          "CONFIG",
		"id_encrypted":    "ENCRYPTED",
		"id_invalid":      "INVALID",
		"id_oversized":    strings.Repeat("x", maxDefaultKeyBytes+1),
		"id_empty":        "",
		"id_not-a-file/x": "IGNORED",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := &sshDefaultKeys{
		dir:  dir,
		read: os.ReadFile,
		inspect: func(privateKey, passphrase, certificate string) (appvault.KeyInspection, error) {
			switch privateKey {
			case "ENCRYPTED":
				return appvault.KeyInspection{Encrypted: true, NeedsPassphrase: true}, nil
			case "INVALID":
				return appvault.KeyInspection{}, appvault.ErrKeyMaterialInvalid
			}
			return appvault.KeyInspection{}, nil
		},
	}

	keys := source.DefaultKeys()
	want := []struct {
		label string
		body  string
	}{
		{"id_ed25519", "ED25519"},
		{"id_ecdsa", "ECDSA"},
		{"id_rsa", "RSA"},
		{"id_custom", "CUSTOM"},
	}
	if len(keys) != len(want) {
		t.Fatalf("default keys = %+v, want %d entries", keys, len(want))
	}
	for i, expected := range want {
		if keys[i].Label != expected.label || keys[i].PrivateKey != expected.body {
			t.Errorf("key %d = %+v, want label %q", i, keys[i], expected.label)
		}
		if keys[i].ID == "" {
			t.Errorf("key %d has no ID", i)
		}
	}
}

func TestSSHDefaultKeysNeedAUsableDirectory(t *testing.T) {
	if got := (&sshDefaultKeys{}).DefaultKeys(); got != nil {
		t.Fatalf("empty source returned %+v", got)
	}
	if got := (&sshDefaultKeys{dir: filepath.Join(t.TempDir(), "missing"), read: os.ReadFile}).DefaultKeys(); got != nil {
		t.Fatalf("missing directory returned %+v", got)
	}
}

func TestSSHDefaultKeysSkipSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "id_target")
	if err := os.WriteFile(target, []byte("KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "id_link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	source := &sshDefaultKeys{
		dir:  dir,
		read: os.ReadFile,
		inspect: func(string, string, string) (appvault.KeyInspection, error) {
			return appvault.KeyInspection{}, nil
		},
	}
	keys := source.DefaultKeys()
	if len(keys) != 1 || keys[0].Label != "id_target" {
		t.Fatalf("default keys = %+v, want only the regular id_target file", keys)
	}
}
