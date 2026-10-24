package transfer

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// dialogMargin is the space a dialog keeps from the window's edges. The
// Transfer center clamps itself to the window the same way.
const dialogMargin float32 = 16

// dialogPadding is the dialog card's inset. The prompt's field subtracts it
// from both sides of the dialog's clamped width.
const dialogPadding float32 = 16

// dialogView renders the open prompt above the panes. Destructive prompts are
// explicit about what they remove; nothing is deleted until the primary
// button is pressed.
func dialogView(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	dialog := s.Dialog()
	if dialog == nil {
		return
	}
	open := true
	width := float32(420)
	if dialog.Kind == transferstate.DialogConflict {
		width = 560
	}
	// Clamp to the window exactly as the Transfer center does. The SFTP
	// surface itself is only 456 DIPs wide at a 720x480 window, so a dialog
	// that ignored the window's width would run off the edge it is centred
	// in; the clamp keeps every dialog inside the client area at both
	// supported sizes.
	if windowWidth, _ := c.Size(); width > windowWidth-2*dialogMargin {
		width = windowWidth - 2*dialogMargin
	}
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(width).Padding(dialogPadding).Gap(10).Background(t.Surface).
			Radius(t.Radius).Border(1, t.Border).Children(func() {
			switch dialog.Kind {
			case transferstate.DialogHostPicker:
				hostPicker(c, s, dialog, m, t)
			case transferstate.DialogDelete:
				deleteDialog(c, s, dialog, m, t)
			case transferstate.DialogConflict:
				conflictDialog(c, s, dialog, m, t)
			default:
				inputDialog(c, s, dialog, m, t, width)
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

func inputDialog(c *ui.Context, s *transferstate.State, dialog *transferstate.Dialog, m locales.Messages, t *ui.Theme, dialogWidth float32) {
	ui.Text(c, dialogTitle(m, dialog)).FontSize(15).Bold().TextColor(t.Text)
	ui.Text(c, dialogLabel(m, dialog)).FontSize(11).TextColor(t.TextMuted).MinWidth(0)
	input := ui.TextInput(c, &dialog.Draft)
	// The field never asks for more than the dialog's content box, which is
	// already clamped to the window.
	input.Width(min(360, dialogWidth-2*dialogPadding))
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

// conflictDescription names the situation for the incoming shape and the
// existing destination's known type. A type that cannot be merged or replaced
// fails closed rather than offering a destructive guess.
func conflictDescription(m locales.Messages, conflict domainsftp.Conflict) string {
	if !conflict.IsDirectory {
		return m.SftpConflictDescription()
	}
	switch conflict.ExistingKind {
	case domainsftp.ExistingFile:
		return m.SftpConflictFolderFileDescription()
	case domainsftp.ExistingSymlink:
		return m.SftpConflictFolderSymlinkDescription()
	case domainsftp.ExistingDirectory:
		return m.SftpConflictFolderDescription()
	default:
		return m.SftpConflictFolderUnknownDescription()
	}
}

// conflictDialog renders all five actions. Stop and Skip are always available;
// Duplicate writes beside the destination; Merge appears for a folder and is
// disabled unless the destination is a folder; Replace appears only when the
// type pair is safe and warns before a folder replacement.
func conflictDialog(c *ui.Context, s *transferstate.State, dialog *transferstate.Dialog, m locales.Messages, t *ui.Theme) {
	conflict := dialog.Conflict
	if conflict == nil {
		return
	}
	title := m.SftpConflictTitle()
	if conflict.IsDirectory {
		title = m.SftpConflictFolderTitle()
	}
	ui.Text(c, title).FontSize(15).Bold().TextColor(t.Text)
	ui.Text(c, conflict.Name+" "+m.SftpConflictAlreadyExists()).FontSize(13).
		TextColor(t.Text).SingleLine()
	ui.Text(c, conflictDescription(m, *conflict)).FontSize(11).TextColor(t.TextMuted)

	if !conflict.IsDirectory {
		fileConflictSummary(c, m, t, conflict)
	}
	if conflict.CanMerge() {
		ui.Text(c, m.SftpConflictMergeHint()).FontSize(11).TextColor(t.TextMuted)
	}
	if conflict.IsDirectory && conflict.ExistingKind == domainsftp.ExistingDirectory {
		ui.Text(c, m.SftpConflictReplaceWarning()).FontSize(11).TextColor(t.Danger)
	}

	count := conflict.ApplyToAllCount
	if count > 0 {
		ui.Checkbox(c, &dialog.ApplyToAll, m.SftpConflictApplyToAll(count)).FontSize(11)
	}
	if dialog.Err != "" {
		ui.Text(c, m.SftpConflictInvalidAction()).FontSize(11).TextColor(t.Danger)
	}

	ui.Row(c).FillWidth().Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
		stop := ui.Button(c, m.SftpConflictStop())
		stop.FontSize(12)
		if stop.Clicked() {
			s.ResolveDialogConflict(apptransfer.ConflictStop, dialog.ApplyToAll)
		}
		ui.Column(c).Grow(1)
		skip := ui.Button(c, m.SftpConflictSkip())
		skip.FontSize(12)
		if skip.Clicked() {
			s.ResolveDialogConflict(apptransfer.ConflictSkip, dialog.ApplyToAll)
		}
		duplicate := ui.Button(c, m.SftpConflictDuplicate())
		duplicate.FontSize(12)
		if duplicate.Clicked() {
			s.ResolveDialogConflict(apptransfer.ConflictDuplicate, dialog.ApplyToAll)
		}
		if conflict.IsDirectory {
			merge := ui.Button(c, m.SftpConflictMerge())
			merge.FontSize(12)
			merge.Disabled(!conflict.CanMerge())
			if merge.Clicked() && conflict.CanMerge() {
				s.ResolveDialogConflict(apptransfer.ConflictMerge, dialog.ApplyToAll)
			}
		}
		if conflict.CanReplace() {
			replace := ui.Button(c, m.SftpConflictReplace())
			replace.FontSize(12)
			if conflict.IsDirectory && conflict.ExistingKind == domainsftp.ExistingDirectory {
				replace.Background(t.Danger).TextColor(t.AccentText)
			} else {
				replace.Background(t.Accent).TextColor(t.AccentText)
			}
			if replace.Clicked() {
				s.ResolveDialogConflict(apptransfer.ConflictReplace, dialog.ApplyToAll)
			}
		}
	})
}

// fileConflictSummary shows the existing and incoming file details side by
// side. Directories have no file metadata to compare.
func fileConflictSummary(c *ui.Context, m locales.Messages, t *ui.Theme, conflict *domainsftp.Conflict) {
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		fileSummary(c, m, t, m.SftpConflictExistingFile(), conflict.ExistingSize, conflict.ExistingModified)
		fileSummary(c, m, t, m.SftpConflictNewFile(), conflict.NewSize, conflict.NewModified)
	})
}

func fileSummary(c *ui.Context, m locales.Messages, t *ui.Theme, title string, size int64, modified time.Time) {
	ui.Column(c).Grow(1).Gap(2).Padding(8).Background(t.SurfaceHover).Radius(t.Radius).Children(func() {
		ui.Text(c, title).FontSize(11).Bold().TextColor(t.Text).SingleLine()
		ui.Text(c, m.SftpConflictSize()+domainsftp.FormatSize(size)).FontSize(10).
			TextColor(t.TextMuted).SingleLine()
		if text := modifiedText(modified); text != "" {
			ui.Text(c, m.SftpConflictModified()+text).FontSize(10).
				TextColor(t.TextMuted).SingleLine()
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
