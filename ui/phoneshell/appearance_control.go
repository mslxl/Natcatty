package phoneshell

// The Session's quick appearance controls. A Session draws the color scheme
// and font size bar under its header, so a reader who opens a different Host
// does not have to leave the terminal to make its text legible. Each tap
// writes the shared Appearance document through the Settings service and
// restyles the live terminal on the same frame, exactly as the Settings page
// does; the next terminal built reads the same values.

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	domainmodel "github.com/mslxl/natcatty/domain/model"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// togglePaneAppearance shows or hides one Pane's quick appearance bar. The
// caller holds no lock; the state lives on the Pane, so focus changes do not
// carry it elsewhere.
func (t *sessionTabs) togglePaneAppearance(tabID, paneID string) {
	t.mu.Lock()
	tab := t.find(tabID)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	pane := tab.paneOf(domainmodel.PaneID(paneID))
	if pane == nil {
		t.mu.Unlock()
		return
	}
	pane.appearanceOpen = !pane.appearanceOpen
	if !pane.appearanceOpen {
		pane.appearanceProblem = ""
	}
	t.mu.Unlock()
}

// drawQuickAppearance is the bar the Palette header button opens. It writes
// nothing on a frame with no tap, so a Session that is simply being read costs
// no preference updates.
func (t *sessionTabs) drawQuickAppearance(c *ui.Context, view tabView) {
	if t.settings == nil {
		kit.EmptyHint(c, "Appearance settings are not available on this device.")
		return
	}
	appearance := t.settings.Preferences().Appearance
	size := int(appearance.TerminalFontSize)
	if size < domainpreferences.MinTerminalFontSize {
		size = domainpreferences.MinTerminalFontSize
	}
	if size > domainpreferences.MaxTerminalFontSize {
		size = domainpreferences.MaxTerminalFontSize
	}
	_, schemeName := quickTerminalScheme(appearance, c.Theme().Dark)
	ui.Column(c).FillWidth().Padding(4, 12, 8, 12).Gap(6).
		Background(c.Theme().Surface).Children(func() {
		if view.appearanceProblem != "" {
			kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, view.appearanceProblem, nil)
		}
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("Font size: %d", size)).
				FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			if kit.IconButton(c, icons.Minus, "Smaller terminal font").Clicked() {
				t.stepTerminalFont(view, -1)
			}
			if kit.IconButton(c, icons.Plus, "Larger terminal font").Clicked() {
				t.stepTerminalFont(view, 1)
			}
			ui.Text(c, "Scheme: "+schemeName).
				FontSize(12).TextColor(c.Theme().TextMuted).SingleLine().
				Grow(1).MinWidth(0)
			if kit.IconButton(c, icons.ChevronLeft, "Previous color scheme").Clicked() {
				t.stepTerminalScheme(view, -1, c.Theme().Dark)
			}
			if kit.IconButton(c, icons.ChevronRight, "Next color scheme").Clicked() {
				t.stepTerminalScheme(view, 1, c.Theme().Dark)
			}
		})
	})
}

// stepTerminalFont moves the shared font size by one within the same bounds
// the Settings stepper enforces, then restyles the live terminal.
func (t *sessionTabs) stepTerminalFont(view tabView, delta int) {
	current := int(t.settings.Preferences().Appearance.TerminalFontSize)
	if current < domainpreferences.MinTerminalFontSize {
		current = domainpreferences.MinTerminalFontSize
	}
	if current > domainpreferences.MaxTerminalFontSize {
		current = domainpreferences.MaxTerminalFontSize
	}
	next := current + delta
	if next < domainpreferences.MinTerminalFontSize || next > domainpreferences.MaxTerminalFontSize {
		return
	}
	t.applyQuickAppearance(view, func(s *domainpreferences.Settings) {
		s.Appearance.TerminalFontSize = float64(next)
	})
}

// stepTerminalScheme cycles the visible schemes in the catalog's order. The
// first manual choice turns FollowAppTerminalTheme off: otherwise the next
// interface appearance change would silently overwrite the scheme the user
// just picked.
func (t *sessionTabs) stepTerminalScheme(view tabView, delta int, systemDark bool) {
	themes := domaintheme.VisibleTerminalThemes()
	if len(themes) == 0 {
		return
	}
	current, _ := quickTerminalScheme(t.settings.Preferences().Appearance, systemDark)
	index := 0
	for i, theme := range themes {
		if theme.ID == current {
			index = i
			break
		}
	}
	next := themes[(index+delta+len(themes))%len(themes)]
	t.applyQuickAppearance(view, func(s *domainpreferences.Settings) {
		s.Appearance.TerminalTheme = next.ID
		s.Appearance.FollowAppTerminalTheme = false
	})
}

// applyQuickAppearance writes the shared document and restyles the live
// screens on the same tap. A failed write keeps the bar open with the reason
// and changes nothing.
func (t *sessionTabs) applyQuickAppearance(view tabView, change func(*domainpreferences.Settings)) {
	if t.settings == nil {
		return
	}
	if err := t.settings.UpdatePreferences(change); err != nil {
		t.setAppearanceProblem(view.id, view.focus, "Could not save the terminal appearance.")
		return
	}
	t.setAppearanceProblem(view.id, view.focus, "")
	t.applyAppearance()
}

// setAppearanceProblem records the last quick-appearance failure on the Pane
// the bar belongs to.
func (t *sessionTabs) setAppearanceProblem(tabID, paneID, problem string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tab := t.find(tabID)
	if tab == nil {
		return
	}
	if pane := tab.paneOf(domainmodel.PaneID(paneID)); pane != nil {
		pane.appearanceProblem = problem
	}
}

// quickTerminalScheme resolves the scheme the bar names. It follows the same
// FollowAppTerminalTheme rule the terminal's own appearance mapping uses, so
// the label never disagrees with what the screen draws.
func quickTerminalScheme(appearance domainpreferences.Appearance, systemDark bool) (string, string) {
	id := appearance.TerminalTheme
	if appearance.FollowAppTerminalTheme {
		dark := appearance.ThemeMode == domainpreferences.ThemeDark
		if appearance.ThemeMode == domainpreferences.ThemeSystem {
			dark = systemDark
		}
		preset := domaintheme.ResolvePreset(dark, appearance.LightTheme, appearance.DarkTheme)
		if match, ok := domaintheme.TerminalThemeForPreset(dark, preset.ID); ok {
			id = match
		}
	}
	scheme, ok := domaintheme.LookupTerminalTheme(id)
	if !ok {
		scheme, _ = domaintheme.LookupTerminalTheme(domaintheme.DefaultTerminalTheme)
	}
	return scheme.ID, scheme.Name
}
