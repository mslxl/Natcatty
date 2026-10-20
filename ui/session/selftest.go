//go:build selftest

package sessionui

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
)

// SelftestInteractionModel is the terminal-interaction chrome state the
// ticket 17 and 18 self-tests render: encoding, logging, the Session menu, the
// paste confirmation, drop feedback, autocomplete, and the Session counters.
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
	ScreenNotice     ScreenNotice
	MenuLabel        string
	CanPaste         bool
	CanReconnect     bool

	Input            string
	Suggestions      []autocomplete.Suggestion
	SuggestionIndex  int
	ShowAutocomplete bool
	ShowGhost        bool
	GhostText        string
	ShowStats        bool
	Stats            appsession.Stats
	Hibernated       bool
}

// SelftestInteractionActions are the production menu and banner callbacks. The
// self-test supplies recording functions and drives the same controls a user
// clicks.
type SelftestInteractionActions struct {
	Copy             func()
	Paste            func()
	PasteSelection   func()
	SelectAll        func()
	OpenBrowser      func()
	Reconnect        func()
	SendYmodem       func()
	ReceiveYmodem    func()
	SaveScreen       func()
	ToggleLog        func()
	SetLogFormat     func(domainterminal.LogFormat)
	SetEncoding      func(domainterminal.Encoding)
	ClearBuffer      func()
	Rename           func()
	AcceptPaste      func()
	CancelPaste      func()
	DismissDrop      func()
	DismissScreen    func()
	SplitRight       func()
	SplitDown        func()
	Detach           func()
	ClosePane        func()
	Hibernate        func()
	Resume           func()
	Observe          func()
	AcceptSuggestion func(index int)
}

// SelftestAcceptSuggestion accepts a suggestion through the same path Tab
// uses. It is built only under the selftest tag so the app package can drive
// the unexported key handler.
func (s *Surface) SelftestAcceptSuggestion(index int) bool {
	s.suggestionIndex = index
	return s.handleAutocompleteKey(0, ui.KeyTab)
}

// PrepareSelftestInteraction puts a real Session surface into the state the
// ticket 17 demonstration shows: a guarded three-line paste waiting for Send
// and a completed two-file drop whose result can be dismissed. The next run
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
	s.dropOver = false
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
		ScreenNotice:     model.ScreenNotice,
		MenuLabel:        model.MenuLabel,
		CanPaste:         model.CanPaste,
		CanReconnect:     model.CanReconnect,
		Input:            model.Input,
		Suggestions:      model.Suggestions,
		SuggestionIndex:  model.SuggestionIndex,
		ShowAutocomplete: model.ShowAutocomplete,
		ShowGhost:        model.ShowGhost,
		GhostText:        model.GhostText,
		ShowStats:        model.ShowStats,
		Stats:            model.Stats,
		Hibernated:       model.Hibernated,
	}, locales.In(c.Locale()), c.Theme(), chromeActions{
		Copy:             actions.Copy,
		Paste:            actions.Paste,
		PasteSelection:   actions.PasteSelection,
		SelectAll:        actions.SelectAll,
		OpenBrowser:      actions.OpenBrowser,
		Reconnect:        actions.Reconnect,
		SendYmodem:       actions.SendYmodem,
		ReceiveYmodem:    actions.ReceiveYmodem,
		SaveScreen:       actions.SaveScreen,
		ToggleLog:        actions.ToggleLog,
		SetLogFormat:     actions.SetLogFormat,
		SetEncoding:      actions.SetEncoding,
		ClearBuffer:      actions.ClearBuffer,
		Rename:           actions.Rename,
		AcceptPaste:      actions.AcceptPaste,
		CancelPaste:      actions.CancelPaste,
		DismissDrop:      actions.DismissDrop,
		DismissScreen:    actions.DismissScreen,
		SplitRight:       actions.SplitRight,
		SplitDown:        actions.SplitDown,
		Detach:           actions.Detach,
		ClosePane:        actions.ClosePane,
		Hibernate:        actions.Hibernate,
		Resume:           actions.Resume,
		Observe:          actions.Observe,
		AcceptSuggestion: actions.AcceptSuggestion,
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
