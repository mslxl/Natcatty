package app

import (
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestQuitConfirmationCountsQuakeSessions proves a live Quake Session reaches
// the one quit prompt beside the main Workspace's Sessions, so quitting never
// ends one without saying so.
func TestQuitConfirmationCountsQuakeSessions(t *testing.T) {
	q := newQuakeGraph(t)
	q.manager.OpenMain()
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)
	if err := q.quake.Activate(); err != nil {
		t.Fatalf("quake Activate: %v", err)
	}
	if q.state.ActiveSessionCount() != 1 || q.quake.SessionCount() != 1 {
		t.Fatalf("session counts = main %d, quake %d; want one each",
			q.state.ActiveSessionCount(), q.quake.SessionCount())
	}

	q.confirmer.answer = true
	if !q.manager.ConfirmQuit() {
		t.Fatal("ConfirmQuit refused an approved quit")
	}
	if len(q.confirmer.prompts) != 1 {
		t.Fatalf("quit prompts = %d, want one", len(q.confirmer.prompts))
	}
	want := locales.In("en").QuitConfirmMessage(2)
	if got := q.confirmer.prompts[0].Message; got != want {
		t.Fatalf("quit prompt = %q, want %q", got, want)
	}
}

// TestQuitTearsDownTheDropdownBeforeQuakeSessions pins the first half of the
// quit order: the Panel is hidden and destroyed before any Quake Session
// closes. The surface observes the Panel state from inside its own Close, so
// reversing the order fails the test even if both eventually happen.
func TestQuitTearsDownTheDropdownBeforeQuakeSessions(t *testing.T) {
	q := newQuakeGraph(t)
	q.manager.OpenMain()
	if err := q.quake.Activate(); err != nil {
		t.Fatalf("quake Activate: %v", err)
	}
	tab, ok := q.quake.TabAt(0)
	if !ok || tab.Session == nil {
		t.Fatal("the Quake workspace has no live Tab")
	}
	q.manager.ShowDropdown()
	panel := dropdownWindow(t, q.windowGraph)
	surface := q.surfaces[0]
	surface.closeCheck = func() bool { return panel.IsDestroyed() }

	shutdownDropdownWorkspace(q.manager, q.quake)

	if !panel.IsDestroyed() {
		t.Fatal("quit left the Dropdown Panel alive")
	}
	if surface.closes != 1 {
		t.Fatalf("Quake surface closes = %d, want 1", surface.closes)
	}
	if !surface.closeCheckOK {
		t.Fatal("the Quake Session closed before the Dropdown Panel was destroyed")
	}
	if q.quake.TabCount() != 0 {
		t.Fatalf("quit left %d Quake Tabs", q.quake.TabCount())
	}
	waitFor(t, "the Quake Session to end", func() bool {
		return tab.Session.Snapshot().Status == appsession.StatusClosed
	})
}

// TestQuakeLocalShellJoinsTheSharedSessionService proves the Quake workspace
// opens its local Shell through the same Session service as the main window.
// Command History, cwd forwarding, and the notification hook are attached to
// that service, so a Quake Shell follows the same paths without a second
// pipeline.
func TestQuakeLocalShellJoinsTheSharedSessionService(t *testing.T) {
	q := newQuakeGraph(t)
	if err := q.quake.Activate(); err != nil {
		t.Fatalf("quake Activate: %v", err)
	}
	tab, _ := q.quake.TabAt(0)
	if tab.Session == nil {
		t.Fatal("the Quake Tab has no Session")
	}
	found := false
	for _, identity := range q.service.LiveIdentities() {
		if identity.ID == tab.Session.ID() {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the Quake Session %q is not a live Session of the shared service", tab.Session.ID())
	}
}
