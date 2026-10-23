package vault

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// backupPanelLayout is the geometry the regression tests assert: the retention
// field and the scroll viewport it sits above, plus the gutter that viewport
// reserves for the overlay scroll bar.
type backupPanelLayout struct {
	retention, viewport ui.Rect
	// action is the first row's Restore button, laid out inside the scrolling
	// list. Tests use it to prove row content clears the overlay scroll bar.
	action ui.Rect
	gutter float32
}

// backupLayoutProbe, when non-nil, receives the panel's laid-out geometry on
// each build. Tests use it to assert content clears the scroll bar; the app
// leaves it nil.
var backupLayoutProbe func(backupPanelLayout)

// backupPanel renders the local backup history. The header, retention control,
// confirmations, and clear action stay fixed; only the list scrolls, so the
// panel stays usable at 720x480.
func backupPanel(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, m.VaultBackupTitle()).FontSize(20).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultBackupBack()).Clicked() {
			s.CancelBackups()
			c.Invalidate()
		}
	})

	if !s.BackupEncryptionAvailable() {
		ui.Text(c, m.VaultBackupUnavailable()).FontSize(12).TextColor(t.Warning)
	} else {
		ui.Text(c, m.VaultBackupIntro()).FontSize(12).TextColor(t.TextMuted)
	}

	var retentionRect ui.Rect
	ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
		ui.Text(c, m.VaultBackupRetentionLabel()).FontSize(12).TextColor(t.TextMuted)
		field := ui.TextInput(c, s.BackupRetentionInput()).Width(90)
		retentionRect = field.LayoutBounds()
		if ui.PrimaryButton(c, m.VaultBackupRetentionSave()).Clicked() {
			s.SaveBackupRetention()
			c.Invalidate()
		}
	})
	ui.Text(c, m.VaultBackupRetentionHint(
		domainbackup.MinRetention, domainbackup.MaxRetention, domainbackup.DefaultRetention,
	)).FontSize(11).TextColor(t.TextMuted)

	if problem := s.BackupProblem(); problem != vaultstate.ProblemNone {
		ui.Text(c, problemText(m, problem)).TextColor(t.Danger)
	}
	backupNoticeBanner(c, s, m, t)

	if pending := s.BackupPendingRestore(); pending != "" {
		message := m.VaultBackupRestoreConfirmUnknown()
		for _, item := range s.BackupItems() {
			if item.ID == pending {
				message = m.VaultBackupRestoreConfirm(backupTime(item))
				break
			}
		}
		ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Wrap().Children(func() {
			ui.Text(c, message).Grow(1).TextColor(t.Danger)
			if ui.Button(c, m.VaultHostCancel()).Clicked() {
				s.CancelRestoreBackup()
				c.Invalidate()
			}
			if ui.PrimaryButton(c, m.VaultBackupRestoreConfirmAction()).Clicked() {
				_ = s.ConfirmRestoreBackup()
				c.Invalidate()
			}
		})
	}

	if s.BackupPendingClear() {
		count := len(s.BackupItems())
		ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Wrap().Children(func() {
			ui.Text(c, m.VaultBackupClearConfirm(count)).Grow(1).TextColor(t.Danger)
			if ui.Button(c, m.VaultHostCancel()).Clicked() {
				s.CancelClearBackups()
				c.Invalidate()
			}
			if ui.Button(c, m.VaultBackupClear()).Clicked() {
				_ = s.ConfirmClearBackups()
				c.Invalidate()
			}
		})
	}

	gutter := scrollbarGutter(t)
	var firstAction ui.Rect
	scrollBox := ui.Scroll(c).Grow(1).FillWidth().PaddingEnd(gutter).TrackScroll(scroll)
	scrollBox.Children(func() {
		if len(s.BackupItems()) == 0 {
			ui.Text(c, m.VaultBackupEmpty()).FontSize(12).TextColor(t.TextMuted)
			return
		}
		for index, item := range s.BackupItems() {
			action := backupRow(c, s, item, m, t)
			if index == 0 {
				firstAction = action
			}
		}
	})
	if backupLayoutProbe != nil {
		backupLayoutProbe(backupPanelLayout{
			retention: retentionRect,
			viewport:  scrollBox.LayoutBounds(),
			action:    firstAction,
			gutter:    gutter,
		})
	}

	ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
		ui.Text(c, m.VaultBackupListTitle(len(s.BackupItems()))).FontSize(12).
			TextColor(t.TextMuted).Grow(1)
		if len(s.BackupItems()) > 0 && !s.BackupPendingClear() {
			if ui.Button(c, m.VaultBackupClear()).Clicked() {
				s.RequestClearBackups()
				c.Invalidate()
			}
		}
	})
}

func backupRow(c *ui.Context, s *vaultstate.State, item domainbackup.Summary,
	m locales.Messages, t *ui.Theme) ui.Rect {

	var action ui.Rect
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Wrap().Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
				ui.Text(c, backupReasonText(m, item.Reason)).Bold()
				ui.Text(c, backupTime(item)).FontSize(12).TextColor(t.TextMuted)
			})
			ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
				ui.Text(c, formatBackupSize(item.SizeBytes)).FontSize(12)
				ui.Text(c, m.VaultBackupRecords(item.Counts.Total())).FontSize(12).
					TextColor(t.TextMuted)
				ui.Text(c, m.VaultBackupFingerprint(shortFingerprint(item.Fingerprint))).
					FontSize(11).TextColor(t.TextMuted)
			})
		})
		if s.BackupEncryptionAvailable() {
			button := ui.Button(c, m.VaultBackupRestore())
			action = button.LayoutBounds()
			if button.Clicked() {
				s.RequestRestoreBackup(item.ID)
				c.Invalidate()
			}
		}
	})
	return action
}

// backupNoticeBanner reports outcomes the user would otherwise never see:
// retention pruning and automatic backups skipped by the cap or a missing
// keychain. A routine "created" or "deduped" result stays quiet.
func backupNoticeBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	notice := s.BackupNotice()
	var message string
	var color ui.Color = t.TextMuted
	switch notice.Kind {
	case appbackup.StatusSkippedTooLarge:
		message = m.VaultBackupSkippedTooLarge()
		color = t.Warning
	case appbackup.StatusSkippedUnavailable:
		message = m.VaultBackupSkippedUnavailable()
		color = t.Warning
	case appbackup.StatusSkippedFailed:
		message = m.VaultBackupSkippedFailed()
		color = t.Warning
	case appbackup.StatusCleared:
		// The clear count is its own field: appending Pruned here would
		// report the same deleted entries a second time.
		message = m.VaultBackupCleared(notice.Deleted)
		color = t.TextMuted
	case appbackup.StatusRestored:
		message = m.VaultBackupRestored()
		color = t.Success
	}
	if notice.Pruned > 0 {
		if message != "" {
			message += " " + m.VaultBackupPruned(notice.Pruned)
		} else {
			message = m.VaultBackupPruned(notice.Pruned)
		}
	}
	if message == "" {
		return
	}
	ui.Text(c, message).FontSize(12).TextColor(color)
}

func backupReasonText(m locales.Messages, reason domainbackup.Reason) string {
	switch reason {
	case domainbackup.ReasonBeforeRestore:
		return m.VaultBackupReasonBeforeRestore()
	case domainbackup.ReasonAppVersionChange:
		return m.VaultBackupReasonAppVersionChange()
	default:
		return m.VaultBackupReasonVaultChange()
	}
}

func backupTime(item domainbackup.Summary) string {
	return time.UnixMilli(item.CreatedAt).Local().Format("2006-01-02 15:04")
}

func shortFingerprint(value string) string {
	if len(value) > 12 {
		return value[:12] + "…"
	}
	return value
}

// formatBackupSize uses binary units, matching the payload cap the panel
// states. It is a display helper only; storage and limits use exact bytes.
func formatBackupSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + " B"
	}
	value := float64(bytes)
	for _, suffix := range []string{"KiB", "MiB", "GiB"} {
		value /= unit
		if value < unit || suffix == "GiB" {
			return strconv.FormatFloat(value, 'f', 1, 64) + " " + suffix
		}
	}
	return strconv.FormatInt(bytes, 10) + " B"
}
