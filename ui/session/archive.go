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
// mouse, so the reporting state is checked here as well: while a program has
// mouse tracking on, the click leaves the line editor alone. The press itself
// is still consumed by MyGo's link layer before the program, which is why the
// ticket records that part as a widget limitation.
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

// mouseTrackingModes are the DEC private modes that make a program take the
// pointer: X10, normal, button-event, and any-event reporting. The encoding
// modes (1005, 1006, 1015, 1016) only change how a report is written.
var mouseTrackingModes = []string{"9", "1000", "1002", "1003"}

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
