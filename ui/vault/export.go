package vault

import (
	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// exportFormatOptions returns the selector's canonical codes and localized
// labels in menu order. The codes are application format constants; the view
// never invents one.
func exportFormatOptions(m locales.Messages) (codes, labels []string) {
	codes = []string{
		string(appvault.ExportJSON), string(appvault.ExportCSV),
		string(appvault.ExportSnippets), string(appvault.ExportNotesMarkdown),
		string(appvault.ExportNotesHTML), string(appvault.ExportNotesZIP),
		string(appvault.ExportKeys), string(appvault.ExportThemes),
		string(appvault.ExportLogsJSON), string(appvault.ExportLogsCSV),
	}
	labels = []string{
		m.VaultExportFormatJSON(), m.VaultExportFormatCSV(),
		m.VaultExportFormatSnippets(), m.VaultExportFormatNotesMarkdown(),
		m.VaultExportFormatNotesHTML(), m.VaultExportFormatNotesZIP(),
		m.VaultExportFormatKeys(), m.VaultExportFormatThemes(),
		m.VaultExportFormatLogsJSON(), m.VaultExportFormatLogsCSV(),
	}
	return codes, labels
}

func exportFormatLabel(m locales.Messages, code string) string {
	codes, labels := exportFormatOptions(m)
	for index, candidate := range codes {
		if candidate == code {
			return labels[index]
		}
	}
	return labels[0]
}

func exportFormatCode(m locales.Messages, label string) string {
	codes, labels := exportFormatOptions(m)
	for index, candidate := range labels {
		if candidate == label {
			return codes[index]
		}
	}
	return codes[0]
}

func exportScopeLabel(m locales.Messages, code string) string {
	if code == vaultstate.ExportScopeSelected {
		return m.VaultExportScopeSelected()
	}
	if code == vaultstate.ExportScopeAll {
		return m.VaultExportScopeAll()
	}
	return m.VaultExportScopeAll()
}

func exportScopeCode(m locales.Messages, label string) string {
	if label == m.VaultExportScopeAll() {
		return vaultstate.ExportScopeAll
	}
	return vaultstate.ExportScopeSelected
}

// exportFieldLayout is the export panel geometry the regression tests assert:
// the secrets toggle and the scroll viewport it sits in, plus the gutter that
// viewport reserves for the bar.
type exportFieldLayout struct {
	secrets, viewport ui.Rect
	gutter            float32
}

// exportLayoutProbe, when non-nil, receives the export panel's laid-out
// geometry on each build. Tests use it to assert the content clears the scroll
// bar; the app leaves it nil.
var exportLayoutProbe func(exportFieldLayout)

// exportPanel renders the export options. The header and action bar stay
// fixed; the options scroll inside a gutter-reserving viewport, so the panel
// stays readable at 720x480.
func exportPanel(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	ui.Text(c, m.VaultExportTitle()).FontSize(20).Bold()
	problem(c, s, t)
	if issue := s.ExportProblem(); issue != vaultstate.ProblemNone {
		ui.Text(c, problemText(m, issue)).TextColor(t.Danger)
	}

	gutter := importScrollbarGutter(t)
	scrollBox := ui.Scroll(c).Grow(1).FillWidth().PaddingEnd(gutter).TrackScroll(scroll)
	scrollBox.Children(func() {
		ui.Text(c, m.VaultExportIntro()).FontSize(12).TextColor(t.TextMuted)

		format := exportFormatLabel(m, s.ExportFormatValue())
		_, labels := exportFormatOptions(m)
		ui.Text(c, m.VaultExportFormat()).FontSize(12).TextColor(t.TextMuted)
		if ui.Select(c, &format, labels).Width(280).Changed() {
			s.SetExportFormat(exportFormatCode(m, format))
			c.Invalidate()
		}

		ui.Text(c, m.VaultExportScope()).FontSize(12).TextColor(t.TextMuted)
		scopeLabels := []string{m.VaultExportScopeAll(), m.VaultExportScopeSelected()}
		scope := exportScopeLabel(m, s.ExportScopeValue())
		if ui.Select(c, &scope, scopeLabels).Width(280).Changed() {
			s.SetExportScope(exportScopeCode(m, scope))
			c.Invalidate()
		}
		if s.ExportScopeValue() == vaultstate.ExportScopeSelected {
			ui.Text(c, m.VaultExportSelectedHost(exportSelectedLabel(s))).
				FontSize(11).TextColor(t.TextMuted)
		}

		include := s.ExportIncludesSecrets()
		toggle := ui.Checkbox(c, &include, m.VaultExportIncludeSecrets())
		if toggle.Changed() {
			s.SetExportIncludeSecrets(include)
			c.Invalidate()
		}
		if exportLayoutProbe != nil {
			exportLayoutProbe(exportFieldLayout{
				secrets:  toggle.LayoutBounds(),
				viewport: scrollBox.LayoutBounds(),
				gutter:   gutter,
			})
		}
		switch {
		case !s.ExportIncludesSecrets():
			ui.Text(c, m.VaultExportSecretsHint()).FontSize(11).TextColor(t.TextMuted)
		case s.ExportSecretsConfirmed():
			ui.Text(c, m.VaultExportSecretsConfirmed()).FontSize(11).TextColor(t.Success)
		default:
			ui.Text(c, m.VaultExportSecretsWarning()).FontSize(11).TextColor(t.Warning)
			if ui.Button(c, m.VaultExportConfirmSecrets()).Clicked() {
				s.ConfirmExportSecrets()
				c.Invalidate()
			}
		}
	})

	if s.ExportIncludesSecrets() && !s.ExportSecretsConfirmed() {
		ui.Text(c, m.VaultExportUnconfirmed()).FontSize(12).TextColor(t.Warning)
	} else {
		ui.Text(c, m.VaultExportSaveHint()).FontSize(12).TextColor(t.TextMuted)
	}
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, m.VaultExportExport()).Clicked() {
			_ = s.Export(m.VaultExportNotesPageTitle())
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelExport()
			c.Invalidate()
		}
	})
}

// exportSelectedLabel names the selected Host in the scope hint.
func exportSelectedLabel(s *vaultstate.State) string {
	selected := s.Selected()
	for _, host := range s.Items() {
		if host.ID == selected {
			return host.Label
		}
	}
	return string(selected)
}

// exportOutcomeBanner reports the last saved export and anything that was
// left out.
func exportOutcomeBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	outcome, ok := s.ExportOutcome()
	if !ok {
		return
	}
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).Children(func() {
			ui.Text(c, m.VaultExportDone(outcome.Path, outcome.Bytes)).FontSize(12)
			if outcome.Unreadable > 0 {
				ui.Text(c, m.VaultExportUnreadable(outcome.Unreadable)).
					FontSize(11).TextColor(t.Warning)
			}
		})
		if ui.Button(c, m.VaultExportDismiss()).Clicked() {
			s.DismissExportOutcome()
			c.Invalidate()
		}
	})
}
