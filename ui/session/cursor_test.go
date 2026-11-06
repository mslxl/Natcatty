package sessionui

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// cursorProbeTheme paints the cursor in pure red, so a test can find it by
// color without depending on the interface theme or the chrome around it.
func cursorProbeTheme() domaintheme.TerminalTheme {
	return domaintheme.TerminalTheme{
		ID:   "cursor-probe",
		Name: "cursor probe",
		Dark: true,
		Colors: domaintheme.TerminalColors{
			Background: "#000000",
			Foreground: "#ffffff",
			Cursor:     "#ff0000",
		},
	}
}

func countCursorPixels(img *image.RGBA) int {
	count := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			off := img.PixOffset(x, y)
			r, g, bl := img.Pix[off], img.Pix[off+1], img.Pix[off+2]
			// Antialiasing blends the cursor toward the black terminal
			// background, so accept dim reds but not the chrome's colors.
			if r > 96 && g < 96 && bl < 96 {
				count++
			}
		}
	}
	return count
}

// sampleCursorFrames draws repeatedly across two blink periods and reports
// whether the cursor was ever visible and ever hidden.
func sampleCursorFrames(t *testing.T, tester *ui.Tester) (visible, hidden bool) {
	t.Helper()
	tester.Frame()
	for i := 0; i < 8; i++ {
		time.Sleep(300 * time.Millisecond)
		tester.Frame()
		if countCursorPixels(tester.Image()) == 0 {
			hidden = true
		} else {
			visible = true
		}
	}
	return visible, hidden
}

func TestTerminalCursorMapping(t *testing.T) {
	tests := []struct {
		name  string
		read  func() (domainpreferences.CursorStyle, bool)
		want  terminal.CursorStyle
		blink bool
	}{
		{"missing preference keeps the blinking block", nil, terminal.CursorBlock, true},
		{"block", func() (domainpreferences.CursorStyle, bool) {
			return domainpreferences.CursorBlock, true
		}, terminal.CursorBlock, true},
		{"bar", func() (domainpreferences.CursorStyle, bool) {
			return domainpreferences.CursorBar, false
		}, terminal.CursorBar, false},
		{"underline", func() (domainpreferences.CursorStyle, bool) {
			return domainpreferences.CursorUnderline, true
		}, terminal.CursorUnderline, true},
		{"unknown falls back to block", func() (domainpreferences.CursorStyle, bool) {
			return domainpreferences.CursorStyle("neon"), true
		}, terminal.CursorBlock, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			surface := &Surface{options: Options{Cursor: test.read}}
			got, blink := surface.terminalCursor()
			if got != test.want || blink != test.blink {
				t.Errorf("terminalCursor() = %v, %v; want %v, %v", got, blink, test.want, test.blink)
			}
		})
	}
}

func TestCursorSequence(t *testing.T) {
	tests := []struct {
		style domainpreferences.CursorStyle
		blink bool
		want  string
	}{
		{domainpreferences.CursorBlock, true, "\x1b[1 q"},
		{domainpreferences.CursorBlock, false, "\x1b[2 q"},
		{domainpreferences.CursorUnderline, true, "\x1b[3 q"},
		{domainpreferences.CursorUnderline, false, "\x1b[4 q"},
		{domainpreferences.CursorBar, true, "\x1b[5 q"},
		{domainpreferences.CursorBar, false, "\x1b[6 q"},
	}
	for _, test := range tests {
		if got := cursorSequence(test.style, test.blink); got != test.want {
			t.Errorf("cursorSequence(%q, %v) = %q, want %q", test.style, test.blink, got, test.want)
		}
	}
}

// TestFocusedTerminalCursorBlinks pins the default: the cursor of a focused
// Surface blinks. The keyed terminal view must see the focus it was given, or
// it draws a hollow, never-blinking cursor.
func TestFocusedTerminalCursorBlinks(t *testing.T) {
	surface := newActionSurface(t, WithTerminalTheme(cursorProbeTheme))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 720, 480)
	tester.SetLocale("en")

	visible, hidden := sampleCursorFrames(t, tester)
	if !visible {
		t.Fatal("the cursor never rendered")
	}
	if !hidden {
		t.Fatal("the cursor never blinked off")
	}
}

// TestSteadyTerminalCursorDoesNotBlink covers the blink preference reaching a
// terminal the Surface creates.
func TestSteadyTerminalCursorDoesNotBlink(t *testing.T) {
	surface := newActionSurface(t,
		WithTerminalTheme(cursorProbeTheme),
		WithTerminalCursor(func() (domainpreferences.CursorStyle, bool) {
			return domainpreferences.CursorBlock, false
		}))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 720, 480)
	tester.SetLocale("en")

	visible, hidden := sampleCursorFrames(t, tester)
	if !visible {
		t.Fatal("the steady cursor never rendered")
	}
	if hidden {
		t.Fatal("the steady cursor blinked off")
	}
}

// TestSetTerminalCursorRestylesLiveTerminal covers applying a cursor change to
// a terminal that is already running: MyGo reads its cursor options only at
// construction, so the Surface feeds the DECSCUSR a program would send.
func TestSetTerminalCursorRestylesLiveTerminal(t *testing.T) {
	surface := newActionSurface(t, WithTerminalTheme(cursorProbeTheme))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	tester.Frame()

	surface.SetTerminalCursor(domainpreferences.CursorBlock, false)
	tester.Frame()
	block := countCursorPixels(tester.Image())

	surface.SetTerminalCursor(domainpreferences.CursorBar, false)
	tester.Frame()
	bar := countCursorPixels(tester.Image())

	if block == 0 {
		t.Fatal("the steady block cursor never rendered")
	}
	if bar == 0 {
		t.Fatal("the bar cursor never rendered after a live change")
	}
	if bar >= block {
		t.Errorf("the bar cursor covers %d pixels, the block %d; the shape did not change", bar, block)
	}
}
