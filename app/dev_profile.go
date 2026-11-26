//go:build !ios && !android

package app

// Dev profile. A development build (mygo.IsDev: go run, go build, and
// `go tool mygo dev` builds) runs on a throwaway profile in the process
// working directory instead of the user's real Vault and OS credential store.
// A production build (`go tool mygo build`) never reaches this file's code
// path, because MyGo links its production marker into that build.
//
// The mode is deliberately not configurable by Natcatty: there is no
// Natcatty-owned environment variable and no application flag. A developer
// runs the app the usual way and the build itself decides. MyGo's own
// MYGO_ENV=production variable also makes any build report production, so a
// developer who sets it deliberately gets the normal app; that is MyGo's
// contract, not a switch this app adds. The layout is:
//
//	<cwd>/.natcatty-test/
//	    natcatty.db          the Vault, settings, history, and Hosts
//	    secrets.json         field key, backup key, and sync tokens (0600)
//	    tmp/                 inherited by child processes through TMPDIR
//	    downloads/           browser downloads
//	    ...                  every other MyGo user-data file
//
// The profile persists between runs. Deleting the directory resets it. The
// file-backed secret store keeps the field key alive across restarts; an
// in-memory key would make every stored credential unreadable on the next
// launch.
//
// Isolation is the "standard" set used by the packaged smoke plus the desktop
// integration a developer machine must not disturb: no single-instance
// handoff, no ssh URL-scheme registration, no real shell-history import, no
// external MCP listener, no tray, no global hotkey, no login-item writes, no
// system notifications, and no Dock icon. HOME stays real, so SSH and Git
// configuration, ssh-agent, and local shells keep working.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"

	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
)

const (
	// devProfileDirName is the throwaway profile created under the working
	// directory.
	devProfileDirName = ".natcatty-test"
	// devProfileSecretsName is the local credential file. It is plaintext at
	// 0600 and exists only for this profile.
	devProfileSecretsName = "secrets.json"
	devProfileTempName    = "tmp"
	devProfileDownloads   = "downloads"
)

// runDevProfile prepares the cwd-local profile and runs the app on it.
func runDevProfile() error {
	root, err := prepareDevProfile(os.Getwd)
	if err != nil {
		return err
	}
	secrets, err := platformsecrets.OpenFileStore(filepath.Join(root, devProfileSecretsName))
	if err != nil {
		return fmt.Errorf("dev profile: %w", err)
	}
	mygo.App.SetPath(mygo.PathUserData, root)
	mygo.App.SetPath(mygo.PathDownloads, filepath.Join(root, devProfileDownloads))
	setDevProfileTemp(root)
	return run(runOptions{devProfile: true, secrets: secrets})
}

// prepareDevProfile resolves and creates the profile directory. getwd is a
// seam so the resolution is testable without changing the process directory.
// The returned path is absolute.
func prepareDevProfile(getwd func() (string, error)) (string, error) {
	working, err := getwd()
	if err != nil {
		return "", fmt.Errorf("dev profile: resolve the working directory: %w", err)
	}
	if working == "" {
		return "", errors.New("dev profile: the working directory is empty")
	}
	working, err = filepath.Abs(working)
	if err != nil {
		return "", fmt.Errorf("dev profile: resolve %q: %w", working, err)
	}
	working = filepath.Clean(working)
	if filepath.Dir(working) == working {
		return "", fmt.Errorf("dev profile: the working directory %q is a filesystem root; start Natcatty from a project directory, not from a file manager", working)
	}

	root := filepath.Join(working, devProfileDirName)
	for _, directory := range []string{
		root,
		filepath.Join(root, devProfileTempName),
		filepath.Join(root, devProfileDownloads),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return "", fmt.Errorf("dev profile: create %s: %w", directory, err)
		}
		// MkdirAll keeps an existing directory's mode, so a profile copied
		// from elsewhere is tightened back to owner-only.
		if err := os.Chmod(directory, 0o700); err != nil {
			return "", fmt.Errorf("dev profile: secure %s: %w", directory, err)
		}
	}
	return root, nil
}

// setDevProfileTemp points every temp-directory variable a child process may
// read at the profile. os.TempDir reads TMPDIR on Unix and TMP or TEMP on
// Windows; sessions and shell hooks inherit the rest.
func setDevProfileTemp(root string) {
	dir := filepath.Join(root, devProfileTempName)
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		_ = os.Setenv(key, dir)
	}
}
