package vault

import (
	"strconv"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// importFormatOptions returns the selector's canonical codes and localized
// labels in the same order.
func importFormatOptions(m locales.Messages) (codes, labels []string) {
	codes = []string{"auto"}
	labels = []string{m.VaultImportFormatAuto()}
	for _, format := range vaultimport.Formats() {
		codes = append(codes, string(format))
		labels = append(labels, importFormatLabelFor(m, format))
	}
	return codes, labels
}

func importFormatLabelFor(m locales.Messages, format vaultimport.Format) string {
	switch format {
	case vaultimport.FormatCSV:
		return m.VaultImportFormatCSV()
	case vaultimport.FormatPuTTY:
		return m.VaultImportFormatPuTTY()
	case vaultimport.FormatMobaXterm:
		return m.VaultImportFormatMobaXterm()
	case vaultimport.FormatFinalShell:
		return m.VaultImportFormatFinalShell()
	case vaultimport.FormatSecureCRT:
		return m.VaultImportFormatSecureCRT()
	case vaultimport.FormatSSHConfig:
		return m.VaultImportFormatSSHConfig()
	default:
		return m.VaultImportFormatAuto()
	}
}

func importFormatLabel(m locales.Messages, code string) string {
	codes, labels := importFormatOptions(m)
	if code == "" {
		code = "auto"
	}
	for index, candidate := range codes {
		if candidate == code {
			return labels[index]
		}
	}
	return labels[0]
}

func importFormatCode(m locales.Messages, label string) string {
	codes, labels := importFormatOptions(m)
	for index, candidate := range labels {
		if candidate == label {
			return codes[index]
		}
	}
	return "auto"
}

func importMergeLabel(m locales.Messages, code string) string {
	if code == string(appvault.ImportMergeOverwrite) {
		return m.VaultImportMergeOverwrite()
	}
	return m.VaultImportMergeSkip()
}

func importMergeCode(m locales.Messages, label string) string {
	if label == m.VaultImportMergeOverwrite() {
		return string(appvault.ImportMergeOverwrite)
	}
	return string(appvault.ImportMergeSkip)
}

// importScrollbarGutter is the width of the scroll bar's track for theme t:
// the thumb (t.ScrollbarWidth DIPs, 6 when unset) plus the 6 DIPs it is inset
// and padded by in MyGo's scrollBars geometry. Reserving it keeps the panel's
// growing fields, such as the master-password input, from running under the
// bar.
func importScrollbarGutter(t *ui.Theme) float32 {
	width := t.ScrollbarWidth
	if width <= 0 {
		width = 6
	}
	return width + 6
}

// importFieldLayout is the import panel geometry the regression tests assert:
// the master-password field and the scroll viewport it sits in, plus the
// gutter that viewport reserves for the bar.
type importFieldLayout struct {
	master, viewport ui.Rect
	gutter           float32
}

// importLayoutProbe, when non-nil, receives the import panel's laid-out
// geometry on each build. Tests use it to assert the field clears the scroll
// bar; the app leaves it nil.
var importLayoutProbe func(importFieldLayout)

// importPanel renders the external-inventory preview. The header and action
// bar stay fixed; the summary, problem report, and row list scroll, so the
// panel stays usable at 720x480.
func importPanel(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	ui.Text(c, m.VaultImportTitle()).FontSize(20).Bold()
	problem(c, s, t)
	if problem := s.ImportProblem(); problem != vaultstate.ProblemNone {
		ui.Text(c, problemText(m, problem)).TextColor(t.Danger)
	}

	preview := s.ImportPreview()
	if preview == nil {
		ui.Text(c, m.VaultImportIntro()).FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, m.VaultImportChooseFile()).Clicked() {
				s.ChooseImportFile()
				c.Invalidate()
			}
			if ui.Button(c, m.VaultHostCancel()).Clicked() {
				s.CancelImport()
				c.Invalidate()
			}
		})
		return
	}

	// PaddingEnd reserves the overlay scroll bar's track: the scroll content
	// is laid out in the remaining width, so grow columns and inputs end
	// left of the bar instead of under it.
	gutter := importScrollbarGutter(t)
	scrollBox := ui.Scroll(c).Grow(1).FillWidth().PaddingEnd(gutter).TrackScroll(scroll)
	scrollBox.Children(func() {
		ui.Text(c, m.VaultImportSummary(
			preview.Counts.Total, preview.Counts.New, preview.Counts.Conflicts,
			preview.Counts.Duplicates, preview.Counts.Invalid,
		)).FontSize(12)
		if fileName := s.ImportFileName(); fileName != "" {
			ui.Text(c, fileName).FontSize(12).TextColor(t.TextMuted)
		}
		if len(preview.GroupsToCreate) > 0 {
			ui.Text(c, m.VaultImportGroups(len(preview.GroupsToCreate))).
				FontSize(12).TextColor(t.TextMuted)
		}

		ui.Row(c).FillWidth().Gap(12).Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				ui.Text(c, m.VaultImportFormat()).FontSize(12).TextColor(t.TextMuted)
				selected := importFormatLabel(m, s.ImportFormatValue())
				_, labels := importFormatOptions(m)
				if ui.Select(c, &selected, labels).Width(220).Changed() {
					s.SetImportFormat(importFormatCode(m, selected))
					c.Invalidate()
				}
			})
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, m.VaultImportMasterPassword()).FontSize(12).TextColor(t.TextMuted)
				field := ui.TextInput(c, s.ImportMasterPasswordInput()).Password()
				if importLayoutProbe != nil {
					importLayoutProbe(importFieldLayout{
						master:   field.LayoutBounds(),
						viewport: scrollBox.LayoutBounds(),
						gutter:   gutter,
					})
				}
				if field.Changed() {
					s.RefreshImport()
					c.Invalidate()
				}
			})
		})
		ui.Text(c, m.VaultImportMasterHint()).FontSize(11).TextColor(t.TextMuted)

		ui.Text(c, m.VaultImportMerge()).FontSize(12).TextColor(t.TextMuted)
		mergeLabels := []string{m.VaultImportMergeSkip(), m.VaultImportMergeOverwrite()}
		merge := importMergeLabel(m, s.ImportMergeValue())
		if ui.Select(c, &merge, mergeLabels).Width(260).Changed() {
			s.SetImportMerge(importMergeCode(m, merge))
			c.Invalidate()
		}

		ui.Text(c, m.VaultImportIssues()).Bold()
		if len(preview.Issues) == 0 {
			ui.Text(c, m.VaultImportNoIssues()).FontSize(12).TextColor(t.TextMuted)
		}
		for _, item := range preview.Issues {
			message := importIssueMessage(m, item.Code)
			line := m.VaultImportIssueDocument(message)
			if item.Row > 0 {
				line = m.VaultImportIssueLine(item.Row, message)
			}
			color := t.TextMuted
			if item.Severity == vaultimport.SeverityError {
				color = t.Danger
			}
			ui.Text(c, line).FontSize(12).TextColor(color)
		}

		ui.Text(c, m.VaultImportRows()).Bold()
		for index := 0; index < s.ImportRowCount(); index++ {
			row, ok := s.ImportRow(index)
			if !ok {
				continue
			}
			label := row.Label
			if label == "" {
				label = row.Hostname
			}
			checked := s.ImportRowSelected(index)
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				if ui.Checkbox(c, &checked, label).Changed() {
					s.ToggleImportRow(index)
					c.Invalidate()
				}
				if row.SourceRow > 0 {
					ui.Text(c, m.VaultImportRowNumber(row.SourceRow)).FontSize(11).
						TextColor(t.TextMuted)
				}
				address := row.Hostname
				if row.Port > 0 {
					address += ":" + strconv.Itoa(row.Port)
				}
				ui.Text(c, address).FontSize(12).TextColor(t.TextMuted).Grow(1)
				ui.Text(c, importStatusText(m, row.Status)).FontSize(12).
					TextColor(importStatusColor(t, row.Status))
			})
			if row.ExistingLabel != "" {
				ui.Text(c, m.VaultImportConflictWith(row.ExistingLabel)).
					FontSize(11).TextColor(t.TextMuted)
			}
		}
	})

	if selected := s.ImportSelectedCount(); selected == 0 {
		ui.Text(c, m.VaultImportNoneSelected()).FontSize(12).TextColor(t.Warning)
	} else {
		ui.Text(c, m.VaultImportSelected(selected)).FontSize(12).TextColor(t.TextMuted)
	}
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, m.VaultImportImport()).Clicked() {
			_ = s.ConfirmImport()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelImport()
			c.Invalidate()
		}
	})
}

func importStatusText(m locales.Messages, status appvault.ImportStatus) string {
	switch status {
	case appvault.ImportStatusConflict:
		return m.VaultImportStatusConflict()
	case appvault.ImportStatusDuplicate:
		return m.VaultImportStatusDuplicate()
	case appvault.ImportStatusInvalid:
		return m.VaultImportStatusInvalid()
	default:
		return m.VaultImportStatusNew()
	}
}

func importStatusColor(t *ui.Theme, status appvault.ImportStatus) ui.Color {
	switch status {
	case appvault.ImportStatusConflict:
		return t.Warning
	case appvault.ImportStatusDuplicate:
		return t.TextMuted
	case appvault.ImportStatusInvalid:
		return t.Danger
	default:
		return t.Success
	}
}

// importIssueMessage localizes a stable parse issue code. The UI never shows
// the parser's raw detail text, which may contain file-specific data.
func importIssueMessage(m locales.Messages, code vaultimport.IssueCode) string {
	switch code {
	case vaultimport.IssueCSVEmpty:
		return m.VaultImportIssueCSVEmpty()
	case vaultimport.IssueCSVHeader:
		return m.VaultImportIssueCSVHeader()
	case vaultimport.IssueCSVQuoting:
		return m.VaultImportIssueCSVQuoting()
	case vaultimport.IssueInvalidHostname:
		return m.VaultImportIssueInvalidHostname()
	case vaultimport.IssueMissingHostname:
		return m.VaultImportIssueMissingHostname()
	case vaultimport.IssueInvalidPort:
		return m.VaultImportIssueInvalidPort()
	case vaultimport.IssueUnsupportedProtocol:
		return m.VaultImportIssueUnsupportedProtocol()
	case vaultimport.IssueUnsupportedEntry:
		return m.VaultImportIssueUnsupportedEntry()
	case vaultimport.IssueInvalidProxy:
		return m.VaultImportIssueInvalidProxy()
	case vaultimport.IssueInvalidField:
		return m.VaultImportIssueInvalidField()
	case vaultimport.IssueInvalidJSON:
		return m.VaultImportIssueInvalidJSON()
	case vaultimport.IssuePasswordUndecrypt:
		return m.VaultImportIssuePasswordUndecryptable()
	case vaultimport.IssueMasterPassword:
		return m.VaultImportIssueMasterPassword()
	case vaultimport.IssuePasswordsInRegistry:
		return m.VaultImportIssuePasswordsInRegistry()
	case vaultimport.IssuePasswordUnmatched:
		return m.VaultImportIssuePasswordUnmatched()
	case vaultimport.IssueDuplicateInFile:
		return m.VaultImportIssueDuplicateInFile()
	case vaultimport.IssueInvalidJump:
		return m.VaultImportIssueInvalidJump()
	case vaultimport.IssueCyclicJump:
		return m.VaultImportIssueCyclicJump()
	case vaultimport.IssueKeyUnreadable:
		return m.VaultImportIssueKeyUnreadable()
	case vaultimport.IssuePassphraseConflict:
		return m.VaultImportIssuePassphraseConflict()
	case vaultimport.IssueFormulaNeutralized:
		return m.VaultImportIssueFormulaNeutralized()
	case vaultimport.IssueNothingParsed:
		return m.VaultImportIssueNothingParsed()
	case vaultimport.IssueX11NotImported:
		return m.VaultImportIssueX11()
	default:
		return m.VaultImportIssueInvalidField()
	}
}

// importOutcomeBanner reports the last completed import on the list, with
// counts only.
func importOutcomeBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	outcome, ok := s.ImportOutcome()
	if !ok {
		return
	}
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Text(c, m.VaultImportDone(outcome.Imported, outcome.Overwritten,
			outcome.Skipped, outcome.Groups)).FontSize(12).Grow(1)
		if ui.Button(c, m.VaultImportDismiss()).Clicked() {
			s.DismissImportOutcome()
			c.Invalidate()
		}
	})
}
