package state

import (
	"context"
	"reflect"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// The read-only contract is the method set: the Pane views and the two Pane
// state commands a second renderer needs, and no destructive Workspace
// command at all.
func TestReadOnlyTabMethodSetIsReadOnly(t *testing.T) {
	typ := reflect.TypeOf(ReadOnlyTab{})
	for _, name := range []string{"Tree", "PaneInfo", "FocusPane", "RenamePane", "RetryPane"} {
		if _, ok := typ.MethodByName(name); !ok {
			t.Errorf("ReadOnlyTab lacks %s", name)
		}
	}
	for _, name := range []string{
		"Close", "CloseTab", "ClosePane", "CloseFocusedPane",
		"SplitRight", "SplitDown", "SplitFocusedPane",
		"Detach", "DetachTab", "DetachPane",
		"MoveTab", "MovePane", "Hibernate", "Resume", "SetSplitRatio",
	} {
		if _, ok := typ.MethodByName(name); ok {
			t.Errorf("ReadOnlyTab exposes the destructive command %s", name)
		}
	}
}

// A read-only reference renders the active Tab and can focus, rename, and
// retry its Panes, but it never changes the Workspace's Tab order, active
// index, or split tree.
func TestReadOnlyTabReadsRendersAndKeepsTheWorkspaceIntact(t *testing.T) {
	graph := newWorkspaceGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
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

	tabID, ok := graph.state.TabID(0)
	if !ok {
		t.Fatal("the Tab has no identity")
	}
	rootBefore, focusBefore, _ := graph.state.TabTree(0)
	panes := domainworkspace.CollectPaneIDs(rootBefore)
	if len(panes) != 2 {
		t.Fatalf("panes = %v, want a split of two", panes)
	}
	activeBefore := graph.state.ActiveTab()

	tab, ok := graph.state.ActiveReadOnlyTab()
	if !ok {
		t.Fatal("the active Tab has no read-only reference")
	}
	if tab.ID() != tabID {
		t.Fatalf("read-only Tab ID = %q, want %q", tab.ID(), tabID)
	}
	tree, focus, ok := tab.Tree()
	if !ok || tree != rootBefore {
		t.Fatalf("read-only tree = %p, want the live tree %p", tree, rootBefore)
	}
	if len(focus) != len(focusBefore) {
		t.Fatalf("read-only focus path = %v, want %v", focus, focusBefore)
	}
	for _, id := range panes {
		info, ok := tab.PaneInfo(id)
		if !ok {
			t.Fatalf("read-only Tab does not know Pane %q", id)
		}
		stateInfo, _ := graph.state.PaneInfoAt(0, id)
		if info.ID != id || info.Number != stateInfo.Number || info.Focused != stateInfo.Focused {
			t.Fatalf("read-only Pane %q = %+v, want %+v", id, info, stateInfo)
		}
	}

	// Focus passes through the reference, as a follow view must re-request it
	// every frame.
	target := panes[0]
	if !tab.FocusPane(target) {
		t.Fatalf("read-only FocusPane(%q) returned false", target)
	}
	if got, _ := graph.state.FocusedPaneID(0); got != target {
		t.Fatalf("focused Pane = %q, want %q", got, target)
	}
	// Rename passes through too.
	if !tab.RenamePane(target, "renamed by the follow view") {
		t.Fatal("read-only RenamePane returned false")
	}
	if info, _ := tab.PaneInfo(target); info.Title != "renamed by the follow view" {
		t.Fatalf("renamed Pane title = %q", info.Title)
	}
	// A live Pane refuses a retry; the reference delegates the same rule.
	if tab.RetryPane(target) {
		t.Fatal("the read-only reference retried a live Pane")
	}

	if graph.state.TabCount() != 1 || graph.state.ActiveTab() != activeBefore {
		t.Fatalf("tabs = %d, active = %d; want 1 and %d",
			graph.state.TabCount(), graph.state.ActiveTab(), activeBefore)
	}
	rootAfter, _, _ := graph.state.TabTree(0)
	if rootAfter != rootBefore {
		t.Fatal("reading through the reference replaced the split tree")
	}
}

// A read-only reference retries a failed Pane through the same retry path as
// the main window.
func TestReadOnlyTabRetriesAFailedPane(t *testing.T) {
	payload := domainworkspace.Payload{
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
	graph := newWorkspaceGraph(t)
	graph.hosts["host-telnet"] = domainvault.Host{
		ID: "host-telnet", Hostname: "127.0.0.1", Port: 2323,
		Protocol: domainvault.ProtocolTelnet,
	}
	var attempts int
	graph.factory.fail = func(appsession.StartSpec) error {
		attempts++
		if attempts == 1 {
			return &appconnection.Error{Kind: appconnection.KindNetwork}
		}
		return nil
	}
	graph.repo.payload = payload
	graph.state.Restore(context.Background())
	graph.flushUntil(t, "the failed Telnet open", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-telnet")
		return ok && !info.Pending && info.Err != nil
	})

	tab, ok := graph.state.ActiveReadOnlyTab()
	if !ok {
		t.Fatal("the restored Tab has no read-only reference")
	}
	if !tab.RetryPane("pane-telnet") {
		t.Fatal("the read-only reference refused the failed Pane")
	}
	graph.flushUntil(t, "the retried surface", func() bool {
		info, ok := graph.state.PaneInfoAt(0, "pane-telnet")
		return ok && info.Surface != nil && info.Err == nil
	})
	if tab.RetryPane("no-such-pane") {
		t.Fatal("the read-only reference retried an unknown Pane")
	}
}

// The zero reference is safe before any Tab exists.
func TestReadOnlyTabIsSafeWithoutATab(t *testing.T) {
	s := NewState()
	if _, ok := s.ActiveReadOnlyTab(); ok {
		t.Fatal("an empty shell reported an active read-only Tab")
	}
	if _, ok := s.ReadOnlyTab(0); ok {
		t.Fatal("an empty shell reported Tab 0")
	}
	var zero ReadOnlyTab
	if zero.ID() != "" {
		t.Fatalf("zero read-only Tab ID = %q", zero.ID())
	}
	if _, _, ok := zero.Tree(); ok {
		t.Fatal("zero read-only Tab has a tree")
	}
	if _, ok := zero.PaneInfo(model.PaneID("pane")); ok {
		t.Fatal("zero read-only Tab has a Pane")
	}
	if zero.FocusPane(model.PaneID("pane")) || zero.RenamePane(model.PaneID("pane"), "x") ||
		zero.RetryPane(model.PaneID("pane")) || zero.BrowserOpen() {
		t.Fatal("zero read-only Tab accepted a Pane command")
	}
}
