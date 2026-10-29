package shell

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appworkspace "github.com/mslxl/natcatty/application/workspace"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// multiFactory gives each Session its own transport so split views can show
// several live Panes in one test.
type multiFactory struct{}

func (multiFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return newFakeTransport(), nil
}

type workspaceViewGraph struct {
	state *state.State
}

func newWorkspaceViewGraph(t *testing.T) *workspaceViewGraph {
	t.Helper()
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	return &workspaceViewGraph{state: state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
	)}
}

func (g *workspaceViewGraph) waitForSurfaces(t *testing.T, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g.state.Flush()
		live := 0
		for index := 0; index < g.state.TabCount(); index++ {
			root, _, ok := g.state.TabTree(index)
			if !ok {
				continue
			}
			for _, id := range domainworkspace.CollectPaneIDs(root) {
				if info, ok := g.state.PaneInfoAt(index, id); ok && info.Surface != nil {
					live++
				}
			}
		}
		if live == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d Pane surfaces", count)
}

func TestShellRendersRecursiveSplitPanes(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 2)
	tt.Frame()

	if got := countText(tt.Texts(), "terminal-surface"); got != 2 {
		t.Fatalf("terminal surfaces = %d, want 2; texts %q", got, tt.Texts())
	}
	if tt.HasText("Split Right") {
		t.Fatal("split menu items are visible before opening a menu")
	}
}

func TestShellPaneMenuSplitsAndCloses(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if err := tt.Click("⋮"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Split Right"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	graph.waitForSurfaces(t, 2)
	tt.Frame()
	if got := countText(tt.Texts(), "terminal-surface"); got != 2 {
		t.Fatalf("terminal surfaces = %d after the split, want 2", got)
	}

	if err := tt.Click("⋮"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Close Pane"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	root, _, _ := graph.state.TabTree(0)
	if root == nil || !root.IsPane() {
		t.Fatalf("root after Close Pane = %+v, want the surviving Pane", root)
	}
	if got := countText(tt.Texts(), "terminal-surface"); got != 1 {
		t.Fatalf("terminal surfaces = %d after Close Pane, want 1", got)
	}
}

func TestShellTabMenuDuplicatesWithCopyTitle(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if err := tt.Click("⋯"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Duplicate Tab"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if graph.state.TabCount() != 2 {
		t.Fatalf("tabs = %d after Duplicate Tab, want 2", graph.state.TabCount())
	}
	want := locales.In("en").TabCopyTitle("Local Session 1", 1)
	if !tt.HasText(want) {
		t.Fatalf("shell does not show %q, texts %q", want, tt.Texts())
	}
}

func TestShellTabMenuClosesOtherTabs(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	graph.state.SelectTab(0)
	tt.Frame()

	if err := tt.Click("⋯"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Close Other Tabs"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d after Close Other Tabs, want 1", graph.state.TabCount())
	}
	if !tt.HasText("Local Session 1") {
		t.Fatalf("the kept Tab is not shown, texts %q", tt.Texts())
	}
}

func TestShellDragReordersTabs(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	tt.Frame()

	third, ok := tt.Find("Local Session 3")
	if !ok {
		t.Fatalf("Local Session 3 is not visible, texts %q", tt.Texts())
	}
	first, ok := tt.Find("Local Session 1")
	if !ok {
		t.Fatalf("Local Session 1 is not visible, texts %q", tt.Texts())
	}
	tt.Press(third.X+third.W/2, third.Y+third.H/2)
	tt.Move(first.X+first.W/2, first.Y+first.H/2)
	tt.Frame()
	tt.Release(first.X+first.W/2, first.Y+first.H/2)
	tt.Frame()

	if got := graph.state.TabNumbers(); len(got) != 3 || got[0] != 3 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("tab numbers after the drag = %v, want [3 1 2]", got)
	}
}

func TestShellShowsTheTabActivityMarker(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.state.SetTabDirty(0, true)
	tt.Frame()

	if !tt.HasText(locales.In("en").TabDirtyMarker()) {
		t.Fatalf("shell does not show the activity marker, texts %q", tt.Texts())
	}
}

func countText(texts []string, want string) int {
	count := 0
	for _, text := range texts {
		if text == want {
			count++
		}
	}
	return count
}

func TestShellDragMovesAPaneOntoAnotherTab(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 2)
	tt.Frame()

	handle, ok := tt.Find("::")
	if !ok {
		t.Fatalf("Pane drag handle is not visible, texts %q", tt.Texts())
	}
	first, ok := tt.Find("Local Session 1")
	if !ok {
		t.Fatalf("Local Session 1 is not visible, texts %q", tt.Texts())
	}
	tt.Press(handle.X+handle.W/2, handle.Y+handle.H/2)
	tt.Move(first.X+first.W/2, first.Y+first.H/2)
	tt.Frame()
	tt.Release(first.X+first.W/2, first.Y+first.H/2)
	tt.Frame()

	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d, want the emptied source Tab removed", graph.state.TabCount())
	}
	root, _, ok := graph.state.TabTree(0)
	if !ok || len(domainworkspace.CollectPaneIDs(root)) != 2 {
		t.Fatalf("target Tab panes = %+v, want two", root)
	}
}

// failingTelnetFactory fails the first Open with a network failure and opens
// every later attempt, so a restored Pane can be reconnected in a test.
type failingTelnetFactory struct {
	mu    sync.Mutex
	opens int
	specs []appsession.StartSpec
}

func (f *failingTelnetFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	f.specs = append(f.specs, spec)
	if f.opens == 1 {
		return nil, &appconnection.Error{Kind: appconnection.KindNetwork}
	}
	return newFakeTransport(), nil
}

// viewWorkspaceRepository keeps one restore payload in memory.
type viewWorkspaceRepository struct {
	payload domainworkspace.Payload
}

func (r *viewWorkspaceRepository) Load(context.Context) (domainworkspace.Payload, error) {
	return r.payload, nil
}

func (r *viewWorkspaceRepository) Save(_ context.Context, payload domainworkspace.Payload) error {
	r.payload = payload
	return nil
}

func (r *viewWorkspaceRepository) Clear(context.Context) error {
	r.payload = domainworkspace.EmptyPayload()
	return nil
}

func telnetRestorePayload() domainworkspace.Payload {
	return domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-telnet", Number: 1, Root: domainworkspace.NewPaneNode("pane-telnet"),
		}},
		Panes: []domainworkspace.RestoredPane{{
			ID: "pane-telnet",
			Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionTelnet, HostID: "host-telnet",
				Hostname: "127.0.0.1", Port: 2323, Label: "telnet-fake",
			},
		}},
	}
}

func TestRestoredPaneShowsTheConnectionFailureAndReconnects(t *testing.T) {
	factory := &failingTelnetFactory{}
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	repo := &viewWorkspaceRepository{payload: telnetRestorePayload()}
	st := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		// A nil Host resolver keeps the stored endpoint, so the failure is the
		// factory's typed network error rather than a missing Host.
		state.WithWorkspace(appworkspace.NewService(repo), nil),
	)
	st.Restore(context.Background())
	tt := ui.NewTester(View(st), 900, 600)
	tt.SetLocale("en")

	deadline := time.Now().Add(2 * time.Second)
	for {
		st.Flush()
		info, ok := st.PaneInfoAt(0, "pane-telnet")
		if ok && !info.Pending && info.Err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the failed Pane; info %+v", info)
		}
		time.Sleep(time.Millisecond)
	}
	tt.Frame()

	networkProblem := locales.In("en").ConnectionErrorNetwork()
	if !tt.HasText(networkProblem) {
		t.Fatalf("Pane does not show %q, texts %q", networkProblem, tt.Texts())
	}
	if !tt.HasText(locales.In("en").SessionMenuReconnect()) {
		t.Fatalf("Pane has no Reconnect control, texts %q", tt.Texts())
	}
	if tt.HasText(locales.In("en").PaneOpenFailed()) {
		t.Fatalf("Pane still shows the generic open failure, texts %q", tt.Texts())
	}

	if err := tt.Click(locales.In("en").SessionMenuReconnect()); err != nil {
		t.Fatalf("click Reconnect: %v", err)
	}
	tt.Frame()

	deadline = time.Now().Add(2 * time.Second)
	for {
		st.Flush()
		info, ok := st.PaneInfoAt(0, "pane-telnet")
		if ok && info.Surface != nil && info.Err == nil {
			tt.Frame()
			if !tt.HasText("terminal-surface") {
				t.Fatalf("reconnected Pane does not render its surface, texts %q", tt.Texts())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the reconnected Pane; info %+v", info)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMissingHostPaneShowsNoReconnect(t *testing.T) {
	repo := &viewWorkspaceRepository{payload: telnetRestorePayload()}
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	st := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		state.WithWorkspace(appworkspace.NewService(repo),
			func(context.Context, model.HostID) (domainvault.Host, bool) {
				return domainvault.Host{}, false
			}),
	)
	st.Restore(context.Background())
	tt := ui.NewTester(View(st), 900, 600)
	tt.SetLocale("en")

	deadline := time.Now().Add(2 * time.Second)
	for {
		st.Flush()
		info, ok := st.PaneInfoAt(0, "pane-telnet")
		if ok && !info.Pending && info.Err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the missing-Host Pane; info %+v", info)
		}
		time.Sleep(time.Millisecond)
	}
	tt.Frame()

	if !tt.HasText(locales.In("en").PaneHostMissing()) {
		t.Fatalf("Pane does not show the missing Host text, texts %q", tt.Texts())
	}
	if tt.HasText(locales.In("en").SessionMenuReconnect()) {
		t.Fatalf("a missing Host Pane offers a retry, texts %q", tt.Texts())
	}
}

func TestShellKeepsWorkTabsVisibleOverTheVault(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.state.ShowVault()
	tt.Frame()

	tab, ok := tt.Find("Local Session 1")
	if !ok {
		t.Fatalf("the work Tab is hidden on the Vault, texts %q", tt.Texts())
	}
	tt.ClickAt(tab.X+tab.W/2, tab.Y+tab.H/2)
	tt.Frame()
	if graph.state.VaultVisible() {
		t.Fatal("clicking the Tab did not return to the Session")
	}
}
