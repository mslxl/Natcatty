package transfer

import (
	"strings"

	"github.com/egoist/mygo/ui"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// The Transfer center is an overlay over the SFTP surface. It follows the
// Quick Switcher's overlay geometry so both panels sit at the same place and
// stay inside the 720x480 window: the width and height are clamped to the
// viewport, and the rows live in a Scroll with a zero minimum height.
const (
	centerMaxWidth  float32 = 720
	centerMaxHeight float32 = 560
	centerTop       float32 = 24
	centerPadding   float32 = 14
)

// centerView renders the global Transfer center: every queued, active, and
// recently finished Transfer, with per-file progress, ETA, and the controls
// the row's state allows. Escape and a click outside close it.
func centerView(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	if !s.CenterOpen() {
		return
	}
	tasks := s.QueueTasks()
	width, height := c.Size()
	panelWidth := min(width-32, centerMaxWidth)
	panelHeight := min(height-32, centerMaxHeight)

	closed := false
	ui.Overlay(c, func() {
		back := ui.Column(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			AlignItems(ui.Center).Justify(ui.Start).Modal().
			Background(ui.RGBA(0, 0, 0, 0.4))
		// The backdrop closes. OnClick runs after this build, so it closes the
		// state directly instead of leaving a flag this pass can no longer see;
		// the panel swallows its own clicks below, so a click inside the center
		// never reaches this handler.
		back.OnClick(func() {
			s.CloseCenter()
			c.Invalidate()
		})
		if back.OverlayShortcut(0, ui.KeyEscape) {
			closed = true
		}
		// The Modal backdrop suppresses the window shortcut that opened the
		// center, so the same effective, rebindable chord has to be offered to
		// the overlay itself to toggle it closed.
		if mods, key, ok := centerChord(s); ok && back.OverlayShortcut(mods, key) {
			closed = true
		}
		back.Children(func() {
			panel := ui.Column(c).Role(ui.RoleDialog).
				Width(panelWidth).Height(panelHeight).
				Margin(centerTop, 0, 0, 0).Padding(centerPadding).Gap(8).
				Background(t.Background).Border(1, t.Border).
				Radius(t.Radius+2).Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, 0.3)).
				Clip()
			panel.OnClick(func() {})
			panel.Children(func() {
				centerHeader(c, s, m, t)
				if code := s.CenterError(); code != "" {
					ui.Text(c, noticeText(m, code)).FontSize(11).
						TextColor(uitheme.DangerText(t)).SingleLine()
				}
				if len(tasks) == 0 {
					ui.Column(c).Grow(1).FillWidth().
						AlignItems(ui.Center).Justify(ui.Center).Children(func() {
						ui.Text(c, m.SftpTransferEmpty()).FontSize(12).
							TextColor(t.TextMuted).AlignSelf(ui.Center)
					})
				} else {
					ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
						ui.Column(c).FillWidth().MinWidth(0).Gap(6).Children(func() {
							for _, task := range tasks {
								centerRow(c, s, m, t, task)
							}
						})
					})
				}
				centerFooter(c, s, m, t)
			})
		})
	})
	if closed {
		s.CloseCenter()
		c.Invalidate()
	}
}

func centerHeader(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	stats := s.TransferStats()
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.SftpTransferCenter()).FontSize(16).Bold().TextColor(t.Text)
		ui.Text(c, m.SftpTransferActive(stats.Active)).FontSize(11).
			TextColor(t.TextMuted).NoWrap()
		ui.Text(c, m.SftpTransferQueued(stats.Queued)).FontSize(11).
			TextColor(t.TextMuted).NoWrap()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.SftpDismiss()).Clicked() {
			s.CloseCenter()
		}
	})
}

// centerChord resolves the effective Transfer-center binding to the key pair
// MyGo matches. Reading the active catalog keeps the overlay in step with a
// user rebinding, so only the chord the user chose closes the center.
func centerChord(s *transferstate.State) (ui.Modifiers, ui.Key, bool) {
	for _, active := range s.Bindings() {
		if active.Binding.Action != "transferCenter" {
			continue
		}
		if active.Parsed.Range || active.Parsed.Arrows {
			continue
		}
		key, ok := shortcutKey(active.Parsed.Key)
		if !ok {
			continue
		}
		return shortcutMods(active.Parsed), key, true
	}
	return 0, 0, false
}

// centerRow is one Transfer: name and status, a progress bar, then the
// labels, byte counts, ETA, and the controls its state allows.
func centerRow(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme, task domainsftp.Task) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(4).
		Padding(8).Background(t.Surface).Radius(t.Radius).
		Border(1, t.Border).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, transferName(m, task)).FontSize(12).Bold().Grow(1).
				MinWidth(0).SingleLine().TextColor(t.Text)
			ui.Text(c, transferStatusLabel(m, task.Status)).FontSize(11).
				TextColor(transferStatusColor(t, task.Status)).NoWrap()
		})
		ui.Progress(c, float64(task.Percent())/100).FillWidth()
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, transferDetail(m, task)).FontSize(10).
				TextColor(t.TextMuted).Grow(1).MinWidth(0).SingleLine()
			transferButtons(c, s, m, task)
		})
		// The recovery sentence gets a line of its own rather than a tail on
		// the detail line: beside the panes, progress, and file count it ran
		// out of room in English at both supported window sizes. It wraps
		// instead of ending in an ellipsis, so the whole instruction is read.
		if task.ReconnectRequired {
			ui.Text(c, m.SftpTransferReconnectRequired()).FontSize(10).
				TextColor(uitheme.WarningText(t)).FillWidth().MinWidth(0)
		}
	})
}

// transferButtons renders only the controls the row's lifecycle state
// permits, so a completed row never offers Pause and an interrupted one never
// offers Resume without a live request.
func transferButtons(c *ui.Context, s *transferstate.State, m locales.Messages, task domainsftp.Task) {
	if task.Status.CanPause() {
		if ui.Button(c, m.SftpTransferPause()).Clicked() {
			s.PauseTransfer(task.ID)
		}
	}
	if task.Status.CanResume() {
		if ui.Button(c, m.SftpTransferResume()).Clicked() {
			s.ResumeTransfer(task.ID)
		}
	}
	if task.Status.CanRetry() {
		if ui.Button(c, m.SftpTransferRetry()).Clicked() {
			s.RetryTransfer(task.ID)
		}
	}
	if task.Status.CanCancel() {
		if ui.Button(c, m.SftpCancel()).Clicked() {
			s.CancelTransfer(task.ID)
		}
	}
}

func centerFooter(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	limit := m.SftpTransferLimit(s.TransferConcurrency())
	hint := ""
	if chord := s.Shortcut("transferCenter"); chord != "" {
		hint = m.SftpTransferShortcut(chord)
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).
		Justify(ui.SpaceBetween).Children(func() {
		ui.Text(c, limit).FontSize(10).TextColor(t.TextMuted).NoWrap()
		if hint != "" {
			ui.Text(c, hint).FontSize(10).TextColor(t.TextMuted).NoWrap()
		}
	})
}

// transferStatusLabel localizes a queue row's stable status token.
func transferStatusLabel(m locales.Messages, status domainsftp.TaskStatus) string {
	switch status {
	case domainsftp.TaskPending:
		return m.SftpTransferStatusPending()
	case domainsftp.TaskQueued:
		return m.SftpTransferStatusQueued()
	case domainsftp.TaskTransferring:
		return m.SftpTransferStatusTransferring()
	case domainsftp.TaskPausing:
		return m.SftpTransferStatusPausing()
	case domainsftp.TaskPaused:
		return m.SftpTransferStatusPaused()
	case domainsftp.TaskAttention:
		return m.SftpTransferStatusAttention()
	case domainsftp.TaskInterrupted:
		return m.SftpTransferStatusInterrupted()
	case domainsftp.TaskCompleted:
		return m.SftpTransferStatusCompleted()
	case domainsftp.TaskFailed:
		return m.SftpTransferStatusFailed()
	case domainsftp.TaskCancelled:
		return m.SftpTransferStatusCancelled()
	default:
		return ""
	}
}

func transferStatusColor(t *ui.Theme, status domainsftp.TaskStatus) ui.Color {
	switch status {
	case domainsftp.TaskCompleted:
		return t.Success
	case domainsftp.TaskFailed:
		return uitheme.DangerText(t)
	case domainsftp.TaskAttention, domainsftp.TaskInterrupted:
		return uitheme.WarningText(t)
	default:
		return t.TextMuted
	}
}

// transferDetail is the row's secondary line: the panes it connects, its
// byte or file progress, and either the ETA or the measured speed.
func transferDetail(m locales.Messages, task domainsftp.Task) string {
	parts := make([]string, 0, 5)
	if task.SourceLabel != "" || task.DestLabel != "" {
		parts = append(parts, paneLabel(m, task.SourceLabel)+" → "+paneLabel(m, task.DestLabel))
	}
	if progress := transferProgress(m, task); progress != "" {
		parts = append(parts, progress)
	}
	if task.FilesTotal > 1 {
		// A multi-file Transfer shows its per-file progress beside the byte
		// total; the row's name is the file currently being copied.
		parts = append(parts, m.SftpTransferFiles(task.FilesDone, task.FilesTotal))
	}
	if task.Skipped > 0 {
		parts = append(parts, m.SftpTransferSkipped(task.Skipped))
	}
	if eta := etaText(m, task.ETASeconds()); eta != "" {
		parts = append(parts, eta)
	} else if task.Status == domainsftp.TaskTransferring && task.Speed > 0 {
		parts = append(parts, m.SftpTransferSpeed(domainsftp.FormatSize(task.Speed)))
	}
	if task.Err != nil {
		parts = append(parts, errorText(m, task.Err))
	} else if task.Error != "" {
		parts = append(parts, errorKindText(m, apptransfer.ErrorKind(task.Error)))
	}
	return strings.Join(parts, " · ")
}

// transferName is the row's label. The domain supplies a name for a single
// source; a multi-file Transfer gets a localized count until its first file
// is known, then the current file's name.
func transferName(m locales.Messages, task domainsftp.Task) string {
	if name := task.Name(); name != "" {
		return name
	}
	if task.CurrentFile != "" {
		return baseName(task.CurrentFile)
	}
	if len(task.Sources) > 1 {
		return m.SftpTransferItems(len(task.Sources))
	}
	return ""
}

// paneLabel translates the local pane sentinel; a Host label is user text and
// stays as it is.
func paneLabel(m locales.Messages, label string) string {
	if label == "local" {
		return m.SftpLocal()
	}
	return label
}

// transferProgress prefers the byte totals and falls back to the file count
// before the copy engine has measured any size.
func transferProgress(m locales.Messages, task domainsftp.Task) string {
	if task.BytesTotal > 0 {
		return m.SftpTransferProgress(
			domainsftp.FormatSize(task.BytesDone),
			domainsftp.FormatSize(task.BytesTotal),
			task.Percent(),
		)
	}
	if task.FilesTotal > 0 {
		return m.SftpTransferFiles(task.FilesDone, task.FilesTotal)
	}
	return ""
}

// etaText formats a remaining-seconds estimate, or "" when there is no
// honest estimate to show.
func etaText(m locales.Messages, seconds int) string {
	switch {
	case seconds <= 0:
		return ""
	case seconds < 60:
		return m.SftpTransferEtaSeconds(seconds)
	case seconds < 3600:
		return m.SftpTransferEtaMinutes((seconds + 59) / 60)
	default:
		return m.SftpTransferEtaHours((seconds + 3599) / 3600)
	}
}
