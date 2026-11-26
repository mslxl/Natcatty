package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	domainmodel "github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// TestPhoneColdStartReadsTheSharedWorkspaceStore proves the phone's relaunch
// path uses the same SQLite Workspace row the desktop writes, not a private
// copy: a desktop split tree stored there is rebuilt with the same structure,
// focus, Pane IDs, and Pane directories, and the phone writes that same format
// back. A Shell without a Session opener records each Pane's restore failure
// without discarding its entry.
func TestPhoneColdStartReadsTheSharedWorkspaceStore(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	prod := domainvault.Host{
		ID:       "prod",
		Label:    "prod box",
		Hostname: "prod.example.com",
		Port:     2222,
		Username: "deploy",
		Protocol: domainvault.ProtocolSSH,
	}
	db1 := domainvault.Host{
		ID:       "db",
		Label:    "db box",
		Hostname: "db.example.com",
		Port:     22,
		Username: "deploy",
		Protocol: domainvault.ProtocolSSH,
	}
	payload := domainworkspace.EmptyPayload()
	payload.Tabs = []domainworkspace.RestoredTab{{
		ID: "tab-1", Number: 1, Title: "prod/db",
		Root: domainworkspace.NewSplitNode("split-1", domainworkspace.Vertical, 61,
			domainworkspace.NewPaneNode("pane-prod"),
			domainworkspace.NewPaneNode("pane-db")),
		FocusPath: []int{1},
	}}
	payload.Panes = []domainworkspace.RestoredPane{
		{ID: "pane-prod", Session: domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionSSH, Label: prod.Label,
			HostID: prod.ID, Hostname: prod.Hostname, Port: prod.Port,
			Username: prod.Username, Dir: "/srv/prod",
		}},
		{ID: "pane-db", Session: domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionSSH, Label: db1.Label,
			HostID: db1.ID, Hostname: db1.Hostname, Port: db1.Port,
			Username: db1.Username, Dir: "/srv/db",
		}},
	}
	if err := db.Workspace().Save(ctx, payload); err != nil {
		t.Fatalf("save Workspace: %v", err)
	}

	shell := phoneshell.New(
		phoneshell.WithHosts(phoneShellHostList{hosts: []domainvault.Host{prod, db1}}),
		phoneshell.WithSessionStore(db.Workspace()),
	)
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	shell.Select(phoneshell.Sessions)

	tt := ui.NewTester(shell.View(phoneshell.Sessions), 390, 844)
	tt.Frame()
	if !tt.HasText("Dormant · deploy@db.example.com:22") {
		t.Fatalf("the persisted Tab's focused Pane is not a dormant entry after a cold start; texts: %q", tt.Texts())
	}

	// The phone wrote the same shared format back: the split, its ratio, focus
	// path, Pane IDs, and each Pane's Dir all survive.
	stored, err := db.Workspace().Load(ctx)
	if err != nil {
		t.Fatalf("reload Workspace: %v", err)
	}
	if len(stored.Tabs) != 1 {
		t.Fatalf("the phone persisted %d Tabs, want 1: %+v", len(stored.Tabs), stored)
	}
	root := stored.Tabs[0].Root
	if root == nil || root.Type != domainworkspace.SplitNode ||
		root.Direction != domainworkspace.Vertical || root.Ratio != 61 ||
		root.SplitID != "split-1" {
		t.Fatalf("the phone persisted tree %+v, want the desktop split", root)
	}
	if got := domainworkspace.CollectPaneIDs(root); len(got) != 2 ||
		got[0] != "pane-prod" || got[1] != "pane-db" {
		t.Fatalf("the phone persisted Panes %v, want pane-prod then pane-db", got)
	}
	if leaf, ok := domainworkspace.PaneAtPath(root, stored.Tabs[0].FocusPath); !ok ||
		leaf.PaneID != "pane-db" {
		t.Fatalf("the phone persisted focus %v, want pane-db", stored.Tabs[0].FocusPath)
	}
	for _, pane := range stored.Panes {
		want := map[domainmodel.PaneID]string{"pane-prod": "/srv/prod", "pane-db": "/srv/db"}[pane.ID]
		if pane.Session.Dir != want {
			t.Fatalf("Pane %q Dir = %q, want %q", pane.ID, pane.Session.Dir, want)
		}
	}
}
