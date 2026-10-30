package transfer

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// dialogMargin is the space a dialog keeps from the window's edges. The
// Transfer center clamps itself to the window the same way.
const dialogMargin float32 = 16

// dialogPadding is the dialog content's own inset; the modal's card already
// pads it. The prompt's field subtracts it from both sides of the width.
const dialogPadding float32 = 0

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
	if dialog.Kind == transferstate.DialogHostPicker {
		width = 512
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
		// The modal draws the card; a frame of the dialog's own would nest a
		// second card inside it.
		ui.Column(c).Width(width).Gap(10).Children(func() {
			switch dialog.Kind {
			case transferstate.DialogHostPicker:
				hostPicker(c, s, dialog, m, t)
			case transferstate.DialogDelete:
				deleteDialog(c, s, dialog, m, t)
			case transferstate.DialogConflict:
				conflictDialog(c, s, dialog, m, t)
			case transferstate.DialogChmod:
				chmodDialog(c, s, dialog, m, t)
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
		ui.Text(c, message).FontSize(11).TextColor(uitheme.DangerText(t))
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

// chmodDialog is the original's permission grid: Read, Write, and Execute
// for the owner, the group, and others, with the mode it adds up to.
func chmodDialog(c *ui.Context, s *transferstate.State, dialog *transferstate.Dialog, m locales.Messages, t *ui.Theme) {
	mode, err := domainsftp.ParseMode(dialog.Draft)
	if err != nil {
		mode = 0
	}
	ui.Text(c, m.SftpPermissionsTitle()).FontSize(15).Bold().TextColor(t.Text)
	if len(dialog.Paths) == 1 {
		ui.Text(c, baseName(dialog.Paths[0])).FontSize(12).TextColor(t.TextMuted).SingleLine().MinWidth(0)
	}
	who := []string{m.SftpPermissionsOwner(), m.SftpPermissionsGroup(), m.SftpPermissionsOthers()}
	what := []string{m.SftpPermissionsRead(), m.SftpPermissionsWrite(), m.SftpPermissionsExecute()}
	ui.Column(c).FillWidth().Gap(8).Padding(12).Radius(kit.RadiusControl).Background(kit.Muted(t)).Children(func() {
		for i, name := range who {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, name).FontSize(12).FontWeight(500).Width(84).SingleLine().TextColor(t.Text)
				for j, right := range what {
					bit := uint32(1) << uint(8-3*i-j)
					on := mode&bit != 0
					// The mode is the dialog's draft, so a click flips its bit
					// rather than a copy the checkbox would write.
					box := ui.Checkbox(c, &on, right).FontSize(12)
					box.Grow(1)
					if box.Clicked() {
						dialog.Draft = domainsftp.FormatOctal(mode ^ bit)
						dialog.Err = ""
					}
				}
			})
		}
	})
	ui.Row(c).FillWidth().Gap(16).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.SftpPermissionsOctal()).FontSize(11).TextColor(t.TextMuted)
		input := ui.TextInput(c, &dialog.Draft)
		input.FontSize(12)
		input.Width(72)
		if input.Submitted() {
			s.SubmitDialog()
		}
		ui.Text(c, m.SftpPermissionsSymbolic()).FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, symbolicMode(mode)).FontSize(12).Font("Consolas").TextColor(t.Text)
	})
	if dialog.Err != "" {
		ui.Text(c, m.SftpInvalidMode()).FontSize(11).TextColor(uitheme.DangerText(t))
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1)
		if ui.PrimaryButton(c, m.SftpApply()).Clicked() {
			s.SubmitDialog()
		}
		if ui.Button(c, m.SftpCancel()).Clicked() {
			s.CloseDialog()
		}
	})
}

// symbolicMode spells a mode's nine permission bits, as ls does.
func symbolicMode(mode uint32) string {
	const letters = "rwxrwxrwx"
	out := []byte("---------")
	for i := range out {
		if mode&(1<<uint(8-i)) != 0 {
			out[i] = letters[i]
		}
	}
	return string(out)
}

func deleteDialog(c *ui.Context, s *transferstate.State, dialog *transferstate.Dialog, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SftpDeleteTitle()).FontSize(15).Bold().TextColor(uitheme.DangerText(t))
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
		ui.Text(c, m.SftpConflictReplaceWarning()).FontSize(11).TextColor(uitheme.DangerText(t))
	}

	count := conflict.ApplyToAllCount
	if count > 0 {
		ui.Checkbox(c, &dialog.ApplyToAll, m.SftpConflictApplyToAll(count)).FontSize(11)
	}
	if dialog.Err != "" {
		ui.Text(c, m.SftpConflictInvalidAction()).FontSize(11).TextColor(uitheme.DangerText(t))
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

// pickerRow is one choice in the Host picker: the local file system, or a
// saved Host.
type pickerRow struct {
	local bool
	host  apptransfer.HostTarget
}

func hostLabel(host apptransfer.HostTarget) string {
	if host.Label != "" {
		return host.Label
	}
	return host.Hostname
}

// pickerRows are the picker's matches for the query, local first.
func pickerRows(s *transferstate.State, query string, m locales.Messages) []pickerRow {
	query = strings.ToLower(strings.TrimSpace(query))
	matches := func(texts ...string) bool {
		if query == "" {
			return true
		}
		for _, text := range texts {
			if strings.Contains(strings.ToLower(text), query) {
				return true
			}
		}
		return false
	}
	var rows []pickerRow
	if s.LocalAvailable() && matches(m.SftpLocalFilesystem(), m.SftpPickerLocal()) {
		rows = append(rows, pickerRow{local: true})
	}
	for _, host := range s.HostTargets() {
		if matches(host.Label, host.Hostname, host.Username) {
			rows = append(rows, pickerRow{host: host})
		}
	}
	return rows
}

// hostPicker is the original's chooser: a search field over the local file
// system and the saved Hosts, driven by the arrow keys and Enter too.
func hostPicker(c *ui.Context, s *transferstate.State, dialog *transferstate.Dialog, m locales.Messages, t *ui.Theme) {
	rows := pickerRows(s, dialog.Draft, m)
	dialog.Cursor = min(max(dialog.Cursor, 0), max(len(rows)-1, 0))
	choose := func(row pickerRow) {
		if row.local {
			s.ChooseLocal(dialog.Side)
		} else {
			s.ChooseHost(dialog.Side, row.host.ID, hostLabel(row.host))
		}
	}

	ui.Row(c).FillWidth().Height(36).Padding(0, 10).Gap(8).AlignItems(ui.Center).
		Radius(kit.RadiusControl).Border(1, t.Border).Background(t.Background).Children(func() {
		ui.Icon(c, icons.Lucide(icons.Search)).Size(14, 14).TextColor(t.TextMuted).Shrink(0)
		before := dialog.Draft
		input := ui.TextInputBase(c, &dialog.Draft)
		input.Placeholder(m.SftpSearchHosts())
		input.FontSize(13)
		input.Grow(1)
		input.MinWidth(0)
		input.AutoFocus()
		if dialog.Draft != before {
			dialog.Cursor = 0
		}
		if input.Shortcut(0, ui.KeyDown) && dialog.Cursor < len(rows)-1 {
			dialog.Cursor++
		}
		if input.Shortcut(0, ui.KeyUp) && dialog.Cursor > 0 {
			dialog.Cursor--
		}
		if input.Submitted() && len(rows) > 0 {
			choose(rows[dialog.Cursor])
		}
		side := m.SftpPaneLeft()
		if dialog.Side == transferstate.Right {
			side = m.SftpPaneRight()
		}
		ui.Text(c, side).FontSize(10).FontWeight(600).TextColor(t.TextMuted).Padding(2, 6).
			Radius(4).Background(kit.Muted(t)).Shrink(0)
	})

	ui.Scroll(c).FillWidth().MaxHeight(320).Children(func() {
		ui.Column(c).FillWidth().Gap(2).Children(func() {
			if len(rows) == 0 {
				text := m.SftpNoMatchingHosts()
				if s.HostsLoading() {
					text = m.SftpLoading()
				}
				ui.Text(c, text).FontSize(12).TextColor(t.TextMuted).Padding(20, 8).FillWidth().TextAlign(ui.Center)
				return
			}
			section := ""
			for index, row := range rows {
				title := m.SftpPickerHosts()
				if row.local {
					title = m.SftpPickerLocal()
				}
				if title != section {
					section = title
					ui.Text(c, strings.ToUpper(title)).FontSize(10).FontWeight(600).
						TextColor(t.TextMuted).Padding(8, 8, 4, 8)
				}
				if pickerItem(c, row, index == dialog.Cursor, m, t).Clicked() {
					choose(row)
				}
			}
			if s.HostsLoading() {
				ui.Text(c, m.SftpLoading()).FontSize(11).TextColor(t.TextMuted).Padding(6, 8)
			}
		})
	})
}

func pickerItem(c *ui.Context, row pickerRow, highlighted bool, m locales.Messages, t *ui.Theme) ui.Element {
	title, detail := hostLabel(row.host), hostAddress(row.host)
	if row.local {
		title, detail = m.SftpLocalFilesystem(), m.SftpBrowseLocal()
	}
	el := ui.Row(c).FillWidth().Height(44).Padding(0, 8).Gap(10).AlignItems(ui.Center).
		Radius(kit.RadiusControl).Cursor(ui.CursorPointer).Label(title)
	if highlighted || el.Hovered() {
		el.Background(kit.Subtle(t))
	}
	el.Children(func() {
		if row.local {
			ui.Column(c).Size(28, 28).Radius(6).Background(t.Accent.Alpha(0.12)).Shrink(0).
				Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icons.Lucide(icons.Monitor)).Size(15, 15).TextColor(t.Accent)
			})
		} else {
			kit.HostAvatar(c, "", kit.AvatarServer, string(row.host.ID), 28)
		}
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, title).FontSize(13).FontWeight(500).SingleLine().MinWidth(0).TextColor(t.Text)
			ui.Text(c, detail).FontSize(11).SingleLine().MinWidth(0).TextColor(t.TextMuted)
		})
	})
	return el
}

// hostAddress renders a Host's destination for the picker.
func hostAddress(host apptransfer.HostTarget) string {
	if host.Username == "" {
		return fmt.Sprintf("%s:%d", host.Hostname, host.Port)
	}
	return fmt.Sprintf("%s@%s:%d", host.Username, host.Hostname, host.Port)
}
