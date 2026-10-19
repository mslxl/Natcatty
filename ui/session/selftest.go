//go:build selftest

package sessionui

import (
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
)

// SelftestInteractionModel is the terminal-interaction chrome state the
// ticket 17 self-test renders: encoding, logging, the paste confirmation, and
// drop feedback.
type SelftestInteractionModel struct {
	Command          appsession.CommandState
	Busy             bool
	Transfer         appsession.TransferState
	Logging          appsession.LogStatus
	ClipboardPending bool
	LogFormat        domainterminal.LogFormat
	LogDirectory     string
	LogProblem       bool
	Encoding         domainterminal.Encoding
	PendingPaste     *PendingPaste
	DropActive       bool
	DropCount        int
	DropAction       domainterminal.DropAction
	DropNotice       DropNotice
}

// SelftestInteractionActions are the production menu and banner callbacks. The
// self-test supplies recording functions and drives the same controls a user
// clicks.
type SelftestInteractionActions struct {
	ToggleLog    func()
	SetLogFormat func(domainterminal.LogFormat)
	SetEncoding  func(domainterminal.Encoding)
	Paste        func()
	ClearBuffer  func()
	AcceptPaste  func()
	CancelPaste  func()
	DismissDrop  func()
	SplitRight   func()
	SplitDown    func()
	Detach       func()
	ClosePane    func()
}

// PrepareSelftestInteraction puts a real Session surface into the state the
// ticket 17 demonstration shows: a guarded three-line paste waiting for Send
// and a completed two-file drop, with files over the terminal. The next run
// replaces it.
func (s *Surface) PrepareSelftestInteraction() {
	text := "echo first\necho second\necho third"
	preview, truncated := domainterminal.PreviewPaste(text, 320)
	s.gate.mu.Lock()
	s.gate.holder = &PendingPaste{
		Text:      text,
		Lines:     domainterminal.PasteLineCount(text),
		Chars:     len([]rune(text)),
		Preview:   preview,
		Truncated: truncated,
	}
	s.gate.mu.Unlock()
	s.dropOver = true
	s.drops.mu.Lock()
	s.drops.notice = DropNoticeInserted
	s.drops.count = 2
	s.drops.mu.Unlock()
	s.redraw()
}

// RenderSelftestInteraction renders the production Session chrome with every
// ticket 17 control. It is built only under the selftest tag.
func RenderSelftestInteraction(c *ui.Context, model SelftestInteractionModel,
	actions SelftestInteractionActions) {
	format := model.LogFormat
	if !validLogFormat(format) {
		format = domainterminal.LogFormatText
	}
	renderChrome(c, chromeModel{
		Command:          model.Command,
		Busy:             model.Busy,
		Transfer:         model.Transfer,
		Logging:          model.Logging,
		ClipboardPending: model.ClipboardPending,
		LogFormat:        format,
		LogDirectory:     model.LogDirectory,
		LogProblem:       model.LogProblem,
		Encoding:         model.Encoding,
		PendingPaste:     model.PendingPaste,
		DropActive:       model.DropActive,
		DropCount:        model.DropCount,
		DropAction:       model.DropAction,
		DropNotice:       model.DropNotice,
	}, locales.In(c.Locale()), c.Theme(), chromeActions{
		ToggleLog:    actions.ToggleLog,
		SetLogFormat: actions.SetLogFormat,
		SetEncoding:  actions.SetEncoding,
		Paste:        actions.Paste,
		ClearBuffer:  actions.ClearBuffer,
		AcceptPaste:  actions.AcceptPaste,
		CancelPaste:  actions.CancelPaste,
		DismissDrop:  actions.DismissDrop,
		SplitRight:   actions.SplitRight,
		SplitDown:    actions.SplitDown,
		Detach:       actions.Detach,
		ClosePane:    actions.ClosePane,
	})
}

// RenderSelftestChrome renders the production Session chrome from a synthetic
// snapshot. The self-test uses it to prove that logging controls, command and
// activity indicators, the transfer gate, and the clipboard prompt lay out
// inside the real default and minimum window sizes. It is built only under the
// selftest tag.
func RenderSelftestChrome(c *ui.Context, snapshot appsession.Snapshot,
	clipboardPending bool, directory string) {
	format := snapshot.Logging.Format
	if !validLogFormat(format) {
		format = domainterminal.LogFormatText
	}
	renderChrome(c, chromeModel{
		Command:          snapshot.Command,
		Busy:             snapshot.Busy,
		Transfer:         snapshot.Transfer,
		Logging:          snapshot.Logging,
		ClipboardPending: clipboardPending,
		LogFormat:        format,
		LogDirectory:     directory,
	}, locales.In(c.Locale()), c.Theme(), chromeActions{})
}
