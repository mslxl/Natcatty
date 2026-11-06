package state

import (
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

func newTitledTestGraph(t *testing.T, opts ...Option) *testGraph {
	t.Helper()
	graph := &testGraph{}
	graph.factory = &fakeFactory{transport: newFakeTransport()}
	service := appsession.NewService(graph.factory)
	t.Cleanup(service.CloseAll)
	graph.surface = &fakeSurface{}
	base := []Option{
		WithRedrawRequest(func() { graph.changes.Add(1) }),
		WithSessions(service, func(session *appsession.Session) (Surface, error) {
			graph.sessions = append(graph.sessions, session)
			return graph.surface, nil
		}),
	}
	graph.state = NewState(append(base, opts...)...)
	return graph
}

func TestLocalTabTitleIsTheLocalTerminalLabel(t *testing.T) {
	graph := newTitledTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	info, ok := graph.state.TabInfo(0)
	if !ok {
		t.Fatal("TabInfo(0) was not found")
	}
	if info.Title != "Local Terminal" || info.Label != "" {
		t.Fatalf("local tab info = %+v, want Title %q and no stored label",
			info, "Local Terminal")
	}
	if label, _, _ := graph.state.TabLabel(0); label != "Local Terminal" {
		t.Fatalf("TabLabel = %q, want %q", label, "Local Terminal")
	}
}

func TestSplitTabTitleFollowsTheFocusedPaneAndCountsHosts(t *testing.T) {
	state := NewState()
	state.tabs = []*Tab{splitTitleTestTab()}
	state.active = 0

	if got, _, _ := state.TabLabel(0); got != "web-01 +1" {
		t.Fatalf("tab title = %q, want %q", got, "web-01 +1")
	}
	if !state.FocusPaneID(0, "pane-b") {
		t.Fatal("FocusPaneID failed")
	}
	if got, _, _ := state.TabLabel(0); got != "db-02 +1" {
		t.Fatalf("tab title after focusing the second pane = %q, want %q", got, "db-02 +1")
	}

	// Two Panes on one Host are one connection, so no suffix appears.
	state.tabs[0].panes["pane-b"].descriptor.HostID = "host-a"
	state.tabs[0].panes["pane-b"].descriptor.Label = "web-02"
	if got, _, _ := state.TabLabel(0); got != "web-02" {
		t.Fatalf("same-host tab title = %q, want no host suffix", got)
	}
}

func splitTitleTestTab() *Tab {
	root := domainworkspace.NewSplitNode(model.SplitID("split-1"),
		domainworkspace.Vertical, 50,
		domainworkspace.NewPaneNode("pane-a"),
		domainworkspace.NewPaneNode("pane-b"))
	return &Tab{
		ID:          "tab-1",
		Number:      1,
		TitleNumber: 1,
		AutoTitle:   true,
		Root:        root,
		FocusPath:   domainworkspace.FocusPath(root, "pane-a"),
		panes: map[model.PaneID]*pane{
			"pane-a": {ID: "pane-a", descriptor: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionSSH, HostID: "host-a", Label: "web-01",
			}},
			"pane-b": {ID: "pane-b", descriptor: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionSSH, HostID: "host-b", Label: "db-02",
			}},
		},
	}
}

func TestMergingSinglePaneTabsComposesBothConnectionLabels(t *testing.T) {
	state := NewState()
	state.tabs = []*Tab{
		singlePaneTestTab("tab-a", 1, "pane-a", "web-01"),
		singlePaneTestTab("tab-b", 2, "pane-b", "db-02"),
	}
	state.active = 0

	if !state.MovePane("tab-b", "pane-b", "tab-a", "pane-a", domainworkspace.SideRight) {
		t.Fatal("MovePane failed")
	}
	if state.TabCount() != 1 {
		t.Fatalf("tabs = %d after the merge, want 1", state.TabCount())
	}
	if got, _, _ := state.TabLabel(0); got != "web-01/db-02" {
		t.Fatalf("merged title = %q, want both connection labels", got)
	}
	info, _ := state.TabInfo(0)
	if info.AutoTitle || !state.tabs[0].GeneratedTitle {
		t.Fatalf("merged tab = %+v, want a generated title", info)
	}
	payload := state.WorkspacePayload()
	if len(payload.Tabs) != 1 || !payload.Tabs[0].GeneratedTitle {
		t.Fatalf("payload tabs = %+v, want the generated title persisted", payload.Tabs)
	}

	// The composed title keeps following the Panes it names.
	state.tabs[0].panes["pane-b"].descriptor.CustomName = "logs"
	if got, _, _ := state.TabLabel(0); got != "web-01/logs" {
		t.Fatalf("title after a Pane rename = %q, want the new label", got)
	}

	// Two Panes on one Host collapse to a single label.
	state.tabs[0].panes["pane-b"].descriptor.CustomName = ""
	state.tabs[0].panes["pane-b"].descriptor.HostID = "host-a"
	state.tabs[0].panes["pane-b"].descriptor.Label = "web-01"
	if got, _, _ := state.TabLabel(0); got != "web-01" {
		t.Fatalf("same-Host merged title = %q, want one label", got)
	}
}

func singlePaneTestTab(id model.TabID, number int, paneID model.PaneID, label string) *Tab {
	root := domainworkspace.NewPaneNode(paneID)
	return &Tab{
		ID:          id,
		Number:      number,
		TitleNumber: number,
		AutoTitle:   true,
		Root:        root,
		FocusPath:   domainworkspace.FocusPath(root, paneID),
		panes: map[model.PaneID]*pane{
			paneID: {ID: paneID, descriptor: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionSSH, HostID: model.HostID("host-" + string(paneID)),
				Label: label,
			}},
		},
	}
}

func TestPaneRenameNamesThatPaneNotTheFocusedOne(t *testing.T) {
	state := NewState()
	state.tabs = []*Tab{splitTitleTestTab()}
	state.active = 0

	if !state.RenamePane(0, "pane-b", "logs") {
		t.Fatal("RenamePane failed")
	}
	if got := state.tabs[0].panes["pane-b"].descriptor.CustomName; got != "logs" {
		t.Fatalf("renamed pane = %q, want %q", got, "logs")
	}
	if got := state.tabs[0].panes["pane-a"].descriptor.CustomName; got != "" {
		t.Fatalf("focused pane was renamed to %q", got)
	}
	if state.RenamePane(0, "pane-b", "   ") {
		t.Fatal("an empty pane rename was accepted")
	}
}

func TestRenameFollowsTheFocusedPaneAndPersists(t *testing.T) {
	graph := newTitledTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if !graph.state.RenameTab(0, "Build shell") {
		t.Fatal("RenameTab failed")
	}
	info, _ := graph.state.TabInfo(0)
	if info.Title != "Build shell" {
		t.Fatalf("tab title = %q, want the rename", info.Title)
	}
	payload := graph.state.WorkspacePayload()
	if len(payload.Panes) != 1 || payload.Panes[0].Session.CustomName != "Build shell" {
		t.Fatalf("payload panes = %+v, want the rename on the descriptor", payload.Panes)
	}
	if graph.state.RenameTab(0, "   ") {
		t.Fatal("an empty rename was accepted")
	}
}

func TestTabTitleFollowsAHostRename(t *testing.T) {
	labels := map[model.HostID]string{"host-1": "prod"}
	graph := newTitledTestGraph(t, WithHostLabel(func(id model.HostID) (string, bool) {
		label, ok := labels[id]
		return label, ok
	}))
	host := domainvault.Host{
		ID: "host-1", Label: "prod", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the SSH tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})
	if info, _ := graph.state.TabInfo(0); info.Title != "prod" {
		t.Fatalf("tab title = %q, want the Host label", info.Title)
	}
	labels["host-1"] = "production"
	if info, _ := graph.state.TabInfo(0); info.Title != "production" {
		t.Fatalf("tab title after the rename = %q, want the new Host label", info.Title)
	}
}

func TestDynamicTabTitleModes(t *testing.T) {
	agents := func() domainterminal.DynamicTabTitleMode { return domainterminal.DynamicTabTitleAgents }

	t.Run("the default follows a plain shell title", func(t *testing.T) {
		graph := newTitledTestGraph(t)
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		titleSession(t, graph, "\x1b]0;root@host:/var/log\x07", "root@host:/var/log")
		if info, _ := graph.state.TabInfo(0); info.Title != "root@host:/var/log" {
			t.Fatalf("tab title = %q, want the shell-reported title", info.Title)
		}
	})

	t.Run("agent mode ignores a plain shell title", func(t *testing.T) {
		graph := newTitledTestGraph(t, WithDynamicTabTitleMode(agents))
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		titleSession(t, graph, "\x1b]0;root@host:/var/log\x07", "root@host:/var/log")
		if info, _ := graph.state.TabInfo(0); info.Title != "Local Terminal" {
			t.Fatalf("tab title = %q, want the local label", info.Title)
		}
	})

	t.Run("agent mode follows an agent title", func(t *testing.T) {
		graph := newTitledTestGraph(t, WithDynamicTabTitleMode(agents))
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		titleSession(t, graph, "\x1b]0;claude: refactor auth\x07", "claude: refactor auth")
		if info, _ := graph.state.TabInfo(0); info.Title != "claude: refactor auth" {
			t.Fatalf("tab title = %q, want the agent title", info.Title)
		}
	})

	t.Run("agent mode keeps a session once an ambiguous title replaces the agent one", func(t *testing.T) {
		graph := newTitledTestGraph(t, WithDynamicTabTitleMode(agents))
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		titleSession(t, graph, "\x1b]0;⠋ working\x07", "⠋ working")
		// A frame observes the agent title before the agent itself renames the
		// Tab to something ambiguous.
		if info, _ := graph.state.TabInfo(0); info.Title != "working" {
			t.Fatalf("tab title = %q, want the agent title", info.Title)
		}
		titleSession(t, graph, "\x1b]0;my-project\x07", "my-project")
		if info, _ := graph.state.TabInfo(0); info.Title != "my-project" {
			t.Fatalf("tab title = %q, want the ambiguous agent title", info.Title)
		}
		// A shell prompt means the agent Session ended, so the tab returns to
		// its connection label.
		titleSession(t, graph, "\x1b]0;root@host:/var/log\x07", "root@host:/var/log")
		if info, _ := graph.state.TabInfo(0); info.Title != "Local Terminal" {
			t.Fatalf("tab title = %q, want the connection label after the shell title", info.Title)
		}
	})

	t.Run("all mode follows a plain shell title", func(t *testing.T) {
		graph := newTitledTestGraph(t, WithDynamicTabTitleMode(func() domainterminal.DynamicTabTitleMode {
			return domainterminal.DynamicTabTitleAll
		}))
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		titleSession(t, graph, "\x1b]0;root@host:/var/log\x07", "root@host:/var/log")
		if info, _ := graph.state.TabInfo(0); info.Title != "root@host:/var/log" {
			t.Fatalf("tab title = %q, want the dynamic title", info.Title)
		}
	})

	t.Run("off mode ignores every dynamic title", func(t *testing.T) {
		graph := newTitledTestGraph(t, WithDynamicTabTitleMode(func() domainterminal.DynamicTabTitleMode {
			return domainterminal.DynamicTabTitleOff
		}))
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		titleSession(t, graph, "\x1b]0;claude: refactor auth\x07", "claude: refactor auth")
		if info, _ := graph.state.TabInfo(0); info.Title != "Local Terminal" {
			t.Fatalf("tab title = %q, want the local label", info.Title)
		}
	})
}

// titleSession feeds one OSC 0 title through the fake transport and waits for
// the Session snapshot to carry want.
func titleSession(t *testing.T, graph *testGraph, sequence, want string) {
	t.Helper()
	go func() {
		graph.factory.transport.output <- []byte(sequence)
	}()
	waitFor(t, "the OSC title", func() bool {
		snapshot, ok := graph.state.ActiveSnapshot()
		return ok && snapshot.Title == want
	})
}
