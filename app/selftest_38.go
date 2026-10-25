//go:build selftest

package app

// Ticket 38 self-test. Press F11, then 3, then 8. The check proves remote
// editing through the app's real seams:
//
//   - Open and save back: the real transfer surface opens a remote fixture
//     file with the internal editor; the user types into the real editor
//     view's text area and presses Save; the file content on the "server"
//     changes through application/transfer.Service.WriteFile, and closing the
//     document removes its materialization directory.
//   - External editing: a fake launcher stands in for the operating system so
//     no editor process starts; the test writes the materialized copy the
//     launcher received and the watcher uploads it after the debounce.
//   - Remote changes: an internal save and an external upload each detect that
//     the remote file changed, show the conflict prompt, write nothing until
//     Overwrite is pressed, and then replace it.
//   - Lost connection: a wrapper around the real local transport fails the
//     staging create; an internal save keeps the buffer and reports the
//     failure, an external upload keeps the local copy and prompts; both
//     retries succeed once the transport recovers.
//   - Temp sweep: an orphan left by a previous process is removed at startup;
//     a live session's directory survives an explicit sweep; closing the
//     document removes its directory; Shutdown sweeps the rest.
//
// The remote pane is the real local adapter viewed as a remote transport, so
// the test exercises the same FileSystem port and staging write a real SFTP
// connection uses, without needing a server or the network. Every wait polls
// the real surface under a 30-second deadline; there is no sleep.
//
// Result: %TEMP%\natcatty-selftest-38.json.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	adaptersstore "github.com/mslxl/natcatty/adapters/store"
	appfileopen "github.com/mslxl/natcatty/application/fileopen"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	editorui "github.com/mslxl/natcatty/ui/editor"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

const selftest38Ticket = 38

const selftest38Wait = 30 * time.Second

type selftest38 struct {
	workdir     string
	remoteDir   string
	downloadDir string
	preferences *apppreferences.Service
	launcher    *selftest38Launcher
	remote      *selftest38RemoteFS
	open        *appfileopen.Service
	editor      *editorui.State
	transfer    *transferstate.State
	view        *ui.Tester
	events      *selftest38Events
	cleanup     func()

	startupSwept int

	saveStep     selftest38Step
	watchStep    selftest38Step
	conflictStep selftest38Step
	failureStep  selftest38Step
	sweepStep    selftest38Step

	paneStarted bool
}

// selftest38Step is one user-path step's progress. The harness calls a step
// once per frame until it reports an outcome, so each step keeps its phase.
type selftest38Step struct {
	phase    int
	started  bool
	deadline time.Time
	text     string
	path     string
	before   int
}

func (t *selftest38) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest38Ticket,
		Name:   "Remote editing",
		Steps: []selftest.Step{
			{Name: "fixtures-and-startup-sweep", Run: t.fixturesStep},
			{Name: "internal-editor-save-back", Run: t.saveBackStep},
			{Name: "external-edit-debounce", Run: t.externalStep},
			{Name: "remote-change-conflict", Run: t.remoteConflictStep},
			{Name: "lost-connection-retry", Run: t.lostConnectionStep},
			{Name: "temp-sweep-on-close", Run: t.tempSweepStep},
		},
	}
}

// selftest38Launcher records what the file-open service asked to start. It
// never runs a process, so the check cannot disturb the user's desktop.
type selftest38Launcher struct {
	mu    sync.Mutex
	calls []selftest38Launch
}

type selftest38Launch struct {
	system      bool
	application string
	path        string
}

func (l *selftest38Launcher) OpenSystem(_ context.Context, path string) error {
	l.record(selftest38Launch{system: true, path: path})
	return nil
}

func (l *selftest38Launcher) OpenApplication(_ context.Context, application, path string) error {
	l.record(selftest38Launch{application: application, path: path})
	return nil
}

func (l *selftest38Launcher) record(call selftest38Launch) {
	l.mu.Lock()
	l.calls = append(l.calls, call)
	l.mu.Unlock()
}

func (l *selftest38Launcher) reset() {
	l.mu.Lock()
	l.calls = nil
	l.mu.Unlock()
}

func (l *selftest38Launcher) lastPath() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.calls) == 0 {
		return "", false
	}
	return l.calls[len(l.calls)-1].path, true
}

// selftest38RemoteFS wraps the real local transport as the remote pane's
// filesystem. Only Create can be made to fail, which simulates a lost
// connection at the point where a save stages its bytes; listing, reading,
// and stats keep working.
type selftest38RemoteFS struct {
	apptransfer.FileSystem
	mu         sync.Mutex
	failWrites bool
}

func (f *selftest38RemoteFS) Backing() string { return "selftest-remote" }

func (f *selftest38RemoteFS) setFailWrites(fail bool) {
	f.mu.Lock()
	f.failWrites = fail
	f.mu.Unlock()
}

func (f *selftest38RemoteFS) Create(ctx context.Context, path string) (io.WriteCloser, error) {
	f.mu.Lock()
	fail := f.failWrites
	f.mu.Unlock()
	if fail {
		return nil, errors.New("the connection was reset")
	}
	return f.FileSystem.Create(ctx, path)
}

// selftest38Opener supplies the local and remote panes. The remote one is the
// wrapper above; it addresses a fixture directory the test navigates to.
type selftest38Opener struct {
	local  apptransfer.FileSystem
	remote apptransfer.FileSystem
}

func (o *selftest38Opener) OpenLocal(context.Context) (apptransfer.FileSystem, error) {
	return o.local, nil
}

func (o *selftest38Opener) OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error) {
	return o.remote, nil
}

func (t *selftest38) fixturesStep(*ui.Context) selftest.Outcome {
	// This is the first step of the check, so dropping any previous instance
	// starts every run from fresh fixtures even when the previous run failed
	// part-way. It is synchronous, so it runs exactly once per check.
	if t.cleanup != nil {
		t.cleanup()
	}
	*t = selftest38{}
	if err := t.setup(); err != nil {
		return selftest.Failed(fmt.Errorf("the ticket 38 fixtures were not created: %v", err))
	}
	if t.startupSwept != 1 {
		return selftest.Failed(fmt.Errorf(
			"the startup sweep removed %d stale directories, want 1", t.startupSwept))
	}
	return selftest.Passed(
		"fresh remote fixtures were created; the app-start sweep removed a stale materialization directory",
		"application/fileopen.Service.SweepTemp (the call app/app.go makes at startup)")
}

func (t *selftest38) setup() error {
	if t.workdir != "" {
		return nil
	}
	workdir, err := os.MkdirTemp("", "natcatty-selftest-38-")
	if err != nil {
		return err
	}
	t.workdir = workdir
	t.remoteDir = filepath.Join(workdir, "remote")
	t.downloadDir = filepath.Join(workdir, "downloads")
	fail := func(err error) error {
		_ = os.RemoveAll(workdir)
		t.workdir = ""
		return err
	}
	for _, dir := range []string{t.remoteDir, t.downloadDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fail(err)
		}
	}
	for _, name := range []string{
		"notes.txt", "watch.txt", "conflict-internal.txt",
		"conflict-external.txt", "lost.txt", "lost-external.txt",
	} {
		if err := os.WriteFile(filepath.Join(t.remoteDir, name),
			[]byte("remote original"), 0o640); err != nil {
			return fail(err)
		}
	}
	// A directory a crashed previous process left behind is what the startup
	// sweep exists for.
	orphan := filepath.Join(t.downloadDir, "natcatty-open-crashed-process")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		return fail(err)
	}

	preferences := apppreferences.NewService(
		adaptersstore.OpenPreferences(filepath.Join(workdir, "preferences.json")))
	if err := preferences.Load(context.Background()); err != nil {
		return fail(err)
	}
	t.preferences = preferences
	if err := t.setDefault(domainpreferences.OpenerInternal); err != nil {
		return fail(err)
	}

	t.launcher = &selftest38Launcher{}
	localFiles, err := adaptersftp.NewLocal().OpenLocal(context.Background())
	if err != nil {
		return fail(err)
	}
	remoteFiles, err := adaptersftp.NewLocal().OpenLocal(context.Background())
	if err != nil {
		return fail(err)
	}
	t.remote = &selftest38RemoteFS{FileSystem: remoteFiles}
	t.open = appfileopen.New(t.preferences, t.launcher,
		appfileopen.WithTempRoot(t.downloadDir),
		appfileopen.WithWatchInterval(20*time.Millisecond),
		appfileopen.WithWatchDebounce(80*time.Millisecond))
	t.editor = editorui.NewState(t.open, editorui.WithRedrawRequest(func() {}))
	t.open.SetEditor(t.editor)
	t.events = &selftest38Events{}
	t.open.SetObserver(func(event appfileopen.WatchEvent) {
		t.events.add(event)
		t.editor.Notify(event)
	})

	removed, err := t.open.SweepTemp(context.Background())
	if err != nil {
		return fail(fmt.Errorf("the startup sweep failed: %w", err))
	}
	t.startupSwept = removed
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		return fail(fmt.Errorf("the startup sweep left the orphan directory"))
	}

	opener := &selftest38Opener{
		local:  localFiles,
		remote: t.remote,
	}
	transferService := apptransfer.NewService(opener, opener)
	t.transfer = transferstate.NewState(transferService,
		transferstate.WithFileOpener(t.open.Open),
		transferstate.WithFileSessionCloser(t.open.Close))
	t.view = ui.NewTester(editorui.View(t.editor), 900, 700)
	t.view.SetLocale("en")
	t.view.Frame()

	t.cleanup = func() {
		if t.transfer != nil {
			t.transfer.Shutdown()
		}
		if t.editor != nil {
			t.editor.Shutdown()
		}
		if t.open != nil {
			t.open.Shutdown()
		}
		_ = os.RemoveAll(workdir)
	}
	return nil
}

// selftest38Events records every watcher outcome. The steps wait for the
// synced event before reading the remote file, so the test never holds a read
// handle while the watcher renames the new version into place (on Windows an
// open reader blocks the replacement rename).
type selftest38Events struct {
	mu     sync.Mutex
	events []selftest38Event
}

type selftest38Event struct {
	kind    appfileopen.WatchEventKind
	session appfileopen.EditID
	name    string
	err     error
}

func (e *selftest38Events) add(event appfileopen.WatchEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, selftest38Event{
		kind: event.Kind, session: event.Session, name: event.Name, err: event.Err,
	})
}

func (e *selftest38Events) mark() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.events)
}

func (e *selftest38Events) syncedSince(mark int, name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, event := range e.events[min(mark, len(e.events)):] {
		if event.kind == appfileopen.WatchSynced && event.name == name {
			return true
		}
	}
	return false
}

func (t *selftest38) setDefault(kind domainpreferences.OpenerKind) error {
	return t.preferences.Update(func(settings *domainpreferences.Settings) {
		opener := domainpreferences.Opener{Kind: kind}
		if kind == domainpreferences.OpenerExternal {
			opener.Application = domainpreferences.ExternalApplication{
				Path: filepath.Join(t.workdir, "viewer.exe"), Name: "Viewer",
			}
		}
		settings.SFTPEditor.DefaultOpener = opener
	})
}

func (t *selftest38) begin(step *selftest38Step) {
	if !step.started {
		step.started = true
		step.deadline = time.Now().Add(selftest38Wait)
	}
}

func (t *selftest38) expired(step *selftest38Step, what string) selftest.Outcome {
	if time.Now().After(step.deadline) {
		return selftest.Failed(fmt.Errorf("%s did not happen within %s", what, selftest38Wait))
	}
	return selftest.Pending
}

func (t *selftest38) frame() {
	t.transfer.Flush()
	t.view.Frame()
}

// remoteReady starts both panes and waits for the remote fixture listing.
func (t *selftest38) remoteReady() bool {
	if !t.paneStarted {
		t.paneStarted = true
		t.transfer.EnsureStarted()
		t.transfer.OpenRemote(transferstate.Right, model.HostID("h1"), "Fixture")
	}
	tab := t.transfer.Tab(transferstate.Right)
	if tab == nil || tab.FS == nil || tab.Loading || tab.Path == "" {
		return false
	}
	if !samePath(tab.Path, t.remoteDir) {
		t.transfer.Navigate(transferstate.Right, t.remoteDir)
		return false
	}
	return true
}

func (t *selftest38) openRemoteFile(name string) error {
	entry, ok := entryByName(t.transfer, transferstate.Right, name)
	if !ok {
		return fmt.Errorf("the remote fixture listing has no %s", name)
	}
	t.transfer.Open(transferstate.Right, entry)
	return nil
}

func (t *selftest38) remoteText(name string) string {
	content, err := os.ReadFile(filepath.Join(t.remoteDir, name))
	if err != nil {
		return ""
	}
	return string(content)
}

func (t *selftest38) tempDirs() []string {
	entries, err := os.ReadDir(t.downloadDir)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "natcatty-open-") {
			dirs = append(dirs, entry.Name())
		}
	}
	return dirs
}

func (t *selftest38) saveBackStep(*ui.Context) selftest.Outcome {
	step := &t.saveStep
	t.begin(step)
	if err := t.setup(); err != nil {
		return selftest.Failed(fmt.Errorf("the ticket 38 fixtures were not created: %v", err))
	}
	t.frame()
	if !t.remoteReady() {
		return t.expired(step, "the remote fixture listing")
	}
	m := locales.In("en")
	switch step.phase {
	case 0:
		if err := t.setDefault(domainpreferences.OpenerInternal); err != nil {
			return selftest.Failed(fmt.Errorf("the internal opener was not selected: %v", err))
		}
		step.before = len(t.tempDirs())
		if err := t.openRemoteFile("notes.txt"); err != nil {
			return selftest.Failed(err)
		}
		step.phase = 1
		return t.expired(step, "the internal editor document")
	case 1:
		if !t.editor.Active() {
			return t.expired(step, "the internal editor document")
		}
		content := t.editor.Content()
		if content == nil || *content != "remote original" {
			return selftest.Failed(fmt.Errorf("the editor opened %q, want the remote content",
				deref(content)))
		}
		if err := t.view.Click("editor-content"); err != nil {
			return selftest.Failed(fmt.Errorf("the editor text area was not reachable: %v", err))
		}
		t.view.Type(" plus editor")
		t.view.Frame()
		if !strings.Contains(*t.editor.Content(), "plus editor") {
			return selftest.Failed(fmt.Errorf("typing into the editor produced %q", *t.editor.Content()))
		}
		step.text = *t.editor.Content()
		step.phase = 2
		return t.expired(step, "the edited buffer")
	case 2:
		if err := t.view.Click(m.SftpEditorSave()); err != nil {
			return selftest.Failed(fmt.Errorf("the Save control was not reachable: %v", err))
		}
		t.view.Frame()
		step.phase = 3
		return t.expired(step, "the save-back")
	case 3:
		t.frame()
		if t.editor.Saving() {
			return t.expired(step, "the save-back")
		}
		if t.editor.SaveError() {
			return selftest.Failed(fmt.Errorf("the save back to the remote file failed"))
		}
		if !t.editor.Saved() {
			return t.expired(step, "the save-back")
		}
		if got := t.remoteText("notes.txt"); got != step.text {
			return selftest.Failed(fmt.Errorf("the remote file holds %q, want the editor buffer %q",
				got, step.text))
		}
		step.phase = 4
		return t.expired(step, "the editor close")
	case 4:
		if err := t.view.Click(m.SftpEditorClose()); err != nil {
			return selftest.Failed(fmt.Errorf("the Close control was not reachable: %v", err))
		}
		t.view.Frame()
		step.phase = 5
		return t.expired(step, "the editor close")
	default:
		t.frame()
		if t.editor.Active() {
			return t.expired(step, "the editor close")
		}
		if dirs := t.tempDirs(); len(dirs) != step.before {
			return t.expired(step, "the closed session's temporary directory removal")
		}
		return selftest.Passed(
			"the real remote pane opened notes.txt in the internal editor; typing and Save wrote the buffer back through the staged remote write, and Close removed the materialized copy",
			"transferstate.State.Open -> application/fileopen.Service -> ui/editor.State -> application/transfer.Service.WriteFile")
	}
}

func (t *selftest38) externalStep(*ui.Context) selftest.Outcome {
	step := &t.watchStep
	t.begin(step)
	if err := t.setup(); err != nil {
		return selftest.Failed(fmt.Errorf("the ticket 38 fixtures were not created: %v", err))
	}
	t.frame()
	if !t.remoteReady() {
		return t.expired(step, "the remote fixture listing")
	}
	switch step.phase {
	case 0:
		if err := t.setDefault(domainpreferences.OpenerExternal); err != nil {
			return selftest.Failed(fmt.Errorf("the external opener was not selected: %v", err))
		}
		t.launcher.reset()
		if err := t.openRemoteFile("watch.txt"); err != nil {
			return selftest.Failed(err)
		}
		step.phase = 1
		return t.expired(step, "the external application launch")
	case 1:
		path, ok := t.launcher.lastPath()
		if !ok || t.transfer.Notice() != "opened-external" {
			return t.expired(step, "the external application launch")
		}
		if !strings.Contains(path, "watch.txt") {
			return selftest.Failed(fmt.Errorf("the external application received %q, want the watch.txt copy", path))
		}
		step.path = path
		step.text = "external edit from the watched copy"
		step.before = t.events.mark()
		if err := os.WriteFile(path, []byte(step.text), 0o600); err != nil {
			return selftest.Failed(fmt.Errorf("the external edit could not be written: %v", err))
		}
		step.phase = 2
		return t.expired(step, "the debounced external upload")
	default:
		// Wait for the watcher's own outcome before reading the remote file:
		// an open read handle would block the replacement rename on Windows.
		if !t.events.syncedSince(step.before, "watch.txt") {
			return t.expired(step, "the debounced external upload")
		}
		if got := t.remoteText("watch.txt"); got != step.text {
			return selftest.Failed(fmt.Errorf("the synced remote file holds %q, want %q", got, step.text))
		}
		return selftest.Passed(
			"the external application's save of the materialized copy reached the remote file after the watcher's debounce",
			"application/fileopen.Service watcher (real launcher seam) -> application/transfer.Service.WriteFile")
	}
}

func (t *selftest38) remoteConflictStep(*ui.Context) selftest.Outcome {
	step := &t.conflictStep
	t.begin(step)
	if err := t.setup(); err != nil {
		return selftest.Failed(fmt.Errorf("the ticket 38 fixtures were not created: %v", err))
	}
	t.frame()
	if !t.remoteReady() {
		return t.expired(step, "the remote fixture listing")
	}
	m := locales.In("en")
	switch step.phase {
	case 0:
		if err := t.setDefault(domainpreferences.OpenerInternal); err != nil {
			return selftest.Failed(fmt.Errorf("the internal opener was not selected: %v", err))
		}
		if err := t.openRemoteFile("conflict-internal.txt"); err != nil {
			return selftest.Failed(err)
		}
		step.phase = 1
		return t.expired(step, "the internal editor document")
	case 1:
		if !t.editor.Active() {
			return t.expired(step, "the internal editor document")
		}
		// The remote file changes after the editor read it. The save must not
		// replace that version without the user's answer.
		if err := os.WriteFile(filepath.Join(t.remoteDir, "conflict-internal.txt"),
			[]byte("changed on the server"), 0o640); err != nil {
			return selftest.Failed(err)
		}
		if err := t.view.Click("editor-content"); err != nil {
			return selftest.Failed(fmt.Errorf("the editor text area was not reachable: %v", err))
		}
		t.view.Type(" mine")
		t.view.Frame()
		step.text = *t.editor.Content()
		if err := t.view.Click(m.SftpEditorSave()); err != nil {
			return selftest.Failed(fmt.Errorf("the Save control was not reachable: %v", err))
		}
		t.view.Frame()
		step.phase = 2
		return t.expired(step, "the remote-change conflict prompt")
	case 2:
		t.frame()
		if t.editor.Saving() {
			return t.expired(step, "the remote-change conflict prompt")
		}
		if t.editor.SaveError() {
			return selftest.Failed(fmt.Errorf("the conflicting save reported a plain failure"))
		}
		if !t.editor.ConflictPending() {
			return t.expired(step, "the remote-change conflict prompt")
		}
		if got := t.remoteText("conflict-internal.txt"); got != "changed on the server" {
			return selftest.Failed(fmt.Errorf("the conflicting save overwrote the remote file with %q", got))
		}
		if err := t.view.Click(m.SftpEditorOverwrite()); err != nil {
			return selftest.Failed(fmt.Errorf("the Overwrite control was not reachable: %v", err))
		}
		t.view.Frame()
		step.phase = 3
		return t.expired(step, "the approved overwrite")
	case 3:
		t.frame()
		if t.editor.Saving() {
			return t.expired(step, "the approved overwrite")
		}
		if t.editor.SaveError() {
			return selftest.Failed(fmt.Errorf("the approved overwrite failed"))
		}
		if !t.editor.Saved() {
			return t.expired(step, "the approved overwrite")
		}
		if got := t.remoteText("conflict-internal.txt"); got != step.text {
			return selftest.Failed(fmt.Errorf("the overwrite wrote %q, want %q", got, step.text))
		}
		if err := t.view.Click(m.SftpEditorClose()); err != nil {
			return selftest.Failed(fmt.Errorf("the Close control was not reachable: %v", err))
		}
		t.view.Frame()
		step.phase = 4
		return t.expired(step, "the editor close")
	case 4:
		t.frame()
		if t.editor.Active() {
			return t.expired(step, "the editor close")
		}
		if err := t.setDefault(domainpreferences.OpenerExternal); err != nil {
			return selftest.Failed(fmt.Errorf("the external opener was not selected: %v", err))
		}
		t.launcher.reset()
		if err := t.openRemoteFile("conflict-external.txt"); err != nil {
			return selftest.Failed(err)
		}
		step.phase = 5
		return t.expired(step, "the external application launch")
	case 5:
		path, ok := t.launcher.lastPath()
		if !ok || t.transfer.Notice() != "opened-external" {
			return t.expired(step, "the external application launch")
		}
		step.path = path
		if err := os.WriteFile(filepath.Join(t.remoteDir, "conflict-external.txt"),
			[]byte("changed by another client"), 0o640); err != nil {
			return selftest.Failed(err)
		}
		step.text = "local external edit"
		if err := os.WriteFile(path, []byte(step.text), 0o600); err != nil {
			return selftest.Failed(fmt.Errorf("the external edit could not be written: %v", err))
		}
		step.phase = 6
		return t.expired(step, "the external conflict prompt")
	case 6:
		t.frame()
		if !t.editor.ConflictPending() {
			if t.editor.FailurePending() {
				return selftest.Failed(fmt.Errorf("the external conflict was reported as a failure"))
			}
			return t.expired(step, "the external conflict prompt")
		}
		if got := t.remoteText("conflict-external.txt"); got != "changed by another client" {
			return selftest.Failed(fmt.Errorf("the external watcher overwrote the changed remote file with %q", got))
		}
		if err := t.view.Click(m.SftpEditorOverwrite()); err != nil {
			return selftest.Failed(fmt.Errorf("the Overwrite control was not reachable: %v", err))
		}
		t.view.Frame()
		step.phase = 7
		step.before = t.events.mark()
		return t.expired(step, "the approved external overwrite")
	default:
		t.frame()
		if t.editor.FailurePending() {
			return selftest.Failed(fmt.Errorf("the approved external overwrite failed"))
		}
		// Wait for the watcher's sync outcome, then read the file; reading
		// while the replacement rename is in flight would block it on Windows.
		if !t.events.syncedSince(step.before, "conflict-external.txt") {
			return t.expired(step, "the approved external overwrite")
		}
		if got := t.remoteText("conflict-external.txt"); got != step.text {
			return selftest.Failed(fmt.Errorf("the overwrite wrote %q, want %q", got, step.text))
		}
		return selftest.Passed(
			"both an internal save and an external upload detected a remote change, wrote nothing until Overwrite, then replaced the remote file",
			"application/fileopen.Service.Save conflict check -> ui/editor.State prompt -> application/transfer.Service.WriteFile")
	}
}

func (t *selftest38) lostConnectionStep(*ui.Context) selftest.Outcome {
	step := &t.failureStep
	t.begin(step)
	if err := t.setup(); err != nil {
		return selftest.Failed(fmt.Errorf("the ticket 38 fixtures were not created: %v", err))
	}
	t.frame()
	if !t.remoteReady() {
		return t.expired(step, "the remote fixture listing")
	}
	m := locales.In("en")
	switch step.phase {
	case 0:
		if err := t.setDefault(domainpreferences.OpenerInternal); err != nil {
			return selftest.Failed(fmt.Errorf("the internal opener was not selected: %v", err))
		}
		if err := t.openRemoteFile("lost.txt"); err != nil {
			return selftest.Failed(err)
		}
		step.phase = 1
		return t.expired(step, "the internal editor document")
	case 1:
		if !t.editor.Active() {
			return t.expired(step, "the internal editor document")
		}
		t.remote.setFailWrites(true)
		if err := t.view.Click("editor-content"); err != nil {
			return selftest.Failed(fmt.Errorf("the editor text area was not reachable: %v", err))
		}
		t.view.Type(" unsaved remote work")
		t.view.Frame()
		step.text = *t.editor.Content()
		if err := t.view.Click(m.SftpEditorSave()); err != nil {
			return selftest.Failed(fmt.Errorf("the Save control was not reachable: %v", err))
		}
		t.view.Frame()
		step.phase = 2
		return t.expired(step, "the failed save")
	case 2:
		t.frame()
		if t.editor.Saving() {
			return t.expired(step, "the failed save")
		}
		if !t.editor.SaveError() {
			return selftest.Failed(fmt.Errorf("the save survived a lost connection without an error"))
		}
		content := t.editor.Content()
		if content == nil || *content != step.text {
			return selftest.Failed(fmt.Errorf("the buffer after the failed save is %q, want %q",
				deref(content), step.text))
		}
		if got := t.remoteText("lost.txt"); got != "remote original" {
			return selftest.Failed(fmt.Errorf("the failed save changed the remote file to %q", got))
		}
		t.remote.setFailWrites(false)
		if err := t.view.Click(m.SftpEditorSave()); err != nil {
			return selftest.Failed(fmt.Errorf("the Save control was not reachable: %v", err))
		}
		t.view.Frame()
		step.phase = 3
		return t.expired(step, "the save retry")
	case 3:
		t.frame()
		if t.editor.Saving() {
			return t.expired(step, "the save retry")
		}
		if t.editor.SaveError() {
			return selftest.Failed(fmt.Errorf("the save retry failed after the connection returned"))
		}
		if !t.editor.Saved() {
			return t.expired(step, "the save retry")
		}
		if got := t.remoteText("lost.txt"); got != step.text {
			return selftest.Failed(fmt.Errorf("the save retry wrote %q, want %q", got, step.text))
		}
		if err := t.view.Click(m.SftpEditorClose()); err != nil {
			return selftest.Failed(fmt.Errorf("the Close control was not reachable: %v", err))
		}
		t.view.Frame()
		step.phase = 4
		return t.expired(step, "the editor close")
	case 4:
		t.frame()
		if t.editor.Active() {
			return t.expired(step, "the editor close")
		}
		if err := t.setDefault(domainpreferences.OpenerExternal); err != nil {
			return selftest.Failed(fmt.Errorf("the external opener was not selected: %v", err))
		}
		t.launcher.reset()
		if err := t.openRemoteFile("lost-external.txt"); err != nil {
			return selftest.Failed(err)
		}
		step.phase = 5
		return t.expired(step, "the external application launch")
	case 5:
		path, ok := t.launcher.lastPath()
		if !ok || t.transfer.Notice() != "opened-external" {
			return t.expired(step, "the external application launch")
		}
		step.path = path
		t.remote.setFailWrites(true)
		step.text = "external work kept locally"
		if err := os.WriteFile(path, []byte(step.text), 0o600); err != nil {
			return selftest.Failed(fmt.Errorf("the external edit could not be written: %v", err))
		}
		step.phase = 6
		return t.expired(step, "the external upload failure prompt")
	case 6:
		t.frame()
		if !t.editor.FailurePending() {
			if t.editor.ConflictPending() {
				return selftest.Failed(fmt.Errorf("the lost connection was reported as a conflict"))
			}
			return t.expired(step, "the external upload failure prompt")
		}
		if got := t.remoteText("lost-external.txt"); got != "remote original" {
			return selftest.Failed(fmt.Errorf("the failed external upload changed the remote file to %q", got))
		}
		if content, err := os.ReadFile(step.path); err != nil || string(content) != step.text {
			return selftest.Failed(fmt.Errorf("the external local copy after the failure = %q, %v",
				content, err))
		}
		t.remote.setFailWrites(false)
		step.before = t.events.mark()
		if err := t.view.Click(m.SftpEditorRetry()); err != nil {
			return selftest.Failed(fmt.Errorf("the Retry control was not reachable: %v", err))
		}
		t.view.Frame()
		step.phase = 7
		return t.expired(step, "the external upload retry")
	default:
		t.frame()
		if t.editor.FailurePending() {
			return selftest.Failed(fmt.Errorf("the external upload retry failed"))
		}
		// Wait for the retry's synced event before reading the remote file, so
		// the test's own read handle cannot block the replacement rename.
		if !t.events.syncedSince(step.before, "lost-external.txt") {
			return t.expired(step, "the external upload retry")
		}
		if got := t.remoteText("lost-external.txt"); got != step.text {
			return selftest.Failed(fmt.Errorf("the retry wrote %q, want %q", got, step.text))
		}
		return selftest.Passed(
			"a lost connection kept the internal buffer and the external local copy; both retries wrote the remote file once the transport returned",
			"application/fileopen.Service error classification -> ui/editor.State retry -> application/transfer.Service.WriteFile")
	}
}

func (t *selftest38) tempSweepStep(*ui.Context) selftest.Outcome {
	step := &t.sweepStep
	t.begin(step)
	if err := t.setup(); err != nil {
		return selftest.Failed(fmt.Errorf("the ticket 38 fixtures were not created: %v", err))
	}
	t.frame()
	switch step.phase {
	case 0:
		live := t.tempDirs()
		orphan := filepath.Join(t.downloadDir, "natcatty-open-later-orphan")
		if err := os.MkdirAll(orphan, 0o700); err != nil {
			return selftest.Failed(err)
		}
		removed, err := t.open.SweepTemp(context.Background())
		if err != nil {
			return selftest.Failed(fmt.Errorf("the sweep failed: %v", err))
		}
		if removed != 1 {
			return selftest.Failed(fmt.Errorf("the sweep removed %d directories, want only the orphan", removed))
		}
		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			return selftest.Failed(fmt.Errorf("the sweep left the orphan directory"))
		}
		if after := t.tempDirs(); len(after) != len(live) {
			return selftest.Failed(fmt.Errorf("the sweep removed a live session: before %v, after %v", live, after))
		}
		if err := os.MkdirAll(orphan, 0o700); err != nil {
			return selftest.Failed(err)
		}
		t.open.Shutdown()
		step.phase = 1
		return t.expired(step, "the shutdown sweep")
	default:
		if dirs := t.tempDirs(); len(dirs) != 0 {
			return t.expired(step, "the shutdown sweep")
		}
		return selftest.Passed(
			"an explicit sweep removed only the orphan and kept live sessions; Shutdown closed every session and swept the remaining temporary directories",
			"application/fileopen.Service.SweepTemp + Close/Shutdown")
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
