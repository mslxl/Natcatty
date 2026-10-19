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
}

// chromeActions are the interactions the chrome offers. Tests pass nil.
type chromeActions struct {
	ToggleLog      func()
	SetLogFormat   func(domainterminal.LogFormat)
	AllowClipboard func()
	DenyClipboard  func()
	CancelTransfer func()
}

func renderChrome(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions) {
	statusBar(c, model, m, t, actions)
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
	})
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
