package sessionui

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

type fakeBrowserPanel struct {
	onClose func()
	views   int
	closes  int
}

func (p *fakeBrowserPanel) View(c *ui.Context) ui.Element {
	p.views++
	return ui.Text(c, "browser-panel-content")
}

func (p *fakeBrowserPanel) Close() { p.closes++ }

func TestSessionMenuOpensTheBrowserAndItsCloseReturnsTheTerminal(t *testing.T) {
	m := locales.In("en")
	panel := &fakeBrowserPanel{}
	opens := 0
	surface := newActionSurface(t, WithBrowser(func(onClose func()) (BrowserPanel, error) {
		opens++
		panel.onClose = onClose
		return panel, nil
	}))
	surface.SetPaneActions(PaneActions{Title: "web-1", MenuLabel: m.SessionMenuFor(1)})
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	if err := tester.Click(m.SessionMenuFor(1)); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuOpenBrowser()); err != nil {
		t.Fatalf("Open Browser: %v", err)
	}
	tester.Frame()
	if opens != 1 || panel.views == 0 {
		t.Fatalf("open calls = %d, views = %d; want the panel shown", opens, panel.views)
	}
	if !tester.HasText("browser-panel-content") {
		t.Fatalf("the browser panel is not rendered: %v", tester.Texts())
	}

	// OpenBrowser is idempotent while a panel is open.
	if err := surface.OpenBrowser(); err != nil {
		t.Fatalf("OpenBrowser again: %v", err)
	}
	if opens != 1 {
		t.Fatalf("a second open created %d panels", opens)
	}

	// The panel's Close control calls the callback the Surface supplied. The
	// surface clears itself first, so the callback may call back in safely.
	panel.onClose()
	tester.Frame()
	if panel.closes != 1 {
		t.Fatalf("panel Close calls = %d, want 1", panel.closes)
	}
	if tester.HasText("browser-panel-content") {
		t.Fatalf("the browser stayed visible after Close: %v", tester.Texts())
	}
}

func TestSessionCloseClosesAnOpenBrowser(t *testing.T) {
	panel := &fakeBrowserPanel{}
	surface := newActionSurface(t, WithBrowser(func(func()) (BrowserPanel, error) {
		return panel, nil
	}))
	if err := surface.OpenBrowser(); err != nil {
		t.Fatalf("OpenBrowser: %v", err)
	}
	surface.Close()
	if panel.closes != 1 {
		t.Fatalf("closing the Session surface called panel Close %d times, want 1", panel.closes)
	}
}

func TestBrowserOpenFailureIsVisibleAndDismissible(t *testing.T) {
	m := locales.In("en")
	surface := newActionSurface(t, WithBrowser(func(func()) (BrowserPanel, error) {
		return nil, errors.New("no saved Host")
	}))
	surface.SetPaneActions(PaneActions{Title: "web-1", MenuLabel: m.SessionMenuFor(1)})
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	if err := tester.Click(m.SessionMenuFor(1)); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuOpenBrowser()); err != nil {
		t.Fatalf("Open Browser: %v", err)
	}
	tester.Frame()
	if !tester.HasText(m.BrowserOpenFailed()) {
		t.Fatalf("the open failure is not shown: %v", tester.Texts())
	}
	if err := tester.Click(m.SessionDismiss()); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	tester.Frame()
	if tester.HasText(m.BrowserOpenFailed()) {
		t.Fatalf("the open failure survived Dismiss: %v", tester.Texts())
	}
}
