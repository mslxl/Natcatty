package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// The shell preamble is the one place both the main view and a second
// renderer flush Session outcomes and take the terminal scheme's chrome. The
// theme must follow the surface only after Flush, so a Tab attached in this
// frame already colors the window.
func TestShellPreambleFlushesAndAppliesTheTerminalChrome(t *testing.T) {
	graph := newTestGraph(t)
	preset := domaintheme.TerminalTheme{
		Dark: true,
		Colors: domaintheme.TerminalColors{
			Background: "#101820",
			Foreground: "#e0e0e0",
			Cursor:     "#ff0000",
		},
	}
	graph.state.SetTerminalTheme(preset)
	if err := graph.state.OpenHost(domainvault.Host{Label: "prod"}); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}

	var base, chrome *ui.Theme
	tester := ui.NewTester(func(c *ui.Context) {
		if base == nil {
			base = c.Theme()
		}
		_, chrome = shellPreamble(c, graph.state, func() bool { return graph.state.TabCount() > 0 })
	}, 400, 300)

	deadline := time.Now().Add(2 * time.Second)
	for graph.state.TabCount() == 0 && time.Now().Before(deadline) {
		tester.Frame()
	}
	if graph.state.TabCount() == 0 {
		t.Fatal("the preamble did not flush the queued Host open")
	}
	tester.Frame()

	want := uitheme.Chrome(base, preset)
	if chrome == nil {
		t.Fatal("the preamble returned no theme")
	}
	if chrome.Background != want.Background || chrome.Surface != want.Surface || chrome.Accent != want.Accent {
		t.Fatalf("preamble chrome = %+v, want the terminal scheme's chrome %+v", chrome, want)
	}
}

// The read-only Tab reference renders the active Tab through the same body
// renderer the main view uses. It draws the split tree and side panel but
// wires no close, split, detach, or ratio command, so a second renderer cannot
// change the main Workspace.
func TestReadOnlyTabRefRendersTheActiveBodyWithoutWorkspaceCommands(t *testing.T) {
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	shellState := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		state.WithFilesPanel(state.FilesPanel{View: func(float32) func(*ui.Context) {
			return func(c *ui.Context) { ui.Text(c, "files-panel") }
		}}),
	)
	if err := shellState.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	(&workspaceViewGraph{state: shellState}).waitForSurfaces(t, 1)

	tab, ok := shellState.ActiveReadOnlyTab()
	if !ok {
		t.Fatal("the active Tab has no read-only reference")
	}
	ref := readOnlyTabRef(state.RenderOwnerDropdown, tab, 0)
	if ref.owner != state.RenderOwnerDropdown {
		t.Fatalf("ref owner = %q, want the dropdown renderer", ref.owner)
	}
	if ref.managesPanes || ref.splitRight != nil || ref.splitDown != nil ||
		ref.closeFocused != nil || ref.detach != nil || ref.setRatio != nil {
		t.Fatal("the read-only ref wires a destructive Workspace command")
	}

	shellState.ToggleFiles(tab.ID(), "", "")
	rootBefore, _, _ := shellState.TabTree(0)
	activeBefore := shellState.ActiveTab()

	tester := ui.NewTester(func(c *ui.Context) {
		m, t := shellPreamble(c, shellState, func() bool { return true })
		activeTabBody(c, shellState, m, t, ref)
	}, 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	if !tester.HasText("terminal-surface") {
		t.Fatalf("the read-only body did not render the terminal: %v", tester.Texts())
	}
	if !tester.HasText("files-panel") {
		t.Fatalf("the read-only body did not render the side panel: %v", tester.Texts())
	}
	if shellState.TabCount() != 1 || shellState.ActiveTab() != activeBefore {
		t.Fatalf("tabs = %d, active = %d; want 1 and %d",
			shellState.TabCount(), shellState.ActiveTab(), activeBefore)
	}
	if rootAfter, _, _ := shellState.TabTree(0); rootAfter != rootBefore {
		t.Fatal("rendering the read-only body replaced the split tree")
	}
}
