package sessionui

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// maxVisibleSuggestions bounds the popup to a scannable list. A pane with less
// room shows fewer rows; placeAutocomplete sizes the window of rows to the
// space above the input line.
const maxVisibleSuggestions = 6

// chromeModel is the Session status and banner state the chrome renders.
// Keeping it a plain value lets geometry tests render the real controls
// without a native terminal.
type chromeModel struct {
	Command          appsession.CommandState
	Busy             bool
	Transfer         appsession.TransferState
	Logging          appsession.LogStatus
	ClipboardPending bool
	LogFormat        domainterminal.LogFormat
	LogDirectory     string
	LogProblem       bool
	Encoding         domainterminal.Encoding

	// PendingPaste is a guarded paste waiting for the user's decision.
	PendingPaste *PendingPaste
	// DropActive reports files dragged over the terminal. DropNotice is the
	// result of the most recent drop.
	DropActive bool
	DropCount  int
	DropAction domainterminal.DropAction
	DropNotice DropNotice
	// ScreenNotice reports the result of Save current screen.
	ScreenNotice ScreenNotice
	// MenuLabel is the accessible name of this Pane's Session menu. The shell
	// supplies a Pane-specific name so several Panes in one window each expose
	// a distinct, clickable menu control. Empty falls back to the generic name.
	MenuLabel string
	// CanPaste reports whether the clipboard holds text right now.
	CanPaste bool
	// CanReconnect reports whether the Session has a reconnect policy and is
	// still able to use it.
	CanReconnect bool

	// Input is the line being typed. Suggestions are the autocomplete rows for
	// it, with SuggestionIndex selecting the row Tab accepts.
	Input            string
	Suggestions      []autocomplete.Suggestion
	SuggestionIndex  int
	ShowAutocomplete bool
	// ShowGhost draws the first suggestion as a one-line hint. GhostText is
	// its text.
	ShowGhost bool
	GhostText string
	// Hibernated reports that the Session has no live screen.
	Hibernated bool
	// FilesOpen reports that the Tab's side panel is open; FilesIcon is its
	// button's glyph.
	FilesOpen bool
	FilesIcon string
}

// ScreenNotice is the result of saving the terminal screen. It is a code, not
// user-visible text.
type ScreenNotice string

const (
	ScreenNoticeNone   ScreenNotice = ""
	ScreenNoticeSaved  ScreenNotice = "saved"
	ScreenNoticeFailed ScreenNotice = "failed"
)

// chromeActions are the interactions the chrome offers. Tests pass nil, which
// also shows the corresponding menu and banner controls as disabled.
type chromeActions struct {
	ToggleLog        func()
	SetLogFormat     func(domainterminal.LogFormat)
	AllowClipboard   func()
	DenyClipboard    func()
	CancelTransfer   func()
	SetEncoding      func(domainterminal.Encoding)
	Copy             func()
	Paste            func()
	PasteSelection   func()
	SelectAll        func()
	OpenBrowser      func()
	Reconnect        func()
	SendYmodem       func()
	ReceiveYmodem    func()
	SaveScreen       func()
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
	// ToggleFiles opens or closes the SFTP panel beside the terminal. Nil
	// hides the control: the Session has no file system to browse.
	ToggleFiles func()
}

// renderChrome draws what the Session needs to say above its terminal: the
// counters when Settings asks for them and the banners of a pending decision.
// Autocomplete is not here: it floats over the terminal (see
// autocompleteLayers), because a row that comes and goes here would resize the
// grid and make the shell repaint on every keystroke. The controls are in the
// Pane header (see toolbar), so with nothing to say the terminal starts right
// under it.
func renderChrome(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions) {
	if model.PendingPaste != nil {
		pastePrompt(c, *model.PendingPaste, m, t, actions)
	}
	if model.DropActive || model.DropNotice != DropNoticeNone {
		dropBanner(c, model, m, t, actions)
	}
	if model.Logging.Active || model.LogProblem {
		logDetail(c, model, m, t)
	}
	if model.ScreenNotice != ScreenNoticeNone {
		screenBanner(c, model, m, t, actions)
	}
	if model.Transfer.Active {
		transferBanner(c, model, m, t, actions)
	}
	if model.ClipboardPending {
		clipboardBanner(c, m, t, actions)
	}
}

// screenBanner reports Save current screen's result and offers Dismiss.
func screenBanner(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions) {
	text := m.SessionScreenSaved()
	if model.ScreenNotice == ScreenNoticeFailed {
		text = m.SessionScreenFailed()
	}
	ui.Row(c).FillWidth().Height(30).Padding(2, 8).Gap(8).Background(t.Surface).
		Border(1, t.Accent).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(kit.M(c).Font.Caption).SingleLine().MinWidth(0).Grow(1).TextColor(t.Text)
		if ui.Button(c, m.SessionDismiss()).Clicked() && actions.DismissScreen != nil {
			actions.DismissScreen()
		}
	})
}

// toolbar is the Session's controls in its Pane's header, as the original's
// terminal toolbar: the side panel (SFTP and the other tools) beside the
// terminal, the two splits, and the Session
// menu, which holds every other command (encoding, logging, YMODEM, the
// browser, rename, and the Pane's lifetime). The header already names the
// Pane, so nothing here repeats the title, the running command, or the
// counters. Each control is a fixed-size icon, so a narrow split Pane keeps
// them all inside its header.
func toolbar(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions) {
	if actions.ToggleFiles != nil {
		icon := model.FilesIcon
		if icon == "" {
			icon = icons.PanelLeft
		}
		files := kit.IconButtonWith(c, icon, m.SettingsBindingToggleSidePanel(),
			kit.IconOptions{Role: kit.IconPane, On: model.FilesOpen})
		if files.Clicked() {
			actions.ToggleFiles()
		}
	}
	if actions.SplitRight != nil && kit.IconButtonWith(c, icons.Columns, m.PaneSplitRight(), kit.IconOptions{Role: kit.IconPane}).Clicked() {
		actions.SplitRight()
	}
	if actions.SplitDown != nil && kit.IconButtonWith(c, icons.Rows, m.PaneSplitDown(), kit.IconOptions{Role: kit.IconPane}).Clicked() {
		actions.SplitDown()
	}
	menuLabel := model.MenuLabel
	if menuLabel == "" {
		menuLabel = m.SessionMenuLabel()
	}
	kit.IconButtonWith(c, icons.EllipsisVertical, menuLabel, kit.IconOptions{Role: kit.IconPane}).Menu(func(menu *ui.Menu) {
		buildSessionMenu(menu, sessionMenuModel{
			Encoding:     domainterminal.NormalizeEncoding(model.Encoding),
			Logging:      model.Logging.Active,
			LogFormat:    model.LogFormat,
			LogDirectory: model.LogDirectory,
			CanPaste:     model.CanPaste && actions.Paste != nil,
			CanReconnect: model.CanReconnect && actions.Reconnect != nil,
			CanClear:     actions.ClearBuffer != nil,
			CanSave:      actions.SaveScreen != nil,
			CanRename:    actions.Rename != nil,
			CanSplit:     actions.SplitRight != nil || actions.SplitDown != nil,
			CanDetach:    actions.Detach != nil,
			CanClosePane: actions.ClosePane != nil,
			Hibernated:   model.Hibernated,
		}, m, sessionMenuActions{
			Copy:           actions.Copy,
			Paste:          actions.Paste,
			PasteSelection: actions.PasteSelection,
			SelectAll:      actions.SelectAll,
			OpenBrowser:    actions.OpenBrowser,
			Reconnect:      actions.Reconnect,
			SendYmodem:     actions.SendYmodem,
			ReceiveYmodem:  actions.ReceiveYmodem,
			SaveScreen:     actions.SaveScreen,
			ClearBuffer:    actions.ClearBuffer,
			Rename:         actions.Rename,
			SetEncoding:    actions.SetEncoding,
			ToggleLog:      actions.ToggleLog,
			SetLogFormat:   actions.SetLogFormat,
			SplitRight:     actions.SplitRight,
			SplitDown:      actions.SplitDown,
			Detach:         actions.Detach,
			ClosePane:      actions.ClosePane,
			Hibernate:      actions.Hibernate,
			Resume:         actions.Resume,
			Observe:        actions.Observe,
		})
	})
}

// autocompleteLayers floats the suggestion popup and the ghost hint over the
// terminal, anchored at the cursor cell. They are Overlay children, so showing
// or hiding them never changes the terminal's size. target is the terminal
// view; its bounds are the previous frame's, one frame behind a resize, which
// the placement clamps away.
func autocompleteLayers(c *ui.Context, target ui.Element, model chromeModel, cursor autocompleteCursor, cols, rows int, m locales.Messages, t *ui.Theme, actions chromeActions) {
	bounds := target.Bounds()
	if bounds.W <= 0 || bounds.H <= 0 {
		return
	}
	placement := placeAutocomplete(bounds, cursor, cols, rows, len(model.Suggestions),
		model.ShowAutocomplete, model.ShowGhost && model.GhostText != "")
	if !placement.ShowPopup && !placement.ShowGhost {
		return
	}
	ui.Overlay(c, func() {
		if placement.ShowGhost {
			autocompleteGhost(c, model, m, t, placement)
		}
		if placement.ShowPopup {
			autocompletePopup(c, model, m, t, actions, placement)
		}
	})
}

// autocompletePopup lists the matching commands at the placement. It shows as
// many rows as fit; the window of rows scrolls so the selected row stays
// visible, and the marked row is the one Tab accepts.
func autocompletePopup(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions, placement autocompletePlacement) {
	rows := min(placement.Rows, len(model.Suggestions))
	if rows <= 0 {
		return
	}
	start := 0
	if model.SuggestionIndex >= rows {
		start = model.SuggestionIndex - rows + 1
	}
	start = min(max(start, 0), max(len(model.Suggestions)-rows, 0))
	visible := model.Suggestions[start : start+rows]
	kit.PopoverSurface(c, ui.Column(c).Absolute().Left(placement.Left).Top(placement.PopupTop).
		Width(placement.Width).Padding(autocompletePadding, 6).Gap(autocompleteRowGap), t.Accent).Children(func() {
		ui.Row(c).FillWidth().Height(autocompleteHeaderHeight).Gap(6).Padding(0, kit.PopoverInset(c)).
			AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SessionAutocompleteLabel()).FontSize(kit.M(c).Font.Caption2).Bold().
				TextColor(t.TextMuted)
			ui.Text(c, m.SessionAutocompleteHint()).FontSize(9).SingleLine().
				MinWidth(0).Grow(1).TextColor(t.TextMuted)
		})
		for index, suggestion := range visible {
			suggestionIndex := start + index
			display := suggestion.Display
			if display == "" {
				display = suggestion.Text
			}
			source := suggestionSourceLabel(suggestion.Source, m)
			selected := suggestionIndex == model.SuggestionIndex
			// Rows are plain full-width rows with a click action, not theme
			// buttons: the styled Button painted a rounded Surface card per
			// label, so a short suggestion showed a narrow pill and neighbors
			// overlapped by a pixel. One full-width row per suggestion, with the
			// selection painted on the row itself, keeps the list even and the
			// selected line legible. KeepFocus leaves the keyboard with the
			// terminal while a row is clicked, so typing continues after an
			// accepted suggestion.
			row := ui.Row(c).FillWidth().Height(autocompleteRowHeight).Padding(0, 4).
				Gap(8).AlignItems(ui.Center).KeepFocus()
			colors := kit.PopoverRow(c, row, selected)
			row.Children(func() {
				marker := " "
				if selected {
					marker = "›"
				}
				ui.Text(c, marker).FontSize(kit.M(c).Font.Caption).TextColor(colors.Marker)
				command := ui.Text(c, display).FontSize(kit.M(c).Font.Caption2).SingleLine().
					MinWidth(0).Grow(1).TextColor(colors.Text)
				if selected {
					command.Bold()
				}
				if source != "" {
					ui.Text(c, source).FontSize(9).SingleLine().TextColor(colors.Muted)
				}
				if suggestion.Frequency > 1 {
					ui.Text(c, m.SessionAutocompleteFrequency(suggestion.Frequency)).
						FontSize(9).SingleLine().TextColor(colors.Muted)
				}
			})
			row.OnClick(func() {
				if actions.AcceptSuggestion != nil {
					actions.AcceptSuggestion(suggestionIndex)
				}
			})
		}
	})
}

// autocompleteGhost draws the first suggestion as a one-line hint beside the
// list. It floats like the popup so turning Ghost text on cannot resize the
// terminal either.
func autocompleteGhost(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, placement autocompletePlacement) {
	ghost := ui.Row(c).Absolute().Left(placement.Left).Top(placement.GhostTop).
		Width(placement.Width).Height(autocompleteGhostHeight).Padding(0, 6).AlignItems(ui.Center)
	kit.PopoverSurface(c, ghost, t.Border).Children(func() {
		ui.Text(c, m.SessionAutocompleteGhost(model.GhostText)).FontSize(kit.M(c).Font.Caption2).
			SingleLine().MinWidth(0).Grow(1).TextColor(t.TextMuted)
	})
}

func suggestionSourceLabel(source string, m locales.Messages) string {
	switch source {
	case autocomplete.SourceHistory:
		return m.SessionAutocompleteHistory()
	case autocomplete.SourceShellHistory:
		return m.SessionAutocompleteShellHistory()
	default:
		return source
	}
}

func encodingLabels(m locales.Messages) []string {
	labels := make([]string, 0, len(domainterminal.SupportedEncodings()))
	for _, encoding := range domainterminal.SupportedEncodings() {
		labels = append(labels, locales.EncodingLabel(m, encoding))
	}
	return labels
}

func encodingAtLabel(m locales.Messages, label string) domainterminal.Encoding {
	for _, encoding := range domainterminal.SupportedEncodings() {
		if locales.EncodingLabel(m, encoding) == label {
			return encoding
		}
	}
	return domainterminal.EncodingUTF8
}

// pastePrompt is the multiline paste confirmation. The preview is scrollable
// and the Send and Cancel actions sit in a fixed header, so a long paste
// cannot push either action out of the window.
func pastePrompt(c *ui.Context, pending PendingPaste, m locales.Messages, t *ui.Theme, actions chromeActions) {
	ui.Column(c).FillWidth().Padding(6).Gap(4).Background(t.Surface).
		Border(1, t.Accent).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SessionPasteConfirmTitle(pending.Lines)).FontSize(kit.M(c).Font.Footnote).
				Bold().SingleLine().MinWidth(0).Grow(1).TextColor(t.Text)
			send := ui.PrimaryButton(c, m.SessionPasteSend())
			if send.Clicked() && actions.AcceptPaste != nil {
				actions.AcceptPaste()
			}
			if ui.Button(c, m.SessionPasteCancel()).Clicked() && actions.CancelPaste != nil {
				actions.CancelPaste()
			}
		})
		ui.Text(c, m.SessionPasteConfirmBody(pending.Chars)).FontSize(kit.M(c).Font.Caption2).
			SingleLine().MinWidth(0).TextColor(t.TextMuted)
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Start).Children(func() {
			ui.Text(c, m.SessionPastePreviewLabel()).FontSize(kit.M(c).Font.Caption2).TextColor(t.TextMuted)
			ui.Scroll(c).Grow(1).Height(42).Children(func() {
				ui.Text(c, pending.Preview).FontSize(kit.M(c).Font.Caption).Wrap().TextColor(t.Text)
			})
		})
		if pending.Truncated {
			ui.Text(c, m.SessionPasteTruncated()).FontSize(kit.M(c).Font.Caption2).
				SingleLine().TextColor(uitheme.WarningText(t))
		}
	})
}

// dropBanner shows files over the terminal and the result of a drop.
func dropBanner(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions) {
	text := dropBannerText(model, m)
	ui.Row(c).FillWidth().Height(30).Padding(2, 8).Gap(8).Background(t.Surface).
		Border(1, t.Accent).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(kit.M(c).Font.Caption).SingleLine().MinWidth(0).Grow(1).TextColor(t.Text)
		if !model.DropActive {
			dismiss := ui.Button(c, m.SessionDropDismiss())
			// The visible label matches the original Cancel. The accessible
			// name stays unique so it cannot collide with the paste prompt's
			// Cancel in a window that shows both banners.
			dismiss.Label(m.SessionDropDismissLabel())
			if dismiss.Clicked() && actions.DismissDrop != nil {
				actions.DismissDrop()
			}
		}
	})
}

func dropBannerText(model chromeModel, m locales.Messages) string {
	if model.DropActive {
		return m.SessionDropActive(dropActionLabel(model.DropAction, m))
	}
	switch model.DropNotice {
	case DropNoticeUploaded:
		return m.SessionDropUploaded(model.DropCount)
	case DropNoticeUnavailable:
		return m.SessionDropUnavailable()
	case DropNoticeFailed:
		return m.SessionDropFailed()
	default:
		return m.SessionDropInserted(model.DropCount)
	}
}

func dropActionLabel(action domainterminal.DropAction, m locales.Messages) string {
	if action == domainterminal.DropUpload {
		return m.SessionDropUploadAction()
	}
	return m.SessionDropInsertAction()
}

func logDetail(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Height(18).Padding(0, 6).Background(t.Surface).Children(func() {
		switch {
		case model.LogProblem:
			ui.Text(c, m.SessionLoggingFailed()).FontSize(kit.M(c).Font.Caption2).
				SingleLine().MinWidth(0).Grow(1).TextColor(uitheme.DangerText(t))
		case model.Logging.Path != "":
			ui.Text(c, m.SessionLoggingPath(filepath.Clean(model.Logging.Path))).FontSize(kit.M(c).Font.Caption2).
				SingleLine().MinWidth(0).Grow(1).TextColor(t.TextMuted)
		case !model.Logging.Active && model.LogDirectory == "":
			ui.Text(c, m.SessionLoggingNoDirectory()).FontSize(kit.M(c).Font.Caption2).
				SingleLine().MinWidth(0).Grow(1).TextColor(t.TextMuted)
		}
	})
}

func transferBanner(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions) {
	ui.Row(c).Height(30).Padding(2, 8).Gap(8).Background(t.Surface).
		Border(1, t.Warning).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.SessionTransferActive(transferKindLabel(model.Transfer.Kind))).FontSize(kit.M(c).Font.Caption).
			SingleLine().MinWidth(0).Grow(1).TextColor(uitheme.WarningText(t))
		if ui.Button(c, m.SessionTransferCancel()).Clicked() && actions.CancelTransfer != nil {
			actions.CancelTransfer()
		}
	})
}

func clipboardBanner(c *ui.Context, m locales.Messages, t *ui.Theme, actions chromeActions) {
	ui.Row(c).Height(32).Padding(2, 8).Gap(8).Background(t.Surface).
		Border(1, t.Accent).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.SessionClipboardRequest()).FontSize(kit.M(c).Font.Caption).
			SingleLine().MinWidth(0).Grow(1).TextColor(t.Text)
		if ui.PrimaryButton(c, m.SessionClipboardAllow()).Clicked() && actions.AllowClipboard != nil {
			actions.AllowClipboard()
		}
		if ui.Button(c, m.SessionClipboardDeny()).Clicked() && actions.DenyClipboard != nil {
			actions.DenyClipboard()
		}
	})
}

func transferKindLabel(kind appsession.TransferKind) string {
	return strings.ToUpper(string(kind))
}

func formatLabels(m locales.Messages) []string {
	return []string{m.SessionLogFormatText(), m.SessionLogFormatRaw(), m.SessionLogFormatHTML()}
}

var chromeFormats = []domainterminal.LogFormat{
	domainterminal.LogFormatText,
	domainterminal.LogFormatRaw,
	domainterminal.LogFormatHTML,
}

func indexOfFormat(format domainterminal.LogFormat) int {
	for index, candidate := range chromeFormats {
		if candidate == format {
			return index
		}
	}
	return 0
}

func formatAtIndex(index int) domainterminal.LogFormat {
	if index < 0 || index >= len(chromeFormats) {
		return domainterminal.LogFormatText
	}
	return chromeFormats[index]
}

func indexOfLabel(labels []string, label string) int {
	for index, candidate := range labels {
		if candidate == label {
			return index
		}
	}
	return 0
}
