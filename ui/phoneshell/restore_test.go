package phoneshell

import (
	"encoding/json"
	"testing"

	domainmodel "github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// restorePayload builds the desktop Workspace a phone might be handed: two
// Tabs, one with a local pane beside an SSH pane and one with a nested split
// of three remote panes, plus a Tab whose only pane cannot be restored.
func restorePayload() domainworkspace.Payload {
	return domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 1,
		Tabs: []domainworkspace.RestoredTab{
			{
				ID: "tab-a", Title: "ops",
				Root: domainworkspace.NewSplitNode("s1", domainworkspace.Vertical, 50,
					domainworkspace.NewPaneNode("pane-a"),
					domainworkspace.NewPaneNode("pane-b")),
			},
			{
				ID: "tab-b", Title: "web/db",
				Root: domainworkspace.NewSplitNode("s2", domainworkspace.Horizontal, 40,
					domainworkspace.NewSplitNode("s3", domainworkspace.Vertical, 65,
						domainworkspace.NewPaneNode("pane-c"),
						domainworkspace.NewPaneNode("pane-d")),
					domainworkspace.NewPaneNode("pane-e")),
			},
			{
				ID: "tab-c", Title: "unsupported",
				Root: domainworkspace.NewPaneNode("pane-f"),
			},
		},
		Panes: []domainworkspace.RestoredPane{
			{ID: "pane-a", Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionSSH, CustomName: "prod-east",
				HostID: "prod", Hostname: "prod.example.com", Port: 2222,
			}},
			// A local shell runs on the machine that created the workspace, so
			// it has no meaning on a phone.
			{ID: "pane-b", Session: domainworkspace.SessionDescriptor{Kind: domainworkspace.SessionLocal}},
			{ID: "pane-c", Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionSSH, Label: "web-01",
				HostID: "web", Hostname: "web.example.com",
			}},
			{ID: "pane-d", Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionTelnet, HostID: "db", Hostname: "db.example.com", Port: 23,
			}},
			{ID: "pane-e", Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionMosh, HostID: "edge", Hostname: "edge.example.com",
			}},
			// Invalid for its kind: the sanitizer drops the pane, and with it the
			// Tab that held nothing else.
			{ID: "pane-f", Session: domainworkspace.SessionDescriptor{Kind: domainworkspace.SessionSSH}},
		},
	}
}

func TestRestoreTabsKeepsTheSplitTreeAndFocus(t *testing.T) {
	tabs := RestoreTabs(restorePayload())

	if len(tabs) != 2 {
		t.Fatalf("restored %d Tabs, want 2: %+v", len(tabs), tabs)
	}
	// The local Pane is dropped and the split collapses into its surviving
	// sibling, rather than flattening the Tab into a new one.
	ops := tabs[0]
	if ops.ID != "tab-a" {
		t.Fatalf("first restored Tab = %q, want tab-a", ops.ID)
	}
	if ops.Root == nil || !ops.Root.IsPane() || ops.Root.PaneID != "pane-a" {
		t.Fatalf("local Pane pruning left %+v, want the pane-a leaf", ops.Root)
	}
	if ops.Focus != "pane-a" || len(ops.Panes) != 1 {
		t.Fatalf("first restored Tab = focus %q, %d Panes; want pane-a and one", ops.Focus, len(ops.Panes))
	}

	// The all-remote Tab keeps its nested structure, ratios, Pane IDs, and
	// focus (the first leaf when the desktop wrote none).
	web := tabs[1]
	if web.ID != "tab-b" || web.Title != "web/db" {
		t.Fatalf("second restored Tab = %q %q, want tab-b web/db", web.ID, web.Title)
	}
	if web.Root == nil || web.Root.Type != domainworkspace.SplitNode ||
		web.Root.Direction != domainworkspace.Horizontal || web.Root.Ratio != 40 {
		t.Fatalf("outer split = %+v, want a 40%% horizontal split", web.Root)
	}
	if web.Root.First == nil || web.Root.First.SplitID != "s3" ||
		web.Root.First.Direction != domainworkspace.Vertical || web.Root.First.Ratio != 65 {
		t.Fatalf("inner split = %+v, want the s3 vertical 65%% split", web.Root.First)
	}
	gotPanes := domainworkspace.CollectPaneIDs(web.Root)
	wantPanes := []domainmodel.PaneID{"pane-c", "pane-d", "pane-e"}
	if len(gotPanes) != len(wantPanes) {
		t.Fatalf("restored Panes = %v, want %v", gotPanes, wantPanes)
	}
	for i := range wantPanes {
		if gotPanes[i] != wantPanes[i] {
			t.Fatalf("restored Panes = %v, want %v", gotPanes, wantPanes)
		}
	}
	if web.Focus != "pane-c" {
		t.Fatalf("restored focus = %q, want the first leaf pane-c", web.Focus)
	}
}

func TestRestoreTabsDropsUnsupportedPanesAndTabs(t *testing.T) {
	payload := domainworkspace.Payload{
		Version: domainworkspace.PayloadVersion,
		Tabs: []domainworkspace.RestoredTab{
			{ID: "tab-local", Title: "local", Root: domainworkspace.NewPaneNode("local")},
			{ID: "tab-empty", Title: "empty", Root: domainworkspace.NewPaneNode("missing")},
			{ID: "tab-et", Title: "et", Root: domainworkspace.NewPaneNode("et")},
			{ID: "tab-null", Title: "empty tree", Root: nil},
		},
		Panes: []domainworkspace.RestoredPane{
			{ID: "local", Session: domainworkspace.SessionDescriptor{Kind: domainworkspace.SessionLocal}},
			{ID: "et", Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionET, HostID: "edge", Hostname: "edge.example.com",
				ResumeKey: "vault:et/session",
			}},
		},
	}
	tabs := RestoreTabs(payload)
	if len(tabs) != 1 {
		t.Fatalf("restored %d Tabs, want only the ET one: %+v", len(tabs), tabs)
	}
	if tabs[0].ID != "tab-et" || len(tabs[0].Panes) != 1 {
		t.Fatalf("restored Tab = %+v, want the ET pane", tabs[0])
	}
	entry := tabs[0].Panes[0]
	if entry.ID != "et" || entry.Descriptor.Kind != domainworkspace.SessionET {
		t.Fatalf("restored Pane = %+v, want the ET pane", entry)
	}
	if entry.Descriptor.ResumeKey != "vault:et/session" {
		t.Errorf("ET resume key was lost: %q", entry.Descriptor.ResumeKey)
	}
}

func TestRestoreTabsRejectsAnUnknownPayloadVersion(t *testing.T) {
	payload := restorePayload()
	payload.Version = domainworkspace.PayloadVersion + 1
	if tabs := RestoreTabs(payload); len(tabs) != 0 {
		t.Fatalf("an unknown payload version restored %d Tabs", len(tabs))
	}
}

func TestRestoredTitlePrefersThePaneName(t *testing.T) {
	cases := []struct {
		name string
		pane domainworkspace.SessionDescriptor
		want string
	}{
		{"custom name", domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionSSH, CustomName: "prod-east", Label: "prod",
			HostID: "prod", Hostname: "prod.example.com",
		}, "prod-east"},
		{"host label", domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionSSH, Label: "prod", HostID: "prod",
			Hostname: "prod.example.com",
		}, "prod"},
		{"hostname", domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionSSH, HostID: "prod",
			Hostname: "prod.example.com",
		}, "prod.example.com"},
		{"whitespace is not a name", domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionSSH, CustomName: "  ", Label: " ",
			HostID: "prod", Hostname: "prod.example.com",
		}, "prod.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := restoredTitle(tc.pane); got != tc.want {
				t.Fatalf("title = %q, want %q", got, tc.want)
			}
		})
	}
}

// adoptPayload restores a shared payload into a fresh phone Workspace without
// starting any Session, so a test can compare the format it writes back.
func adoptPayload(t *testing.T, payload domainworkspace.Payload) *sessionTabs {
	t.Helper()
	tabs := &sessionTabs{}
	tabs.adoptRestored(RestoreTabs(payload), func(string) (domainvault.Host, bool) {
		return domainvault.Host{}, false
	})
	return tabs
}

// sharedFormat returns the canonical JSON the desktop and the phone share:
// sanitized Tabs and Panes, without the active-Tab index (the phone has no
// active Tab of its own).
func sharedFormat(t *testing.T, payload domainworkspace.Payload) string {
	t.Helper()
	payload = domainworkspace.SanitizePayload(payload)
	encoded, err := json.Marshal(struct {
		Tabs  []domainworkspace.RestoredTab
		Panes []domainworkspace.RestoredPane
	}{Tabs: payload.Tabs, Panes: payload.Panes})
	if err != nil {
		t.Fatalf("marshal shared format: %v", err)
	}
	return string(encoded)
}

// remoteTreePayload is a desktop Workspace with only remote Panes: two Tabs, a
// nested split with focus in the second leaf, distinct ratios, and a directory
// per Pane. Round-tripping it proves the phone reads and writes the same shape.
func remoteTreePayload() domainworkspace.Payload {
	payload := domainworkspace.EmptyPayload()
	payload.ActiveTab = 1
	payload.Tabs = []domainworkspace.RestoredTab{
		{
			ID: "tab-a", Number: 1, TitleNumber: 1, Title: "ops",
			Root: domainworkspace.NewSplitNode("split-a", domainworkspace.Vertical, 37,
				domainworkspace.NewPaneNode("pane-a"),
				domainworkspace.NewPaneNode("pane-b")),
			FocusPath: []int{1},
		},
		{
			ID: "tab-b", Number: 2, TitleNumber: 2, Title: "web/db",
			AutoTitle: true, GeneratedTitle: true, Copy: 1,
			Root: domainworkspace.NewSplitNode("split-b", domainworkspace.Horizontal, 61,
				domainworkspace.NewSplitNode("split-c", domainworkspace.Vertical, 22,
					domainworkspace.NewPaneNode("pane-c"),
					domainworkspace.NewPaneNode("pane-d")),
				domainworkspace.NewPaneNode("pane-e")),
			FocusPath: []int{0, 1},
		},
	}
	payload.Panes = []domainworkspace.RestoredPane{
		{ID: "pane-a", Session: domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionSSH, CustomName: "prod-east", Label: "prod",
			HostID: "prod", Hostname: "prod.example.com", Port: 2222, Username: "deploy",
			Dir: "/srv/prod", X11Forwarding: true, AutoAttachTmux: true,
		}},
		{ID: "pane-b", Session: domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionTelnet, Label: "console",
			HostID: "console", Hostname: "console.example.com", Port: 23, Dir: "/root",
		}},
		{ID: "pane-c", Session: domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionSSH, Label: "web-01",
			HostID: "web", Hostname: "web.example.com", Dir: "/var/www",
		}},
		{ID: "pane-d", Session: domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionMosh, Label: "db-01",
			HostID: "db", Hostname: "db.example.com", Dir: "/data",
		}},
		{ID: "pane-e", Session: domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionET, Label: "edge",
			HostID: "edge", Hostname: "edge.example.com", ResumeKey: "vault:et/session",
			Dir: "/home/ops",
		}},
	}
	return payload
}

// TestDesktopTreeRoundTripsThroughThePhonePayload proves the shared-format
// seam: a desktop-written split tree read by the phone is written back with the
// same Tab and Pane IDs, split structure and ratios, focus paths, and Pane
// directories.
func TestDesktopTreeRoundTripsThroughThePhonePayload(t *testing.T) {
	before := remoteTreePayload()
	tabs := adoptPayload(t, before)
	after := payloadOf(tabs.tabs)

	if got, want := sharedFormat(t, after), sharedFormat(t, before); got != want {
		t.Fatalf("phone round trip changed the shared format\n got: %s\nwant: %s", got, want)
	}
}

// TestPhoneCreatedTreeRoundTripsAtTheSharedSeam creates a split tree with the
// phone's own gestures, writes it in the shared format, and reads it back: the
// structure, focus, and Pane directories survive, which is what the desktop
// opens.
func TestPhoneCreatedTreeRoundTripsAtTheSharedSeam(t *testing.T) {
	hosts := phoneHosts()
	tabs := &sessionTabs{}
	tab, first := tabs.begin(hosts[0])
	second, _ := tabs.addPane(tab.id, hosts[1])
	third, _ := tabs.addPane(tab.id, hosts[2])
	tabs.mu.Lock()
	first.dir = "/srv/one"
	first.descriptor.Dir = first.dir
	second.dir = "/srv/two"
	second.descriptor.Dir = second.dir
	third.dir = "/srv/three"
	third.descriptor.Dir = third.dir
	tabs.mu.Unlock()
	tabs.focusPane(tab.id, string(second.id))

	payload := payloadOf(tabs.tabs)
	if len(payload.Tabs) != 1 || len(payload.Panes) != 3 {
		t.Fatalf("phone saved %d Tab(s), %d Pane(s), want 1 and 3", len(payload.Tabs), len(payload.Panes))
	}
	saved := domainworkspace.SanitizePayload(payload)
	root := saved.Tabs[0].Root
	if root == nil || root.Type != domainworkspace.SplitNode ||
		root.Direction != domainworkspace.Vertical || root.Ratio != domainworkspace.DefaultRatio {
		t.Fatalf("phone-created tree = %+v, want a 50%% vertical split", root)
	}
	if got := domainworkspace.CollectPaneIDs(root); len(got) != 3 {
		t.Fatalf("phone-created tree holds %v, want three Panes", got)
	}
	if node, ok := domainworkspace.PaneAtPath(root, saved.Tabs[0].FocusPath); !ok || node.PaneID != second.id {
		t.Fatalf("saved focus path = %v, want the focused Pane %q", saved.Tabs[0].FocusPath, second.id)
	}
	for _, pane := range saved.Panes {
		if pane.Session.Dir == "" {
			t.Fatalf("Pane %q lost its directory", pane.ID)
		}
	}

	// A fresh phone reading that payload writes byte-identical shared data.
	reopened := adoptPayload(t, payload)
	if got, want := sharedFormat(t, payloadOf(reopened.tabs)), sharedFormat(t, payload); got != want {
		t.Fatalf("phone-created tree did not reopen unchanged\n got: %s\nwant: %s", got, want)
	}
}

// TestLegacySinglePaneRecordRestoresWithoutMigration pins the compatibility
// subset: the older phone shape (one Tab, one root Pane, one descriptor) is a
// valid shared tree and needs no migration step.
func TestLegacySinglePaneRecordRestoresWithoutMigration(t *testing.T) {
	host := phoneHosts()[0]
	legacy := storedTabs(host)
	tabs := adoptPayload(t, legacy)

	if len(tabs.tabs) != 1 {
		t.Fatalf("legacy record restored %d Tabs, want 1", len(tabs.tabs))
	}
	tab := tabs.tabs[0]
	if len(tab.panes) != 1 || tab.focus != tab.panes[0].id {
		t.Fatalf("legacy record restored %+v, want its one Pane focused", tab)
	}
	if tab.root == nil || !tab.root.IsPane() || tab.root.PaneID != tab.panes[0].id {
		t.Fatalf("legacy record restored tree %+v, want the single Pane leaf", tab.root)
	}
	if got, want := sharedFormat(t, payloadOf(tabs.tabs)), sharedFormat(t, legacy); got != want {
		t.Fatalf("legacy record changed on restore and save\n got: %s\nwant: %s", got, want)
	}
}

// dirReportingScreen is a terminal that reports an OSC 7 directory, which the
// real screen does through its optional Dir method.
type dirReportingScreen struct {
	*fakeScreen
	dir string
}

func (s *dirReportingScreen) Dir() string { return s.dir }

// TestPayloadCapturesTheVisiblePanesDirectory proves a Pane's live working
// directory reaches the shared format, not only a value restored from it.
func TestPayloadCapturesTheVisiblePanesDirectory(t *testing.T) {
	tabs := &sessionTabs{}
	_, pane := tabs.begin(phoneHosts()[0])
	tabs.mu.Lock()
	pane.screen = &dirReportingScreen{dir: "/srv/live"}
	pane.status = tabReady
	tabs.mu.Unlock()

	payload := payloadOf(tabs.tabs)
	if len(payload.Panes) != 1 {
		t.Fatalf("saved %d Panes, want 1", len(payload.Panes))
	}
	if got := payload.Panes[0].Session.Dir; got != "/srv/live" {
		t.Fatalf("saved Dir = %q, want the terminal's OSC 7 directory", got)
	}
}

// TestEmptyTabsNeverReachOrComeBackFromThePayload closes a Tab's last Pane and
// proves the in-memory empty Tab is not written, and with no persisted entry it
// cannot be restored after a relaunch.
func TestEmptyTabsNeverReachOrComeBackFromThePayload(t *testing.T) {
	tabs := &sessionTabs{}
	tab, pane := tabs.begin(phoneHosts()[0])
	tabs.removePane(tab.id, string(pane.id))
	if tabs.tabs[0].root != nil {
		t.Fatalf("an emptied Tab kept a tree: %+v", tabs.tabs[0].root)
	}
	payload := payloadOf(tabs.tabs)
	if len(payload.Tabs) != 0 || len(payload.Panes) != 0 {
		t.Fatalf("an empty Tab reached the payload: %+v", payload)
	}
	if restored := adoptPayload(t, payload); len(restored.tabs) != 0 {
		t.Fatalf("an empty payload restored %d Tabs", len(restored.tabs))
	}
}
