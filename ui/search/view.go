package search

import (
	"strings"

	"github.com/egoist/mygo/ui"

	appsearch "github.com/mslxl/natcatty/application/search"
	"github.com/mslxl/natcatty/ui/locales"
)

// The overlay follows the original Quick Switcher's proportions: a panel at
// most 672 DIPs wide, top-anchored under the window's title area, with rows
// tall enough for a Host's label and hostname and headers that name each
// section.
const (
	panelMaxWidth  float32 = 672
	panelTop       float32 = 48
	panelMaxHeight float32 = 460
	rowHeight      float32 = 38
	sectionHeight  float32 = 26
	emptyHeight    float32 = 76
	panelPadding   float32 = 12
	headerHeight   float32 = 36
	hintHeight     float32 = 20
	panelGap       float32 = 8
)

// Overlay renders the Quick Switcher or the command palette while the state
// is open. The overlay traps the focus, closes on Escape or a click outside,
// and leaves the keyboard shortcuts behind it waiting.
func Overlay(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		if !s.IsOpen() {
			return
		}
		m := locales.In(c.Locale())
		t := c.Theme()
		s.SetLocale(c.Locale())

		sections := s.Sections()
		count := 0
		for _, section := range sections {
			count += len(section.Results)
		}
		s.Clamp(count)
		reveal := s.TakeReveal()
		selected := s.Selected()

		width, height := c.Size()
		panelWidth := minf(width-32, panelMaxWidth)
		listHeight := float32(count)*rowHeight + float32(len(sections))*sectionHeight
		if count == 0 {
			listHeight = emptyHeight
		}
		chrome := panelPadding*2 + headerHeight + hintHeight + panelGap*2
		panelHeight := minf(minf(panelMaxHeight, height-panelTop-16), chrome+listHeight)

		closed := false
		ui.Overlay(c, func() {
			back := ui.Column(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).
				AlignItems(ui.Center).Justify(ui.Start).Modal().
				Background(ui.RGBA(0, 0, 0, 0.4))
			// Clicking the backdrop closes; the panel swallows its own clicks
			// below, so a click inside it never reaches this handler.
			back.OnClick(func() {
				s.Close()
				c.Invalidate()
			})
			if back.OverlayShortcut(0, ui.KeyEscape) {
				closed = true
			}
			back.Children(func() {
				panel := ui.Column(c).Role(ui.RoleDialog).
					Width(panelWidth).Height(panelHeight).
					Margin(panelTop, 0, 0, 0).Padding(panelPadding).Gap(panelGap).
					Background(t.Background).Border(1, t.Border).
					Radius(t.Radius+2).Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, 0.3)).
					Clip()
				panel.OnClick(func() {})
				panel.Children(func() {
					placeholder := placeholderText(m, s.Mode())
					ui.TextInput(c, s.QueryPtr()).
						Label(placeholder).
						Placeholder(placeholder).
						FillWidth().
						AutoFocus().
						OnTextKey(func(k ui.TextKey) bool {
							switch k.Key {
							case ui.KeyDown:
								if s.Move(1) {
									c.Invalidate()
								}
								return true
							case ui.KeyUp:
								if s.Move(-1) {
									c.Invalidate()
								}
								return true
							case ui.KeyEnter:
								if s.Activate() {
									c.Invalidate()
								}
								return true
							case ui.KeyEscape:
								s.Close()
								c.Invalidate()
								return true
							}
							return false
						})
					hintRow(c, m, t)
					if count == 0 {
						emptyState(c, s, m, t)
						return
					}
					ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
						ui.Column(c).FillWidth().MinWidth(0).Children(func() {
							ListAt(c, sections, m, t, ListOptions{
								Selected:  selected,
								Reveal:    reveal,
								KeyPrefix: "search-row",
								Activate: func(index int) {
									if s.Select(index) {
										c.Invalidate()
									}
									if s.Activate() {
										c.Invalidate()
									}
								},
							})
						})
					})
				})
			})
		})
		if closed {
			s.Close()
			c.Invalidate()
		}
	}
}

func hintRow(c *ui.Context, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Height(hintHeight).AlignItems(ui.Center).
		Justify(ui.SpaceBetween).Children(func() {
		ui.Text(c, m.SearchJumpTo()).FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, m.SearchHint()).FontSize(11).TextColor(t.TextMuted).NoWrap()
	})
}

func emptyState(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	title, body := m.SearchEmpty(), m.SearchEmptyBody()
	// The palette's empty state names commands; the Quick Switcher's names
	// Hosts and Sessions. A query that matches nothing says so either way.
	if s.Mode() == ModeCommandPalette {
		title, body = m.SearchEmptyCommands(), m.SearchEmptyCommandsBody()
	}
	if strings.TrimSpace(s.Query()) != "" {
		title, body = m.SearchNoResults(), m.SearchNoResultsBody(strings.TrimSpace(s.Query()))
	}
	ui.Column(c).Grow(1).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Gap(4).
		Children(func() {
			ui.Text(c, title).Bold().AlignSelf(ui.Center)
			ui.Text(c, body).FontSize(12).TextColor(t.TextMuted).AlignSelf(ui.Center)
		})
}

func placeholderText(m locales.Messages, mode Mode) string {
	if mode == ModeCommandPalette {
		return m.SearchCommandPlaceholder()
	}
	return m.SearchQuickSwitcherPlaceholder()
}

// SectionTitle localizes a section token: a record kind for the Vault and
// the Quick Switcher, or a command category for the palette.
func SectionTitle(m locales.Messages, key string) string {
	switch key {
	case string(appsearch.KindHost):
		return m.SearchSectionHost()
	case string(appsearch.KindHostGroup):
		return m.SearchSectionHostGroup()
	case string(appsearch.KindKey):
		return m.SearchSectionKey()
	case string(appsearch.KindIdentity):
		return m.SearchSectionIdentity()
	case string(appsearch.KindSnippet):
		return m.SearchSectionSnippet()
	case string(appsearch.KindSnippetPackage):
		return m.SearchSectionSnippetPackage()
	case string(appsearch.KindNote):
		return m.SearchSectionNote()
	case string(appsearch.KindNoteGroup):
		return m.SearchSectionNoteGroup()
	case string(appsearch.KindProxyProfile):
		return m.SearchSectionProxyProfile()
	case string(appsearch.KindKnowledge):
		return m.SearchSectionKnowledge()
	case string(appsearch.KindSession):
		return m.SearchSectionSession()
	case string(appsearch.KindCommand):
		return m.SearchSectionCommand()
	case CategoryApp:
		return m.SearchCategoryApp()
	case CategoryTab:
		return m.SearchCategoryTab()
	case CategoryPane:
		return m.SearchCategoryPane()
	case CategoryVault:
		return m.SearchCategoryVault()
	default:
		return ""
	}
}

// Command categories. They double as section tokens for the command palette.
const (
	CategoryApp   = "app"
	CategoryTab   = "tab"
	CategoryPane  = "pane"
	CategoryVault = "vault"
)

// ListOptions customizes ListAt.
type ListOptions struct {
	// Selected is the index of the highlighted row across all sections.
	Selected int
	// Reveal scrolls the highlighted row into view this frame, after a
	// keyboard move or an open surface.
	Reveal bool
	// KeyPrefix namespaces the row keys when two lists share a parent.
	KeyPrefix string
	// Activate opens the row at the flat index.
	Activate func(index int)
}

// ListAt renders the sectioned results. It is the same list the overlay uses
// and the Vault renders inline, so both surfaces rank and highlight
// identically.
func ListAt(c *ui.Context, sections []Section, m locales.Messages, t *ui.Theme, opts ListOptions) {
	index := 0
	for _, section := range sections {
		if title := SectionTitle(m, section.Key); title != "" {
			ui.Text(c, title).FontSize(11).TextColor(t.TextMuted).
				Height(sectionHeight).NoWrap()
		}
		for _, result := range section.Results {
			result := result
			position := index
			row(c, result, position == opts.Selected, func() {
				if opts.Activate != nil {
					opts.Activate(position)
				}
			}, opts, t)
			index++
		}
	}
}

func row(c *ui.Context, result appsearch.Result, selected bool, activate func(), opts ListOptions, t *ui.Theme) {
	record := result.Record
	key := opts.KeyPrefix + ":" + string(record.Kind) + ":" + record.ID
	if record.ID == "" {
		key += record.Title
	}
	e := ui.Row(c).Key(key).FillWidth().MinWidth(0).Height(rowHeight).
		Padding(0, 8).Gap(10).AlignItems(ui.Center).Clip()
	if selected {
		e = e.Background(t.Selection)
		if opts.Reveal {
			e = e.ScrollIntoView()
		}
	}
	e.OnClick(activate)
	e.Children(func() {
		ui.Text(c, record.Title).FontSize(14).Grow(1).MinWidth(0).NoWrap()
		if record.Subtitle != "" {
			ui.Text(c, record.Subtitle).FontSize(11).TextColor(t.TextMuted).
				MaxWidth(200).NoWrap()
		}
		if record.Shortcut != "" {
			ui.Text(c, record.Shortcut).FontSize(11).TextColor(t.TextMuted).NoWrap()
		}
	})
}

func minf(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}
