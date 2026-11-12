package state

import (
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

func browserHost() domainvault.Host {
	return domainvault.Host{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}
}

func TestOpenHostBrowserOpensThePanelAfterTheTabAttaches(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.OpenHostBrowser(browserHost()); err != nil {
		t.Fatalf("OpenHostBrowser: %v", err)
	}
	waitFor(t, "the SSH tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})
	if !graph.surface.browserOpened {
		t.Fatal("the browser panel was not opened on the new surface")
	}
	if graph.state.Problem() != "" {
		t.Fatalf("problem = %q after a successful open", graph.state.Problem())
	}
}

func TestOpenHostLeavesTheBrowserPanelClosed(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.OpenHost(browserHost()); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the SSH tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})
	if graph.surface.browserOpened {
		t.Fatal("a plain connection opened the browser panel")
	}
}

// ActiveBrowserOpen is the read a follow or topdown renderer makes before
// choosing a placeholder: an open browser owns a native page that cannot be
// mounted in a second window.
func TestActiveBrowserOpenReportsAnOpenBrowser(t *testing.T) {
	graph := newTestGraph(t)
	if graph.state.ActiveBrowserOpen() {
		t.Fatal("a shell with no Tab reports an open browser")
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if graph.state.ActiveBrowserOpen() {
		t.Fatal("a fresh Tab reports an open browser")
	}
	graph.surface.browserOpen = true
	if !graph.state.ActiveBrowserOpen() {
		t.Fatal("ActiveBrowserOpen missed the Pane's open browser")
	}
}

// A split renderer mounts every Pane, so a browser open in a Pane that does
// not hold focus still requires the placeholder.
func TestActiveBrowserOpenSeesABrowserInAnyPane(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if err := graph.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	graph.flushUntil(t, "the split surfaces", func() bool {
		root, _, ok := graph.state.TabTree(0)
		if !ok {
			return false
		}
		for _, id := range domainworkspace.CollectPaneIDs(root) {
			if info, ok := graph.state.PaneInfoAt(0, id); !ok || info.Surface == nil {
				return false
			}
		}
		return true
	})

	focused, _ := graph.state.FocusedPaneID(0)
	root, _, _ := graph.state.TabTree(0)
	marked := false
	for _, id := range domainworkspace.CollectPaneIDs(root) {
		if id == focused {
			continue
		}
		info, _ := graph.state.PaneInfoAt(0, id)
		if surface, ok := info.Surface.(*fakeSurface); ok {
			surface.browserOpen = true
			marked = true
		}
	}
	if !marked {
		t.Fatal("the split has no non-focused Pane to mark")
	}
	if !graph.state.ActiveBrowserOpen() {
		t.Fatal("ActiveBrowserOpen missed a non-focused Pane's open browser")
	}
}

// A Surface without a browser route has no browser to mount, so it never asks
// for a placeholder.
func TestActiveBrowserOpenIgnoresSurfacesWithoutABrowser(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if graph.state.ActiveBrowserOpen() {
		t.Fatal("a Surface without a browser route reported an open browser")
	}
}
