//go:build selftest

package app

// Ticket 15 self-test.
//
// Press F11, then 1, then 5. The check runs on the UI thread one step per
// frame and writes %TEMP%\natcatty-selftest-15.json.
//
// Three steps open real local shell Sessions through the production Session
// service and local PTY adapter, one per log format. Two steps use a pipe
// transport as the injectable input seam, feeding the real terminal tap: the
// first proves OSC 7/52/99/133 and title state plus log redaction; the second
// proves ZMODEM/YMODEM gating, dropped input, and the Ctrl+C abort. A layout
// step renders the production Session chrome at 1100x720 and 720x480 through
// the selftest-only renderer seam. The last step opens a real Tab so the
// running window shows the new chrome for visual inspection.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
)

type selftest15 struct {
	state *state.State

	prepared bool
	logs     [3]selftest15Log
	osc      *selftest15FakeSession
	transfer *selftest15FakeSession
	demoTab  model.TabID
}

type selftest15Log struct {
	format   domainterminal.LogFormat
	label    string
	marker   string
	session  *appsession.Session
	deadline time.Time

	readMu     sync.Mutex
	output     strings.Builder
	markerSeen bool
}

var selftest15Formats = []domainterminal.LogFormat{
	domainterminal.LogFormatRaw,
	domainterminal.LogFormatText,
	domainterminal.LogFormatHTML,
}

func (t *selftest15) check() selftest.Check {
	return selftest.Check{
		Ticket: 15,
		Name:   "Terminal logging, command tracking, and OSC handling",
		Steps: []selftest.Step{
			t.loggingStep(0),
			t.loggingStep(1),
			t.loggingStep(2),
			{Name: "osc-state-and-log-redaction", Run: t.oscStep},
			{Name: "zmodem-ymodem-gate", Run: t.transferStep},
			{Name: "chrome-layout", Run: t.layoutStep},
			{Name: "real-session-chrome", Run: t.demoStep},
		},
	}
}

func (t *selftest15) logRoot() string {
	return filepath.Join(os.TempDir(), "natcatty-selftest-15-logs")
}

// resetRunState removes files and Sessions from a previous F11,1,5 run. It is
// called at the start of the first step, so repeated runs see the same initial
// conditions.
func (t *selftest15) resetRunState() {
	_ = os.RemoveAll(t.logRoot())
	_ = os.MkdirAll(t.logRoot(), 0o755)
	for index := range t.logs {
		if t.logs[index].session != nil {
			_ = t.logs[index].session.Close()
		}
	}
	t.logs = [3]selftest15Log{}
	if t.osc != nil {
		t.osc.close()
		t.osc = nil
	}
	if t.transfer != nil {
		t.transfer.close()
		t.transfer = nil
	}
	if t.state != nil && t.demoTab != "" {
		if index := tabIndex(t.state, t.demoTab); index >= 0 {
			t.state.CloseTab(index)
			t.state.Flush()
		}
	}
	t.demoTab = ""
}

func (t *selftest15) loggingStep(index int) selftest.Step {
	format := selftest15Formats[index]
	return selftest.Step{
		Name: "log-" + string(format),
		Run: func(*ui.Context) selftest.Outcome {
			if !t.prepared {
				t.resetRunState()
				t.prepared = true
			}
			run := &t.logs[index]
			if run.session == nil {
				marker := "NATCATTY_" + strings.ToUpper(string(format)) + "_MARKER"
				if format == domainterminal.LogFormatHTML {
					marker = "<b>&NATCATTY_HTML_MARKER"
				}
				command, err := selftest15EchoCommand(marker)
				if err != nil {
					return selftest.Failed(err)
				}
				folder := t.logRoot()
				service := appsession.NewService(localpty.Factory{},
					appsession.WithAutoLogging(func(spec appsession.StartSpec) *appsession.LogOptions {
						return &appsession.LogOptions{
							Directory:  folder,
							Format:     format,
							Timestamps: true,
							Label:      spec.Label,
						}
					}))
				run.format = format
				run.label = "log-" + string(format)
				run.marker = marker
				run.deadline = time.Now().Add(selftestTimeout)
				session, err := service.Open(context.Background(), appsession.StartSpec{
					Kind:    appsession.KindLocal,
					Label:   run.label,
					Command: command,
				})
				if err != nil {
					return selftest.Failed(fmt.Errorf("open a real shell for the %s log: %w", format, err))
				}
				run.session = session
				go selftest15WatchOutput(run, run.marker)
			}

			run.readMu.Lock()
			seen := run.markerSeen
			run.readMu.Unlock()
			if !seen {
				return pending(run.deadline, "the real "+string(format)+" shell output")
			}

			status := run.session.StopLogging()
			if status.Problem != "" {
				return selftest.Failed(fmt.Errorf("%s log problem: %s", format, status.Problem))
			}
			if status.Path == "" {
				return selftest.Failed(fmt.Errorf("%s log did not report a path", format))
			}
			if err := selftest15VerifyLog(format, run.label, run.marker, status.Path); err != nil {
				return selftest.Failed(err)
			}
			_ = run.session.Close()
			return selftest.Passed(fmt.Sprintf(
				"a real local shell wrote %s to %s", string(format), status.Path), "localpty.Factory")
		},
	}
}

func selftest15EchoCommand(marker string) ([]string, error) {
	if runtime.GOOS == "windows" {
		shell, err := exec.LookPath("pwsh.exe")
		if err != nil {
			shell, err = exec.LookPath("powershell.exe")
		}
		if err != nil {
			return nil, fmt.Errorf("find PowerShell for the logging check: %w", err)
		}
		return []string{shell, "-NoProfile", "-NonInteractive", "-Command",
			"Write-Output '" + marker + "'"}, nil
	}
	return []string{"/bin/sh", "-c", "printf '%s\\n' '" + marker + "'"}, nil
}

// selftest15WatchOutput drains the terminal side until the echo marker
// appears. The local PTY does not report EOF when the child exits, so the
// check waits on the output rather than on Session Done.
func selftest15WatchOutput(run *selftest15Log, marker string) {
	buffer := make([]byte, 4096)
	for {
		n, err := run.session.TerminalConn().Read(buffer)
		if n > 0 {
			run.readMu.Lock()
			run.output.Write(buffer[:n])
			if strings.Contains(run.output.String(), marker) {
				run.markerSeen = true
			}
			run.readMu.Unlock()
		}
		if err != nil {
			return
		}
		run.readMu.Lock()
		seen := run.markerSeen
		run.readMu.Unlock()
		if seen {
			return
		}
	}
}

func selftest15VerifyLog(format domainterminal.LogFormat, label, marker, path string) error {
	extension := ".txt"
	expected := marker
	switch format {
	case domainterminal.LogFormatRaw:
		extension = ".log"
	case domainterminal.LogFormatHTML:
		extension = ".html"
		expected = "&lt;b&gt;&amp;NATCATTY_HTML_MARKER"
	}
	if filepath.Ext(path) != extension {
		return fmt.Errorf("%s log extension = %q, want %q", format, filepath.Ext(path), extension)
	}
	if filepath.Base(filepath.Dir(path)) != label {
		return fmt.Errorf("%s log folder = %q, want %q", format, filepath.Base(filepath.Dir(path)), label)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s log: %w", format, err)
	}
	text := string(data)
	if !strings.Contains(text, expected) {
		return fmt.Errorf("%s log does not contain %q: %q", format, expected, text)
	}
	if format == domainterminal.LogFormatHTML {
		if !strings.Contains(text, "<title>Session Log - "+label+"</title>") ||
			!strings.Contains(text, "Host: "+label) ||
			!strings.Contains(text, "Date: ") {
			return fmt.Errorf("HTML log lacks its Host/Date wrapper: %q", text[:min(len(text), 400)])
		}
	}
	if format == domainterminal.LogFormatText || format == domainterminal.LogFormatHTML {
		stamp := regexp.MustCompile(`\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}\] `)
		if !stamp.MatchString(text) {
			return fmt.Errorf("%s log has no timestamp prefix: %q", format, text)
		}
	}
	return nil
}

type selftest15FakeSession struct {
	service   *appsession.Service
	transport *selftest15Transport
	session   *appsession.Session

	secret          string
	notifications   *[]appsession.TerminalNotification
	notificationsMu *sync.Mutex

	transfers   *[]appsession.TransferState
	transfersMu *sync.Mutex
	stageSent   bool
	beforeRead  bool
	afterRead   bool
	ymodemSent  bool
	cancelled   bool
	promptRead  bool
}

func (f *selftest15FakeSession) close() {
	if f.session != nil {
		_ = f.session.Close()
		select {
		case <-f.session.Done():
		case <-time.After(2 * time.Second):
		}
	}
	f.transport.close()
}

type selftest15Factory struct {
	transport *selftest15Transport
}

func (f *selftest15Factory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

type selftest15Transport struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	once   sync.Once

	mu    sync.Mutex
	input bytes.Buffer
}

func newSelftest15Transport() *selftest15Transport {
	reader, writer := io.Pipe()
	return &selftest15Transport{reader: reader, writer: writer}
}

func (t *selftest15Transport) Read(p []byte) (int, error) { return t.reader.Read(p) }
func (t *selftest15Transport) Resize(int, int) error      { return nil }
func (t *selftest15Transport) close() {
	t.once.Do(func() {
		_ = t.writer.Close()
		_ = t.reader.Close()
	})
}
func (t *selftest15Transport) Close() error {
	t.close()
	return nil
}

func (t *selftest15Transport) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.input.Write(p)
	t.mu.Unlock()
	return len(p), nil
}

func (t *selftest15Transport) send(value string) {
	_, _ = t.writer.Write([]byte(value))
}

func (t *selftest15Transport) inputText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.input.String()
}

func (t *selftest15) newFake(options ...appsession.Option) (*selftest15FakeSession, error) {
	transport := newSelftest15Transport()
	fake := &selftest15FakeSession{transport: transport}
	options = append(options, appsession.WithClipboardPolicy(func() domainterminal.ClipboardAccess {
		return domainterminal.ClipboardWriteOnly
	}))
	fake.service = appsession.NewService(&selftest15Factory{transport: transport}, options...)
	session, err := fake.service.Open(context.Background(), appsession.StartSpec{
		Kind:  appsession.KindLocal,
		Label: "selftest",
	})
	if err != nil {
		fake.transport.close()
		return nil, err
	}
	fake.session = session
	return fake, nil
}

func (t *selftest15) oscStep(*ui.Context) selftest.Outcome {
	if t.osc == nil {
		var mu sync.Mutex
		var notifications []appsession.TerminalNotification
		fake, err := t.newFake(appsession.WithNotificationHook(
			func(_ model.SessionID, notification appsession.TerminalNotification) {
				mu.Lock()
				notifications = append(notifications, notification)
				mu.Unlock()
			}))
		if err != nil {
			return selftest.Failed(fmt.Errorf("open the OSC session: %w", err))
		}
		t.osc = fake
		t.osc.notifications = &notifications
		t.osc.notificationsMu = &mu
		if err := fake.session.StartLogging(appsession.LogOptions{
			Directory: t.logRoot(),
			Format:    domainterminal.LogFormatRaw,
			Label:     "osc",
		}); err != nil {
			return selftest.Failed(fmt.Errorf("start the OSC log: %w", err))
		}
		if _, err := fake.session.TerminalConn().Write([]byte("echo hi\r")); err != nil {
			return selftest.Failed(err)
		}
		secret := base64.StdEncoding.EncodeToString([]byte("selftest-clipboard-secret"))
		t.osc.secret = secret
		t.osc.transport.send("visible\r\n" +
			"\x1b]7;file://host/tmp/work%20dir\x07" +
			"\x1b]2;Build Window\x07" +
			"\x1b]52;c;" + secret + "\x07" +
			"\x1b]777;notify;Build;Finished\x07" +
			"\x1b]133;D;0\x07done\r\n")
	}

	snapshot := t.osc.session.Snapshot()
	if snapshot.CWD == "" || snapshot.Title == "" || snapshot.Clipboard == nil ||
		snapshot.Notification == nil || !snapshot.Command.HasExitCode {
		return selftest.Pending
	}
	if snapshot.CWD != "/tmp/work dir" {
		return selftest.Failed(fmt.Errorf("OSC 7 cwd = %q", snapshot.CWD))
	}
	if snapshot.Title != "Build Window" {
		return selftest.Failed(fmt.Errorf("OSC 0/2 title = %q", snapshot.Title))
	}
	clipboard := snapshot.Clipboard
	if clipboard.Operation != appsession.ClipboardWrite || !clipboard.Allowed ||
		clipboard.Bytes != len(t.osc.secret) {
		return selftest.Failed(fmt.Errorf("OSC 52 event = %+v", clipboard))
	}
	if strings.Contains(fmt.Sprintf("%+v", snapshot), t.osc.secret) {
		return selftest.Failed(errors.New("the clipboard payload was retained in Session state"))
	}
	notification := snapshot.Notification
	if notification.Title != "Build" || notification.Body != "Finished" {
		return selftest.Failed(fmt.Errorf("OSC notification = %+v", notification))
	}
	t.osc.notificationsMu.Lock()
	received := append([]appsession.TerminalNotification(nil), (*t.osc.notifications)...)
	t.osc.notificationsMu.Unlock()
	if len(received) != 1 || received[0].Title != "Build" || received[0].Body != "Finished" {
		return selftest.Failed(fmt.Errorf("notification hook = %+v", received))
	}
	if snapshot.Command.Running || snapshot.Command.ExitCode != 0 ||
		!snapshot.Busy {
		return selftest.Failed(fmt.Errorf("OSC 133 command/activity = %+v busy=%v",
			snapshot.Command, snapshot.Busy))
	}

	output, err := selftest15ReadOutput(t.osc.session, 2*time.Second)
	if err != nil {
		return selftest.Failed(err)
	}
	if !strings.Contains(output, "visible") || !strings.Contains(output, "done") {
		return selftest.Failed(fmt.Errorf("screen output = %q", output))
	}
	if strings.Contains(output, "notify") || strings.Contains(output, "Finished") {
		return selftest.Failed(fmt.Errorf("app-owned notification reached the terminal: %q", output))
	}

	status := t.osc.session.StopLogging()
	logged, err := os.ReadFile(status.Path)
	if err != nil {
		return selftest.Failed(fmt.Errorf("read the OSC log: %w", err))
	}
	if strings.Contains(string(logged), t.osc.secret) {
		return selftest.Failed(errors.New("the OSC 52 payload reached the Session log"))
	}
	if !strings.Contains(string(logged), "visible") || !strings.Contains(string(logged), "done") {
		return selftest.Failed(fmt.Errorf("OSC log = %q", string(logged)))
	}
	if strings.Contains(string(logged), "Finished") {
		return selftest.Failed(errors.New("an app-owned notification reached the Session log"))
	}
	return selftest.Passed(
		"OSC 7 cwd, OSC 0/2 title, OSC 52 policy, OSC 777 notification, OSC 133 exit, and activity reached the snapshot; the raw log kept screen text but no clipboard payload or app-owned notification",
		"pipe transport")
}

func (t *selftest15) transferStep(*ui.Context) selftest.Outcome {
	if t.transfer == nil {
		var mu sync.Mutex
		var transfers []appsession.TransferState
		fake, err := t.newFake(appsession.WithTransferHook(
			func(_ model.SessionID, transfer appsession.TransferState) {
				mu.Lock()
				transfers = append(transfers, transfer)
				mu.Unlock()
			}))
		if err != nil {
			return selftest.Failed(fmt.Errorf("open the transfer session: %w", err))
		}
		t.transfer = fake
		t.transfer.transfers = &transfers
		t.transfer.transfersMu = &mu
	}

	transport := t.transfer.transport
	if !t.transfer.stageSent {
		transport.send("BEFORE**\x18Bzmodem-payload")
		t.transfer.stageSent = true
	}
	snapshot := t.transfer.session.Snapshot()
	if !t.transfer.beforeRead {
		if !snapshot.Transfer.Active || snapshot.Transfer.Kind != appsession.TransferZMODEM {
			return selftest.Pending
		}
		if !snapshot.Busy {
			return selftest.Failed(errors.New("the active transfer did not keep the activity indicator lit"))
		}
		output, err := selftest15ReadOutput(t.transfer.session, 2*time.Second)
		if err != nil {
			return selftest.Failed(err)
		}
		if output != "BEFORE" {
			return selftest.Failed(fmt.Errorf("ZMODEM protocol bytes reached the renderer: %q", output))
		}
		t.transfer.beforeRead = true
		transport.send("OOAFTER")
	}
	snapshot = t.transfer.session.Snapshot()
	if !t.transfer.afterRead {
		if snapshot.Transfer.Active || snapshot.Transfer.End != appsession.TransferEndCompleted {
			return selftest.Pending
		}
		output, err := selftest15ReadOutput(t.transfer.session, 2*time.Second)
		if err != nil {
			return selftest.Failed(err)
		}
		if output != "AFTER" {
			return selftest.Failed(fmt.Errorf("completion output = %q", output))
		}
		t.transfer.afterRead = true
		transport.send("\x01\x00\xffymodem-payload")
	}
	snapshot = t.transfer.session.Snapshot()
	if !t.transfer.ymodemSent {
		if !snapshot.Transfer.Active || snapshot.Transfer.Kind != appsession.TransferYMODEM {
			return selftest.Pending
		}
		if _, err := t.transfer.session.TerminalConn().Write([]byte("ls\n")); err != nil {
			return selftest.Failed(err)
		}
		if got := transport.inputText(); got != "" {
			return selftest.Failed(fmt.Errorf("normal input passed the transfer gate: %q", got))
		}
		if _, err := t.transfer.session.TerminalConn().Write([]byte{0x03}); err != nil {
			return selftest.Failed(err)
		}
		t.transfer.ymodemSent = true
	}
	snapshot = t.transfer.session.Snapshot()
	if !t.transfer.cancelled {
		want := string([]byte{0x18, 0x18, 0x18, 0x18, 0x18})
		if got := transport.inputText(); got != want {
			if got != "" {
				return selftest.Failed(fmt.Errorf("Ctrl+C abort bytes = %q, want five CAN", got))
			}
			return selftest.Pending
		}
		if snapshot.Transfer.Active || snapshot.Transfer.End != appsession.TransferEndCancelled {
			return selftest.Pending
		}
		t.transfer.cancelled = true
		transport.send("prompt")
	}
	if !t.transfer.promptRead {
		output, err := selftest15ReadOutput(t.transfer.session, 2*time.Second)
		if err != nil {
			return selftest.Failed(err)
		}
		if output != "prompt" {
			return selftest.Failed(fmt.Errorf("post-transfer renderer output = %q", output))
		}
		t.transfer.promptRead = true
	}
	t.transfer.transfersMu.Lock()
	events := len(*t.transfer.transfers)
	t.transfer.transfersMu.Unlock()
	if events < 4 {
		return selftest.Failed(fmt.Errorf("transfer hook events = %d, want 4", events))
	}
	return selftest.Passed(
		"ZMODEM and YMODEM starts, completions, and the Ctrl+C abort were detected; protocol bytes never reached the renderer and normal input stayed out of the transport",
		"pipe transport")
}

func (t *selftest15) layoutStep(*ui.Context) selftest.Outcome {
	snapshot := appsession.Snapshot{
		Logging: appsession.LogStatus{
			Active: true,
			Format: domainterminal.LogFormatText,
			Path:   filepath.Join(t.logRoot(), "host", "2026-10-20T10-00-00.txt"),
		},
		Command: appsession.CommandState{Text: "go test ./...", Running: true},
		Busy:    true,
		Transfer: appsession.TransferState{
			Active: true,
			Kind:   appsession.TransferZMODEM,
		},
	}
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		tester := ui.NewTester(func(c *ui.Context) {
			sessionui.RenderSelftestChrome(c, snapshot, true, t.logRoot())
		}, size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()
		for _, text := range []string{
			m.SessionActivityBusy(),
			m.SessionCommandRunning("go test ./..."),
			m.SessionMenuLabel(),
			m.SessionTransferActive("ZMODEM"),
			m.SessionTransferCancel(),
			m.SessionClipboardRequest(),
			m.SessionClipboardAllow(),
			m.SessionClipboardDeny(),
		} {
			rect, ok := tester.Find(text)
			if !ok {
				return selftest.Failed(fmt.Errorf("%dx%d lacks %q", size[0], size[1], text))
			}
			if rect.X < 0 || rect.Y < 0 ||
				rect.X+rect.W > float32(size[0]) || rect.Y+rect.H > float32(size[1]) {
				return selftest.Failed(fmt.Errorf("%dx%d clips %q: %+v", size[0], size[1], text, rect))
			}
		}
		// Logging moved from the status bar into the Session menu so a narrow
		// split Pane can never clip its controls. The menu must still expose it.
		if err := tester.Click(m.SessionMenuLabel()); err != nil {
			return selftest.Failed(fmt.Errorf("%dx%d open the Session menu: %w", size[0], size[1], err))
		}
		found := false
		for _, item := range tester.Menu() {
			if item == m.SessionMenuLogging() {
				found = true
			}
		}
		if !found {
			return selftest.Failed(fmt.Errorf("%dx%d Logging is not in the Session menu: %v",
				size[0], size[1], tester.Menu()))
		}
		tester.CloseMenu()
	}
	return selftest.Passed(
		"the production chrome rendered command, activity, the Session menu with Logging, transfer, and clipboard controls inside 1100x720 and 720x480",
		"sessionui.RenderSelftestChrome")
}

func (t *selftest15) demoStep(*ui.Context) selftest.Outcome {
	if t.state == nil {
		return selftest.Failed(errors.New("the shell is not wired"))
	}
	if t.demoTab == "" {
		if err := t.state.NewTab(); err != nil {
			return selftest.Failed(fmt.Errorf("open a Session tab: %w", err))
		}
		index := t.state.ActiveTab()
		id, ok := t.state.TabID(index)
		if !ok {
			return selftest.Failed(errors.New("the new Tab has no identity"))
		}
		t.demoTab = id
		t.state.Flush()
	}
	t.state.Flush()
	if mainSurface(t.state, t.demoTab) == nil {
		return selftest.Pending
	}
	// The run is complete; the next F11,1,5 starts from a clean state.
	t.prepared = false
	return selftest.Passed(
		"a real local Session Tab now shows the logging, command, and activity chrome", "shell State")
}

func selftest15ReadOutput(session *appsession.Session, timeout time.Duration) (string, error) {
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		buffer := make([]byte, 64<<10)
		n, err := session.TerminalConn().Read(buffer)
		if err != nil {
			done <- result{err: err}
			return
		}
		done <- result{text: string(buffer[:n])}
	}()
	select {
	case got := <-done:
		return got.text, got.err
	case <-time.After(timeout):
		return "", errors.New("timed out reading terminal output")
	}
}
