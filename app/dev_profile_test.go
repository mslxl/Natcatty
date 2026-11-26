//go:build !ios && !android

package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
)

func devProfileWorkingDirectory(path string) func() (string, error) {
	return func() (string, error) { return path, nil }
}

func TestPrepareDevProfileUsesTheWorkingDirectory(t *testing.T) {
	working := t.TempDir()
	root, err := prepareDevProfile(devProfileWorkingDirectory(working))
	if err != nil {
		t.Fatalf("prepareDevProfile: %v", err)
	}
	want := filepath.Join(working, devProfileDirName)
	if root != want {
		t.Fatalf("root = %q, want %q", root, want)
	}
	for _, directory := range []string{
		root,
		filepath.Join(root, devProfileTempName),
		filepath.Join(root, devProfileDownloads),
	} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("Stat %s: %v", directory, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", directory)
		}
		if runtime.GOOS != "windows" {
			if perm := info.Mode().Perm(); perm != 0o700 {
				t.Fatalf("%s mode = %04o, want 0700", directory, perm)
			}
		}
	}
}

func TestPrepareDevProfileTightensAnExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not express permissions as mode bits")
	}
	working := t.TempDir()
	root := filepath.Join(working, devProfileDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if _, err := prepareDevProfile(devProfileWorkingDirectory(working)); err != nil {
		t.Fatalf("prepareDevProfile: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("mode = %04o, want 0700", perm)
	}
}

func TestPrepareDevProfileRejectsAFilesystemRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the volume-root spelling differs on Windows")
	}
	_, err := prepareDevProfile(devProfileWorkingDirectory(string(filepath.Separator)))
	if err == nil || !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("error = %v, want a filesystem-root rejection", err)
	}
}

func TestPrepareDevProfileRejectsAnUnreadableWorkingDirectory(t *testing.T) {
	getwd := func() (string, error) { return "", os.ErrNotExist }
	_, err := prepareDevProfile(getwd)
	if err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("error = %v, want a working-directory error", err)
	}
}

func TestSetDevProfileTemp(t *testing.T) {
	// Create the temp directory before TMPDIR is rewritten: t.TempDir
	// resolves os.TempDir, and a bogus "before" value would break it.
	root := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, "before")
	}
	setDevProfileTemp(root)
	want := filepath.Join(root, devProfileTempName)
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if got := os.Getenv(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestDevProfileSecretFileStaysInsideTheProfile(t *testing.T) {
	working := t.TempDir()
	root, err := prepareDevProfile(devProfileWorkingDirectory(working))
	if err != nil {
		t.Fatalf("prepareDevProfile: %v", err)
	}
	store, err := platformsecrets.OpenFileStore(filepath.Join(root, devProfileSecretsName))
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	if err := store.Set("field", []byte("key")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, devProfileSecretsName)); err != nil {
		t.Fatalf("the secret file is not inside the profile: %v", err)
	}
}

func TestDevProfileRunOptions(t *testing.T) {
	dev := runOptions{devProfile: true}
	if !dev.isolated() {
		t.Fatal("the dev profile is not isolated")
	}
	if dev.importsShellHistory() {
		t.Fatal("the dev profile would import the real shell history")
	}
	if dev.registersURLScheme() {
		t.Fatal("the dev profile would take over the real ssh URL handler")
	}
	if !dev.suppressesDesktopIntegration() {
		t.Fatal("the dev profile would touch the tray, hotkeys, or notifications")
	}

	smoke := runOptions{packagedSmoke: true}
	if !smoke.isolated() || smoke.importsShellHistory() || smoke.registersURLScheme() {
		t.Fatal("the packaged smoke lost its isolation")
	}
	if smoke.suppressesDesktopIntegration() {
		t.Fatal("the packaged smoke must keep its normal desktop wiring")
	}

	normal := runOptions{}
	if normal.isolated() {
		t.Fatal("the normal app must not be isolated")
	}
	if !normal.importsShellHistory() || !normal.registersURLScheme() {
		t.Fatal("the normal app must keep importing history and registering the handler")
	}
	if normal.suppressesDesktopIntegration() {
		t.Fatal("the normal app must keep its desktop integration")
	}
}
