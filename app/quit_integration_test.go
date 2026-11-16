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

// TestClosingTheLastWindowConfirmsLiveQuakeSessions proves closing the last
// counted window cannot silently end live Quake Sessions. The close prompt is
// the same one the app asks for its own Sessions, with the Quake count added.
func TestClosingTheLastWindowConfirmsLiveQuakeSessions(t *testing.T) {
	q := newQuakeGraph(t)
	q.manager.OpenMain()
	if err := q.quake.Activate(); err != nil {
		t.Fatalf("quake Activate: %v", err)
	}
	if err := q.quake.AddLocalTab(); err != nil {
		t.Fatalf("quake AddLocalTab: %v", err)
	}
	if q.state.ActiveSessionCountMain() != 0 || q.quake.SessionCount() != 2 {
		t.Fatalf("session counts = main %d, quake %d; want zero and two",
			q.state.ActiveSessionCountMain(), q.quake.SessionCount())
	}

	main := q.manager.Main()
	main.Close()
	if main.IsDestroyed() {
		t.Fatal("closing the last window discarded two live Quake Sessions without confirmation")
	}
	if len(q.confirmer.prompts) != 1 {
		t.Fatalf("close prompts = %d, want one", len(q.confirmer.prompts))
	}
	if got, want := q.confirmer.prompts[0].Message, locales.In("en").CloseWindowMessage(2); got != want {
		t.Fatalf("close prompt = %q, want %q", got, want)
	}
	if q.manager.Quitting() {
		t.Fatal("a cancelled close marked the quit")
	}

	q.confirmer.answer = true
	main.Close()
	if !main.IsDestroyed() {
		t.Fatal("an approved close did not destroy the last window")
	}
	if !q.manager.Quitting() {
		t.Fatal("an approved last-window close did not mark the quit")
	}
}

// TestClosingSettingsAsTheLastWindowConfirmsLiveQuakeSessions covers the other
// last-window kind: Settings owns no Sessions, but closing it still quits and
// ends the live Quake Sessions.
func TestClosingSettingsAsTheLastWindowConfirmsLiveQuakeSessions(t *testing.T) {
	q := newQuakeGraph(t)
	q.manager.OpenMain()
	q.manager.OpenSettings()
	if err := q.quake.Activate(); err != nil {
		t.Fatalf("quake Activate: %v", err)
	}
	if err := q.quake.AddLocalTab(); err != nil {
		t.Fatalf("quake AddLocalTab: %v", err)
	}

	// The main window is not last while Settings is open, so its close needs
	// no Quake count and leaves the Quake Sessions running.
	q.confirmer.answer = true
	q.manager.Main().Close()
	if len(q.confirmer.prompts) != 0 {
		t.Fatalf("closing the main window beside Settings asked a prompt: %+v", q.confirmer.prompts)
	}
	if q.manager.Quitting() {
		t.Fatal("closing the main window beside Settings marked the quit")
	}

	settings := q.manager.settings
	q.confirmer.answer = false
	settings.Close()
	if settings.IsDestroyed() {
		t.Fatal("closing the last window (Settings) discarded the Quake Sessions without confirmation")
	}
	if len(q.confirmer.prompts) != 1 {
		t.Fatalf("settings close prompts = %d, want one", len(q.confirmer.prompts))
	}
	if got, want := q.confirmer.prompts[0].Message, locales.In("en").CloseWindowMessage(2); got != want {
		t.Fatalf("settings close prompt = %q, want %q", got, want)
	}

	q.confirmer.answer = true
	settings.Close()
	if !settings.IsDestroyed() || !q.manager.Quitting() {
		t.Fatal("an approved settings close did not quit")
	}
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
