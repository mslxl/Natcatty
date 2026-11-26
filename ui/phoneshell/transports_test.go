package phoneshell

// The phone's Session lifecycle is one implementation, but it must be proved
// against each remote transport rather than inferred from SSH. These tests
// drive the shell with SSH, Telnet, Mosh, and EternalTerminal Host records and
// assert the same background, foreground, dormant-restore, and resume-key
// behaviour for every one.

import (
	"testing"

	domainmodel "github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// remoteTransportHosts is one saved Host per remote transport. The ET Host
// carries the server port a real record needs.
func remoteTransportHosts() []domainvault.Host {
	return []domainvault.Host{
		{
			ID: "ssh", Label: "ssh box", Hostname: "ssh.example.com", Port: 22,
			Username: "deploy", Protocol: domainvault.ProtocolSSH,
		},
		{
			ID: "telnet", Label: "telnet box", Hostname: "telnet.example.com", Port: 23,
			Protocol: domainvault.ProtocolTelnet,
		},
		{
			ID: "mosh", Label: "mosh box", Hostname: "mosh.example.com", Port: 22,
			Username: "deploy", Protocol: domainvault.ProtocolSSH, Transport: domainvault.TransportMosh,
		},
		{
			ID: "et", Label: "et box", Hostname: "et.example.com", Port: 22,
			Username: "deploy", Protocol: domainvault.ProtocolSSH, Transport: domainvault.TransportET, ETPort: 2022,
		},
	}
}

// descriptorPayload is a previous launch's persisted Workspace for one Tab.
func descriptorPayload(host domainvault.Host, resumeKey string) domainworkspace.Payload {
	payload := domainworkspace.EmptyPayload()
	pane := domainmodel.PaneID("saved-pane-" + string(host.ID))
	payload.Tabs = append(payload.Tabs, domainworkspace.RestoredTab{
		ID:     domainmodel.TabID("saved-tab-" + string(host.ID)),
		Number: 1,
		Title:  host.Label,
		Root:   domainworkspace.NewPaneNode(pane),
	})
	payload.Panes = append(payload.Panes, domainworkspace.RestoredPane{
		ID:      pane,
		Session: descriptorOf(host, host.Label, resumeKey),
	})
	return payload
}

// TestBackgroundAndForegroundAreTheSameForEveryRemoteTransport runs the phone
// suspend/return sequence once per transport. Each Tab must release its one
// view, stay alive, persist as hibernated with its transport kind, and resume
// the same handle on return without reopening the Host.
func TestBackgroundAndForegroundAreTheSameForEveryRemoteTransport(t *testing.T) {
	for _, host := range remoteTransportHosts() {
		host := host
		t.Run(hostProtocolLabel(host), func(t *testing.T) {
			store := &fakeSessionStore{}
			opener := &fakeOpener{output: "keep running\r\n"}
			fixture := newPhoneFixtureWith(t, []domainvault.Host{host}, opener,
				WithSessionStore(store))
			handle := fixture.openSession(t, host.Label)
			fixture.await(t, "the terminal output", func() bool {
				return fixture.tester.HasText("keep running")
			})

			fixture.shell.Background()
			fixture.tester.Frame()
			if handle.isAttached() || opener.liveViews() != 0 {
				t.Fatal("backgrounding kept the terminal view attached")
			}
			if handle.isEnded() {
				t.Fatal("backgrounding ended the Session")
			}
			views := fixture.shell.sessions.list()
			if len(views) != 1 || views[0].status != tabHibernated {
				t.Fatalf("the backgrounded Tab is not hibernated: %+v", views)
			}

			last := store.last()
			if len(last.Tabs) != 1 || len(last.Panes) != 1 {
				t.Fatalf("backgrounding persisted %d Tab(s), %d Pane(s)",
					len(last.Tabs), len(last.Panes))
			}
			descriptor := last.Panes[0].Session
			if descriptor.Kind != sessionKindOf(host) {
				t.Fatalf("persisted kind = %q, want %q",
					descriptor.Kind, sessionKindOf(host))
			}
			if got := descriptor.HostID; got != host.ID {
				t.Fatalf("persisted HostID = %q, want %q", got, host.ID)
			}
			// A new ET Session writes the opaque locator its saved credentials
			// answer to; no other transport invents one. The opener receives
			// that same locator, so the saved credentials and the restore
			// payload cannot disagree.
			keys := opener.resumeKeysUsed()
			if host.TransportLabel() == domainvault.TransportET {
				if descriptor.ResumeKey == "" {
					t.Fatal("a new ET Tab persisted no resume key")
				}
				if len(keys) != 1 || keys[0] != descriptor.ResumeKey {
					t.Fatalf("a new ET Tab opened with resume keys %q, want %q",
						keys, descriptor.ResumeKey)
				}
			} else {
				if descriptor.ResumeKey != "" {
					t.Fatalf("%s persisted an ET resume key", host.Protocol)
				}
				if len(keys) != 0 {
					t.Fatalf("%s opened through the ET resume path: %q", host.Protocol, keys)
				}
			}

			fixture.shell.Foreground()
			fixture.await(t, "the resumed Session", func() bool {
				return handle.isAttached() && opener.count() == 1
			})
			if handle.isEnded() {
				t.Fatal("returning to the foreground ended the Session")
			}
		})
	}
}

// TestDormantRestoreIsPerTransportAndKeepsTheETResumeKey restores each
// transport from the persisted Workspace and asserts the opener received the
// right Host. Only EternalTerminal passes a resume key, through
// OpenHostWithResume; SSH, Telnet, and Mosh use the ordinary OpenHost path.
func TestDormantRestoreIsPerTransportAndKeepsTheETResumeKey(t *testing.T) {
	for _, host := range remoteTransportHosts() {
		host := host
		t.Run(hostProtocolLabel(host), func(t *testing.T) {
			resumeKey := ""
			if host.TransportLabel() == domainvault.TransportET {
				resumeKey = "et-resume-restored-tab"
			}
			store := &fakeSessionStore{payload: descriptorPayload(host, resumeKey)}
			opener := &fakeOpener{output: "restored output\r\n"}
			fixture := newPhoneFixtureWith(t, []domainvault.Host{host}, opener,
				WithSessionStore(store))
			fixture.selectSessions(t)

			// A cold start restores the persisted Pane by itself. With the
			// Sessions list visible it stays hibernated, not attached.
			fixture.await(t, "the restored Session", func() bool {
				views := fixture.shell.sessions.list()
				return opener.count() == 1 && len(views) == 1 &&
					views[0].status == tabHibernated
			})
			if err := fixture.tester.Click("Open session " + host.Label); err != nil {
				t.Fatalf("tap the restored Tab: %v", err)
			}
			fixture.settle()
			fixture.await(t, "the restored view", func() bool {
				return opener.liveViews() == 1
			})
			opened := opener.opened()
			if len(opened) != 1 || opened[0].ID != host.ID ||
				opened[0].Protocol != host.Protocol {
				t.Fatalf("restore opened %+v, want Host %q", opened, host.ID)
			}

			keys := opener.resumeKeysUsed()
			if host.TransportLabel() == domainvault.TransportET {
				if len(keys) != 1 || keys[0] != resumeKey {
					t.Fatalf("ET restore resume keys = %q, want %q", keys, resumeKey)
				}
			} else if len(keys) != 0 {
				t.Fatalf("%s restore used the ET resume path: %q", host.Protocol, keys)
			}

			// The restored Tab is persisted again with the same locator, so a
			// second relaunch can still reattach.
			last := store.last()
			if len(last.Panes) != 1 {
				t.Fatalf("restore persisted %d Pane(s)", len(last.Panes))
			}
			if got := last.Panes[0].Session.ResumeKey; got != resumeKey {
				t.Fatalf("persisted resume key = %q, want %q", got, resumeKey)
			}
		})
	}
}
