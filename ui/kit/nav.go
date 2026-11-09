package kit

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
)

// NavRole picks a navigation column's recipe.
type NavRole int

const (
	// NavHome is the main window's sidebar: tall rows on a rounded fill.
	NavHome NavRole = iota
	// NavSettings is the Settings window's area list: compact rows, a
	// smaller glyph, and a fainter fill.
	NavSettings
)

// navRecipe is a NavRole's geometry.
type navRecipe struct {
	height, padX, padY, gap, glyph, radius, listGap float32
}

func navRecipeOf(c *ui.Context, role NavRole) navRecipe {
	if role == NavSettings {
		return navRecipe{padX: 12, padY: 8, gap: 8, glyph: M(c).Icon.Medium,
			radius: M(c).Radius.Control, listGap: 4}
	}
	return navRecipe{height: 36, padX: 12, gap: 12, glyph: M(c).Icon.Regular, radius: 8, listGap: 2}
}

// NavChrome is the width a NavItem of role adds around its label: its
// padding, its glyph, and the gap between them.
func NavChrome(c *ui.Context, role NavRole) float32 {
	r := navRecipeOf(c, role)
	return 2*r.padX + r.glyph + r.gap
}

// NavList is the column NavItems of role stack in.
func NavList(c *ui.Context, role NavRole, items func()) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(navRecipeOf(c, role).listGap).Children(items)
}

// NavItem is one entry of a navigation column: a glyph and a label, muted
// until it is hovered or is the visible page.
func NavItem(c *ui.Context, role NavRole, key, icon, label string, active bool) ui.Element {
	t := c.Theme()
	r := navRecipeOf(c, role)
	item := ui.ButtonBase(c).Key(key).Label(label).FillWidth().MinWidth(0).Padding(r.padY, r.padX).
		Gap(r.gap).Radius(r.radius).Justify(ui.Start).AlignItems(ui.Center).TextColor(t.TextMuted)
	if r.height > 0 {
		item.Height(r.height)
	}
	strong, subtle := Strong(t), Subtle(t)
	if role == NavSettings {
		strong, subtle = t.Text.Alpha(0.08), t.Text.Alpha(0.04)
	}
	switch {
	case active:
		item.Background(strong).TextColor(t.Text)
	case role == NavHome && item.Pressed():
		item.Background(strong).TextColor(t.Text)
	case item.Hovered() || item.Pressed():
		item.Background(subtle).TextColor(t.Text)
	}
	item.Children(func() {
		IconSized(c, icon, r.glyph).Shrink(0)
		text := ui.Text(c, label).SingleLine().MinWidth(0)
		if role == NavSettings {
			text.FontSize(M(c).Font.Body).Shrink(1)
			return
		}
		text.Grow(1)
		if active {
			text.FontWeight(500)
		}
	})
	return item
}

// SidePanelHeader is a side tool's search field with the tool's actions at
// its end. actions may be nil. It reports whether the query changed.
func SidePanelHeader(c *ui.Context, query *string, placeholder string, actions func()) bool {
	changed := false
	ui.Row(c).FillWidth().Gap(4).Padding(8, 8, 4, 8).AlignItems(ui.Center).Shrink(0).Children(func() {
		changed = ui.SearchField(c, query).Placeholder(placeholder).Label(placeholder).
			Grow(1).MinWidth(0).Changed()
		if actions != nil {
			actions()
		}
	})
	return changed
}

// SearchRole picks a SearchField's recipe.
type SearchRole int

const (
	// SearchSidebar is a column's search over its entries: a tall themed
	// input with the glyph and the clear button inset.
	SearchSidebar SearchRole = iota
	// SearchPalette is a command palette's query: the themed input alone.
	SearchPalette
	// SearchFilter is the compact field of a filter bar.
	SearchFilter
	// SearchPicker is a chooser's field above its list.
	SearchPicker
)

// SearchOptions configure a SearchField.
type SearchOptions struct {
	Role        SearchRole
	Placeholder string
	AutoFocus   bool
	// Clear names the clear button, which shows while ShowClear is set.
	// ClearKey keys it; OnClear runs on a click, and nil empties the query.
	Clear     string
	ClearKey  string
	ShowClear bool
	OnClear   func()
	// Trailing draws inside the field after the input, such as a tag.
	Trailing func()
}

// SearchField is a search input editing *query. It returns the input, so
// the caller adds its own keys (arrows, Enter, Escape) to it.
func SearchField(c *ui.Context, query *string, o SearchOptions) ui.Element {
	t := c.Theme()
	var input ui.Element
	field := func() {
		input = ui.TextInputBase(c, query)
		input.Placeholder(o.Placeholder).Label(o.Placeholder).Grow(1).MinWidth(0)
		if o.AutoFocus {
			input.AutoFocus()
		}
	}
	switch o.Role {
	case SearchPalette:
		input = ui.TextInput(c, query).Label(o.Placeholder).Placeholder(o.Placeholder).FillWidth()
		if o.AutoFocus {
			input.AutoFocus()
		}
		return input
	case SearchSidebar:
		ui.Row(c).FillWidth().Shrink(0).Height(36).Children(func() {
			input = ui.TextInput(c, query).Label(o.Placeholder).Placeholder(o.Placeholder).
				FillWidth().Height(36).FontSize(M(c).Font.Body).Padding(0, 32, 0, 32)
			if o.AutoFocus {
				input.AutoFocus()
			}
			ui.Box(c).Absolute().Left(10).Top(11).Size(14, 14).TextColor(t.TextMuted).Children(func() {
				IconSized(c, icons.Search, M(c).Icon.Medium)
			})
			if o.Clear != "" && o.ShowClear {
				clear := ui.ButtonBase(c).Key(o.ClearKey).Label(o.Clear).
					Absolute().Right(6).Top(7).Size(22, 22).Radius(3).TextColor(t.TextMuted)
				if clear.Hovered() || clear.Pressed() {
					clear.Background(t.Text.Alpha(0.08)).TextColor(t.Text)
				}
				clear.Children(func() { IconSized(c, icons.X, M(c).Icon.Medium) })
				if clear.Clicked() {
					o.clear(query)
					c.Invalidate()
				}
			}
		})
	case SearchFilter:
		ui.Row(c).Grow(1).MinWidth(0).Height(24).Padding(0, 8).Gap(6).AlignItems(ui.Center).
			Radius(5).Border(1, t.Border).Background(t.Surface).Children(func() {
			IconSized(c, icons.Search, M(c).Icon.Small).TextColor(t.TextMuted).Shrink(0)
			field()
			input.FontSize(M(c).Font.Footnote)
			if o.Clear != "" && o.ShowClear {
				clear := ui.ButtonBase(c).Key(o.ClearKey).Label(o.Clear).Tooltip(o.Clear).
					Size(16, 16).Justify(ui.Center).AlignItems(ui.Center).TextColor(t.TextMuted)
				clear.Children(func() { IconSized(c, icons.X, M(c).Icon.Small) })
				if clear.Clicked() {
					o.clear(query)
				}
			}
			if o.Trailing != nil {
				o.Trailing()
			}
		})
	case SearchPicker:
		ui.Row(c).FillWidth().Height(36).Padding(0, 10).Gap(8).AlignItems(ui.Center).
			Radius(M(c).Radius.Control).Border(1, t.Border).Background(t.Background).Children(func() {
			IconSized(c, icons.Search, M(c).Icon.Medium).TextColor(t.TextMuted).Shrink(0)
			field()
			input.FontSize(M(c).Font.Callout)
			if o.Trailing != nil {
				o.Trailing()
			}
		})
	}
	return input
}

func (o SearchOptions) clear(query *string) {
	if o.OnClear != nil {
		o.OnClear()
		return
	}
	*query = ""
}
