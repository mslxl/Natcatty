package phoneshell

// The accessory key bar. A phone keyboard has no Esc, Tab, Control or arrow
// keys, so full-screen programs are unusable without one. MyGo has no native
// accessory-view API: the bar is an ordinary row in the terminal page,
// directly above the terminal. The window's root keeps the page inside the
// safe area, whose bottom is the keyboard's inset, so the bar lands just
// above the keyboard and above nothing else; it is not hidden when the
// keyboard is (a hardware keyboard still needs Esc and Ctrl).
//
// Every element keeps the focus where it is (KeepFocus). A button that took
// the focus would make the terminal stop reporting an active text input,
// and the keyboard would collapse on the first tap.
//
// The bar's keys go through TerminalScreen.SendKey, which uses the
// terminal's own key encoder: arrows follow application cursor mode and
// Control combinations are the control characters a shell reads (Ctrl+C is
// 0x03). Ctrl and Alt are one-shot latches: the next bar key carries them,
// and tapping the latch again without a key cancels it. ^C, ^D and ^Z are
// the combinations a phone user needs most, without a latch.

import "github.com/egoist/mygo/ui"

// accessoryState is one terminal page's latch state. It lives on the page's
// Tab and survives frames; the zero value has no latch held.
type accessoryState struct {
	ctrl, alt bool
}

// take consumes the held latches and returns their modifiers.
func (a *accessoryState) take() ui.Modifiers {
	var mods ui.Modifiers
	if a.ctrl {
		mods |= ui.Ctrl
		a.ctrl = false
	}
	if a.alt {
		mods |= ui.Alt
		a.alt = false
	}
	return mods
}

// accessoryKey is one key of the bar. mods are the modifications the key
// itself needs, as a Shift for | on a US layout; the held latches are added
// to them.
type accessoryKey struct {
	label string
	key   ui.Key
	mods  ui.Modifiers
}

// The bar's two rows. The first holds the keys a terminal needs most, the
// second the rest and the symbols a shell uses constantly.
var (
	accessoryRowOne = []accessoryKey{
		{label: "Esc", key: ui.KeyEscape},
		{label: "Tab", key: ui.KeyTab},
		{label: "←", key: ui.KeyLeft},
		{label: "↑", key: ui.KeyUp},
		{label: "↓", key: ui.KeyDown},
		{label: "→", key: ui.KeyRight},
	}
	accessoryRowTwo = []accessoryKey{
		{label: "PgUp", key: ui.KeyPageUp},
		{label: "PgDn", key: ui.KeyPageDown},
		{label: "^C", key: ui.KeyC, mods: ui.Ctrl},
		{label: "^D", key: ui.KeyD, mods: ui.Ctrl},
		{label: "^Z", key: ui.KeyZ, mods: ui.Ctrl},
		{label: "|", key: ui.KeyBackslash, mods: ui.Shift},
		{label: "-", key: ui.KeyMinus},
		{label: "~", key: ui.KeyBackquote, mods: ui.Shift},
		{label: "/", key: ui.KeySlash},
	}
)

// accessoryBar draws the bar and sends tapped keys to screen. It takes the
// latches it reads and writes, so the page's Ctrl and Alt buttons toggle the
// same state the keys consult.
func accessoryBar(c *ui.Context, acc *accessoryState, screen TerminalScreen) ui.Element {
	if acc == nil {
		acc = &accessoryState{}
	}
	t := c.Theme()
	bar := ui.Column(c).FillWidth().Shrink(0).Padding(4, 6).Gap(4).
		Background(t.Surface).KeepFocus().Label("Accessory keys")
	bar.Children(func() {
		accessoryRow(c, acc, screen, accessoryRowOne,
			accessoryLatch{label: "Ctrl", held: &acc.ctrl},
			accessoryLatch{label: "Alt", held: &acc.alt})
		accessoryRow(c, acc, screen, accessoryRowTwo)
	})
	return bar
}

// accessoryLatch is a modifier button that toggles held.
type accessoryLatch struct {
	label string
	held  *bool
}

// accessoryRow draws one row of keys, after any latches.
func accessoryRow(c *ui.Context, acc *accessoryState, screen TerminalScreen, keys []accessoryKey, latches ...accessoryLatch) {
	ui.Row(c).FillWidth().Shrink(0).Gap(4).KeepFocus().Children(func() {
		for _, latch := range latches {
			on := *latch.held
			b := accessoryButton(c, latch.label, on)
			if b.Clicked() {
				*latch.held = !on
			}
		}
		for _, k := range keys {
			k := k
			b := accessoryButton(c, k.label, false)
			if b.Clicked() {
				screen.SendKey(k.key, k.mods|acc.take())
			}
		}
	})
}

// accessoryButton is one bar key: a compact, focus-keeping button, filled
// with the accent color while its latch is held.
func accessoryButton(c *ui.Context, label string, active bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).KeepFocus().Shrink(0).
		Height(30).Padding(0, 8).Radius(6).AlignItems(ui.Center).Justify(ui.Center).
		FontSize(12).TextColor(t.Text).Background(t.Background).Border(1, t.Border)
	switch {
	case active:
		b.Background(t.Accent).TextColor(t.AccentText).Border(1, t.Accent)
	case b.Pressed():
		b.Background(t.Background.Mix(t.Text, 0.12))
	case b.Hovered():
		b.Background(t.Background.Mix(t.Text, 0.06))
	}
	b.Children(func() { ui.Text(c, label).FontSize(12).SingleLine() })
	return b
}
