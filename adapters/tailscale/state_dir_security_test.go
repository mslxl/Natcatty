// SPDX-License-Identifier: GPL-3.0-or-later

package tailscale

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// requireSymlink creates a symlink or skips when the platform forbids it. Only
// Windows is expected to lack the privilege; any other failure is a real test
// setup failure.
func requireSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("cannot create a symlink on this Windows host (SeCreateSymbolicLinkPrivilege not held): %v", err)
		}
		t.Fatalf("create symlink %q -> %q: %v", link, target, err)
	}
}

// fakeSystemTailscaleRoot registers a writable directory as a system Tailscale
// root for the current platform and returns that root. The real system roots
// stay in the list; this only adds one the test can create and inspect.
func fakeSystemTailscaleRoot(t *testing.T, parent string) string {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("ProgramData", parent)
		return filepath.Join(parent, "Tailscale")
	case "darwin":
		t.Setenv("HOME", parent)
		return filepath.Join(parent, "Library", "Application Support", "Tailscale")
	default:
		t.Setenv("HOME", parent)
		return filepath.Join(parent, ".config", "tailscale")
	}
}

func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read directory %q: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func dirPerm(t *testing.T, dir string) os.FileMode {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat directory %q: %v", dir, err)
	}
	return info.Mode().Perm()
}

// assertDirectoryUnchanged proves the rejected call neither created entries in
// nor chmodded the directory outside the base.
func assertDirectoryUnchanged(t *testing.T, dir string, entries []string, perm os.FileMode) {
	t.Helper()
	if after := dirEntryNames(t, dir); !slices.Equal(after, entries) {
		t.Fatalf("%q entries = %v after rejection, want %v", dir, after, entries)
	}
	if runtime.GOOS != "windows" {
		if after := dirPerm(t, dir); after != perm {
			t.Fatalf("%q mode = %o after rejection, want %o", dir, after, perm)
		}
	}
}

// TestPrepareStateDirRejectsSymlinkedProfileDirectoryEscape makes the
// per-profile directory itself a symlink to a directory outside the base. The
// real constructor must reject it without creating or chmodding anything
// outside the base.
func TestPrepareStateDirRejectsSymlinkedProfileDirectoryEscape(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	dir, err := stateDirFor(base, "net-escape")
	if err != nil {
		t.Fatalf("stateDirFor: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatalf("create tailscale component: %v", err)
	}
	requireSymlink(t, outside, dir)

	entries := dirEntryNames(t, outside)
	perm := dirPerm(t, outside)
	got, err := prepareStateDir(base, "net-escape")
	if err == nil {
		t.Fatalf("prepareStateDir accepted symlink %q -> outside %q and returned %q", dir, outside, got)
	}
	if got != "" {
		t.Fatalf("prepareStateDir returned %q together with error %v", got, err)
	}
	assertDirectoryUnchanged(t, outside, entries, perm)
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "untouched" {
		t.Fatalf("sentinel after rejection = %q, %v", data, err)
	}
}

// TestPrepareStateDirRejectsSymlinkedTailscaleComponentEscape makes the
// intermediate tailscale component a symlink to a directory outside the base.
// A fail-closed guard must reject before MkdirAll can create the profile
// directory through the link.
func TestPrepareStateDirRejectsSymlinkedTailscaleComponentEscape(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	component := filepath.Join(base, "tailscale")
	requireSymlink(t, outside, component)

	entries := dirEntryNames(t, outside)
	perm := dirPerm(t, outside)
	got, err := prepareStateDir(base, "net-escape")
	if err == nil {
		t.Fatalf("prepareStateDir accepted symlinked component %q -> outside %q and returned %q",
			component, outside, got)
	}
	if got != "" {
		t.Fatalf("prepareStateDir returned %q together with error %v", got, err)
	}
	assertDirectoryUnchanged(t, outside, entries, perm)
}

// createJunction creates a Windows directory junction, which needs no
// SeCreateSymbolicLinkPrivilege. It skips on other platforms and when the
// command is unavailable.
func createJunction(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("directory junctions are Windows-specific")
	}
	command := os.Getenv("ComSpec")
	if command == "" {
		command = "cmd"
	}
	if out, err := exec.Command(command, "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("cannot create a directory junction: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// TestPrepareStateDirRejectsWindowsJunctionEscapes covers the same escape on
// Windows, where directory junctions are available without the symlink
// privilege. Go reports a junction as a non-directory, not as a symlink.
func TestPrepareStateDirRejectsWindowsJunctionEscapes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("directory junctions are Windows-specific")
	}

	t.Run("profile directory", func(t *testing.T) {
		base := t.TempDir()
		outside := t.TempDir()
		sentinel := filepath.Join(outside, "sentinel")
		if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
			t.Fatalf("write sentinel: %v", err)
		}
		dir, err := stateDirFor(base, "net-junction")
		if err != nil {
			t.Fatalf("stateDirFor: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			t.Fatalf("create tailscale component: %v", err)
		}
		createJunction(t, outside, dir)

		entries := dirEntryNames(t, outside)
		perm := dirPerm(t, outside)
		got, err := prepareStateDir(base, "net-junction")
		if err == nil {
			t.Fatalf("prepareStateDir accepted junction %q -> outside %q and returned %q", dir, outside, got)
		}
		if got != "" {
			t.Fatalf("prepareStateDir returned %q together with error %v", got, err)
		}
		assertDirectoryUnchanged(t, outside, entries, perm)
	})

	t.Run("tailscale component", func(t *testing.T) {
		base := t.TempDir()
		outside := t.TempDir()
		component := filepath.Join(base, "tailscale")
		createJunction(t, outside, component)

		entries := dirEntryNames(t, outside)
		perm := dirPerm(t, outside)
		got, err := prepareStateDir(base, "net-junction")
		if err == nil {
			t.Fatalf("prepareStateDir accepted junction %q -> outside %q and returned %q",
				component, outside, got)
		}
		if got != "" {
			t.Fatalf("prepareStateDir returned %q together with error %v", got, err)
		}
		assertDirectoryUnchanged(t, outside, entries, perm)
	})

	t.Run("base into system directory", func(t *testing.T) {
		systemRoot := fakeSystemTailscaleRoot(t, t.TempDir())
		if err := os.MkdirAll(systemRoot, 0o700); err != nil {
			t.Fatalf("create constructed system root: %v", err)
		}
		entries := dirEntryNames(t, systemRoot)
		perm := dirPerm(t, systemRoot)
		link := filepath.Join(t.TempDir(), "appdata")
		createJunction(t, systemRoot, link)

		got, err := prepareStateDir(link, "net-junction")
		if err == nil {
			t.Fatalf("prepareStateDir accepted junction base %q -> system root %q and returned %q",
				link, systemRoot, got)
		}
		if !strings.Contains(err.Error(), "system Tailscale") {
			t.Fatalf("prepareStateDir(junction base) error = %v, want a system Tailscale rejection", err)
		}
		if got != "" {
			t.Fatalf("prepareStateDir returned %q together with error %v", got, err)
		}
		assertDirectoryUnchanged(t, systemRoot, entries, perm)
	})
}

// TestStateDirForAndPrepareStateDirRejectSystemTailscaleBases drives both real
// constructors with bases under every system Tailscale root, including a root
// constructed for this test, rather than only checking the predicate helper.
func TestStateDirForAndPrepareStateDirRejectSystemTailscaleBases(t *testing.T) {
	fakeRoot := fakeSystemTailscaleRoot(t, t.TempDir())

	roots := systemTailscaleDirs()
	if len(roots) == 0 {
		t.Skip("this platform exposes no known system Tailscale directory")
	}
	fakeSeen := false
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if filepath.Clean(root) == filepath.Clean(fakeRoot) {
			fakeSeen = true
		}
		for _, base := range []string{
			root,
			filepath.Join(root, "nested"),
			filepath.Join(root, "nested", "deeper"),
		} {
			dir, err := stateDirFor(base, "net-system")
			if err == nil {
				t.Errorf("stateDirFor(%q) = %q, want a system Tailscale rejection", base, dir)
			} else if !strings.Contains(err.Error(), "system Tailscale") {
				t.Errorf("stateDirFor(%q) error = %v, want a system Tailscale rejection", base, err)
			}
			if dir != "" {
				t.Errorf("stateDirFor(%q) returned %q with the rejection", base, dir)
			}

			dir, err = prepareStateDir(base, "net-system")
			if err == nil {
				t.Errorf("prepareStateDir(%q) = %q, want a system Tailscale rejection", base, dir)
			} else if !strings.Contains(err.Error(), "system Tailscale") {
				t.Errorf("prepareStateDir(%q) error = %v, want a system Tailscale rejection", base, err)
			}
			if dir != "" {
				t.Errorf("prepareStateDir(%q) returned %q with the rejection", base, dir)
			}
		}
	}
	if !fakeSeen {
		t.Fatalf("constructed system root %q is missing from systemTailscaleDirs() = %v", fakeRoot, roots)
	}
}

// TestPrepareStateDirRejectsAncestorLinkIntoSystemDirectoryWithMissingLeaf
// covers a link that is an ancestor of the base rather than the base itself:
// appdata is a junction or symlink into a system Tailscale root, and the leaf
// network-providers does not exist yet. Creating that missing leaf would write
// through the link, so prepareStateDir must resolve the deepest existing
// ancestor and reject before MkdirAll. The system root must stay untouched and
// the error must be the system-directory rejection, not a generic resolve one.
func TestPrepareStateDirRejectsAncestorLinkIntoSystemDirectoryWithMissingLeaf(t *testing.T) {
	check := func(t *testing.T, createLink func(target, link string)) {
		t.Helper()
		systemRoot := fakeSystemTailscaleRoot(t, t.TempDir())
		if err := os.MkdirAll(systemRoot, 0o700); err != nil {
			t.Fatalf("create constructed system root: %v", err)
		}
		entries := dirEntryNames(t, systemRoot)
		perm := dirPerm(t, systemRoot)

		link := filepath.Join(t.TempDir(), "appdata")
		createLink(systemRoot, link)
		base := filepath.Join(link, "network-providers")
		if _, err := os.Lstat(base); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("precondition: base %q must be missing, Lstat error = %v", base, err)
		}

		got, err := prepareStateDir(base, "net-ancestor")
		if err == nil {
			t.Fatalf("prepareStateDir accepted base %q through link %q -> system root %q and returned %q",
				base, link, systemRoot, got)
		}
		if !strings.Contains(err.Error(), "system Tailscale") {
			t.Fatalf("prepareStateDir(base with linked ancestor) error = %v, want a system Tailscale rejection", err)
		}
		if got != "" {
			t.Fatalf("prepareStateDir returned %q together with error %v", got, err)
		}
		if _, statErr := os.Lstat(filepath.Join(systemRoot, "network-providers")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("system root %q gained network-providers after rejection: Lstat error = %v", systemRoot, statErr)
		}
		assertDirectoryUnchanged(t, systemRoot, entries, perm)
	}

	t.Run("junction", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("directory junctions are Windows-specific")
		}
		check(t, func(target, link string) { createJunction(t, target, link) })
	})

	t.Run("symlink", func(t *testing.T) {
		check(t, func(target, link string) { requireSymlink(t, target, link) })
	})
}

// TestPrepareStateDirCreatesMissingUserDataBase proves the fail-before-write
// guard still allows a legitimate userData base whose directories do not exist
// yet, including nested missing components, and that the result stays private.
func TestPrepareStateDirCreatesMissingUserDataBase(t *testing.T) {
	base := filepath.Join(t.TempDir(), "missing", "nested", "network-providers")
	if _, err := os.Lstat(base); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition: base %q must be missing, Lstat error = %v", base, err)
	}

	dir, err := prepareStateDir(base, "net-new")
	if err != nil {
		t.Fatalf("prepareStateDir(missing but legitimate base): %v", err)
	}
	if !within(base, dir) {
		t.Fatalf("state directory %q escapes %q", dir, base)
	}
	for _, d := range []string{base, dir} {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %q: %v", d, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", d)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("%q mode = %o, want 700", d, info.Mode().Perm())
		}
	}
}

// TestPrepareStateDirRejectsSymlinkedBaseIntoSystemDirectory covers the base
// symlink case: a base that is itself a link has to resolve before the
// system-directory guard runs. Rejection must happen before anything is
// created under the system root.
func TestPrepareStateDirRejectsSymlinkedBaseIntoSystemDirectory(t *testing.T) {
	systemRoot := fakeSystemTailscaleRoot(t, t.TempDir())
	if err := os.MkdirAll(systemRoot, 0o700); err != nil {
		t.Fatalf("create constructed system root: %v", err)
	}
	entries := dirEntryNames(t, systemRoot)
	perm := dirPerm(t, systemRoot)

	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "appdata")
	requireSymlink(t, systemRoot, link)

	got, err := prepareStateDir(link, "net-escape")
	if err == nil {
		t.Fatalf("prepareStateDir accepted base symlink %q -> system root %q and returned %q",
			link, systemRoot, got)
	}
	if !strings.Contains(err.Error(), "system Tailscale") {
		t.Fatalf("prepareStateDir(base symlink) error = %v, want a system Tailscale rejection", err)
	}
	if got != "" {
		t.Fatalf("prepareStateDir returned %q together with error %v", got, err)
	}
	assertDirectoryUnchanged(t, systemRoot, entries, perm)
}
