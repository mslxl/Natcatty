package transfer

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// dialogView renders the open prompt above the panes. Destructive prompts are
// explicit about what they remove; nothing is deleted until the primary
// button is pressed.
func dialogView(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	dialog := s.Dialog()
	if dialog == nil {
		return
	}
	open := true
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(420).Padding(16).Gap(10).Background(t.Surface).
			Radius(t.Radius).Border(1, t.Border).Children(func() {
			switch dialog.Kind {
			case transferstate.DialogHostPicker:
				hostPicker(c, s, dialog, m, t)
			case transferstate.DialogDelete:
				deleteDialog(c, s, dialog, m, t)
			case transferstate.DialogOverwrite:
				overwriteDialog(c, s, dialog, m, t)
			default:
				inputDialog(c, s, dialog, m, t)
			}
		})
	})
	if !open {
		s.CloseDialog()
	}
}

func dialogTitle(m locales.Messages, dialog *transferstate.Dialog) string {
	switch dialog.Kind {
	case transferstate.DialogNewFolder:
		return m.SftpNewFolderTitle()
	case transferstate.DialogNewFile:
		return m.SftpNewFileTitle()
	case transferstate.DialogRename:
		return m.SftpRenameTitle()
	case transferstate.DialogChmod:
		return m.SftpPermissionsTitle()
	case transferstate.DialogHostPicker:
		return m.SftpConnectTitle()
	}
	return ""
}

func dialogLabel(m locales.Messages, dialog *transferstate.Dialog) string {
	switch dialog.Kind {
	case transferstate.DialogNewFolder:
		return m.SftpNewFolderLabel()
	case transferstate.DialogNewFile:
		return m.SftpNewFileLabel()
	case transferstate.DialogRename:
		return m.SftpRenameLabel()
	case transferstate.DialogChmod:
		return m.SftpPermissionsLabel()
	}
	return ""
}

func inputDialog(c *ui.Context, s *transferstate.State, dialog *transferstate.Dialog, m locales.Messages, t *ui.Theme) {
	ui.Text(c, dialogTitle(m, dialog)).FontSize(15).Bold().TextColor(t.Text)
	ui.Text(c, dialogLabel(m, dialog)).FontSize(11).TextColor(t.TextMuted)
	input := ui.TextInput(c, &dialog.Draft)
	input.Width(360)
	submitted := input.Submitted()
	if dialog.Err != "" {
		message := m.SftpInvalidName()
		if dialog.Err == "invalid-mode" {
			message = m.SftpInvalidMode()
		}
		ui.Text(c, message).FontSize(11).TextColor(t.Danger)
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1)
		confirmLabel := m.SftpCreate()
		if dialog.Kind != transferstate.DialogNewFolder && dialog.Kind != transferstate.DialogNewFile {
			confirmLabel = m.SftpApply()
		}
		confirm := ui.PrimaryButton(c, confirmLabel)
		if confirm.Clicked() || submitted {
			s.SubmitDialog()
		}
		if ui.Button(c, m.SftpCancel()).Clicked() {
			s.CloseDialog()
		}
	})
}

func deleteDialog(c *ui.Context, s *transferstate.State, dialog *transferstate.Dialog, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SftpDeleteTitle()).FontSize(15).Bold().TextColor(t.Danger)
	ui.Text(c, m.SftpDeleteBody(len(dialog.Paths))).FontSize(12).TextColor(t.Text)
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1)
		confirm := ui.Button(c, m.SftpDelete())
		confirm.Background(t.Danger).TextColor(t.AccentText)
		if confirm.Clicked() {
			s.SubmitDialog()
		}
		if ui.Button(c, m.SftpCancel()).Clicked() {
			s.CloseDialog()
		}
	})
}

func overwriteDialog(c *ui.Context, s *transferstate.State, dialog *transferstate.Dialog, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SftpOverwriteTitle()).FontSize(15).Bold().TextColor(t.Text)
	ui.Text(c, m.SftpOverwriteBody(len(dialog.Paths))).FontSize(12).TextColor(t.Text)
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1)
		confirm := ui.PrimaryButton(c, m.SftpReplace())
		if confirm.Clicked() {
			s.SubmitDialog()
		}
		if ui.Button(c, m.SftpCancel()).Clicked() {
			s.CloseDialog()
		}
	})
}

func hostPicker(c *ui.Context, s *transferstate.State, dialog *transferstate.Dialog, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SftpConnectTitle()).FontSize(15).Bold().TextColor(t.Text)
	ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1)
		if ui.Button(c, m.SftpCancel()).Clicked() {
			s.CloseDialog()
		}
	})
	if s.HostsLoading() {
		ui.Text(c, m.SftpConnecting()).FontSize(12).TextColor(t.TextMuted)
		return
	}
	hosts := s.HostTargets()
	if len(hosts) == 0 {
		ui.Text(c, m.SftpConnectEmpty()).FontSize(12).TextColor(t.TextMuted)
		return
	}
	ui.Scroll(c).FillWidth().MaxHeight(280).Children(func() {
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			for _, host := range hosts {
				host := host
				row := ui.Row(c).FillWidth().Gap(8).Padding(8).AlignItems(ui.Center).
					Radius(t.Radius).Background(t.Background).Cursor(ui.CursorPointer)
				row.Children(func() {
					label := host.Label
					if label == "" {
						label = host.Hostname
					}
					ui.Text(c, label).FontSize(13).Bold().Grow(1).MinWidth(0).SingleLine().TextColor(t.Text)
					ui.Text(c, hostAddress(host)).FontSize(11).TextColor(t.TextMuted).SingleLine()
				})
				if row.Clicked() {
					label := host.Label
					if label == "" {
						label = host.Hostname
					}
					s.ChooseHost(dialog.Side, host.ID, label)
				}
			}
		})
	})
}

// hostAddress renders a Host's destination for the picker.
func hostAddress(host apptransfer.HostTarget) string {
	if host.Username == "" {
		return fmt.Sprintf("%s:%d", host.Hostname, host.Port)
	}
	return fmt.Sprintf("%s@%s:%d", host.Username, host.Hostname, host.Port)
}
