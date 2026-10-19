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
}

// chromeActions are the interactions the chrome offers. Tests pass nil, which
// also shows the corresponding menu and banner controls as disabled.
type chromeActions struct {
	ToggleLog      func()
	SetLogFormat   func(domainterminal.LogFormat)
	AllowClipboard func()
	DenyClipboard  func()
	CancelTransfer func()
	SetEncoding    func(domainterminal.Encoding)
	Paste          func()
	ClearBuffer    func()
	AcceptPaste    func()
	CancelPaste    func()
	DismissDrop    func()
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
	if model.Transfer.Active {
		transferBanner(c, model, m, t, actions)
	}
	if model.ClipboardPending {
		clipboardBanner(c, m, t, actions)
	}
}

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

		// With no folder configured the detail row explains why Start is
		// disabled. Repeating that long sentence here would squeeze the
		// command to a few DIPs at the minimum window width.
		if model.Logging.Active {
			ui.Text(c, m.SessionLoggingOn()).FontSize(10).TextColor(t.Success)
		} else if model.LogDirectory != "" {
			ui.Text(c, m.SessionLoggingOff()).FontSize(10).TextColor(t.TextMuted)
		}

		labels := formatLabels(m)
		selected := labels[indexOfFormat(model.LogFormat)]
		selectElement := ui.Select(c, &selected, labels).Width(88)
		if selectElement.Changed() && actions.SetLogFormat != nil {
			actions.SetLogFormat(formatAtIndex(indexOfLabel(labels, selected)))
		}

		buttonLabel := m.SessionLoggingStart()
		if model.Logging.Active {
			buttonLabel = m.SessionLoggingStop()
		}
		button := ui.Button(c, buttonLabel)
		button.Disabled(model.LogDirectory == "" && !model.Logging.Active)
		if button.Clicked() && actions.ToggleLog != nil {
			actions.ToggleLog()
		}

		// The encoding selector is always visible: a Session that renders
		// mojibake must not require opening a menu to fix it.
		encoding := domainterminal.NormalizeEncoding(model.Encoding)
		encodingChoices := encodingLabels(m)
		selectedEncoding := locales.EncodingLabel(m, encoding)
		encodingSelect := ui.Select(c, &selectedEncoding, encodingChoices).Width(104)
		encodingSelect.Label(m.SessionEncoding())
		if encodingSelect.Changed() && actions.SetEncoding != nil {
			actions.SetEncoding(encodingAtLabel(m, selectedEncoding))
		}

		sessionMenuButton(c, sessionMenuModel{
			Encoding:     encoding,
			Logging:      model.Logging.Active,
			LogFormat:    model.LogFormat,
			LogDirectory: model.LogDirectory,
			CanPaste:     actions.Paste != nil,
			CanClear:     actions.ClearBuffer != nil,
			CanSplit:     actions.SplitRight != nil || actions.SplitDown != nil,
			CanDetach:    actions.Detach != nil,
			CanClosePane: actions.ClosePane != nil,
		}, m, t, sessionMenuActions{
			Paste:        actions.Paste,
			ClearBuffer:  actions.ClearBuffer,
			SetEncoding:  actions.SetEncoding,
			ToggleLog:    actions.ToggleLog,
			SetLogFormat: actions.SetLogFormat,
			SplitRight:   actions.SplitRight,
			SplitDown:    actions.SplitDown,
			Detach:       actions.Detach,
			ClosePane:    actions.ClosePane,
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
		if !model.DropActive && ui.Button(c, m.SessionPasteCancel()).Clicked() && actions.DismissDrop != nil {
			actions.DismissDrop()
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
