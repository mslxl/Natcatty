package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

func workspacePayload() domainworkspace.Payload {
	root, _ := domainworkspace.SplitAt(
		domainworkspace.NewPaneNode("p1"), "p1", "p2", "s1", domainworkspace.SideRight)
	root.Ratio = 40
	return domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID:        "tab-1",
			Number:    1,
			AutoTitle: true,
			Root:      root,
			FocusPath: []int{1},
		}},
		Panes: []domainworkspace.RestoredPane{
			{ID: "p1", Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionLocal, Number: 1, Dir: "/work",
			}},
			{ID: "p2", Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionSSH, HostID: "host-1",
				Hostname: "example.com", Port: 22, Username: "root", Label: "web",
			}},
		},
	}
}

func TestWorkspaceLayoutSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "natcatty.db")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := workspacePayload()
	if err := first.Workspace().Save(context.Background(), want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	got, err := second.Workspace().Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("loaded = %s, want %s", gotJSON, wantJSON)
		}
	}
}

func TestWorkspaceSaveReplacesTheSingleRowAndClearRemovesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "natcatty.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Workspace().Save(context.Background(), workspacePayload()); err != nil {
		t.Fatal(err)
	}
	empty := domainworkspace.EmptyPayload()
	if err := store.Workspace().Save(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.Query(context.Background(), `SELECT COUNT(*) FROM workspace_layout`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Values[0][0].(int64) != 1 {
		t.Fatalf("workspace rows = %v, want 1", rows.Values[0][0])
	}
	got, err := store.Workspace().Load(context.Background())
	if err != nil || len(got.Tabs) != 0 {
		t.Fatalf("Load after empty save = %+v, %v; want empty", got, err)
	}

	if err := store.Workspace().Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err = store.Workspace().Load(context.Background())
	if err != nil || len(got.Tabs) != 0 {
		t.Fatalf("Load after clear = %+v, %v; want empty", got, err)
	}
}

func TestCorruptWorkspaceRowLoadsAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "natcatty.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.db.Execute(context.Background(),
		`INSERT INTO workspace_layout (id, version, data, updated_at) VALUES (1, 1, ?, 0)`,
		"{not json"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Workspace().Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Tabs) != 0 || got.Version != domainworkspace.PayloadVersion {
		t.Fatalf("payload = %+v, want the empty Workspace", got)
	}
}
