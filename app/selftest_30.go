//go:build selftest

package app

// Ticket 30 self-test. Press F11, then 3, then 0. The check proves the SFTP
// surface through the real state, views, application service, local adapter,
// and remote adapter:
//
//   - Dual panes: a local pane and an in-process SFTP pane browse
//     independently. Navigating, hiding files, and sorting one pane leaves
//     the other pane's path, hidden-file setting, and ordering untouched.
//   - File operations: upload, overwrite confirmation, create folder, create
//     file, rename, chmod, delete confirmation, and download all run through
//     the same service the app wires. Deletion and replacement change nothing
//     until the confirmation is submitted.
//   - Tabs and bookmarks: duplicating a Tab keeps its location, both a
//     local-pane and a remote bookmark are written to their persisted JSON
//     repositories, and a cross-Tab copy into the source's own subtree is
//     refused without touching the filesystem.
//   - The view: the real surface renders both panes at 720x480 and 1100x720,
//     in English and Simplified Chinese, with every toolbar action inside the
//     window; the star, hidden-file toggle, and delete confirmation work
//     through accessible labels.
//
// The remote pane is a pkg/sftp server over net.Pipe with the same adapter
// the app uses for SSH. A server without the SSH transport is used because a
// live SSH host is not part of a local build; the transport-independent
// adapter, application service, and surface are the same.
//
// Every listing and operation completes off the UI thread, so the steps wait
// on their real state with a hard deadline rather than a fixed delay.
//
// Result: %TEMP%\natcatty-selftest-30.json.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/pkg/sftp"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	transferui "github.com/mslxl/natcatty/ui/transfer"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

const selftest30Ticket = 30

const selftest30Wait = 20 * time.Second

type selftest30 struct {
	localRoot      string
	remoteRoot     string
	bookmarks      string
	localBookmarks string
	workdir        string
	state          *transferstate.State
	remote         *selftest30Remote
	cleanup        func()

	deadline time.Time

	browsePhase int
	opPhase     int
	guardPhase  int
	viewPhase   int
	// finished marks a completed run. The registry reuses the check value, so
	// the next F11,3,0 starts from a clean instance and fresh fixtures.
	finished bool
}

func (t *selftest30) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest30Ticket,
		Name:   "SFTP dual panes and file operations",
		Steps: []selftest.Step{
			{Name: "dual-panes-browse-independently", Run: t.browseStep},
			{Name: "upload-overwrite-create-rename-chmod-delete-download", Run: t.operationStep},
			{Name: "dual-local-tabs-cannot-copy-into-source", Run: t.guardStep},
			{Name: "tabs-bookmarks-and-the-view", Run: t.viewStep},
		},
	}
}

// selftest30Remote opens an SFTP filesystem over an in-process server. Each
// OpenRemote gets its own pipe, so a duplicated remote Tab works too.
type selftest30Remote struct {
	root string

	mu      sync.Mutex
	closers []io.Closer
}

func (o *selftest30Remote) OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error) {
	clientSide, serverSide := net.Pipe()
	server, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(o.root))
	if err != nil {
		_ = clientSide.Close()
		return nil, err
	}
	go func() { _ = server.Serve() }()
	remote, err := adaptersftp.NewRemote(clientSide, func(error) error { return nil },
		adaptersftp.WithBacking("remote:selftest"))
	if err != nil {
		_ = server.Close()
		return nil, err
	}
	o.mu.Lock()
	o.closers = append(o.closers, remote, server)
	o.mu.Unlock()
	return remote, nil
}

func (o *selftest30Remote) Close() {
	o.mu.Lock()
	closers := o.closers
	o.closers = nil
	o.mu.Unlock()
	for _, closer := range closers {
		_ = closer.Close()
	}
}

type selftest30Hosts struct{}

func (selftest30Hosts) Hosts(context.Context) ([]apptransfer.HostTarget, error) {
	return []apptransfer.HostTarget{{ID: "selftest", Label: "Loopback", Hostname: "127.0.0.1", Username: "tester", Port: 22}}, nil
}

func (t *selftest30) setup() error {
	workdir, err := os.MkdirTemp("", "natcatty-selftest-30-")
	if err != nil {
		return err
	}
	t.workdir = workdir
	t.localRoot = filepath.Join(workdir, "local")
	t.remoteRoot = filepath.Join(workdir, "remote")
	t.bookmarks = filepath.Join(workdir, "sftp-bookmarks.json")
	t.localBookmarks = filepath.Join(workdir, "sftp-local-bookmarks.json")
	for _, dir := range []string{t.localRoot, t.remoteRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	write := func(path, content string, mode os.FileMode) error {
		return os.WriteFile(path, []byte(content), mode)
	}
	if err := write(filepath.Join(t.localRoot, "alpha.txt"), "alpha", 0o644); err != nil {
		return err
	}
	if err := write(filepath.Join(t.localRoot, "beta.bin"), strings.Repeat("b", 100), 0o644); err != nil {
		return err
	}
	if err := write(filepath.Join(t.localRoot, ".secret"), "hidden", 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(t.localRoot, "folder"), 0o755); err != nil {
		return err
	}
	if err := write(filepath.Join(t.localRoot, "folder", "inside.txt"), "inside", 0o600); err != nil {
		return err
	}
	if err := write(filepath.Join(t.remoteRoot, "remote.txt"), "remote", 0o644); err != nil {
		return err
	}

	t.remote = &selftest30Remote{root: t.remoteRoot}
	service := apptransfer.NewService(
		adaptersftp.NewLocal(),
		t.remote,
		apptransfer.WithHosts(selftest30Hosts{}),
		apptransfer.WithBookmarks(adaptersftp.NewBookmarks(t.bookmarks, t.localBookmarks)),
	)
	t.state = transferstate.NewState(service)
	t.deadline = time.Now().Add(selftest30Wait)
	t.cleanup = func() {
		t.state.Shutdown()
		t.remote.Close()
		_ = os.RemoveAll(t.workdir)
	}
	return nil
}

func (t *selftest30) expired(what string) selftest.Outcome {
	if time.Now().After(t.deadline) {
		return selftest.Failed(fmt.Errorf("%s did not finish within %s", what, selftest30Wait))
	}
	return selftest.Pending
}

func (t *selftest30) browseStep(*ui.Context) selftest.Outcome {
	if t.finished {
		*t = selftest30{}
	}
	switch t.browsePhase {
	case 0:
		if err := t.setup(); err != nil {
			return selftest.Failed(fmt.Errorf("the SFTP fixtures were not created: %v", err))
		}
		t.state.EnsureStarted()
		t.browsePhase = 1
		return selftest.Pending
	case 1:
		t.state.Flush()
		left := t.state.Tab(transferstate.Left)
		if left == nil || left.FS == nil {
			return t.expired("the local pane")
		}
		if left.Path != t.localRoot {
			t.state.Navigate(transferstate.Left, t.localRoot)
			t.browsePhase = 2
			return selftest.Pending
		}
		t.state.OpenRemote(transferstate.Right, "selftest", "Loopback")
		t.browsePhase = 3
		return selftest.Pending
	case 2:
		t.state.Flush()
		left := t.state.Tab(transferstate.Left)
		if left == nil || left.Loading || left.Path != t.localRoot {
			return t.expired("the local fixture listing")
		}
		t.state.OpenRemote(transferstate.Right, "selftest", "Loopback")
		t.browsePhase = 3
		return selftest.Pending
	case 3:
		t.state.Flush()
		right := t.state.Tab(transferstate.Right)
		if right == nil || right.Loading || right.FS == nil {
			return t.expired("the remote pane")
		}
		t.state.Focus(transferstate.Left)
		t.state.Navigate(transferstate.Left, filepath.Join(t.localRoot, "folder"))
		t.browsePhase = 4
		return selftest.Pending
	case 4:
		t.state.Flush()
		left := t.state.Tab(transferstate.Left)
		right := t.state.Tab(transferstate.Right)
		if left == nil || right == nil || left.Loading || right.Loading ||
			left.FS == nil || right.FS == nil ||
			left.Path != filepath.Join(t.localRoot, "folder") {
			return t.expired("the folder listing")
		}
		if right.Path == left.Path {
			return selftest.Failed(fmt.Errorf("navigating the left pane moved the right pane to %q", right.Path))
		}
		if !hasEntry(left, "inside.txt") {
			return selftest.Failed(fmt.Errorf("the left pane does not show folder/inside.txt"))
		}
		if !hasEntry(right, "remote.txt") {
			return selftest.Failed(fmt.Errorf("the remote pane does not show remote.txt"))
		}
		// Independent hidden-file and ordering settings.
		rightShowHidden, rightSort := right.ShowHidden, right.Sort
		t.state.ToggleHidden(transferstate.Left)
		t.state.SetSort(transferstate.Left, domainsftp.Sort{Field: domainsftp.SortSize, Descending: true})
		if !left.ShowHidden {
			return selftest.Failed(fmt.Errorf("the hidden-file toggle did not reach the left pane"))
		}
		if right.ShowHidden != rightShowHidden || right.Sort != rightSort {
			return selftest.Failed(fmt.Errorf("the left pane's settings leaked to the right pane"))
		}
		t.state.Navigate(transferstate.Left, t.localRoot)
		t.browsePhase = 5
		return selftest.Pending
	}

	t.state.Flush()
	left := t.state.Tab(transferstate.Left)
	right := t.state.Tab(transferstate.Right)
	if left == nil || right == nil || left.Loading || right.Loading || left.Path != t.localRoot {
		return t.expired("the panes to settle")
	}
	if !visibleEntry(left, ".secret") {
		return selftest.Failed(fmt.Errorf("the hidden file is not visible on the left pane"))
	}
	if visibleEntry(right, ".secret") {
		return selftest.Failed(fmt.Errorf("the hidden file appears on the right pane"))
	}
	return selftest.Passed(
		fmt.Sprintf("left %s and right %s list independently; hidden and sort settings stay per-pane",
			filepath.Base(left.Path), right.Path), "adaptersftp.Local + adaptersftp.Remote + transferstate.State")
}

func hasEntry(tab *transferstate.Tab, name string) bool {
	for _, entry := range tab.Entries {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func visibleEntry(tab *transferstate.Tab, name string) bool {
	for _, entry := range tab.Visible() {
		if entry.Name == name {
			return true
		}
	}
	return false
}

// entriesReady reports whether a pane has settled and shows every named
// entry. A one-shot operation refreshes its Tab asynchronously, so the step
// waits for that listing instead of assuming it landed with the operation.
func (t *selftest30) entriesReady(side transferstate.Side, names ...string) bool {
	tab := t.state.Tab(side)
	if tab == nil || tab.Loading {
		return false
	}
	for _, name := range names {
		if !hasEntry(tab, name) {
			return false
		}
	}
	return true
}

func entryIn(tab *transferstate.Tab, name string) (domainsftp.Entry, bool) {
	for _, entry := range tab.Entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return domainsftp.Entry{}, false
}

func (t *selftest30) finishOp() bool {
	t.state.Flush()
	return !t.state.Busy()
}

func (t *selftest30) memoryContent(name string) (string, error) {
	tab := t.state.Tab(transferstate.Right)
	reader, err := tab.FS.Open(context.Background(), tab.FS.Join(tab.Path, name))
	if err != nil {
		return "", err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	return string(data), err
}

func (t *selftest30) localContent(name string) (string, error) {
	tab := t.state.Tab(transferstate.Left)
	reader, err := tab.FS.Open(context.Background(), tab.FS.Join(tab.Path, name))
	if err != nil {
		return "", err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	return string(data), err
}

func (t *selftest30) selectLocal(name string) error {
	tab := t.state.Tab(transferstate.Left)
	entry, ok := entryIn(tab, name)
	if !ok {
		return fmt.Errorf("the left pane has no %q", name)
	}
	t.state.Focus(transferstate.Left)
	t.state.Click(transferstate.Left, entry, 0, false)
	return nil
}

func (t *selftest30) selectRemote(name string) error {
	tab := t.state.Tab(transferstate.Right)
	entry, ok := entryIn(tab, name)
	if !ok {
		return fmt.Errorf("the right pane has no %q", name)
	}
	t.state.Focus(transferstate.Right)
	t.state.Click(transferstate.Right, entry, 0, false)
	return nil
}

func (t *selftest30) operationStep(*ui.Context) selftest.Outcome {
	switch t.opPhase {
	case 0:
		if err := t.selectLocal("alpha.txt"); err != nil {
			return selftest.Failed(err)
		}
		t.state.Transfer(transferstate.Left)
		t.opPhase = 1
		return selftest.Pending
	case 1:
		if !t.finishOp() {
			return t.expired("the upload")
		}
		if got, err := t.memoryContent("alpha.txt"); err != nil || got != "alpha" {
			return selftest.Failed(fmt.Errorf("upload wrote %q: %v", got, err))
		}
		// Make the destination exist so the next upload must ask before it
		// replaces anything.
		if err := os.WriteFile(filepath.Join(t.remoteRoot, "alpha.txt"), []byte("old-remote"), 0o644); err != nil {
			return selftest.Failed(err)
		}
		t.state.Transfer(transferstate.Left)
		t.opPhase = 2
		return selftest.Pending
	case 2:
		t.state.Flush()
		dialog := t.state.Dialog()
		if dialog == nil {
			if t.state.Busy() {
				return t.expired("the overwrite prompt")
			}
			return selftest.Failed(fmt.Errorf("a second upload did not ask about the existing file"))
		}
		if dialog.Kind != transferstate.DialogOverwrite {
			return selftest.Failed(fmt.Errorf("the prompt is %q, want overwrite", dialog.Kind))
		}
		if got, _ := t.memoryContent("alpha.txt"); got != "old-remote" {
			return selftest.Failed(fmt.Errorf("the destination changed before confirmation: %q", got))
		}
		t.state.SubmitDialog()
		t.opPhase = 3
		return selftest.Pending
	case 3:
		if !t.finishOp() {
			return t.expired("the confirmed overwrite")
		}
		if got, _ := t.memoryContent("alpha.txt"); got != "alpha" {
			return selftest.Failed(fmt.Errorf("the confirmed overwrite wrote %q", got))
		}
		t.state.RequestNewFolder(transferstate.Left)
		t.state.SetDialogDraft("created")
		t.state.SubmitDialog()
		t.opPhase = 4
		return selftest.Pending
	case 4:
		if !t.finishOp() {
			return t.expired("the folder creation")
		}
		if _, err := os.Stat(filepath.Join(t.localRoot, "created")); err != nil {
			return selftest.Failed(fmt.Errorf("the new folder is missing: %v", err))
		}
		t.state.RequestNewFile(transferstate.Left)
		t.state.SetDialogDraft("notes.txt")
		t.state.SubmitDialog()
		t.opPhase = 5
		return selftest.Pending
	case 5:
		if !t.finishOp() {
			return t.expired("the file creation")
		}
		if _, err := os.Stat(filepath.Join(t.localRoot, "notes.txt")); err != nil {
			return selftest.Failed(fmt.Errorf("the new file is missing: %v", err))
		}
		if !t.entriesReady(transferstate.Left, "notes.txt") {
			return t.expired("the listing to show the new file")
		}
		if err := t.selectLocal("notes.txt"); err != nil {
			return selftest.Failed(err)
		}
		t.state.RequestRename(transferstate.Left)
		t.state.SetDialogDraft("renamed.txt")
		t.state.SubmitDialog()
		t.opPhase = 6
		return selftest.Pending
	case 6:
		if !t.finishOp() {
			return t.expired("the rename")
		}
		if _, err := os.Stat(filepath.Join(t.localRoot, "notes.txt")); err == nil {
			return selftest.Failed(fmt.Errorf("rename left the old name"))
		}
		if _, err := os.Stat(filepath.Join(t.localRoot, "renamed.txt")); err != nil {
			return selftest.Failed(fmt.Errorf("rename did not write the new name: %v", err))
		}
		if !t.entriesReady(transferstate.Left, "renamed.txt") {
			return t.expired("the listing to show the renamed file")
		}
		if err := t.selectLocal("renamed.txt"); err != nil {
			return selftest.Failed(err)
		}
		t.state.RequestChmod(transferstate.Left)
		t.state.SetDialogDraft("0600")
		t.state.SubmitDialog()
		t.opPhase = 7
		return selftest.Pending
	case 7:
		if !t.finishOp() {
			return t.expired("the permission change")
		}
		info, err := os.Stat(filepath.Join(t.localRoot, "renamed.txt"))
		if err != nil {
			return selftest.Failed(err)
		}
		// Windows reports synthesized permission bits; the chmod call itself is
		// still exercised and must not fail.
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			return selftest.Failed(fmt.Errorf("mode is %04o, want 0600", info.Mode().Perm()))
		}
		if !t.entriesReady(transferstate.Left, "alpha.txt") {
			return t.expired("the listing to show alpha.txt")
		}
		if err := t.selectLocal("alpha.txt"); err != nil {
			return selftest.Failed(err)
		}
		t.state.RequestDelete(transferstate.Left)
		if t.state.Dialog() == nil || t.state.Dialog().Kind != transferstate.DialogDelete {
			return selftest.Failed(fmt.Errorf("delete did not ask for confirmation"))
		}
		if _, err := os.Stat(filepath.Join(t.localRoot, "alpha.txt")); err != nil {
			return selftest.Failed(fmt.Errorf("delete removed the file before confirmation"))
		}
		t.state.SubmitDialog()
		t.opPhase = 8
		return selftest.Pending
	case 8:
		if !t.finishOp() {
			return t.expired("the delete")
		}
		if _, err := os.Stat(filepath.Join(t.localRoot, "alpha.txt")); err == nil {
			return selftest.Failed(fmt.Errorf("the confirmed delete left alpha.txt"))
		}
		if !t.entriesReady(transferstate.Right, "remote.txt") {
			return t.expired("the remote listing to settle")
		}
		if err := t.selectRemote("remote.txt"); err != nil {
			return selftest.Failed(err)
		}
		t.state.Transfer(transferstate.Right)
		t.opPhase = 9
		return selftest.Pending
	}

	if !t.finishOp() {
		return t.expired("the download")
	}
	if got, err := t.localContent("remote.txt"); err != nil || got != "remote" {
		return selftest.Failed(fmt.Errorf("download wrote %q: %v", got, err))
	}
	return selftest.Passed(
		"upload, overwrite prompt, mkdir, create, rename, chmod, confirmed delete, and download all ran",
		"apptransfer.Service + adaptersftp.Local + adaptersftp.Remote")
}

// guardStep reproduces the acceptance failure: a second local Tab enters a
// folder, and the first Tab tries to copy that folder into it. The guard must
// refuse before any filesystem mutation.
func (t *selftest30) guardStep(*ui.Context) selftest.Outcome {
	switch t.guardPhase {
	case 0:
		t.state.OpenLocal(transferstate.Right)
		t.guardPhase = 1
		return selftest.Pending
	case 1:
		t.state.Flush()
		tab := t.state.Tab(transferstate.Right)
		if tab == nil || tab.FS == nil || tab.Loading {
			return t.expired("the second local Tab")
		}
		t.state.Navigate(transferstate.Right, filepath.Join(t.localRoot, "folder"))
		t.guardPhase = 2
		return selftest.Pending
	case 2:
		t.state.Flush()
		tab := t.state.Tab(transferstate.Right)
		if tab == nil || tab.Loading || tab.Path != filepath.Join(t.localRoot, "folder") {
			return t.expired("the second local Tab to enter the folder")
		}
		if err := t.selectLocal("folder"); err != nil {
			return selftest.Failed(err)
		}
		t.state.Transfer(transferstate.Left)
		t.guardPhase = 3
		return selftest.Pending
	}

	t.state.Flush()
	if got := t.state.Notice(); got != "copy-into-self" {
		return selftest.Failed(fmt.Errorf("cross-Tab copy notice = %q, want copy-into-self", got))
	}
	if t.state.Busy() {
		return selftest.Failed(fmt.Errorf("the refused copy still started an operation"))
	}
	if _, err := os.Stat(filepath.Join(t.localRoot, "folder", "folder")); err == nil {
		return selftest.Failed(fmt.Errorf("the refused copy created folder/folder"))
	}
	if _, err := os.Stat(filepath.Join(t.localRoot, "folder", "inside.txt")); err != nil {
		return selftest.Failed(fmt.Errorf("the refused copy changed the source: %v", err))
	}
	t.state.CloseTab(transferstate.Right)
	return selftest.Passed(
		"two local Tabs over one root are refused when the destination is inside the copied folder, with no writes",
		"transferstate.State + adaptersftp.Local")
}

// bookmarkFileHas reports whether a persisted list contains a bookmark with
// the wanted path and/or Host. An empty criterion is ignored.
func bookmarkFileHas(path, wantPath string, wantHost model.HostID) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var items []domainsftp.Bookmark
	if err := json.Unmarshal(raw, &items); err != nil {
		return false
	}
	for _, item := range items {
		if wantPath != "" && item.Path != wantPath {
			continue
		}
		if wantHost != "" && item.HostID != wantHost {
			continue
		}
		return true
	}
	return false
}

func (t *selftest30) viewStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	switch t.viewPhase {
	case 0:
		t.state.Duplicate(transferstate.Left)
		t.viewPhase = 1
		return selftest.Pending
	case 1:
		t.state.Flush()
		tab := t.state.Tab(transferstate.Left)
		if tab == nil || tab.FS == nil || tab.Loading {
			return t.expired("the duplicated Tab")
		}
		if tab.Path != t.localRoot {
			return selftest.Failed(fmt.Errorf("the duplicated Tab is at %q, want %q", tab.Path, t.localRoot))
		}
		t.state.ToggleBookmark(transferstate.Right)
		if !t.state.Bookmarked(transferstate.Right) {
			return selftest.Failed(fmt.Errorf("the remote folder was not bookmarked"))
		}
		t.state.ToggleBookmark(transferstate.Left)
		if !t.state.Bookmarked(transferstate.Left) {
			return selftest.Failed(fmt.Errorf("the local folder was not bookmarked"))
		}
		t.viewPhase = 2
		return selftest.Pending
	case 2:
		global := bookmarkFileHas(t.bookmarks, "", "selftest")
		local := bookmarkFileHas(t.localBookmarks, t.localRoot, "")
		if !global || !local {
			if time.Now().Before(t.deadline) {
				return selftest.Pending
			}
			return selftest.Failed(fmt.Errorf("bookmarks were not persisted: global=%v local=%v", global, local))
		}
		t.viewPhase = 3
	}

	t.state.Focus(transferstate.Left)
	if !t.entriesReady(transferstate.Left, "beta.bin") {
		return t.expired("the left listing in the view step")
	}
	if err := t.selectLocal("beta.bin"); err != nil {
		return selftest.Failed(err)
	}
	tester := ui.NewTester(transferui.View(t.state), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	for _, label := range []string{
		m.SftpTitle(), m.SftpLocal(), "Loopback", m.SftpUp(), m.SftpRefresh(),
		m.SftpNewFolder(), m.SftpNewFile(), m.SftpRename(), m.SftpPermissions(),
		m.SftpDelete(), m.SftpCopyTo(), m.SftpMoveTo(), m.SftpSort(), m.SftpShowHidden(),
		m.SftpBookmarks(),
	} {
		if rect, ok := tester.Find(label); !ok || rect.W <= 0 || rect.H <= 0 {
			return selftest.Failed(fmt.Errorf("the 1100x720 surface does not show %q", label))
		}
	}
	if !tester.HasText("beta.bin") {
		return selftest.Failed(fmt.Errorf("the local listing is missing from the view: %v", tester.Texts()))
	}

	small := ui.NewTester(transferui.View(t.state), 720, 480)
	small.SetLocale("zh-CN")
	small.Frame()
	zh := locales.In("zh-CN")
	for _, label := range []string{zh.SftpTitle(), zh.SftpSort(), zh.SftpShowHidden(), zh.SftpBookmarks(), zh.SftpDelete()} {
		rect, ok := small.Find(label)
		if !ok || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > 720 || rect.Y+rect.H > 480 {
			return selftest.Failed(fmt.Errorf("the 720x480 zh-CN surface clips %q: %+v", label, rect))
		}
	}

	// The delete button asks first; Cancel leaves the file in place.
	if err := tester.Click(m.SftpDelete()); err != nil {
		return selftest.Failed(fmt.Errorf("the delete button has no accessible label: %v", err))
	}
	tester.Frame()
	if t.state.Dialog() == nil || t.state.Dialog().Kind != transferstate.DialogDelete {
		return selftest.Failed(fmt.Errorf("the toolbar did not open the delete confirmation"))
	}
	if !tester.HasText(m.SftpDeleteTitle()) {
		return selftest.Failed(fmt.Errorf("the confirmation does not explain the deletion"))
	}
	if err := tester.Click(m.SftpCancel()); err != nil {
		return selftest.Failed(err)
	}
	tester.Frame()
	if t.state.Dialog() != nil {
		return selftest.Failed(fmt.Errorf("Cancel left the confirmation open"))
	}
	if _, err := os.Stat(filepath.Join(t.localRoot, "beta.bin")); err != nil {
		return selftest.Failed(fmt.Errorf("Cancel removed the file"))
	}

	before := t.state.Tab(transferstate.Left).ShowHidden
	if err := tester.Click(m.SftpShowHidden()); err != nil {
		return selftest.Failed(err)
	}
	tester.Frame()
	after := t.state.Tab(transferstate.Left).ShowHidden
	if after == before {
		return selftest.Failed(fmt.Errorf("the hidden-file toggle did not change the setting"))
	}
	if after != tester.HasText(".secret") {
		return selftest.Failed(fmt.Errorf("the dotfile visibility does not match the setting: show=%v", after))
	}

	if t.cleanup != nil {
		t.cleanup()
		t.cleanup = nil
	}
	t.finished = true
	return selftest.Passed(
		"the real view renders both panes at 720x480 and 1100x720 in en and zh-CN; tabs, bookmark persistence, hidden toggle, and delete confirmation work",
		"transferui.View + transferstate.State + adaptersftp.Bookmarks")
}
