package sessionui

import (
	"bytes"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// openArchiveLink is MyGo's Options.OpenLink callback for printed file paths.
// A Command+click over an archive name fills its extraction command at the
// line editor without submitting it; every other link, including URLs, falls
// through to MyGo's own handling.
//
// MyGo checks the link before it offers the press to a program that takes the
// mouse. The fork in third_party/mygo suppresses the link while tracking is
// on, so the click reaches the program as a mouse report; the reporting state
// is checked here as well for a press that was already pending when tracking
// started. The click leaves the line editor alone in either case.
//
// The command is inserted at the line editor's cursor, like typed text, and
// does not clear what the user already typed: this path has no reliable erase
// seam, and clearing input from a click would lose a draft. MyGo's own
// affordance is Command (Control elsewhere); the tests drive that same
// modifier rather than inventing a Netcatty-only gesture.
func (s *Surface) openArchiveLink(link string) bool {
	command, ok := domainterminal.ArchiveExtractCommand(link)
	if !ok {
		return false
	}
	// The archive is recognized, so it is handled even when it cannot be
	// filled: an archive path must never fall through to the system opener.
	if s.session == nil || s.terminal == nil || s.session.Snapshot().Command.Running ||
		mouseReportingEnabled(s.terminal.Snapshot()) {
		return true
	}
	_ = s.InsertText(command)
	return true
}

// openPaneLink is the Pane's link hook. The Pane resolves text links itself,
// because it draws the grid and owns the pointer mapping, so it says whether a
// link is a printed path. An archive path fills its extraction command; a URL
// is left to the Pane, which opens it in the browser, and a path that is not an
// archive opens nothing, as MyGo leaves declined paths to the app.
func (s *Surface) openPaneLink(link string, path bool) bool {
	if !path {
		return false
	}
	return s.openArchiveLink(link)
}

// mouseTrackingModes are the DEC private modes that make a program take the
// pointer: X10, normal, highlight, button-event, and any-event reporting. The
// encoding modes (1005, 1006, 1015, 1016) only change how a report is written.
var mouseTrackingModes = []string{"9", "1000", "1001", "1002", "1003"}

// mouseReportingEnabled reads the last DEC private-mode state from a terminal
// snapshot. It mirrors bracketedPasteEnabled: the emulator's own VT replay is
// the source of truth, not a second parser over the transport.
func mouseReportingEnabled(snapshot []byte) bool {
	states := make(map[string]bool, len(mouseTrackingModes))
	for index := 0; index+3 < len(snapshot); index++ {
		if snapshot[index] != 0x1b || snapshot[index+1] != '[' || snapshot[index+2] != '?' {
			continue
		}
		end := index + 3
		for end < len(snapshot) && snapshot[end] >= 0x30 && snapshot[end] <= 0x3f {
			end++
		}
		if end >= len(snapshot) || (snapshot[end] != 'h' && snapshot[end] != 'l') {
			continue
		}
		enabled := snapshot[end] == 'h'
		for _, parameter := range bytes.Split(snapshot[index+3:end], []byte(";")) {
			for _, mode := range mouseTrackingModes {
				if string(parameter) == mode {
					states[mode] = enabled
				}
			}
		}
		index = end
	}
	for _, mode := range mouseTrackingModes {
		if states[mode] {
			return true
		}
	}
	return false
}
