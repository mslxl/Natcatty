package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestFocusSessionSelectsTheTabAndPaneOwningTheSession(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("second NewTab: %v", err)
	}
	first := graph.state.tabs[0]
	firstID := first.panes[first.paneIDs()[0]].session.ID()

	graph.state.ShowLogs()
	if !graph.state.FocusSession(firstID) {
		t.Fatal("FocusSession did not find the live Session")
	}
	if graph.state.ActiveTab() != 0 {
		t.Fatalf("active tab = %d, want 0", graph.state.ActiveTab())
	}
	if graph.state.LogsVisible() {
		t.Fatal("focusing a Session left the Logs surface visible")
	}
}

func TestFocusSessionReattachesADetachedTab(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	tab := graph.state.tabs[0]
	id := tab.panes[tab.paneIDs()[0]].session.ID()
	graph.state.DetachTab(0)
	graph.state.ShowLogs()

	if !graph.state.FocusSession(id) {
		t.Fatal("FocusSession did not find the detached Session")
	}
	if graph.state.TabCount() != 1 || graph.state.LogsVisible() {
		t.Fatalf("tabs = %d, logs visible = %v", graph.state.TabCount(),
			graph.state.LogsVisible())
	}
}

func TestOpenHistoryTargetFocusesALiveHost(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{ID: "host-1", Label: "web-1", Hostname: "10.0.0.1", Port: 22}
	graph.state.resolveHost = func(context.Context, model.HostID) (domainvault.Host, bool) {
		return host, true
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for graph.state.TabCount() == 0 && time.Now().Before(deadline) {
		graph.state.Flush()
		time.Sleep(5 * time.Millisecond)
	}
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d, want 1", graph.state.TabCount())
	}
	tab := graph.state.tabs[0]
	paneID := tab.paneIDs()[0]
	sessionID := tab.panes[paneID].session.ID()
	graph.state.ShowLogs()

	if err := graph.state.OpenHistoryTarget(sessionID, "host-1"); err != nil {
		t.Fatalf("OpenHistoryTarget: %v", err)
	}
	if graph.state.LogsVisible() || graph.state.ActiveTab() != 0 {
		t.Fatal("OpenHistoryTarget did not focus the live Host tab")
	}

	// Without a Session identity the Host still resolves to the live Tab.
	graph.state.ShowLogs()
	if err := graph.state.OpenHistoryTarget("", "host-1"); err != nil {
		t.Fatalf("OpenHistoryTarget by Host: %v", err)
	}
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d, want the live Host reused", graph.state.TabCount())
	}
}

func TestOpenHistoryTargetOpensALocalTerminal(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.ShowLogs()
	if err := graph.state.OpenHistoryTarget("", ""); err != nil {
		t.Fatalf("OpenHistoryTarget local: %v", err)
	}
	if graph.state.TabCount() != 1 || graph.state.LogsVisible() {
		t.Fatalf("tabs = %d, logs visible = %v", graph.state.TabCount(),
			graph.state.LogsVisible())
	}
	if graph.state.tabs[0].panes[graph.state.tabs[0].paneIDs()[0]].session == nil {
		t.Fatal("the local Tab has no Session")
	}
}

func TestOpenHistoryTargetReportsAMissingHost(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.resolveHost = func(context.Context, model.HostID) (domainvault.Host, bool) {
		return domainvault.Host{}, false
	}
	err := graph.state.OpenHistoryTarget("", "deleted-host")
	if !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("OpenHistoryTarget error = %v, want ErrHostUnavailable", err)
	}
	if graph.state.TabCount() != 0 {
		t.Fatalf("tabs = %d, want none invented", graph.state.TabCount())
	}
}
