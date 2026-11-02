package titlebar

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

type fakeWindow struct {
	minimized, maximized, closed bool
}

func (w *fakeWindow) Minimize()         { w.minimized = true }
func (w *fakeWindow) ToggleMaximize()   { w.maximized = !w.maximized }
func (w *fakeWindow) IsMaximized() bool { return w.maximized }
func (w *fakeWindow) Close()            { w.closed = true }

func framed(style Style, window *fakeWindow, width, height int) *ui.Tester {
	content := func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			ui.Row(c).FillWidth().Height(Height).DragWindow().Children(func() {
				DragStripe(c)
				ui.Text(c, "Natcatty")
			})
		})
	}
	tt := ui.NewTester(Frame(style, func() Window {
		if window == nil {
			return nil
		}
		return window
	}, content), width, height)
	tt.SetLocale("en")
	tt.Frame()
	return tt
}

// TestControlsSitInTheTopRightCorner keeps each bar style's three controls in
// the room its title bar leaves them, in the original's order and widths.
func TestControlsSitInTheTopRightCorner(t *testing.T) {
	const width = 800
	m := locales.In("en")
	for _, style := range []struct {
		style        Style
		button, room float32
	}{{Tabs, ButtonWidth, Room}, {Popup, PopupButtonWidth, PopupRoom}} {
		tt := framed(style.style, &fakeWindow{}, width, 500)
		var previous float32
		for i, label := range []string{m.WindowMinimize(), m.WindowMaximize(), m.WindowClose()} {
			rect, ok := tt.Find(label)
			if !ok {
				t.Fatalf("style %d: no %q control: %v", style.style, label, tt.Texts())
			}
			if rect.Y != 0 || rect.H != Height || rect.W != style.button ||
				rect.X < width-style.room-0.5 || rect.X+rect.W > width+0.5 {
				t.Fatalf("style %d: %q at %+v, outside the %v-wide corner", style.style, label, rect, style.room)
			}
			if i > 0 && rect.X <= previous {
				t.Fatalf("style %d: %q at %+v is not right of the previous control", style.style, label, rect)
			}
			previous = rect.X
		}
	}
}

// TestDialogShowsOnlyAClose keeps the Settings window to the original's lone
// close button at the end of its header row.
func TestDialogShowsOnlyAClose(t *testing.T) {
	const width = 800
	m := locales.In("en")
	window := &fakeWindow{}
	tt := framed(Dialog, window, width, 500)
	for _, label := range []string{m.WindowMinimize(), m.WindowMaximize()} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("the dialog shows %q", label)
		}
	}
	rect, ok := tt.Find(m.WindowClose())
	if !ok || rect.W != CloseSize || rect.H != CloseSize || rect.Y != CloseTop ||
		rect.X+rect.W != width-CloseInset {
		t.Fatalf("close at %+v (%v)", rect, ok)
	}
	if err := tt.Click(m.WindowClose()); err != nil || !window.closed {
		t.Fatalf("close: %v, closed %v", err, window.closed)
	}
}

func TestControlsActOnTheWindow(t *testing.T) {
	m := locales.In("en")
	for _, style := range []Style{Tabs, Popup} {
		window := &fakeWindow{}
		tt := framed(style, window, 800, 500)
		if err := tt.Click(m.WindowMinimize()); err != nil || !window.minimized {
			t.Fatalf("minimize: %v, minimized %v", err, window.minimized)
		}
		if err := tt.Click(m.WindowMaximize()); err != nil || !window.maximized {
			t.Fatalf("maximize: %v, maximized %v", err, window.maximized)
		}
		tt.Frame()
		// A maximized window offers to restore it instead.
		if _, ok := tt.Find(m.WindowMaximize()); ok {
			t.Fatal("a maximized window still offers to maximize")
		}
		if err := tt.Click(m.WindowRestore()); err != nil || window.maximized {
			t.Fatalf("restore: %v, maximized %v", err, window.maximized)
		}
		if err := tt.Click(m.WindowClose()); err != nil || !window.closed {
			t.Fatalf("close: %v, closed %v", err, window.closed)
		}
	}
}

func TestFrameWithoutAWindowDrawsNoControls(t *testing.T) {
	tt := framed(Tabs, nil, 800, 500)
	if _, ok := tt.Find(locales.In("en").WindowClose()); ok {
		t.Fatal("controls drawn before the window exists")
	}
	if !tt.HasText("Natcatty") {
		t.Fatalf("the content is gone: %v", tt.Texts())
	}
}
