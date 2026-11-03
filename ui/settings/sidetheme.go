package settings

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// TerminalLook reads and changes the terminal appearance settings. The
// preferences service implements it.
type TerminalLook interface {
	Settings() domain.Settings
	Update(change func(*domain.Settings)) error
}

// ThemePanel is the Theme tool of a Session's side panel, as the original's:
// the terminal's font size and color scheme, applied to the open terminals as
// they change. It keeps its own search, apart from the Settings window's.
type ThemePanel struct {
	themes *ThemesState
	look   TerminalLook
	search string
}

// NewThemePanel returns the side panel's Theme tool over the scheme catalog
// and the appearance settings.
func NewThemePanel(themes *ThemesState, look TerminalLook) *ThemePanel {
	return &ThemePanel{themes: themes, look: look}
}

// View renders the tool.
func (p *ThemePanel) View() func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		t := c.Theme()
		appearance := p.look.Settings().Appearance
		ui.Column(c).Fill().MinWidth(0).Children(func() {
			ui.Row(c).FillWidth().Gap(4).Padding(10, 8, 6, 12).AlignItems(ui.Center).Shrink(0).Children(func() {
				ui.Text(c, m.SettingsTerminalFontSize()).FontSize(12).FontWeight(500).Grow(1).MinWidth(0).SingleLine()
				size := appearance.TerminalFontSize
				if size <= 0 {
					size = domain.DefaultTerminalFontSize
				}
				step := func(delta float64) {
					next := min(max(size+delta, domain.MinTerminalFontSize), domain.MaxTerminalFontSize)
					if next != size {
						_ = p.look.Update(func(s *domain.Settings) { s.Appearance.TerminalFontSize = next })
						c.Invalidate()
					}
				}
				if kit.IconButton(c, icons.Minus, m.SidePanelFontSmaller()).Clicked() {
					step(-1)
				}
				ui.Text(c, fmt.Sprintf("%g", size)).FontSize(12).Width(28).TextAlign(ui.Center)
				if kit.IconButton(c, icons.Plus, m.SidePanelFontLarger()).Clicked() {
					step(1)
				}
			})
			ui.Box(c).FillWidth().Height(1).Background(t.Border).Shrink(0)
			ui.Row(c).FillWidth().Padding(8, 8, 4, 8).Shrink(0).Children(func() {
				if ui.SearchField(c, &p.search).Placeholder(m.SettingsTerminalThemeFilter()).
					Label(m.SettingsTerminalThemeFilter()).Grow(1).MinWidth(0).Changed() {
					c.Invalidate()
				}
			})
			follow := appearance.FollowAppTerminalTheme
			schemes := p.matching(follow)
			if len(schemes) == 0 {
				kit.EmptyState(c, icons.Palette, m.SettingsTerminalThemeNoResults(strings.TrimSpace(p.search)), "", nil)
				return
			}
			current := appearance.TerminalTheme
			if current == "" {
				current = domain.DefaultTerminalTheme
			}
			if follow {
				// The window's theme says which scheme the terminal shows.
				preset := domaintheme.ResolvePreset(t.Dark, appearance.LightTheme, appearance.DarkTheme)
				if id, ok := domaintheme.TerminalThemeForPreset(t.Dark, preset.ID); ok {
					current = id
				}
			}
			ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
				ui.Column(c).FillWidth().Padding(0, 8, 8, 8).Gap(6).Children(func() {
					for _, scheme := range schemes {
						p.schemeRow(c, m, t, scheme, scheme.ID == current)
					}
				})
			})
		})
	}
}

// matching are the schemes whose name contains the search. A terminal that
// follows the app offers only the schemes matched to a window theme, as the
// original's, since picking one switches the window theme.
func (p *ThemePanel) matching(follow bool) []domaintheme.TerminalTheme {
	query := strings.ToLower(strings.TrimSpace(p.search))
	catalog := p.themes.Themes()
	if follow {
		catalog = domaintheme.FollowAppTerminalThemes()
	}
	var out []domaintheme.TerminalTheme
	for _, scheme := range catalog {
		if query == "" || strings.Contains(strings.ToLower(scheme.Name), query) {
			out = append(out, scheme)
		}
	}
	return out
}

// schemeRow is one scheme as a small terminal: its background, a prompt in
// its foreground, and its eight normal colors, with the name below.
func (p *ThemePanel) schemeRow(c *ui.Context, m locales.Messages, t *ui.Theme,
	scheme domaintheme.TerminalTheme, current bool) {
	c.Key("side-theme-" + scheme.ID)
	card := ui.ButtonBase(c).Label(scheme.Name).FillWidth().Padding(6).Gap(6).Radius(kit.RadiusControl).
		Border(1, t.Border)
	switch {
	case current:
		card.Border(2, t.Accent).Background(t.Accent.Alpha(0.06))
	case card.Hovered():
		card.Border(1, t.Border.Mix(t.Text, 0.25)).Background(kit.Subtle(t))
	}
	colors := scheme.Colors
	// A button lays its children in a row; the column stacks the name below
	// the preview, so a narrow panel never cuts the name short.
	card.Children(func() {
		ui.Column(c).FillWidth().Gap(6).Children(func() {
			ui.Column(c).FillWidth().Height(44).Radius(6).Padding(6, 8).Gap(6).
				Background(uitheme.TerminalBackground(scheme)).Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "$").FontSize(11).TextColor(uitheme.TerminalSwatch(colors.Green)).SingleLine()
					ui.Text(c, "ls -la").FontSize(11).TextColor(uitheme.TerminalForeground(scheme)).SingleLine()
					ui.Box(c).Size(6, 11).Background(uitheme.TerminalSwatch(colors.Cursor))
				})
				ui.Row(c).Gap(4).Children(func() {
					for _, value := range []string{colors.Red, colors.Green, colors.Yellow, colors.Blue,
						colors.Magenta, colors.Cyan, colors.White, colors.BrightBlack} {
						ui.Box(c).Size(14, 6).Radius(2).Background(uitheme.TerminalSwatch(value))
					}
				})
			})
			ui.Row(c).FillWidth().Gap(6).Padding(0, 2).AlignItems(ui.Center).Children(func() {
				ui.Text(c, scheme.Name).FontSize(12).FontWeight(500).SingleLine().Grow(1).MinWidth(0)
				if scheme.Custom {
					kit.Pill(c, m.SidePanelThemeCustom())
				}
				if current {
					ui.Icon(c, icons.Lucide(icons.Check)).Size(14, 14).TextColor(t.Accent)
				}
			})
		})
	})
	if card.Clicked() && !current {
		id := scheme.ID
		_ = p.look.Update(func(s *domain.Settings) {
			if s.Appearance.FollowAppTerminalTheme {
				// The terminal shows the window theme's scheme, so the pick
				// switches the window theme and its appearance.
				if preset, dark, ok := domaintheme.PresetForTerminalTheme(id); ok {
					if dark {
						s.Appearance.ThemeMode, s.Appearance.DarkTheme = domain.ThemeDark, preset
					} else {
						s.Appearance.ThemeMode, s.Appearance.LightTheme = domain.ThemeLight, preset
					}
					return
				}
			}
			s.Appearance.TerminalTheme = id
		})
		c.Invalidate()
	}
}
