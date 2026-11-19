package phoneshell

import (
	"testing"

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
					domainworkspace.NewSplitNode("s3", domainworkspace.Vertical, 50,
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

func TestFlattenRestoredMakesOneSessionPerRemotePane(t *testing.T) {
	sessions := FlattenRestored(restorePayload())

	want := []struct {
		pane  string
		title string
		kind  domainworkspace.SessionKind
	}{
		{"pane-a", "prod-east", domainworkspace.SessionSSH},
		{"pane-c", "web-01", domainworkspace.SessionSSH},
		{"pane-d", "db.example.com", domainworkspace.SessionTelnet},
		{"pane-e", "edge.example.com", domainworkspace.SessionMosh},
	}
	if len(sessions) != len(want) {
		t.Fatalf("flattened %d Sessions, want %d: %+v", len(sessions), len(want), sessions)
	}
	for i, w := range want {
		got := sessions[i]
		if got.PaneID != w.pane {
			t.Errorf("Session %d pane = %q, want %q", i, got.PaneID, w.pane)
		}
		if got.Title != w.title {
			t.Errorf("Session %d title = %q, want %q", i, got.Title, w.title)
		}
		if got.Descriptor.Kind != w.kind {
			t.Errorf("Session %d kind = %q, want %q", i, got.Descriptor.Kind, w.kind)
		}
	}
	// The local pane and the Tab's generated title do not leak into a Session.
	for _, session := range sessions {
		if session.Title == "ops" || session.Title == "web/db" || session.Title == "unsupported" {
			t.Fatalf("a Tab title labelled a flattened Session: %q", session.Title)
		}
	}
}

func TestFlattenRestoredDropsUnsupportedAndDormantTabs(t *testing.T) {
	payload := domainworkspace.Payload{
		Version: domainworkspace.PayloadVersion,
		Tabs: []domainworkspace.RestoredTab{
			{ID: "tab-local", Title: "local", Root: domainworkspace.NewPaneNode("local")},
			{ID: "tab-empty", Title: "empty", Root: domainworkspace.NewPaneNode("missing")},
			{ID: "tab-et", Title: "et", Root: domainworkspace.NewPaneNode("et")},
		},
		Panes: []domainworkspace.RestoredPane{
			{ID: "local", Session: domainworkspace.SessionDescriptor{Kind: domainworkspace.SessionLocal}},
			{ID: "et", Session: domainworkspace.SessionDescriptor{
				Kind: domainworkspace.SessionET, HostID: "edge", Hostname: "edge.example.com",
				ResumeKey: "vault:et/session",
			}},
		},
	}
	sessions := FlattenRestored(payload)
	if len(sessions) != 1 {
		t.Fatalf("flattened %d Sessions, want only the ET one: %+v", len(sessions), sessions)
	}
	if sessions[0].PaneID != "et" || sessions[0].Descriptor.Kind != domainworkspace.SessionET {
		t.Fatalf("flattened Session = %+v, want the ET pane", sessions[0])
	}
	if sessions[0].Descriptor.ResumeKey != "vault:et/session" {
		t.Errorf("ET resume key was lost: %q", sessions[0].Descriptor.ResumeKey)
	}
}

func TestFlattenRestoredRejectsAnUnknownPayloadVersion(t *testing.T) {
	payload := restorePayload()
	payload.Version = domainworkspace.PayloadVersion + 1
	if sessions := FlattenRestored(payload); len(sessions) != 0 {
		t.Fatalf("an unknown payload version flattened to %d Sessions", len(sessions))
	}
}

func TestRestoredSessionTitlePrefersThePaneName(t *testing.T) {
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
			Kind: domainworkspace.SessionSSH, HostID: "prod", Hostname: "prod.example.com",
		}, "prod.example.com"},
		{"whitespace is not a name", domainworkspace.SessionDescriptor{
			Kind: domainworkspace.SessionSSH, CustomName: "  ", Label: " ",
			HostID: "prod", Hostname: "prod.example.com",
		}, "prod.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := domainworkspace.Payload{
				Version: domainworkspace.PayloadVersion,
				Tabs: []domainworkspace.RestoredTab{
					{ID: "tab", Title: "desktop title", Root: domainworkspace.NewPaneNode("pane")},
				},
				Panes: []domainworkspace.RestoredPane{{ID: "pane", Session: tc.pane}},
			}
			sessions := FlattenRestored(payload)
			if len(sessions) != 1 {
				t.Fatalf("flattened %d Sessions, want 1", len(sessions))
			}
			if got := sessions[0].Title; got != tc.want {
				t.Fatalf("title = %q, want %q", got, tc.want)
			}
		})
	}
}
