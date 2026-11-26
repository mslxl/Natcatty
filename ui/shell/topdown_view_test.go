package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsearch "github.com/mslxl/natcatty/application/search"
	appsession "github.com/mslxl/natcatty/application/session"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	"github.com/mslxl/natcatty/ui/locales"
	uisearch "github.com/mslxl/natcatty/ui/search"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// TestTopdownViewRendersTheFullMainShell proves the top-down route is the
// complete application shell, not a compact terminal strip: the home and SFTP
// Tabs, the Vault sidebar, the work Tab strip, and the active terminal.
func TestTopdownViewRendersTheFullMainShell(t *testing.T) {
	graph := newFollowGraph(t)
	m := locales.In("en")
	tester := ui.NewTester(TopdownView(graph.state), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	if !tester.HasText(m.NavVault()) || !tester.HasText(m.NavSftp()) {
		t.Fatalf("the top-down view lacks the window's home Tabs: %v", tester.Texts())
	}
	for _, label := range []string{m.NavHosts(), m.NavKeychain(), m.NavSettings()} {
		if !tester.HasText(label) {
			t.Fatalf("the top-down view lacks the Vault sidebar entry %q: %v", label, tester.Texts())
		}
	}

	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	graph.waitForSurfaces(t, 1)
	tester.Frame()

	if !tester.HasText("follow-terminal-1") {
		t.Fatalf("the top-down view did not render the terminal: %v", tester.Texts())
	}
	info, ok := graph.state.TabInfo(0)
	if !ok {
		t.Fatal("no work Tab info")
	}
	if !tester.HasText(tabTitle(m, info)) {
		t.Fatalf("the top-down view lacks the work Tab strip entry %q: %v", tabTitle(m, info), tester.Texts())
	}
	if !tester.HasText(m.NavVault()) || !tester.HasText(m.NavSftp()) {
		t.Fatalf("the top-down view lost the fixed home Tabs: %v", tester.Texts())
	}

	// The home surfaces are the real Vault and SFTP pages, not stand-ins.
	home := state.NewState(
		state.WithVault(func(c *ui.Context) { ui.Text(c, "vault-surface") }),
		state.WithSFTP(func(c *ui.Context) { ui.Text(c, "sftp-surface") }),
	)
	homeTester := ui.NewTester(TopdownView(home), 1100, 720)
	homeTester.SetLocale("en")
	homeTester.Frame()
	if !homeTester.HasText("vault-surface") {
		t.Fatalf("the top-down view did not render the Vault surface: %v", homeTester.Texts())
	}
	if err := homeTester.Click(m.NavSftp()); err != nil {
		t.Fatalf("clicking the SFTP Tab: %v", err)
	}
	homeTester.Frame()
	if !homeTester.HasText("sftp-surface") {
		t.Fatalf("the top-down view did not render the SFTP surface: %v", homeTester.Texts())
	}
}

// TestTopdownViewRendersTheGlobalOverlays proves the overlays the main window
// draws above every surface are part of the top-down route too.
func TestTopdownViewRendersTheGlobalOverlays(t *testing.T) {
	search := uisearch.New(uisearch.WithSource(
		func(uisearch.Mode, string) []appsearch.Record { return nil }))
	shellState := state.NewState(state.WithSearch(search))
	tester := ui.NewTester(TopdownView(shellState), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if tester.HasText(m.SearchJumpTo()) {
		t.Fatalf("the closed overlay rendered: %v", tester.Texts())
	}
	search.Open(uisearch.ModeQuickSwitcher)
	tester.Frame()
	if !tester.HasText(m.SearchJumpTo()) || !tester.HasText(m.SearchEmpty()) {
		t.Fatalf("the Quick Switcher did not render above the top-down shell: %v", tester.Texts())
	}
}

// TestTopdownViewShowsABrowserPlaceholder proves an open browser panel never
// mounts its webview in the Panel: the route offers the main window instead,
// exactly like the follow view.
func TestTopdownViewShowsABrowserPlaceholder(t *testing.T) {
	graph := newFollowGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	graph.waitForSurfaces(t, 1)
	graph.surfaces[0].browser = true

	opened := 0
	tester := ui.NewTester(TopdownView(graph.state, WithFollowOpenMain(func() { opened++ })), 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	m := locales.In("en")
	if !tester.HasText(m.FollowBrowserTitle()) || !tester.HasText(m.FollowBrowserDescription()) {
		t.Fatalf("the browser placeholder is missing: %v", tester.Texts())
	}
	if tester.HasText("follow-terminal-1") {
		t.Fatalf("the top-down view mounted a browser Tab's terminal: %v", tester.Texts())
	}
	if graph.surfaces[0].views != 0 {
		t.Fatalf("the top-down view mounted the Surface %d times with a browser open", graph.surfaces[0].views)
	}
	if err := tester.Click(m.FollowOpenMain()); err != nil {
		t.Fatalf("clicking Open main window: %v", err)
	}
	if opened != 1 {
		t.Fatalf("Open main window fired %d times", opened)
	}
}

// TestTopdownViewReRequestsFocusEveryFrame proves the top-down renderer does
// not rely on MyGo element state: every rebuilt frame asks the focused Pane
// for the keyboard again.
func TestTopdownViewReRequestsFocusEveryFrame(t *testing.T) {
	graph := newFollowGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	graph.waitForSurfaces(t, 1)

	tester := ui.NewTester(TopdownView(graph.state), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	if graph.surfaces[0].focuses == 0 {
		t.Fatal("the top-down view never focused the active terminal")
	}
	before := graph.surfaces[0].focuses
	tester.Frame()
	if got := graph.surfaces[0].focuses; got != before+1 {
		t.Fatalf("focus requests = %d after a rebuilt frame, want %d", got, before+1)
	}
}

// TestTopdownViewUsesTheLiveShellTheme proves the top-down route takes the
// terminal scheme's chrome, and a scheme change reaches the next frame.
func TestTopdownViewUsesTheLiveShellTheme(t *testing.T) {
	graph := newFollowGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 1)

	first := domaintheme.TerminalTheme{
		Dark:   true,
		Colors: domaintheme.TerminalColors{Background: "#101820", Foreground: "#e0e0e0"},
	}
	second := domaintheme.TerminalTheme{
		Dark:   true,
		Colors: domaintheme.TerminalColors{Background: "#204060", Foreground: "#f0f0f0"},
	}
	graph.state.SetTerminalTheme(first)

	var base *ui.Theme
	tester := ui.NewTester(func(c *ui.Context) {
		if base == nil {
			base = c.Theme()
		}
		TopdownView(graph.state)(c)
	}, 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	want := uitheme.Chrome(base, first).Background
	if got := graph.surfaces[0].chromeBg; got != want {
		t.Fatalf("top-down chrome = %v, want the first scheme's %v", got, want)
	}

	graph.state.SetTerminalTheme(second)
	tester.Frame()
	want = uitheme.Chrome(base, second).Background
	if got := graph.surfaces[0].chromeBg; got != want {
		t.Fatalf("top-down chrome after the live change = %v, want %v", got, want)
	}
}

// TestTopdownViewFinishesItsOwnSideFrame proves the top-down renderer takes
// over the per-frame side-frame bookkeeping under the dropdown owner, so a
// tool that leaves the screen is stopped by this renderer.
func TestTopdownViewFinishesItsOwnSideFrame(t *testing.T) {
	hidden := 0
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	shellState := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return &followSurface{text: "topdown-terminal"}, nil
		}),
		state.WithFilesPanel(state.FilesPanel{View: func(float32) func(*ui.Context) {
			return func(c *ui.Context) { ui.Text(c, "files-panel") }
		}}),
		state.WithSideTools(state.SideTool{
			ID: state.SideNotes,
			View: func(state.SideTarget, float32) func(*ui.Context) {
				return func(c *ui.Context) { ui.Text(c, "notes-tool") }
			},
			Hide: func() { hidden++ },
		}),
	)
	if err := shellState.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	(&workspaceViewGraph{state: shellState}).waitForSurfaces(t, 1)

	tester := ui.NewTester(TopdownView(shellState), 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	index := 0
	tabID, ok := shellState.TabID(index)
	if !ok {
		t.Fatalf("no Tab at %d", index)
	}
	shellState.ToggleFiles(tabID, "", "")
	shellState.SetSideTool(tabID, state.SideNotes)
	tester.Frame()
	if !tester.HasText("notes-tool") {
		t.Fatalf("the top-down view did not render the side panel: %v", tester.Texts())
	}
	if hidden != 0 {
		t.Fatalf("the live tool was stopped while drawn: %d", hidden)
	}
	// Let the opening drawer finish so the close starts from the panel the
	// user sees.
	tester.Advance(2 * time.Second)
	tester.Frame()

	shellState.CloseFiles(tabID)
	tester.Frame()
	if hidden != 0 {
		t.Fatalf("the closing drawer stopped the tool before it left: hides = %d", hidden)
	}
	if !tester.HasText("notes-tool") {
		t.Fatalf("the closing drawer did not paint the tool: %v", tester.Texts())
	}
	// The drawer holds the tool until its run ends; then this renderer, under
	// the dropdown owner, finishes the side frame.
	tester.Advance(2 * time.Second)
	tester.Frame()
	if hidden != 1 {
		t.Fatalf("the top-down renderer did not finish its side frame: hides = %d", hidden)
	}
}
