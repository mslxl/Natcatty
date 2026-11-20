package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// TestPhoneColdStartReadsTheSharedWorkspaceStore proves the phone's relaunch
// path uses the same SQLite Workspace row the desktop writes, not a private
// copy: a payload stored there becomes dormant Sessions rows, and no Session
// is opened until one is tapped.
func TestPhoneColdStartReadsTheSharedWorkspaceStore(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	host := domainvault.Host{
		ID:       "prod",
		Label:    "prod box",
		Hostname: "prod.example.com",
		Port:     2222,
		Username: "deploy",
		Protocol: domainvault.ProtocolSSH,
	}
	pane := model.PaneID("pane-tab-1")
	payload := domainworkspace.EmptyPayload()
	payload.Tabs = []domainworkspace.RestoredTab{{
		ID: "tab-1", Number: 1, Title: host.Label,
		Root: domainworkspace.NewPaneNode(pane),
	}}
	payload.Panes = []domainworkspace.RestoredPane{{
		ID: pane,
		Session: domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionSSH, Label: host.Label,
			HostID: host.ID, Hostname: host.Hostname, Port: host.Port, Username: host.Username,
		},
	}}
	if err := db.Workspace().Save(ctx, payload); err != nil {
		t.Fatalf("save Workspace: %v", err)
	}

	shell := phoneshell.New(
		phoneshell.WithHosts(phoneShellHostList{hosts: []domainvault.Host{host}}),
		phoneshell.WithSessionStore(db.Workspace()),
	)
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	shell.Select(phoneshell.Sessions)

	tt := ui.NewTester(shell.View(phoneshell.Sessions), 390, 844)
	tt.Frame()
	if !tt.HasText("Dormant · deploy@prod.example.com:2222") {
		t.Fatalf("the persisted Tab is not a dormant entry after a cold start; texts: %q", tt.Texts())
	}
}
