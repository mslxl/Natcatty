package sessionui

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
)

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
	ToggleLog      func()
	SetLogFormat   func(domainterminal.LogFormat)
	AllowClipboard func()
	DenyClipboard  func()
	CancelTransfer func()
	SetEncoding    func(domainterminal.Encoding)
	Copy           func()
	Paste          func()
	PasteSelection func()
	SelectAll      func()
	OpenBrowser    func()
	Reconnect      func()
	SendYmodem     func()
	ReceiveYmodem  func()
	SaveScreen     func()
	ClearBuffer    func()
	Rename         func()
	AcceptPaste    func()
	CancelPaste    func()
	DismissDrop    func()
	DismissScreen  func()
	SplitRight     func()
	SplitDown      func()
	Detach         func()
	ClosePane      func()
}

func renderChrome(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions) {
	statusBar(c, model, m, t, actions)
	if model.PendingPaste != nil {
		pastePrompt(c, *model.PendingPaste, m, t, actions)
	}
	if model.DropActive || model.DropNotice != DropNoticeNone {
		dropBanner(c, model, m, t, actions)
	}
	if model.Logging.Active || model.LogProblem || model.LogDirectory == "" {
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
		ui.Text(c, text).FontSize(11).SingleLine().MinWidth(0).Grow(1).TextColor(t.Text)
		if ui.Button(c, m.SessionDismiss()).Clicked() && actions.DismissScreen != nil {
			actions.DismissScreen()
		}
	})
}

// statusBar keeps only the activity dot, the command, and the Session menu.
// The logging selector, its Start/Stop button, and the encoding selector live
// in the menu: at the minimum window width a split Pane can be narrower than
// those controls combined, and a Row draws what does not fit outside the Pane.
// The Session menu is the one control that must always stay reachable, so it
// is the only fixed-width item at the right edge and the command takes the
// remaining width.
func statusBar(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions) {
	ui.Row(c).Height(28).Padding(0, 6).Gap(8).Background(t.Surface).AlignItems(ui.Center).Children(func() {
		dotColor := t.TextMuted
		label := m.SessionActivityIdle()
		if model.Busy {
			dotColor = t.Success
			label = m.SessionActivityBusy()
		}
		ui.Text(c, "●").FontSize(9).TextColor(dotColor).Label(label)

		ui.Text(c, commandText(m, model.Command)).FontSize(11).
			SingleLine().MinWidth(0).Grow(1).TextColor(t.Text)

		encoding := domainterminal.NormalizeEncoding(model.Encoding)
		menuLabel := model.MenuLabel
		if menuLabel == "" {
			menuLabel = m.SessionMenuLabel()
		}
		sessionMenuButton(c, sessionMenuModel{
			Encoding:     encoding,
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
		}, menuLabel, m, t, sessionMenuActions{
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
		})
	})
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
			ui.Text(c, m.SessionPasteConfirmTitle(pending.Lines)).FontSize(12).
				Bold().SingleLine().MinWidth(0).Grow(1).TextColor(t.Text)
			send := ui.PrimaryButton(c, m.SessionPasteSend())
			if send.Clicked() && actions.AcceptPaste != nil {
				actions.AcceptPaste()
			}
			if ui.Button(c, m.SessionPasteCancel()).Clicked() && actions.CancelPaste != nil {
				actions.CancelPaste()
			}
		})
		ui.Text(c, m.SessionPasteConfirmBody(pending.Chars)).FontSize(10).
			SingleLine().MinWidth(0).TextColor(t.TextMuted)
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Start).Children(func() {
			ui.Text(c, m.SessionPastePreviewLabel()).FontSize(10).TextColor(t.TextMuted)
			ui.Scroll(c).Grow(1).Height(42).Children(func() {
				ui.Text(c, pending.Preview).FontSize(11).Wrap().TextColor(t.Text)
			})
		})
		if pending.Truncated {
			ui.Text(c, m.SessionPasteTruncated()).FontSize(10).
				SingleLine().TextColor(t.Warning)
		}
	})
}

// dropBanner shows files over the terminal and the result of a drop.
func dropBanner(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions) {
	text := dropBannerText(model, m)
	ui.Row(c).FillWidth().Height(30).Padding(2, 8).Gap(8).Background(t.Surface).
		Border(1, t.Accent).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(11).SingleLine().MinWidth(0).Grow(1).TextColor(t.Text)
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

func commandText(m locales.Messages, command appsession.CommandState) string {
	switch {
	case command.Running:
		if command.Text == "" {
			return m.SessionCommandRunning(m.SessionCommandIdle())
		}
		return m.SessionCommandRunning(command.Text)
	case command.Text != "" && command.HasExitCode:
		return m.SessionCommandExited(command.Text, command.ExitCode)
	case command.Text != "":
		return m.SessionCommandFinished(command.Text)
	default:
		return m.SessionCommandIdle()
	}
}

func logDetail(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Height(18).Padding(0, 6).Background(t.Surface).Children(func() {
		switch {
		case model.LogProblem:
			ui.Text(c, m.SessionLoggingFailed()).FontSize(10).
				SingleLine().MinWidth(0).Grow(1).TextColor(t.Danger)
		case model.Logging.Path != "":
			ui.Text(c, m.SessionLoggingPath(filepath.Clean(model.Logging.Path))).FontSize(10).
				SingleLine().MinWidth(0).Grow(1).TextColor(t.TextMuted)
		case !model.Logging.Active && model.LogDirectory == "":
			ui.Text(c, m.SessionLoggingNoDirectory()).FontSize(10).
				SingleLine().MinWidth(0).Grow(1).TextColor(t.TextMuted)
		}
	})
}

func transferBanner(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions) {
	ui.Row(c).Height(30).Padding(2, 8).Gap(8).Background(t.Surface).
		Border(1, t.Warning).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.SessionTransferActive(transferKindLabel(model.Transfer.Kind))).FontSize(11).
			SingleLine().MinWidth(0).Grow(1).TextColor(t.Warning)
		if ui.Button(c, m.SessionTransferCancel()).Clicked() && actions.CancelTransfer != nil {
			actions.CancelTransfer()
		}
	})
}

func clipboardBanner(c *ui.Context, m locales.Messages, t *ui.Theme, actions chromeActions) {
	ui.Row(c).Height(32).Padding(2, 8).Gap(8).Background(t.Surface).
		Border(1, t.Accent).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.SessionClipboardRequest()).FontSize(11).
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
