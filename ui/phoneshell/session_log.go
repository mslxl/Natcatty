package phoneshell

// The Session Log tool of the Tools surface. It starts and stops the current
// Session's log through the Session handle, then lists and exports every log
// the app has written under Documents/Session Logs.
//
// The log itself is the shared application Session log: StartLogging installs
// the logger before the Session's read loop moves transport output, so the file
// is fed by exactly the stream the terminal already reads. The phone adds no
// second reader and never proxies bytes through the UI.
//
// Where the files live and how they leave the device are platform facts, so
// they come from a SessionLogWorkspace the Session opener implements. The
// surface still opens and explains itself when that seam is absent, exactly as
// the other phone tools do.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// SessionLogOptions is what starting one Session log needs. Directory is the
// Session Logs root; the shared logger appends a safe Session label and a
// local timestamp. Label distinguishes the Session and carries its stable Tab
// and Pane identity.
type SessionLogOptions struct {
	Directory  string
	Format     domainterminal.LogFormat
	Timestamps bool
	Label      string
}

// SessionLogStatus is the logger state the Tools surface renders. Problem is a
// write or finalize failure that the user can act on; it never replaces the
// terminal.
type SessionLogStatus struct {
	Active  bool
	Format  domainterminal.LogFormat
	Path    string
	Started time.Time
	Problem string
}

// SessionLogFile is one saved log the export entry lists. Session is the
// logger's safe label directory, which distinguishes one Session's logs from
// another's; Name is the timestamped file.
type SessionLogFile struct {
	Path     string
	Name     string
	Session  string
	Modified time.Time
	Size     int64
}

// SessionLogHandle is implemented by a Session handle that can record its
// output. The application Session is the only writer: the handle maps these
// calls onto the shared logger, so the phone never reads the transport itself.
type SessionLogHandle interface {
	// SessionLogStatus is the current log state, including a write problem.
	SessionLogStatus() SessionLogStatus
	// StartSessionLog starts a log, replacing any previous one.
	StartSessionLog(options SessionLogOptions) error
	// StopSessionLog flushes and closes the log, returning its final state.
	StopSessionLog() SessionLogStatus
}

// SessionLogWorkspace is implemented by the Session opener. It supplies the
// platform's Session Logs directory, the saved-log list, and the system export
// path. A cancelled export reports an empty destination and keeps the file.
type SessionLogWorkspace interface {
	SessionLogDefaults() (SessionLogOptions, error)
	SessionLogFiles() ([]SessionLogFile, error)
	ExportSessionLog(path, name string) (string, error)
}

// sessionLogViewState is one Pane's Session Log page state. Only the saved-log
// list and the last outcome live here; the recording state is read from the
// Session handle each frame so an end or a write failure is never stale.
type sessionLogViewState struct {
	loaded        bool
	files         []SessionLogFile
	loadProblem   string
	result        string
	resultProblem bool
}

// sessionLogWorkspace returns the opener's Session Log platform seam.
func (t *sessionTabs) sessionLogWorkspace() (SessionLogWorkspace, bool) {
	if t == nil || t.opener == nil {
		return nil, false
	}
	workspace, ok := t.opener.(SessionLogWorkspace)
	return workspace, ok
}

// sessionLogHandle returns the focused Pane's Session Log handle. The Pane is
// addressed by the Tab and focus identity the frame drew, so a surface left
// over from another Pane can never record the wrong Session.
func (t *sessionTabs) sessionLogHandle(view tabView) SessionLogHandle {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tab := t.find(view.id)
	if tab == nil {
		return nil
	}
	for _, pane := range tab.panes {
		if string(pane.id) != view.focus {
			continue
		}
		handle, _ := pane.handle.(SessionLogHandle)
		return handle
	}
	return nil
}

// sessionLogLabel names one Session's log directory. The stable Tab and Pane
// identity is what tells two Sessions on the same Host apart; the shared logger
// sanitizes every unsafe character and adds the start timestamp to the file.
func sessionLogLabel(view tabView) string {
	host := strings.TrimSpace(hostName(view.host))
	identity := strings.TrimSpace(view.id) + "-" + strings.TrimSpace(view.focus)
	if host == "" {
		return identity
	}
	return host + " " + identity
}

// loadSessionLogs refreshes the saved-log list behind the export entry.
func (t *sessionTabs) loadSessionLogs(st *toolsState) {
	if st == nil {
		return
	}
	st.sessionLog.loaded = true
	st.sessionLog.files = nil
	st.sessionLog.loadProblem = ""
	workspace, ok := t.sessionLogWorkspace()
	if !ok {
		st.sessionLog.loadProblem = "Session logs are not available in this build."
		return
	}
	files, err := workspace.SessionLogFiles()
	if err != nil {
		st.sessionLog.loadProblem = "Saved logs are unavailable: " + err.Error()
		return
	}
	st.sessionLog.files = files
}

// startSessionLog starts recording the focused Pane's Session.
func (t *sessionTabs) startSessionLog(st *toolsState, view tabView) {
	if st == nil {
		return
	}
	workspace, ok := t.sessionLogWorkspace()
	if !ok {
		st.sessionLog.result = "Session logs are not available in this build."
		st.sessionLog.resultProblem = true
		return
	}
	handle := t.sessionLogHandle(view)
	if handle == nil {
		st.sessionLog.result = "This Session is not recording-capable."
		st.sessionLog.resultProblem = true
		return
	}
	options, err := workspace.SessionLogDefaults()
	if err != nil {
		st.sessionLog.result = "Could not prepare the Session Logs folder: " + err.Error()
		st.sessionLog.resultProblem = true
		return
	}
	options.Label = sessionLogLabel(view)
	if err := handle.StartSessionLog(options); err != nil {
		st.sessionLog.result = "Could not start logging: " + err.Error()
		st.sessionLog.resultProblem = true
		return
	}
	st.sessionLog.result = "Recording this Session."
	st.sessionLog.resultProblem = false
	t.loadSessionLogs(st)
}

// stopSessionLog finalizes the focused Pane's log. It never tears down the
// Session or detaches the terminal.
func (t *sessionTabs) stopSessionLog(st *toolsState, view tabView) {
	if st == nil {
		return
	}
	handle := t.sessionLogHandle(view)
	if handle == nil {
		st.sessionLog.result = "This Session has no log to stop."
		st.sessionLog.resultProblem = true
		return
	}
	status := handle.StopSessionLog()
	if status.Problem != "" {
		st.sessionLog.result = "Log saved, but finalizing it failed: " + status.Problem
		st.sessionLog.resultProblem = true
	} else if status.Path != "" {
		st.sessionLog.result = "Log saved as " + filepath.Base(status.Path) + "."
		st.sessionLog.resultProblem = false
	} else {
		st.sessionLog.result = "Logging stopped."
		st.sessionLog.resultProblem = false
	}
	t.loadSessionLogs(st)
}

// retrySessionLog replaces a log that hit a write problem. The old file stays
// on disk; the new log starts fresh so no partial snapshot is ever presented as
// the whole record.
func (t *sessionTabs) retrySessionLog(st *toolsState, view tabView) {
	if handle := t.sessionLogHandle(view); handle != nil {
		_ = handle.StopSessionLog()
	}
	t.startSessionLog(st, view)
}

// exportSessionLog copies one log through the platform's export path. An empty
// destination means the user cancelled; the log is untouched either way.
func (t *sessionTabs) exportSessionLog(st *toolsState, path, name string) {
	if st == nil {
		return
	}
	workspace, ok := t.sessionLogWorkspace()
	if !ok {
		st.sessionLog.result = "Session logs are not available in this build."
		st.sessionLog.resultProblem = true
		return
	}
	saved, err := workspace.ExportSessionLog(path, name)
	switch {
	case err != nil:
		st.sessionLog.result = "Could not export the log: " + err.Error()
		st.sessionLog.resultProblem = true
	case saved == "":
		// A cancelled picker never deletes the log: it remains in the list.
		st.sessionLog.result = "Export cancelled. The log is still saved."
		st.sessionLog.resultProblem = false
	default:
		st.sessionLog.result = "Exported " + filepath.Base(saved) + "."
		st.sessionLog.resultProblem = false
	}
	t.loadSessionLogs(st)
}

// drawToolSessionLog renders the Session Log tool: the live recording state,
// start/stop and retry, and every saved log with an export action.
func (t *sessionTabs) drawToolSessionLog(c *ui.Context, st *toolsState, view tabView) {
	if kit.GhostButton(c, icons.ChevronLeft, "All tools").Clicked() {
		st.tool = toolNone
		return
	}
	if _, ok := t.sessionLogWorkspace(); !ok {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
			"Session logs are not available in this build.", nil)
		return
	}
	if !st.sessionLog.loaded {
		t.loadSessionLogs(st)
	}
	handle := t.sessionLogHandle(view)
	var status SessionLogStatus
	if handle != nil {
		status = handle.SessionLogStatus()
	}

	kit.SectionBar(c, "Session log · "+hostName(view.host), nil)
	switch {
	case handle == nil:
		ui.Text(c, "This Session is no longer running. Saved logs can still be exported.").
			FontSize(12).TextColor(c.Theme().TextMuted)
	case status.Active:
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Recording").FontSize(13).Bold()
			if status.Path != "" {
				ui.Text(c, filepath.Base(status.Path)).FontSize(12).
					TextColor(c.Theme().TextMuted).SingleLine()
			}
		})
		if !status.Started.IsZero() {
			ui.Text(c, "Started "+status.Started.Format("2006-01-02 15:04:05")).
				FontSize(12).TextColor(c.Theme().TextMuted)
		}
	default:
		ui.Text(c, "Not recording.").FontSize(13).TextColor(c.Theme().TextMuted)
		if status.Path != "" {
			ui.Text(c, "Last log: "+filepath.Base(status.Path)).FontSize(12).
				TextColor(c.Theme().TextMuted).SingleLine()
		}
	}

	ui.Row(c).Gap(8).Wrap().Children(func() {
		if handle != nil {
			if status.Active {
				if kit.GhostButton(c, icons.Square, "Stop logging").Clicked() {
					t.stopSessionLog(st, view)
				}
			} else if kit.GhostButton(c, icons.Play, "Start logging").Clicked() {
				t.startSessionLog(st, view)
			}
			if status.Problem != "" {
				if kit.GhostButton(c, icons.RefreshCw, "Retry logging").Clicked() {
					t.retrySessionLog(st, view)
				}
			}
			if status.Path != "" {
				if kit.GhostButton(c, icons.Download, "Export current log").Clicked() {
					t.exportSessionLog(st, status.Path, filepath.Base(status.Path))
				}
			}
		}
		if kit.GhostButton(c, icons.RefreshCw, "Refresh logs").Clicked() {
			t.loadSessionLogs(st)
		}
	})

	if status.Problem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
			"Logging is not writing: "+status.Problem+
				". Retry starts a new log; the terminal keeps running.", nil)
	}
	if st.sessionLog.result != "" {
		tone := c.Theme().Success
		icon := icons.CircleCheck
		if st.sessionLog.resultProblem {
			tone, icon = c.Theme().Danger, icons.TriangleAlert
		}
		kit.Notice(c, icon, tone, st.sessionLog.result, nil)
	}
	if st.sessionLog.loadProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, st.sessionLog.loadProblem, nil)
	}

	ui.Text(c, sessionLogCount(len(st.sessionLog.files))).FontSize(12).
		TextColor(c.Theme().TextMuted)
	if len(st.sessionLog.files) == 0 {
		if st.sessionLog.loadProblem == "" {
			kit.EmptyHint(c, "No logs saved yet.")
		}
		return
	}
	for _, file := range st.sessionLog.files {
		file := file
		row := kit.ListRow(c)
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, file.Name).FontSize(13).SingleLine()
				ui.Text(c, sessionLogFileDetail(file)).FontSize(12).
					TextColor(c.Theme().TextMuted).SingleLine()
			})
			if kit.IconButton(c, icons.Download, "Export "+file.Name).Clicked() {
				t.exportSessionLog(st, file.Path, file.Name)
			}
		})
	}
}

// sessionLogCount is the saved-log list's caption.
func sessionLogCount(count int) string {
	switch count {
	case 0:
		return "No saved logs"
	case 1:
		return "1 saved log"
	default:
		return fmt.Sprintf("%d saved logs", count)
	}
}

// sessionLogFileDetail is one saved-log row's second line.
func sessionLogFileDetail(file SessionLogFile) string {
	detail := file.Session
	if detail == "" {
		detail = "Session log"
	}
	if !file.Modified.IsZero() {
		detail += " · " + file.Modified.Local().Format("2006-01-02 15:04:05")
	}
	if file.Size > 0 {
		detail += fmt.Sprintf(" · %d B", file.Size)
	}
	return detail
}
