package kit

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
)

// SegmentedRole picks a Segmented control's recipe.
type SegmentedRole int

const (
	// SegmentedText is the toolkit's segmented control: labels in a radio
	// group, moved between with the arrows.
	SegmentedText SegmentedRole = iota
	// SegmentedIcons is a row of icon buttons, the chosen one on the accent,
	// as the Vault's view mode.
	SegmentedIcons
	// SegmentedTrack is compact labels on a sunken track, the chosen one
	// raised, as the quick theme switch.
	SegmentedTrack
	// SegmentedFill is labels that share the row's width, the chosen one on
	// the accent, as the quick opacity presets.
	SegmentedFill
)

// Segment is one choice of a Segmented control. Key keys its button; Icon
// is drawn instead of Label by SegmentedIcons, which shows Label as the
// tooltip.
type Segment struct {
	Key, Label, Icon string
}

// Segmented draws a choice between segments, of which *chosen is the index
// of the one chosen, or -1 for none. label names the control for assistive
// technology. A click on another segment sets *chosen, and Segmented reports
// it.
func Segmented(c *ui.Context, role SegmentedRole, chosen *int, label string, segments []Segment) bool {
	if role == SegmentedText {
		labels := make([]string, len(segments))
		for i, segment := range segments {
			labels[i] = segment.Label
		}
		control := ui.Segmented(c, chosen, labels...)
		if label != "" {
			control.Label(label)
		}
		return control.Changed()
	}
	t := c.Theme()
	row := ui.Row(c)
	switch role {
	case SegmentedIcons:
		row.Gap(1).Shrink(0).AlignItems(ui.Center)
		if CurrentStyle().Native() {
			// A macOS segmented control: the choices on a sunken capsule,
			// the chosen one raised.
			// It is shaded from whatever it sits on, as a page or a card.
			row.Padding(2).Gap(2).Radius(M(c).Radius.Pill).Background(t.Text.Alpha(0.07))
		}
	case SegmentedTrack:
		row.Padding(2).Gap(2).Radius(M(c).Radius.Control).Background(Subtle(t))
	case SegmentedFill:
		row.FillWidth().Gap(4)
	}
	if label != "" {
		row.Label(label)
	}
	changed := false
	row.Children(func() {
		for i, segment := range segments {
			on := i == *chosen
			if segment.Key != "" {
				c.Key(segment.Key)
			}
			button := ui.ButtonBase(c).Label(segment.Label)
			switch role {
			case SegmentedIcons:
				button.Tooltip(segment.Label).Size(28, M(c).Control.Large).Radius(M(c).Radius.Control).
					Justify(ui.Center).AlignItems(ui.Center)
				native := CurrentStyle().Native()
				if native {
					button.Size(32, M(c).Control.Large-4).Radius(M(c).Radius.Pill)
				}
				switch {
				case on && native:
					raisedFill(c, button).TextColor(t.Text)
				case on:
					button.Background(t.Accent).TextColor(t.AccentText)
				case button.Hovered():
					button.Background(Subtle(t)).TextColor(t.Text)
				default:
					button.TextColor(t.TextMuted)
				}
				button.Children(func() { ui.Icon(c, icons.Lucide(segment.Icon)).Size(15, 15) })
			case SegmentedTrack:
				button.Height(M(c).Control.Mini).Padding(0, 6).Radius(M(c).Radius.Small).
					AlignItems(ui.Center).FontSize(M(c).Font.Caption).FontWeight(500).TextColor(t.TextMuted)
				if on {
					button.Background(t.Background).TextColor(t.Text).Shadow(0, 1, 2, 0, CardShadow(t))
				} else if button.Hovered() {
					button.TextColor(t.Text)
				}
				button.Children(func() { ui.Text(c, segment.Label).SingleLine() })
			case SegmentedFill:
				button.Grow(1).Height(M(c).Control.Compact).Radius(M(c).Radius.Control).
					Justify(ui.Center).AlignItems(ui.Center).FontSize(M(c).Font.Caption).FontWeight(500)
				switch {
				case on:
					button.Background(t.Accent).TextColor(t.AccentText)
				case button.Hovered():
					button.Background(Strong(t)).TextColor(t.Text)
				default:
					button.Background(Subtle(t)).TextColor(t.TextMuted)
				}
				button.Children(func() { ui.Text(c, segment.Label).SingleLine() })
			}
			if button.Clicked() && !on {
				*chosen, changed = i, true
			}
		}
	})
	return changed
}

// ToolStrip is the head of a panel that shows one of several tools, as a
// terminal's side panel: the tools, of which *chosen is the index of the
// one shown, then end's controls at the strip's end. Classic draws the tools
// as icon buttons, the chosen one tinted, over a rule; the macOS style as a
// segmented control. Each tool's Key keys its button. ToolStrip reports a
// click on another tool, which sets *chosen.
func ToolStrip(c *ui.Context, tools []Segment, chosen *int, label string, end func()) bool {
	changed := false
	if CurrentStyle().Native() {
		ui.Row(c).FillWidth().Height(M(c).Bar.Strip+4).Shrink(0).Padding(0, 8).Gap(4).
			AlignItems(ui.Center).Children(func() {
			changed = Segmented(c, SegmentedIcons, chosen, label, tools)
			ui.Column(c).Grow(1)
			end()
		})
		return changed
	}
	Bar(c, BarStrip).Padding(0, 6).Gap(4).Children(func() {
		for i, tool := range tools {
			if tool.Key != "" {
				c.Key(tool.Key)
			}
			if IconButtonWith(c, tool.Icon, tool.Label, IconOptions{Role: IconCard, Selected: i == *chosen}).Clicked() && i != *chosen {
				*chosen, changed = i, true
			}
		}
		ui.Column(c).Grow(1)
		end()
	})
	return changed
}

// raisedFill fills el as the macOS style's chosen segment or Tab: the
// surface lifted by a faint shadow, or in a dark theme, whose surface is no
// lighter than the track under it, a shade of the text.
func raisedFill(c *ui.Context, el ui.Element) ui.Element {
	t := c.Theme()
	if t.Dark {
		return el.Background(t.Text.Alpha(0.18))
	}
	return el.Background(t.Surface).Shadow(0, 1, 2, 0, CardShadow(t))
}
