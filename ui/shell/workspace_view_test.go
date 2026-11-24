package shell

import (
	"context"
	"fmt"
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

func (g *workspaceViewGraph) waitForSurfaces(t testing.TB, count int) {
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
	// Split Right and Split Down are header buttons too; Detach Session is
	// only ever a menu item.
	if tt.HasText(locales.In("en").PaneDetach()) {
		t.Fatal("Pane menu items are visible before opening a menu")
	}
}

// TestShellRendersOnlyTheMagnifiedPane pins the view side of the magnified
// guard: hidden members draw no surface and therefore no drop zone.
func TestShellRendersOnlyTheMagnifiedPane(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 2)
	root, _, _ := graph.state.TabTree(0)
	panes := domainworkspace.CollectPaneIDs(root)
	if len(panes) != 2 {
		t.Fatalf("panes = %v, want 2", panes)
	}

	if !graph.state.SetMagnifiedPane(0, panes[0]) {
		t.Fatal("SetMagnifiedPane rejected a member Pane")
	}
	tt.Frame()
	if got := countText(tt.Texts(), "terminal-surface"); got != 1 {
		t.Fatalf("terminal surfaces = %d while magnified, want 1", got)
	}
	if !graph.state.SetMagnifiedPane(0, "") {
		t.Fatal("clearing the zoom failed")
	}
	tt.Frame()
	if got := countText(tt.Texts(), "terminal-surface"); got != 2 {
		t.Fatalf("terminal surfaces = %d after restoring the split, want 2", got)
	}
}

// TestShellShowsTheSiblingAfterDetachingTheMagnifiedPane pins the rendered
// side of the stale-zoom defect: after the zoomed Pane leaves the Tab, the
// surviving member still draws its surface and the Tab body is not blank.
func TestShellShowsTheSiblingAfterDetachingTheMagnifiedPane(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 2)
	root, _, _ := graph.state.TabTree(0)
	panes := domainworkspace.CollectPaneIDs(root)
	if len(panes) != 2 {
		t.Fatalf("panes = %v, want 2", panes)
	}

	if !graph.state.SetMagnifiedPane(0, panes[0]) {
		t.Fatal("SetMagnifiedPane rejected a member Pane")
	}
	tt.Frame()
	if got := countText(tt.Texts(), "terminal-surface"); got != 1 {
		t.Fatalf("terminal surfaces = %d while magnified, want 1", got)
	}
	if !graph.state.DetachPane(0, panes[0]) {
		t.Fatal("DetachPane failed")
	}
	tt.Frame()
	if got := countText(tt.Texts(), "terminal-surface"); got != 1 {
		t.Fatalf("terminal surfaces = %d after detaching the zoomed Pane, want the sibling", got)
	}
	if graph.state.MagnifiedPaneID(0) != "" {
		t.Fatal("the detached Pane is still reported as magnified")
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

func TestShellTabMenuDuplicateKeepsTheSameTitle(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.state.RenameTab(0, "Build shell")
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
	// The original gives a duplicate its source's title instead of marking a
	// copy generation.
	first, _, _ := graph.state.TabLabel(0)
	second, _, _ := graph.state.TabLabel(1)
	if first != "Build shell" || second != "Build shell" {
		t.Fatalf("duplicated Tab titles = %q and %q, want the source title", first, second)
	}
	if suffix := locales.In("en").TabCopyTitle("Build shell", 1); tt.HasText(suffix) {
		t.Fatalf("the duplicate is marked as a copy: %q", suffix)
	}
}

func TestShellTabMenuClosesOtherTabs(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	for index := range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		graph.state.RenameTab(index, fmt.Sprintf("Tab %d", index+1))
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
	if !tt.HasText("Tab 1") {
		t.Fatalf("the kept Tab is not shown, texts %q", tt.Texts())
	}
}

func TestShellDragReordersTabs(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	for index := range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		graph.state.RenameTab(index, fmt.Sprintf("Tab %d", index+1))
	}
	tt.Frame()

	third, ok := tt.Find("Tab 3")
	if !ok {
		t.Fatalf("Tab 3 is not visible, texts %q", tt.Texts())
	}
	first, ok := tt.Find("Tab 1")
	if !ok {
		t.Fatalf("Tab 1 is not visible, texts %q", tt.Texts())
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
	graph.state.RenameTab(0, "Tab 1")
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.state.RenameTab(1, "Tab 2")
	graph.waitForSurfaces(t, 2)
	tt.Frame()

	handle, ok := tt.Find("::")
	if !ok {
		t.Fatalf("Pane drag handle is not visible, texts %q", tt.Texts())
	}
	first, ok := tt.Find("Tab 1")
	if !ok {
		t.Fatalf("Tab 1 is not visible, texts %q", tt.Texts())
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

// TestShellShowsTheSiblingAfterAMagnifiedPaneJoinsAnotherTab pins the rendered
// side of the cross-Tab defect: dropping the zoomed Pane on a Tab strip must
// clear the zoom, so going back to the source Tab shows its surviving sibling
// instead of an empty body.
func TestShellShowsTheSiblingAfterAMagnifiedPaneJoinsAnotherTab(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 2)
	root, _, _ := graph.state.TabTree(0)
	panes := domainworkspace.CollectPaneIDs(root)
	if len(panes) != 2 {
		t.Fatalf("panes = %v, want 2", panes)
	}
	if !graph.state.SetMagnifiedPane(0, panes[0]) {
		t.Fatal("SetMagnifiedPane rejected a member Pane")
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.state.RenameTab(1, "Tab 2")
	graph.waitForSurfaces(t, 3)
	graph.state.SelectTab(0)
	tt.Frame()

	handle, ok := tt.Find("::")
	if !ok {
		t.Fatalf("the zoomed Pane has no drag handle, texts %q", tt.Texts())
	}
	tab, ok := tt.Find("Tab 2")
	if !ok {
		t.Fatalf("Tab 2 is not visible, texts %q", tt.Texts())
	}
	tt.Press(handle.X+handle.W/2, handle.Y+handle.H/2)
	tt.Move(tab.X+tab.W/2, tab.Y+tab.H/2)
	tt.Frame()
	tt.Release(tab.X+tab.W/2, tab.Y+tab.H/2)
	tt.Frame()

	if graph.state.TabCount() != 2 {
		t.Fatalf("tabs = %d, want the source Tab to survive with its sibling", graph.state.TabCount())
	}
	if got := graph.state.MagnifiedPaneID(0); got != "" {
		t.Fatalf("magnified Pane = %q after the tab-strip drop, want the zoom cleared", got)
	}
	graph.state.SelectTab(0)
	tt.Frame()
	if got := countText(tt.Texts(), "terminal-surface"); got != 1 {
		t.Fatalf("terminal surfaces = %d on the source Tab, want the surviving sibling", got)
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

// pendingVNCViewFactory holds a VNC handshake open so a test can observe the
// connecting Tab, while every other Kind opens at once.
type pendingVNCViewFactory struct {
	started chan struct{}
	release chan struct{}
}

func (f *pendingVNCViewFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if spec.Kind != appsession.KindVNC {
		return newFakeTransport(), nil
	}
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
		return newDisplayTransport(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// displayTransport carries pixels instead of bytes, as a VNC backend does.
type displayTransport struct {
	*fakeTransport
	display *viewFakeDisplay
}

func newDisplayTransport() *displayTransport {
	return &displayTransport{
		fakeTransport: newFakeTransport(),
		display:       &viewFakeDisplay{events: make(chan appsession.DisplayEvent)},
	}
}

func (t *displayTransport) Display() appsession.DisplaySession { return t.display }

func (t *displayTransport) Close() error {
	_ = t.display.Close()
	return t.fakeTransport.Close()
}

type viewFakeDisplay struct {
	events    chan appsession.DisplayEvent
	closeOnce sync.Once
}

func (d *viewFakeDisplay) Events() <-chan appsession.DisplayEvent { return d.events }
func (d *viewFakeDisplay) SendKey(uint32, bool) error             { return nil }
func (d *viewFakeDisplay) SendPhysicalKey(uint32, uint32, bool) error {
	return nil
}
func (d *viewFakeDisplay) SendPointer(int, int, uint8) error                { return nil }
func (d *viewFakeDisplay) SendCutText(string) error                         { return nil }
func (d *viewFakeDisplay) RequestUpdate(bool) error                         { return nil }
func (d *viewFakeDisplay) Screens() []appsession.DisplayScreen              { return nil }
func (d *viewFakeDisplay) SetScreenCount(int) error                         { return nil }
func (d *viewFakeDisplay) SetScreenLayout([]appsession.DisplayScreen) error { return nil }
func (d *viewFakeDisplay) Close() error {
	d.closeOnce.Do(func() { close(d.events) })
	return nil
}

// TestConnectingVNCTabIsClickableAndShowsItsEndpoint is the VNC connecting
// contract: the Tab exists at once, can be selected away from and back to
// while the desktop handshake runs, and its Pane names the endpoint.
func TestConnectingVNCTabIsClickableAndShowsItsEndpoint(t *testing.T) {
	m := locales.In("en")
	factory := &pendingVNCViewFactory{started: make(chan struct{}, 1), release: make(chan struct{})}
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	st := state.NewState(state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
		return fakeSurface{}, nil
	}))
	tt := ui.NewTester(View(st), 1100, 720)
	tt.SetLocale("en")
	tt.Frame()

	if err := st.OpenHost(domainvault.Host{
		ID: "vnc-desk", Label: "desk", Hostname: "desk.local", Port: 5901,
		Username: "viewer", Protocol: domainvault.ProtocolVNC,
	}); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	select {
	case <-factory.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the VNC dial never started")
	}
	tt.Frame()
	if !tt.HasText(m.PaneConnecting()) {
		t.Fatalf("the connecting Pane shows no state; texts %q", tt.Texts())
	}
	endpoint := "viewer" + "@" + "desk.local:5901"
	if !tt.HasText(endpoint) {
		t.Fatalf("the connecting Pane does not show %q; texts %q", endpoint, tt.Texts())
	}

	// A second Tab makes the VNC Tab inactive; select it from the strip while
	// its handshake is still running.
	if err := st.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	tt.Frame()
	if st.ActiveTab() != 1 {
		t.Fatalf("active tab = %d, want the new local Tab", st.ActiveTab())
	}
	if err := tt.Click("desk"); err != nil {
		t.Fatalf("click the connecting Tab: %v", err)
	}
	tt.Frame()
	if st.ActiveTab() != 0 {
		t.Fatalf("active tab = %d, want the connecting VNC Tab", st.ActiveTab())
	}
	if !tt.HasText(m.PaneConnecting()) || !tt.HasText(endpoint) {
		t.Fatalf("the reopened connecting Pane lost its state; texts %q", tt.Texts())
	}

	close(factory.release)
	deadline := time.Now().Add(2 * time.Second)
	for st.TabPending(0) {
		if time.Now().After(deadline) {
			t.Fatal("the VNC Tab never finished connecting")
		}
		st.Flush()
		time.Sleep(time.Millisecond)
	}
	tt.Frame()
	if !tt.HasText("terminal-surface") {
		t.Fatalf("the connected Pane does not render its surface; texts %q", tt.Texts())
	}
}

func TestShellKeepsWorkTabsVisibleOverTheVault(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.state.RenameTab(0, "Work Tab")
	graph.state.ShowVault()
	tt.Frame()

	tab, ok := tt.Find("Work Tab")
	if !ok {
		t.Fatalf("the work Tab is hidden on the Vault, texts %q", tt.Texts())
	}
	tt.ClickAt(tab.X+tab.W/2, tab.Y+tab.H/2)
	tt.Frame()
	if graph.state.VaultVisible() {
		t.Fatal("clicking the Tab did not return to the Session")
	}
}

// TestShellShowsTheBellMarkerFromABell proves the actual bell path, not the
// dirty setter, reaches the main Tab strip: a queued bell marks its Tab and
// the strip renders the marker.
func TestShellShowsTheBellMarkerFromABell(t *testing.T) {
	graph := newWorkspaceViewGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 1)
	snapshot, ok := graph.state.ActiveSnapshot()
	if !ok {
		t.Fatal("the new Tab has no Session")
	}
	graph.state.NoteSessionBell(snapshot.ID)
	graph.state.Flush()
	tt.Frame()

	if !tt.HasText(locales.In("en").TabDirtyMarker()) {
		t.Fatalf("shell does not show the bell marker, texts %q", tt.Texts())
	}
}

// toolbarChromeSurface is a viewer Surface with both toolbar forms so a test
// can see which one a render owner chooses.
type toolbarChromeSurface struct{}

func (toolbarChromeSurface) View(c *ui.Context) ui.Element { return ui.Text(c, "view").Fill() }
func (toolbarChromeSurface) ViewFocused(c *ui.Context, _ bool) ui.Element {
	return ui.Text(c, "view").Fill()
}
func (toolbarChromeSurface) Close()             {}
func (toolbarChromeSurface) ViewerChrome() bool { return true }
func (toolbarChromeSurface) Toolbar(c *ui.Context) {
	ui.Text(c, "host-toolbar")
}
func (toolbarChromeSurface) ToolbarEmbedded(c *ui.Context) {
	ui.Text(c, "embedded-toolbar")
}

// TestPaneHeaderChoosesTheToolbarForItsOwner proves the manages-Pane branch,
// which the main window and the top-down Panel share, renders the host toolbar
// for the main owner and the embedded one (no fullscreen) for the Dropdown
// Panel. The detached branch is covered by TestDetachedViewKeepsTheViewerChrome.
func TestPaneHeaderChoosesTheToolbarForItsOwner(t *testing.T) {
	tests := []struct {
		name    string
		owner   state.RenderOwner
		want    string
		notWant string
	}{
		{name: "main window", owner: state.RenderOwnerMain, want: "host-toolbar", notWant: "embedded-toolbar"},
		{name: "dropdown panel", owner: state.RenderOwnerDropdown, want: "embedded-toolbar", notWant: "host-toolbar"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shellState := state.NewState()
			info := state.PaneInfo{ID: "pane", Surface: toolbarChromeSurface{}, Focused: true}
			ref := tabRef{owner: test.owner, managesPanes: true}
			tester := ui.NewTester(func(c *ui.Context) {
				paneHeader(c, shellState, locales.In("en"), c.Theme(), ref, info)
			}, 700, 120)
			tester.Frame()
			if !tester.HasText(test.want) {
				t.Fatalf("%s toolbar missing: %v", test.want, tester.Texts())
			}
			if tester.HasText(test.notWant) {
				t.Fatalf("%s rendered in the wrong context: %v", test.notWant, tester.Texts())
			}
		})
	}
}
