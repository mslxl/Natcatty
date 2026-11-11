package kit

import (
	"github.com/egoist/mygo/ui"
)

// IconTabItem is one tab of an IconTabs strip. Key keys its button; Icon
// names its Lucide glyph; Label names the tab, and is drawn beside the
// glyph when the labeled strip fits.
type IconTabItem struct {
	Key, Label, Icon string
}

// IconTabsOptions configure an IconTabs strip.
type IconTabsOptions struct {
	// Label names the strip for assistive technology, as "System tabs".
	Label string
	// Width is the room the whole strip has, trailing controls included. It
	// is measured against the labeled strip; when that does not fit, every
	// tab drops its label at once. Zero keeps the labeled form unless
	// Compact is set.
	Width float32
	// Compact asks for the icon-only form even when the labels would fit.
	Compact bool
	// Reveal scrolls the chosen tab wholly into view when the icon-only
	// strip itself does not fit and scrolls.
	Reveal bool
	// Trailing builds controls that follow the tabs, as the tab layout
	// button and the Collapsed Tabs trigger. They stay outside the strip's
	// horizontal scroll, so they remain reachable when the tabs scroll.
	Trailing func()
	// TrailingWidth is the width of Trailing, used to decide whether the
	// labeled strip fits. It excludes the gap before the controls.
	TrailingWidth float32
}

// IconTabs draws a surface's view switcher as a strip of icon-and-label
// tabs, of which *chosen is the index chosen. The strip measures its whole
// labeled form, trailing controls included, against Width: when it does not
// fit, every tab becomes icon-only at once, never a mix, and keeps its
// label as its accessible name and tooltip. If even the icon-only strip
// overflows, it scrolls sideways and Reveal keeps the chosen tab whole.
// Classic draws the chosen tab as a muted filled chip with accent text;
// the macOS style raises it as a glass capsule on a sunken track. TabsBase
// handles clicking and the arrow keys, and IconTabs reports a change.
func IconTabs(c *ui.Context, tabs []IconTabItem, chosen *int, o IconTabsOptions) bool {
	t := c.Theme()
	metrics := M(c)
	font := metrics.Font.Footnote
	tabHeight := metrics.Control.Compact
	stripHeight := max(metrics.Bar.Tabs, tabHeight+8)
	padding := float32(10)
	iconSize := metrics.Icon.Regular
	innerGap := metrics.Space.Small
	// An icon-only tab keeps a 32-DIP square: the glyph, a 6-DIP gap, and an
	// 8-DIP horizontal pad apiece. At the side panel's 280-DIP minimum the
	// seven tabs and the controls no longer fit, so the strip scrolls there,
	// as the residual overflow fallback.
	compactWidth := float32(32)
	gap := metrics.Space.XSmall
	native := CurrentStyle().Native()
	track := float32(0)
	if native {
		gap = 2
		track = 4
	}

	// Measure the labeled form before choosing it. The tab width is the
	// glyph, the gap, the label, and the tab's own horizontal padding, plus
	// a rounding DIP apiece; the room between tabs, and the macOS track's
	// padding, are measured too.
	labeled := track
	for _, tab := range tabs {
		textWidth, _ := c.MeasureText(0, ui.Span{Text: tab.Label, Size: font, Weight: 500})
		labeled += 2*padding + iconSize + innerGap + textWidth + 2
	}
	if len(tabs) > 1 {
		labeled += gap * float32(len(tabs)-1)
	}
	trailingRoom := float32(0)
	if o.Trailing != nil {
		trailingRoom = metrics.Space.Small + o.TrailingWidth
	}
	compact := o.Compact
	if !compact && o.Width > 0 && labeled+trailingRoom > o.Width {
		compact = true
	}

	before := *chosen
	strip := ui.Row(c).FillWidth().Height(stripHeight).AlignItems(ui.Center).Shrink(0)
	if o.Width > 0 {
		strip.Width(o.Width)
	}
	if o.Label != "" {
		strip.Label(o.Label)
	}
	if o.Trailing != nil {
		strip.Gap(metrics.Space.Small)
	}
	strip.Children(func() {
		scroll := ui.ScrollHorizontal(c).Grow(1).MinWidth(0).Height(stripHeight).
			AlignItems(ui.Center).Shrink(1)
		scroll.Children(func() {
			parts := ui.TabsBase(c, chosen, len(tabs))
			parts.List.Gap(gap).AlignItems(ui.Center).Shrink(0)
			if native {
				parts.List.Padding(2).Radius(metrics.Radius.Pill).
					Height(tabHeight + 4).Background(t.Text.Alpha(0.07))
			} else {
				parts.List.Height(tabHeight)
			}
			parts.List.Children(func() {
				for i, tab := range tabs {
					if tab.Key != "" {
						c.Key(tab.Key)
					}
					on := i == *chosen
					width := compactWidth
					if !compact {
						textWidth, _ := c.MeasureText(0, ui.Span{Text: tab.Label, Size: font, Weight: 500})
						width = 2*padding + iconSize + innerGap + textWidth + 2
					}
					element := parts.Tab(i).
						Width(width).Height(tabHeight).
						Justify(ui.Center).AlignItems(ui.Center).
						Label(tab.Label).Tooltip(tab.Label).
						FocusRing(false)
					switch {
					case native:
						element.Radius(metrics.Radius.Pill)
						switch {
						case on:
							raisedFill(c, element).TextColor(t.Text)
						case element.Hovered():
							element.Background(t.Text.Alpha(0.06)).TextColor(t.Text)
						default:
							element.TextColor(t.TextMuted)
						}
					case on:
						element.Radius(metrics.Radius.Control).
							Background(Subtle(t)).TextColor(t.Accent)
					case element.Hovered():
						element.Radius(metrics.Radius.Control).
							Background(Subtle(t)).TextColor(t.Text)
					default:
						element.Radius(metrics.Radius.Control).TextColor(t.TextMuted)
					}
					if on && o.Reveal {
						element.ScrollIntoView()
					}
					element.Children(func() {
						IconSized(c, tab.Icon, iconSize)
						if !compact {
							ui.Text(c, tab.Label).FontSize(font).FontWeight(500).SingleLine()
						}
					})
					if !compact {
						// The label sits a gap after its glyph; a compact tab
						// centers the glyph alone.
						element.Gap(innerGap)
					}
				}
			})
		})
		if o.Trailing != nil {
			o.Trailing()
		}
	})
	return *chosen != before
}
