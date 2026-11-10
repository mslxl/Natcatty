package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/internal/styletest"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// TestLocalTerminalFilesButtonOpensTheSidePanel clicks the side panel button
// in a local terminal Pane's header, which opens the Tab's side panel.
func TestLocalTerminalFilesButtonOpensTheSidePanel(t *testing.T) {
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	s := state.NewState(
		state.WithSessions(service, func(session *appsession.Session) (state.Surface, error) {
			return sessionui.New(session)
		}),
		state.WithFilesPanel(state.FilesPanel{View: func(float32) func(*ui.Context) {
			return func(c *ui.Context) { ui.Text(c, "files-panel") }
		}}),
	)
	graph := &workspaceViewGraph{state: s}
	if err := s.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 1)
	tt := ui.NewTester(View(s), 1100, 720)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()
	t.Logf("texts: %v", tt.Texts())
	if err := tt.Click("Toggle Side Panel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("files-panel") {
		t.Fatal("the side panel did not open")
	}
}

func TestToggleSidePanelShortcutOpensALocalTerminalsPanel(t *testing.T) {
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	s := state.NewState(
		state.WithSessions(service, func(session *appsession.Session) (state.Surface, error) {
			return sessionui.New(session)
		}),
		state.WithFilesPanel(state.FilesPanel{View: func(float32) func(*ui.Context) {
			return func(c *ui.Context) { ui.Text(c, "files-panel") }
		}}),
	)
	graph := &workspaceViewGraph{state: s}
	if err := s.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 1)
	if !s.ToggleActiveFiles() {
		t.Fatal("ToggleActiveFiles reported no file system")
	}
	tt := ui.NewTester(View(s), 1100, 720)
	tt.Frame()
	if !tt.HasText("files-panel") {
		t.Fatal("the side panel did not open")
	}
}

// TestHoveringATabKeepsTheTabsAfterItInPlace hovers the first of two Tabs,
// which shows its menu and close buttons; the strip after it must not move.
func TestHoveringATabKeepsTheTabsAfterItInPlace(t *testing.T) {
	styletest.Each(t, func(t *testing.T) {
		graph := newWorkspaceViewGraph(t)
		for range 2 {
			if err := graph.state.NewTab(); err != nil {
				t.Fatal(err)
			}
		}
		graph.waitForSurfaces(t, 2)
		tt := ui.NewTester(View(graph.state), 1100, 720)
		tt.SetLocale("en")
		tt.Frame()
		m := locales.In("en")
		before, ok := tt.Find(m.MenuNewTab())
		first, found := tt.Find("Local Terminal")
		if !ok || !found || first.Y >= topBarHeight {
			t.Fatalf("want the Tabs and New Tab in the bar: %q", tt.Texts())
		}
		tt.Move(first.X+first.W/2, first.Y+first.H/2)
		tt.Frame()
		if after, _ := tt.Find(m.MenuNewTab()); after.X != before.X {
			t.Fatalf("New Tab moved from x=%v to x=%v when the first Tab was hovered", before.X, after.X)
		}
	})
}
