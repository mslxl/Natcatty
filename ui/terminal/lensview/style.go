package lensview

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/terminal/surface"
)

// style is the surface design system a lens view is drawn with: the tokens Terminal's
// own components are built from, measured from the CSS it ships (see
// findings/lens-implementation.md), paired for a light or a dark appearance.
//
// The point of these numbers is that a lens view is a widget tree whose look comes
// from the system, not from each lens: 12 DIP cards with a one-pixel tone ring and a
// 36 DIP body indent, 8 DIP inner gaps, 14/1 DIP key-value gaps, 24 DIP table
// headers over a hairline, 26 DIP mono code heads, and a four-step text ramp.
type style struct {
	// Box metrics, in DIPs.
	CardR      float32 // --r-card: 12
	CodeR      float32 // code and diff corners: 8
	InnerGap   float32 // --sf-inner-gap: 8
	KVColGap   float32 // key-value column gap: 14
	KVRowGap   float32 // key-value row gap: 1
	HeadMinH   float32 // card head: line height + 12
	RowH       float32 // table row / list row: line height + 6
	BodyIndent float32 // card body padding-left: 36
	CellPadX   float32 // table cell padding: 7
	LH         float32 // --sf-lh: one line of the surface
	BodySize   float32 // the terminal font's size: the body text's size
	SmallSize  float32 // 11.5: headers, badges, code heads
	MeterH     float32 // a meter or bar is 6 DIP tall
	MeterR     float32 // and rounded by 3
	MeterW     float32 // a stacked meter is 180 DIP wide

	// Text ramp, Terminal's --t1 to --t4.
	T1, T2, T3, T4 ui.Color

	// Hairlines, Terminal's --l1 to --l3, and the surfaces they sit on.
	L1, L2, L3 ui.Color
	CardBG     ui.Color
	CodeBG     ui.Color
	ChipBG     ui.Color
	Accent     ui.Color
	Success    ui.Color
	Warning    ui.Color
	Danger     ui.Color
	Live       ui.Color // Terminal's --live (#a86af4): a running block's bar, at 40%
	Dark       bool
	CharW      float32 // advance of one monospace cell, for table budgets
	// CardAlpha is how much white --sf-card-bg lays over the window's background:
	// 55% in the light appearance, 2.8% in the dark one.
	CardAlpha float32
}

// styleFor reads the tokens for the appearance in use. The text ramp, hairlines,
// code and chip surfaces are Terminal's own values; accents come from the window theme
// so a changed accent still moves the surfaces with it.
func styleFor(c *ui.Context) style {
	t := c.Theme()
	// The body is terminal text, so its metrics follow the terminal's own font
	// size; the UI theme's size is only a fallback when none was reported.
	fs := t.FontSize
	if _, size := surface.TerminalFont(); size > 0 {
		fs = size
	}
	if fs <= 0 {
		fs = 13
	}
	s := style{
		CardR:      12,
		CardAlpha:  0.028,
		CodeR:      8,
		InnerGap:   8,
		KVColGap:   14,
		KVRowGap:   1,
		BodyIndent: 36,
		CellPadX:   7,
		LH:         22, // Terminal --he-lh: 22px
		BodySize:   fs,
		SmallSize:  11.5,
		MeterH:     6,
		MeterR:     3,
		MeterW:     180,
		Accent:     t.Accent,
		Live:       ui.RGBA(0xa8, 0x6a, 0xf4, 1),
		CardBG:     t.Surface,
		CharW:      round(fs * 0.602),
		Dark:       t.Dark,
	}
	s.HeadMinH = s.LH + 12
	s.RowH = s.LH + 6
	// The semantic colours are Terminal's own --ok/--warn/--bad, not the window theme's
	// approximations: its surfaces were designed against these.
	if t.Dark {
		s.Success = ui.RGBA(0x3e, 0xcf, 0x8e, 1)
		s.Warning = ui.RGBA(0xf5, 0xa5, 0x24, 1)
		s.Danger = ui.RGBA(0xff, 0x61, 0x66, 1)
	} else {
		s.Success = ui.RGBA(0x2f, 0xae, 0x74, 1)
		s.Warning = ui.RGBA(0xd9, 0x8a, 0x0b, 1)
		s.Danger = ui.RGBA(0xe5, 0x48, 0x4d, 1)
	}
	if t.Dark {
		s.T1 = ui.RGBA(0xed, 0xed, 0xed, 1)
		s.T2 = ui.RGBA(0xa1, 0xa1, 0xa1, 1)
		s.T3 = ui.RGBA(0x8a, 0x8a, 0x92, 1)
		s.T4 = ui.RGBA(0x55, 0x55, 0x5c, 1)
		s.L1 = ui.RGBA(255, 255, 255, 0.07)
		s.L2 = ui.RGBA(255, 255, 255, 0.10)
		s.L3 = ui.RGBA(255, 255, 255, 0.15)
		s.CodeBG = ui.RGBA(0x0c, 0x0c, 0x0d, 1)
		s.ChipBG = ui.RGBA(255, 255, 255, 0.07)
	} else {
		s.T1 = ui.RGBA(0x1b, 0x1c, 0x20, 1)
		s.T2 = ui.RGBA(0x55, 0x57, 0x5f, 1)
		s.T3 = ui.RGBA(0x6a, 0x6c, 0x74, 1)
		s.T4 = ui.RGBA(0xb3, 0xb5, 0xbb, 1)
		s.L1 = ui.RGBA(24, 26, 44, 0.06)
		s.L2 = ui.RGBA(24, 26, 44, 0.09)
		s.L3 = ui.RGBA(24, 26, 44, 0.14)
		s.CodeBG = ui.RGBA(0xf8, 0xf8, 0xf6, 1)
		s.CardAlpha = 0.55
		s.ChipBG = ui.RGBA(27, 28, 48, 0.055)
	}
	return s
}

// round rounds to a tenth of a DIP, so the metrics read like the CSS they came
// from and stay stable between frames.
func round(v float32) float32 {
	return float32(int(v*10+0.5)) / 10
}

// withAlpha returns col at the given opacity, which is how Terminal builds a tone's
// fill and ring: rgb(from var(--tc) r g b/10%) and /22%.
func withAlpha(col ui.Color, a float32) ui.Color {
	if col.A == 0 {
		col.A = 255
	}
	return ui.RGBA(col.R, col.G, col.B, a*(float32(col.A)/255))
}

// BlockColors are the tokens the block chrome draws with, in one value: the text
// ramp, the hairlines, the chip surface and the state colours Terminal's blocks use.
type BlockColors struct {
	T1, T2, T3, T4 ui.Color
	L1, L2, L3     ui.Color
	Chip           ui.Color
	Ok, Warn, Bad  ui.Color
	Accent         ui.Color
	Live           ui.Color
	CodeBG         ui.Color
	SmallSize, LH  float32
}

// BlockColorsFor reads the block chrome's tokens for this appearance.
func BlockColorsFor(c *ui.Context) BlockColors {
	s := styleFor(c)
	return BlockColors{
		T1: s.T1, T2: s.T2, T3: s.T3, T4: s.T4,
		L1: s.L1, L2: s.L2, L3: s.L3,
		Chip: s.ChipBG, Ok: s.Success, Warn: s.Warning, Bad: s.Danger, Accent: s.Accent,
		Live: s.Live, CodeBG: s.CodeBG,
		SmallSize: s.SmallSize, LH: s.LH,
	}
}

// Hairlines exposes the two surface hairlines (Terminal's --l1 and --l3) for the block
// chrome: the left state bar rests at L1 and brightens to L3 on hover.
func Hairlines(c *ui.Context) (l1, l3 ui.Color) {
	s := styleFor(c)
	return s.L1, s.L3
}

// InnerGap is --sf-inner-gap: the gap between the parts of a block body.
func InnerGap(c *ui.Context) float32 { return styleFor(c).InnerGap }

// toneRingT is the same ring for an explicit tone colour.
func (s style) toneRingT(tone ui.Color) ui.Color {
	if s.Dark {
		return withAlpha(tone, 0.20)
	}
	return withAlpha(tone, 0.16)
}

// toneRing is a tone's one-pixel ring (16% light, 20% dark), as the card rules ask.
func (s style) toneRing(col ui.Color) ui.Color {
	if s.Dark {
		return withAlpha(col, 0.20)
	}
	return withAlpha(col, 0.16)
}

// toneFill is a tone's background tint (10%).
func (s style) toneFill(col ui.Color) ui.Color {
	return mix(s.CardBG, col, 0.10)
}
