//go:build windows || darwin || linux

package state

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
)

// These tests run the picker's real listing through the production
// Session.Command path and a real PTY. The wire protocol exists because the
// previous one assumed a NUL-framed, newline-terminated stream that ConPTY and
// interactive shells do not deliver, so a fake transport cannot prove it.

// composeDirectoryRealSession opens a real local shell. The Session runs for
// the whole test; the listing has its own deadline.
func composeDirectoryRealSession(t *testing.T, command []string) *appsession.Session {
	t.Helper()
	service := appsession.NewService(localpty.Factory{})
	t.Cleanup(service.CloseAll)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	session, err := service.Open(ctx, appsession.StartSpec{
		Kind: appsession.KindLocal, Command: command, Cols: 80, Rows: 30,
	})
	if err != nil {
		t.Fatalf("open the real shell %v: %v", command, err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// composeDirectoryRealFixture creates one directory whose names exercise what
// the old protocol could not carry: the property separator, spaces, a quote, a
// complete sentinel-shaped string, and non-ASCII text. POSIX filesystems also
// allow a newline and a control byte inside a name; NTFS does not.
func composeDirectoryRealFixture(t *testing.T) (string, map[string]bool) {
	t.Helper()
	dir := t.TempDir()
	want := map[string]bool{}
	if err := os.Mkdir(filepath.Join(dir, "nested dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	want["nested dir"] = true
	files := []string{
		" spaced name.txt",
		"quote'name.txt",
		"~ncdir-e2e~B00000000~ncdir-e2e~Z",
		"héllo.txt",
		strings.Repeat("long", 40) + ".txt",
	}
	if runtime.GOOS != "windows" {
		files = append(files, "line\nbreak.txt", "ctrl\x01name.txt")
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("create %q: %v", name, err)
		}
		want[name] = false
	}
	return dir, want
}

// checkComposeDirectoryEntries compares names and types without depending on
// the sort order the parser owns.
func checkComposeDirectoryEntries(t *testing.T, entries []ComposeDirectoryEntry, want map[string]bool) {
	t.Helper()
	got := map[string]bool{}
	for _, entry := range entries {
		if _, seen := got[entry.Name]; seen {
			t.Fatalf("duplicate entry %q", entry.Name)
		}
		got[entry.Name] = entry.Directory
	}
	if len(got) != len(want) {
		t.Fatalf("listed %d entries, want %d: %+v", len(got), len(want), entries)
	}
	for name, dir := range want {
		if entryDir, ok := got[name]; !ok || entryDir != dir {
			t.Fatalf("entry %q directory=%v present=%v, want directory=%v; got %+v",
				name, entryDir, ok, dir, entries)
		}
	}
}

// TestComposeDirectoryListsARealShSession drives the POSIX branch through a
// real shell. The generated command runs under `sh -c` regardless of the
// Session's shell, so this also proves the script needs no zsh/fish features.
// Cols is 80, so the hex frame is long enough for ConPTY to hard-wrap it.
func TestComposeDirectoryListsARealShSession(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on this host")
	}
	session := composeDirectoryRealSession(t, []string{"sh"})
	if got := session.ShellKind(); got != "posix" {
		t.Fatalf("ShellKind() = %q, want posix", got)
	}
	dir, want := composeDirectoryRealFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	listing, err := listComposeDirectory(ctx, session, dir)
	if err != nil {
		t.Fatalf("listing %q: %v", dir, err)
	}
	t.Logf("sh listing resolved %q with %d entries: %+v", listing.Path, len(listing.Entries), listing.Entries)
	checkComposeDirectoryEntries(t, listing.Entries, want)
	// Git's sh reports PWD as /c/..., which os.Stat does not accept on Windows.
	// The trailing component still proves the cd reached the requested
	// directory; on a POSIX filesystem the full path must match.
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(listing.Path, "/"+filepath.Base(dir)) {
			t.Fatalf("resolved path = %q, want a POSIX path ending in %q", listing.Path, filepath.Base(dir))
		}
	} else if listing.Path != dir {
		t.Fatalf("resolved path = %q, want %q", listing.Path, dir)
	}

	empty := t.TempDir()
	listing, err = listComposeDirectory(ctx, session, empty)
	if err != nil {
		t.Fatalf("listing the empty directory: %v", err)
	}
	if len(listing.Entries) != 0 {
		t.Fatalf("empty directory listed %+v", listing.Entries)
	}
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(listing.Path, "/"+filepath.Base(empty)) {
			t.Fatalf("empty resolved path = %q, want a POSIX path ending in %q", listing.Path, filepath.Base(empty))
		}
	} else if listing.Path != empty {
		t.Fatalf("empty resolved path = %q, want %q", listing.Path, empty)
	}
}

// TestComposeDirectoryListsARealPowerShellSession drives the PowerShell branch
// through the same Session.Command path on a real Windows console. The whole
// frame is one Console.Out.Write, and the parser must heal any line wrapping
// the console still inserts.
func TestComposeDirectoryListsARealPowerShellSession(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell listing is Windows-specific")
	}
	session := composeDirectoryRealSession(t, nil)
	if got := session.ShellKind(); got != "powershell" {
		t.Fatalf("ShellKind() = %q, want powershell", got)
	}
	dir, want := composeDirectoryRealFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	listing, err := listComposeDirectory(ctx, session, dir)
	if err != nil {
		t.Fatalf("listing %q: %v", dir, err)
	}
	t.Logf("PowerShell listing resolved %q with %d entries: %+v", listing.Path, len(listing.Entries), listing.Entries)
	checkComposeDirectoryEntries(t, listing.Entries, want)
	checkComposeDirectoryRealPath(t, listing.Path, dir)

	empty := t.TempDir()
	listing, err = listComposeDirectory(ctx, session, empty)
	if err != nil {
		t.Fatalf("listing the empty directory: %v", err)
	}
	if len(listing.Entries) != 0 {
		t.Fatalf("empty directory listed %+v", listing.Entries)
	}
	checkComposeDirectoryRealPath(t, listing.Path, empty)
}

// checkComposeDirectoryRealPath accepts the console's canonical spelling of a
// path, which can differ from the requested one by case or a short name.
func checkComposeDirectoryRealPath(t *testing.T, got, want string) {
	t.Helper()
	gotInfo, gotErr := os.Stat(got)
	wantInfo, wantErr := os.Stat(want)
	if gotErr == nil && wantErr == nil {
		if !os.SameFile(gotInfo, wantInfo) {
			t.Fatalf("resolved path = %q, want %q", got, want)
		}
		return
	}
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
		t.Fatalf("resolved path = %q, want %q (%v)", got, want, gotErr)
	}
}

// TestComposeDirectorySweepsTheFrameLength is the regression for the ConPTY
// end-marker padding boundary. The console pads the exit-marker row with
// spaces before it repositions the cursor, and whether it does depends on
// where the hex frame leaves the marker row. The sweep varies the entry count,
// and so the frame length, across the alignments that used to time out: odd
// counts in the original reproduction failed every time. Each listing has a
// short deadline so a regression fails the suite instead of holding it for the
// production 15-second timeout.
func TestComposeDirectorySweepsTheFrameLength(t *testing.T) {
	session := composeDirectoryRealSession(t, nil)
	for entries := 1; entries <= 30; entries++ {
		dir := t.TempDir()
		for index := 0; index < entries; index++ {
			name := fmt.Sprintf("entry-%02d-%s.txt", index, strings.Repeat("x", 8))
			if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		listing, err := listComposeDirectory(ctx, session, dir)
		cancel()
		if err != nil {
			t.Fatalf("listing %d entries in %q: %v", entries, dir, err)
		}
		if len(listing.Entries) != entries {
			t.Fatalf("listing %d entries returned %d", entries, len(listing.Entries))
		}
	}
}

// TestComposeDirectoryListsTheSystemDriveRoot reproduces the exact listing
// that failed: a real PowerShell session listing the root of the system drive.
// Its entry count lands on the padding boundary that made every attempt time
// out at the production deadline.
func TestComposeDirectoryListsTheSystemDriveRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the root listing reproduction is Windows-specific")
	}
	session := composeDirectoryRealSession(t, nil)
	root := os.Getenv("SystemDrive") + `\`
	if root == `\` {
		root = `C:\`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listing, err := listComposeDirectory(ctx, session, root)
	if err != nil {
		t.Fatalf("listing %q: %v", root, err)
	}
	if len(listing.Entries) == 0 {
		t.Fatalf("listing %q returned no entries", root)
	}
	checkComposeDirectoryRealPath(t, listing.Path, root)
}
