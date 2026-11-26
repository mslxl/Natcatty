package phoneshell

// Behavior tests for the Session Log tool. They drive the real Tools surface
// through the offscreen ui.Tester and a fake Session handle / workspace, so
// start, stop, naming, export cancellation, finalization, and write failures
// are proven without a simulator or the native terminal library.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeSessionLogWorkspace models the platform seam: a fixed Session Logs
// directory, a scripted saved-log list, and an export picker a test can make
// cancel or fail.
type fakeSessionLogWorkspace struct {
	mu          sync.Mutex
	dir         string
	files       []SessionLogFile
	filesErr    error
	exports     []string
	exportSave  string
	exportErr   error
	canceled    bool
	lastOptions SessionLogOptions
}

func (w *fakeSessionLogWorkspace) SessionLogDefaults() (SessionLogOptions, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return SessionLogOptions{Directory: w.dir, Format: "txt"}, nil
}

func (w *fakeSessionLogWorkspace) SessionLogFiles() ([]SessionLogFile, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.filesErr != nil {
		return nil, w.filesErr
	}
	return append([]SessionLogFile(nil), w.files...), nil
}

func (w *fakeSessionLogWorkspace) ExportSessionLog(path, name string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.exports = append(w.exports, path)
	if w.exportErr != nil {
		return "", w.exportErr
	}
	if w.canceled {
		return "", nil
	}
	return w.exportSave, nil
}

// logHandle is one recording-capable Session. It embeds fakeHandle for the
// terminal plumbing and adds the Session Log seam, finalizing its log when the
// Session ends exactly as the application Session does.
type logHandle struct {
	*fakeHandle

	mu         sync.Mutex
	status     SessionLogStatus
	startCalls int
	stopCalls  int
	startErr   error
	lastOption SessionLogOptions
}

func newLogHandle() *logHandle {
	return &logHandle{fakeHandle: newFakeHandle("remote prompt $ ")}
}

func (h *logHandle) SessionLogStatus() SessionLogStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status
}

func (h *logHandle) StartSessionLog(options SessionLogOptions) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.startCalls++
	h.lastOption = options
	if h.startErr != nil {
		return h.startErr
	}
	h.status = SessionLogStatus{
		Active:  true,
		Format:  options.Format,
		Path:    filepath.Join(options.Directory, options.Label, "2026-11-24T10-00-00.txt"),
		Started: time.Date(2026, 11, 24, 10, 0, 0, 0, time.Local),
	}
	return nil
}

func (h *logHandle) StopSessionLog() SessionLogStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopCalls++
	h.status.Active = false
	return h.status
}

// End finalizes the log before ending the Session, which is the application
// Session's teardown order.
func (h *logHandle) End() error {
	h.StopSessionLog()
	return h.fakeHandle.End()
}

func (h *logHandle) setProblem(problem string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status.Problem = problem
}

func (h *logHandle) failNextStart(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.startErr = err
}

func (h *logHandle) calls() (start, stop int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.startCalls, h.stopCalls
}

// logOpener opens one recording-capable Session and is also the platform
// workspace, exactly as the phone runtime is.
type logOpener struct {
	mu      sync.Mutex
	logs    *fakeSessionLogWorkspace
	handles []*logHandle
}

func (o *logOpener) OpenHost(_ context.Context, _ domainvault.Host) (SessionHandle, error) {
	handle := newLogHandle()
	o.mu.Lock()
	o.handles = append(o.handles, handle)
	o.mu.Unlock()
	return handle, nil
}

func (o *logOpener) SessionLogDefaults() (SessionLogOptions, error) {
	return o.logs.SessionLogDefaults()
}

func (o *logOpener) SessionLogFiles() ([]SessionLogFile, error) {
	return o.logs.SessionLogFiles()
}

func (o *logOpener) ExportSessionLog(path, name string) (string, error) {
	return o.logs.ExportSessionLog(path, name)
}

func (o *logOpener) first() *logHandle {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.handles) == 0 {
		return nil
	}
	return o.handles[0]
}

// sessionLogFixture opens one recording-capable Session under the Tools
// surface.
type sessionLogFixture struct {
	shell  *Shell
	opener *logOpener
	logs   *fakeSessionLogWorkspace
	tester *ui.Tester
}

func newSessionLogFixture(t *testing.T, logs *fakeSessionLogWorkspace) *sessionLogFixture {
	t.Helper()
	host := domainvault.Host{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com", Port: 2222,
		Username: "deploy", Protocol: domainvault.ProtocolSSH,
	}
	opener := &logOpener{logs: logs}
	shell := New(
		WithHosts(&fakeHostStore{hosts: []domainvault.Host{host}}),
		WithSessionOpener(opener),
		WithScreenFactory(fakeScreens),
	)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	shell.Select(Hosts)
	tester := ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	tester.Frame()
	if err := tester.Click("Open prod box"); err != nil {
		t.Fatalf("open the Host: %v; texts: %q", err, tester.Texts())
	}
	fixture := &sessionLogFixture{shell: shell, opener: opener, logs: logs, tester: tester}
	fixture.await(t, "the terminal", func() bool { return tester.HasText("remote prompt $") })
	return fixture
}

func (f *sessionLogFixture) await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; texts: %q", what, f.tester.Texts())
		}
		f.tester.Frame()
		f.tester.Advance(20 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	f.tester.Frame()
}

func (f *sessionLogFixture) openTool(t *testing.T) {
	t.Helper()
	if err := f.tester.Click("Tools for prod box"); err != nil {
		t.Fatalf("open Tools: %v; texts: %q", err, f.tester.Texts())
	}
	f.tester.Frame()
	if err := f.tester.Click("Open Session log"); err != nil {
		t.Fatalf("open Session log: %v; texts: %q", err, f.tester.Texts())
	}
	f.tester.Frame()
}

func (f *sessionLogFixture) click(t *testing.T, label string) {
	t.Helper()
	if err := f.tester.Click(label); err != nil {
		t.Fatalf("click %q: %v; texts: %q", label, err, f.tester.Texts())
	}
	f.tester.Frame()
}

func (f *sessionLogFixture) focusIdentity(t *testing.T) (tabID, paneID string) {
	t.Helper()
	f.shell.sessions.mu.Lock()
	defer f.shell.sessions.mu.Unlock()
	tab := f.shell.sessions.tabs[0]
	return tab.id, string(tab.focusedPane().id)
}

// TestSessionLogStartsAndStopsTheCurrentSession covers the first box: the
// tool starts and stops the focused Session's log and shows the state.
func TestSessionLogStartsAndStopsTheCurrentSession(t *testing.T) {
	logs := &fakeSessionLogWorkspace{dir: t.TempDir()}
	fixture := newSessionLogFixture(t, logs)
	handle := fixture.opener.first()
	fixture.openTool(t)

	if !fixture.tester.HasText("Not recording.") {
		t.Fatalf("a fresh Session is not shown as stopped; texts: %q", fixture.tester.Texts())
	}
	fixture.click(t, "Start logging")
	if start, _ := handle.calls(); start != 1 {
		t.Fatalf("Start logging called the handle %d times, want 1", start)
	}
	if !fixture.tester.HasText("Recording") {
		t.Fatalf("the page does not show the recording state; texts: %q", fixture.tester.Texts())
	}

	// The label names the Session by stable Tab and Pane identity, and the
	// directory is the workspace's Session Logs folder.
	tabID, paneID := fixture.focusIdentity(t)
	handle.mu.Lock()
	options := handle.lastOption
	handle.mu.Unlock()
	if options.Directory != logs.dir {
		t.Fatalf("log directory = %q, want %q", options.Directory, logs.dir)
	}
	if !strings.Contains(options.Label, tabID) || !strings.Contains(options.Label, paneID) ||
		!strings.HasPrefix(options.Label, "prod box") {
		t.Fatalf("log label %q does not carry the Session identity %q/%q", options.Label, tabID, paneID)
	}

	fixture.click(t, "Stop logging")
	if _, stop := handle.calls(); stop != 1 {
		t.Fatalf("Stop logging called the handle %d times, want 1", stop)
	}
	if !fixture.tester.HasText("Not recording.") {
		t.Fatalf("the page does not show the stopped state; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("Start logging") {
		t.Fatalf("a stopped Session cannot be restarted; texts: %q", fixture.tester.Texts())
	}
	if handle.isEnded() {
		t.Fatal("stopping the log ended the Session")
	}
}

// TestSessionLogExportCancelKeepsTheFile covers the export box: a cancelled
// picker reports the cancellation and leaves the saved log listed.
func TestSessionLogExportCancelKeepsTheFile(t *testing.T) {
	logs := &fakeSessionLogWorkspace{
		dir:      t.TempDir(),
		canceled: true,
		files: []SessionLogFile{{
			Path: "/logs/prod-tab-1-pane-1/example.txt", Name: "example.txt",
			Session: "prod-tab-1-pane-1",
		}},
	}
	fixture := newSessionLogFixture(t, logs)
	fixture.openTool(t)

	fixture.click(t, "Export example.txt")
	if !fixture.tester.HasText("Export cancelled. The log is still saved.") {
		t.Fatalf("the cancellation is not reported; texts: %q", fixture.tester.Texts())
	}
	logs.mu.Lock()
	exported := append([]string(nil), logs.exports...)
	files := append([]SessionLogFile(nil), logs.files...)
	logs.mu.Unlock()
	if len(exported) != 1 || exported[0] != "/logs/prod-tab-1-pane-1/example.txt" {
		t.Fatalf("export was asked for %v", exported)
	}
	if len(files) != 1 || files[0].Name != "example.txt" {
		t.Fatalf("a cancelled export removed the saved log: %+v", files)
	}
	if !fixture.tester.HasText("example.txt") {
		t.Fatalf("the saved log left the export entry; texts: %q", fixture.tester.Texts())
	}

	// A successful export names where it went.
	logs.mu.Lock()
	logs.canceled = false
	logs.exportSave = "/exports/example.txt"
	logs.mu.Unlock()
	fixture.click(t, "Export example.txt")
	if !fixture.tester.HasText("Exported example.txt.") {
		t.Fatalf("a successful export is not reported; texts: %q", fixture.tester.Texts())
	}
}

// TestSessionLogWriteFailureIsActionableAndRetryable covers the failure box:
// a write problem is stated inline and Retry starts a fresh log without
// touching the terminal or ending the Session.
func TestSessionLogWriteFailureIsActionableAndRetryable(t *testing.T) {
	t.Run("start failure", func(t *testing.T) {
		logs := &fakeSessionLogWorkspace{dir: t.TempDir()}
		fixture := newSessionLogFixture(t, logs)
		handle := fixture.opener.first()
		handle.failNextStart(errors.New("disk is full"))
		fixture.openTool(t)

		fixture.click(t, "Start logging")
		if !fixture.tester.HasText("Could not start logging: disk is full") {
			t.Fatalf("the start failure is not stated; texts: %q", fixture.tester.Texts())
		}
		if handle.isEnded() {
			t.Fatal("a start failure ended the Session")
		}
	})

	t.Run("write problem and retry", func(t *testing.T) {
		logs := &fakeSessionLogWorkspace{dir: t.TempDir()}
		fixture := newSessionLogFixture(t, logs)
		handle := fixture.opener.first()
		fixture.openTool(t)
		fixture.click(t, "Start logging")

		handle.setProblem("no space left on device")
		fixture.tester.Frame()
		if !fixture.tester.HasText("Logging is not writing: no space left on device. Retry starts a new log; the terminal keeps running.") {
			t.Fatalf("the write problem is not stated; texts: %q", fixture.tester.Texts())
		}
		if !fixture.tester.HasText("Retry logging") {
			t.Fatalf("the failure has no retry action; texts: %q", fixture.tester.Texts())
		}

		fixture.click(t, "Retry logging")
		start, stop := handle.calls()
		if start != 2 || stop < 1 {
			t.Fatalf("retry = start %d / stop %d, want a stop and a fresh start", start, stop)
		}
		if status := handle.SessionLogStatus(); !status.Active || status.Problem != "" {
			t.Fatalf("retry did not start a clean log: %+v", status)
		}
		if !fixture.tester.HasText("Recording") {
			t.Fatalf("the page does not show the retried log; texts: %q", fixture.tester.Texts())
		}
		if handle.isEnded() {
			t.Fatal("the write failure or retry ended the Session")
		}
	})
}

// TestSessionLogFinalizesWhenTheSessionEnds covers the lifecycle box: ending
// the Session flushes and closes its log, and the saved file stays listed.
func TestSessionLogFinalizesWhenTheSessionEnds(t *testing.T) {
	logs := &fakeSessionLogWorkspace{dir: t.TempDir()}
	fixture := newSessionLogFixture(t, logs)
	handle := fixture.opener.first()
	fixture.openTool(t)
	fixture.click(t, "Start logging")
	if !handle.SessionLogStatus().Active {
		t.Fatal("the log did not start")
	}
	path := handle.SessionLogStatus().Path
	logs.mu.Lock()
	logs.files = []SessionLogFile{{
		Path: path, Name: filepath.Base(path), Session: "prod box tab-1-pane-1",
	}}
	logs.mu.Unlock()

	fixture.click(t, "Close session prod box")
	fixture.click(t, "Close")
	fixture.tester.Advance(600 * time.Millisecond)
	fixture.tester.Frame()

	if !handle.isEnded() {
		t.Fatal("the Session did not end")
	}
	if _, stop := handle.calls(); stop < 1 {
		t.Fatal("ending the Session did not finalize its log")
	}
	if handle.SessionLogStatus().Active {
		t.Fatal("the log still reports active after the Session ended")
	}
}

// TestSessionLogUnavailableStillOpensTheSurface checks the failure path: with
// no workspace wired the tool says why and the terminal is untouched.
func TestSessionLogUnavailableStillOpensTheSurface(t *testing.T) {
	fixture := newPhoneFixtureWith(t, phoneHosts(), &fakeOpener{}, WithScreenFactory(fakeScreens))
	fixture.openSession(t, "prod box")
	fixture.openToolsPanel(t, "prod box")
	if !fixture.tester.HasText("Unavailable") {
		t.Fatalf("the catalog does not mark Session log unavailable; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Open Session log"); err != nil {
		t.Fatalf("open Session log: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("Session logs are not available in this build.") {
		t.Fatalf("the reason is missing; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("All tools") {
		t.Fatalf("the surface cannot return to its catalog; texts: %q", fixture.tester.Texts())
	}
}
