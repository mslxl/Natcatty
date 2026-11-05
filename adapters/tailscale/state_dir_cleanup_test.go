// SPDX-License-Identifier: GPL-3.0-or-later

package tailscale

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveStateDirDeletesOnlyTheRequestedProfile(t *testing.T) {
	base := filepath.Join(t.TempDir(), "network-providers")
	removeDir, err := prepareStateDir(base, "profile-remove")
	if err != nil {
		t.Fatalf("prepareStateDir(remove): %v", err)
	}
	keepDir, err := prepareStateDir(base, "profile-keep")
	if err != nil {
		t.Fatalf("prepareStateDir(keep): %v", err)
	}
	if err := os.WriteFile(filepath.Join(removeDir, "node.state"), []byte("identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	keepFile := filepath.Join(keepDir, "node.state")
	if err := os.WriteFile(keepFile, []byte("other identity"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RemoveStateDir(base, "profile-remove"); err != nil {
		t.Fatalf("RemoveStateDir: %v", err)
	}
	if _, err := os.Stat(removeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed profile state still exists: %v", err)
	}
	if data, err := os.ReadFile(keepFile); err != nil || string(data) != "other identity" {
		t.Fatalf("another profile's state was changed: %q, %v", data, err)
	}

	// Delete is idempotent: a retry after the directory is gone succeeds.
	if err := RemoveStateDir(base, "profile-remove"); err != nil {
		t.Fatalf("repeated RemoveStateDir: %v", err)
	}
	if err := RemoveStateDir(base, "never-created"); err != nil {
		t.Fatalf("RemoveStateDir for an unused profile: %v", err)
	}
	if err := RemoveStateDir(base, "  "); err == nil {
		t.Fatal("RemoveStateDir accepted an empty profile id")
	}
}

func TestRemoveStateDirRefusesALinkToOutsideState(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "network-providers")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "must-survive")
	if err := os.WriteFile(sentinel, []byte("not node state"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := stateDirFor(base, "profile-link")
	if err != nil {
		t.Fatalf("stateDirFor: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		// Creating a symlink may need Developer Mode or elevation on Windows.
		t.Skipf("symlink is unavailable: %v", err)
	}

	err = RemoveStateDir(base, "profile-link")
	if err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("RemoveStateDir followed a linked state directory: %v", err)
	}
	if data, readErr := os.ReadFile(sentinel); readErr != nil || string(data) != "not node state" {
		t.Fatalf("the delete escaped its base: %q, %v", data, readErr)
	}
}

func TestRemoveStateDirRefusesASystemTailscaleBase(t *testing.T) {
	for _, system := range systemTailscaleDirs() {
		if strings.TrimSpace(system) == "" {
			continue
		}
		base := filepath.Join(system, "network-providers")
		err := RemoveStateDir(base, "profile-system")
		if err == nil || !strings.Contains(err.Error(), "system Tailscale") {
			t.Fatalf("RemoveStateDir(%q) = %v, want a system-directory refusal", base, err)
		}
		return
	}
	t.Skip("this platform exposes no system Tailscale directory")
}
