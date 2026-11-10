package shell

import (
	"github.com/mslxl/natcatty/ui/internal/styletest"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// TestControlPanelFitsAtMinimumClient is the ticket-43 regression at the
// app's enforced minimum window, a 704x421 client area. The Tab bar's right
// side once held a language picker that overflowed; the language is now in
// Settings, as the original's, and the bar's control panel button must stay
// inside the bar and open its content.
func TestControlPanelFitsAtMinimumClient(t *testing.T) {
	styletest.Each(t, func(t *testing.T) {
		const width, height = 704, 421
		shellState := state.NewState(state.WithQuickControls(func(c *ui.Context) {
			ui.Text(c, "panel content")
		}))
		tester := ui.NewTester(View(shellState), width, height)
		tester.SetLocale("en")
		tester.Frame()
		m := locales.In("en")
		if _, ok := tester.Find(m.NavLanguage()); ok {
			t.Fatalf("the Tab bar still has a language control: %v", tester.Texts())
		}
		label := m.NavControlPanel()
		rect, ok := tester.Find(label)
		if !ok || rect.W <= 0 || rect.H <= 0 {
			t.Fatalf("the Tab bar lost the control panel: %+v present=%v: %v", rect, ok, tester.Texts())
		}
		// Classic keeps it in the Tab bar; the macOS style at the sidebar's
		// foot.
		bottom, right := float32(topBarHeight), float32(width)
		if kit.CurrentStyle().Native() {
			bottom, right = height, sidebarWidth
		}
		if rect.X < -0.5 || rect.X+rect.W > right+0.5 || rect.Y < -0.5 || rect.Y+rect.H > bottom+0.5 {
			t.Fatalf("the control panel is clipped at %dx%d: %+v", width, height, rect)
		}
		if err := tester.Click(label); err != nil {
			t.Fatalf("the control panel is not clickable: %v", err)
		}
		tester.Frame()
		if _, ok := tester.Find("panel content"); !ok {
			t.Fatalf("the control panel did not open: %v", tester.Texts())
		}
	})
}
