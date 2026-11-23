package phoneshell

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeCommandHistory is the CommandHistoryService the Tools tests drive. It
// records which clear ran, so a test can prove one confirmation never deletes
// another scope's rows.
type fakeCommandHistory struct {
	mu      sync.Mutex
	entries []domainhistory.Entry
	err     error

	clearDeviceCalls     int
	deleteEverywhereCall int
	pruneCalls           [][]string
}

func newFakeCommandHistory(entries ...domainhistory.Entry) *fakeCommandHistory {
	return &fakeCommandHistory{entries: entries}
}

func (h *fakeCommandHistory) Entries(context.Context) ([]domainhistory.Entry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return nil, h.err
	}
	return append([]domainhistory.Entry(nil), h.entries...), nil
}

// ClearThisDevice models the desktop scope: rows this install created go, rows
// synced from elsewhere stay.
func (h *fakeCommandHistory) ClearThisDevice(context.Context) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clearDeviceCalls++
	removed := 0
	kept := make([]domainhistory.Entry, 0, len(h.entries))
	for _, entry := range h.entries {
		if entry.Source == "synced" {
			kept = append(kept, entry)
			continue
		}
		removed++
	}
	h.entries = kept
	return removed, nil
}

func (h *fakeCommandHistory) PruneHistoryEntries(_ context.Context, ids []string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneCalls = append(h.pruneCalls, append([]string(nil), ids...))
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	removed := 0
	kept := make([]domainhistory.Entry, 0, len(h.entries))
	for _, entry := range h.entries {
		if wanted[entry.ID] {
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	h.entries = kept
	return removed, nil
}

func (h *fakeCommandHistory) DeleteEverywhere(context.Context) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deleteEverywhereCall++
	removed := len(h.entries)
	h.entries = nil
	return removed, nil
}

func (h *fakeCommandHistory) remaining() []domainhistory.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]domainhistory.Entry(nil), h.entries...)
}

func (h *fakeCommandHistory) calls() (device, prune, everywhere int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clearDeviceCalls, len(h.pruneCalls), h.deleteEverywhereCall
}

func (h *fakeCommandHistory) prunedIDs() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	copied := make([][]string, len(h.pruneCalls))
	for i, ids := range h.pruneCalls {
		copied[i] = append([]string(nil), ids...)
	}
	return copied
}

func commandEntry(id, command, host, source string) domainhistory.Entry {
	return domainhistory.Entry{ID: id, Command: command, HostLabel: host, Source: source}
}

// toolHistoryEntries is the shared fixture journal: two local commands and one
// synced row, so "clear this device" and "delete everywhere" differ.
func toolHistoryEntries() []domainhistory.Entry {
	return []domainhistory.Entry{
		commandEntry("a", "ls -la", "prod box", "session"),
		commandEntry("b", "git status", "prod box", "session"),
		commandEntry("c", "deploy", "prod box", "synced"),
	}
}

// newToolsFixture opens the phone shell over a fake history service.
func newToolsFixture(t *testing.T, history CommandHistoryService) (*phoneFixture, *fakeOpener) {
	t.Helper()
	opener := &fakeOpener{output: "remote prompt $ "}
	options := []Option{WithScreenFactory(fakeScreens)}
	if history != nil {
		options = append(options, WithCommandHistory(history))
	}
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener, options...)
	return fixture, opener
}

// focusedPane returns the Pane the visible page draws.
func (f *phoneFixture) focusedPane() *phonePane {
	f.shell.sessions.mu.Lock()
	defer f.shell.sessions.mu.Unlock()
	if len(f.shell.sessions.tabs) == 0 {
		return nil
	}
	return f.shell.sessions.tabs[0].focusedPane()
}

// focusedScreen returns the fake terminal the visible Pane is drawing through.
func (f *phoneFixture) focusedScreen(t *testing.T) *fakeScreen {
	t.Helper()
	pane := f.focusedPane()
	if pane == nil || pane.screen == nil {
		t.Fatalf("no focused Pane with a screen; texts: %q", f.tester.Texts())
	}
	screen, ok := pane.screen.(*fakeScreen)
	if !ok {
		t.Fatalf("focused screen is %T, want *fakeScreen", pane.screen)
	}
	return screen
}

func (f *phoneFixture) openToolsPanel(t *testing.T, label string) {
	t.Helper()
	if err := f.tester.Click("Tools for " + label); err != nil {
		t.Fatalf("open the Tools surface: %v; texts: %q", err, f.tester.Texts())
	}
	f.tester.Frame()
}

func (f *phoneFixture) openHistoryTool(t *testing.T) {
	t.Helper()
	if err := f.tester.Click("Open Command history"); err != nil {
		t.Fatalf("open Command history: %v; texts: %q", err, f.tester.Texts())
	}
	f.tester.Frame()
}

func (f *phoneFixture) searchHistory(t *testing.T, query string) {
	t.Helper()
	if err := f.tester.Click("Search"); err != nil {
		t.Fatalf("focus the history search: %v; texts: %q", err, f.tester.Texts())
	}
	f.tester.Type(query)
	f.tester.Frame()
}

// pastedText reads what the fake terminal was handed by the fill or run path.
func pastedText(t *testing.T, screen *fakeScreen) string {
	t.Helper()
	data, err := screen.pasted.Read(transfer.Text)
	if err != nil {
		t.Fatalf("the terminal received no text: %v", err)
	}
	return string(data)
}

// TestToolsOpenCloseKeepsThePaneAndTerminalPosition is the ticket's core
// promise: the surface covers the terminal without ending or reattaching it,
// and closing returns to the same Pane with its scroll position intact.
func TestToolsOpenCloseKeepsThePaneAndTerminalPosition(t *testing.T) {
	history := newFakeCommandHistory(toolHistoryEntries()...)
	fixture, opener := newToolsFixture(t, history)
	handle := fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal", func() bool {
		return fixture.tester.HasText("remote prompt $")
	})

	pane := fixture.focusedPane()
	screen := fixture.focusedScreen(t)
	screen.scrolled = true
	paneID := pane.id
	path := fixture.shell.Router(Sessions).Path()

	fixture.openToolsPanel(t, "prod box")
	for _, want := range []string{"Command history", "Files", "Port forwarding", "System management", "Session log", "Notes"} {
		if !fixture.tester.HasText(want) {
			t.Errorf("Tools catalog lacks %q; texts: %q", want, fixture.tester.Texts())
		}
	}
	if got := fixture.shell.Router(Sessions).Path(); got != path {
		t.Fatalf("opening Tools changed the route to %q, want %q", got, path)
	}
	if fixture.tester.HasText("remote prompt $") {
		t.Error("the terminal is still drawn under the Tools surface")
	}
	if !handle.isAttached() {
		t.Fatal("opening Tools hibernated the Session")
	}
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens with Tools open, want the same one", got)
	}
	if pane := fixture.focusedPane(); pane == nil || pane.id != paneID {
		t.Fatalf("focused Pane changed while Tools was open: %v", pane)
	}

	if err := fixture.tester.Click("Done"); err != nil {
		t.Fatalf("close Tools: %v", err)
	}
	fixture.tester.Frame()
	if got := fixture.shell.Router(Sessions).Path(); got != path {
		t.Fatalf("closing Tools changed the route to %q, want %q", got, path)
	}
	if !fixture.tester.HasText("remote prompt $") {
		t.Fatalf("the terminal did not return; texts: %q", fixture.tester.Texts())
	}
	pane = fixture.focusedPane()
	if pane == nil || pane.id != paneID {
		t.Fatalf("closing Tools changed the focused Pane: %v", pane)
	}
	if !fixture.focusedScreen(t).scrolled {
		t.Fatal("closing Tools lost the terminal's scrollback position")
	}
	if !handle.isAttached() || fixture.liveScreens() != 1 {
		t.Fatal("closing Tools reattached or dropped the Session view")
	}
	if got := opener.count(); got != 1 {
		t.Fatalf("%d Sessions opened, want the same one", got)
	}
}

// TestToolsSurfaceBelongsToTheFocusedPane opens the surface on one Pane, moves
// focus away, and checks the surface neither follows the focus nor corrupts
// either Session.
func TestToolsSurfaceBelongsToTheFocusedPane(t *testing.T) {
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	history := newFakeCommandHistory(toolHistoryEntries()...)
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener,
		WithScreenFactory(fakeScreens), WithCommandHistory(history))

	prod := fixture.openSession(t, "prod box")
	fixture.await(t, "the first Pane", func() bool {
		return fixture.tester.HasText("prod output")
	})
	fixture.addPaneToTab(t, "prod box", "console")
	fixture.await(t, "the second Pane", func() bool {
		return opener.count() == 2 && opener.liveViews() == 1 &&
			fixture.tester.HasText("console output")
	})

	// Focus the first Pane through the switcher and open its Tools surface.
	if err := fixture.tester.Click("Show prod box"); err != nil {
		t.Fatalf("focus the first Pane: %v", err)
	}
	fixture.await(t, "the first Pane's view", func() bool {
		return prod.isAttached() && fixture.tester.HasText("prod output")
	})
	fixture.openToolsPanel(t, "prod box")
	if !fixture.tester.HasText("Command history") {
		t.Fatalf("the first Pane's Tools surface did not open; texts: %q", fixture.tester.Texts())
	}

	// Move focus with the Pane model's own operation, as a deep link or a
	// future gesture would while the surface is open. The surface belongs to
	// the Pane that opened it and closes when focus leaves.
	fixture.shell.sessions.mu.Lock()
	tabID := fixture.shell.sessions.tabs[0].id
	firstID := string(fixture.shell.sessions.tabs[0].panes[0].id)
	secondID := string(fixture.shell.sessions.tabs[0].panes[1].id)
	fixture.shell.sessions.mu.Unlock()
	fixture.shell.sessions.focusPane(tabID, secondID)
	fixture.await(t, "the second Pane after the focus switch", func() bool {
		return fixture.tester.HasText("console output")
	})
	if fixture.tester.HasText("Command history") || fixture.tester.HasText("Done") {
		t.Fatalf("the first Pane's Tools surface followed the focus; texts: %q", fixture.tester.Texts())
	}
	if prod.isEnded() || opener.ended() != 0 {
		t.Fatal("the focus switch ended a Session")
	}
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens after the focus switch, want 1", got)
	}

	// Returning reattaches the first Pane and shows its terminal, not the
	// surface it had left open.
	fixture.shell.sessions.focusPane(tabID, firstID)
	fixture.await(t, "the first Pane again", func() bool {
		return fixture.tester.HasText("prod output")
	})
	if fixture.tester.HasText("Command history") || fixture.tester.HasText("Done") {
		t.Fatalf("a stale Tools surface returned with the Pane; texts: %q", fixture.tester.Texts())
	}
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens after returning, want 1", got)
	}
}

// TestClosingAPaneClosesItsToolsSurface checks that closing a Pane while its
// surface is open removes both, leaving the Tab's empty-session entry.
func TestClosingAPaneClosesItsToolsSurface(t *testing.T) {
	history := newFakeCommandHistory(toolHistoryEntries()...)
	fixture, _ := newToolsFixture(t, history)
	fixture.openSession(t, "prod box")
	fixture.openToolsPanel(t, "prod box")
	if !fixture.tester.HasText("Command history") {
		t.Fatalf("the Tools surface did not open; texts: %q", fixture.tester.Texts())
	}

	// The header keeps its close action while the surface is open.
	if err := fixture.tester.Click("Close session prod box"); err != nil {
		t.Fatalf("request the Pane close: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Close"); err != nil {
		t.Fatalf("confirm the Pane close: %v", err)
	}
	fixture.settle()

	if fixture.tester.HasText("Command history") || fixture.tester.HasText("Done") {
		t.Fatalf("the closed Pane's Tools surface survived; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("This Tab is empty.") {
		t.Fatalf("closing the Pane did not leave the empty Tab; texts: %q", fixture.tester.Texts())
	}
	fixture.shell.sessions.mu.Lock()
	panes := len(fixture.shell.sessions.tabs[0].panes)
	fixture.shell.sessions.mu.Unlock()
	if panes != 0 {
		t.Fatalf("the Tab still holds %d Panes, want none", panes)
	}
}

// TestToolsHistorySearchFillAndRun covers the search, fill, and direct-run
// actions. Fill and Run both close the surface so the user sees the terminal
// the command was written to.
func TestToolsHistorySearchFillAndRun(t *testing.T) {
	history := newFakeCommandHistory(toolHistoryEntries()...)
	fixture, opener := newToolsFixture(t, history)
	handle := fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal", func() bool {
		return fixture.tester.HasText("remote prompt $")
	})

	fixture.openToolsPanel(t, "prod box")
	fixture.openHistoryTool(t)
	fixture.searchHistory(t, "git")
	if !fixture.tester.HasText("1 matching entries") {
		t.Fatalf("search did not narrow the list; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("git status") {
		t.Fatalf("the matching command is missing; texts: %q", fixture.tester.Texts())
	}
	if fixture.tester.HasText("ls -la") {
		t.Fatalf("a non-matching command is still listed; texts: %q", fixture.tester.Texts())
	}

	if err := fixture.tester.Click("Fill: git status"); err != nil {
		t.Fatalf("fill the command: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("remote prompt $") {
		t.Fatalf("fill did not return to the terminal; texts: %q", fixture.tester.Texts())
	}
	if got := pastedText(t, fixture.focusedScreen(t)); got != "git status" {
		t.Fatalf("terminal received %q, want the filled command", got)
	}
	if keys := handle.sentKeys(); len(keys) != 0 {
		t.Fatalf("fill sent %v, want no key", keys)
	}

	// Run does the same write and then submits it with Enter through the
	// terminal's own encoder.
	fixture.openToolsPanel(t, "prod box")
	fixture.openHistoryTool(t)
	if err := fixture.tester.Click("Run: git status"); err != nil {
		t.Fatalf("run the command: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.tester.Frame()
	if got := pastedText(t, fixture.focusedScreen(t)); got != "git status" {
		t.Fatalf("run wrote %q, want the command", got)
	}
	keys := handle.sentKeys()
	if len(keys) != 1 || keys[0].key != ui.KeyEnter || keys[0].mods != 0 {
		t.Fatalf("run sent %v, want one plain Enter", keys)
	}
	if got := opener.count(); got != 1 {
		t.Fatalf("run opened %d Sessions, want the same one", got)
	}
}

// TestToolsClearScopesAreIndependentAndCancelDeletesNothing checks each of the
// three clear confirmations separately: Cancel never calls the service, and a
// confirmed clear calls only its own scope.
func TestToolsClearScopesAreIndependentAndCancelDeletesNothing(t *testing.T) {
	t.Run("this device", func(t *testing.T) {
		history := newFakeCommandHistory(toolHistoryEntries()...)
		fixture, _ := newToolsFixture(t, history)
		fixture.openSession(t, "prod box")
		fixture.openToolsPanel(t, "prod box")
		fixture.openHistoryTool(t)

		if err := fixture.tester.Click("Clear this device"); err != nil {
			t.Fatalf("request this-device clear: %v", err)
		}
		fixture.tester.Frame()
		if !fixture.tester.HasText("Clear history recorded on this device?") {
			t.Fatalf("the this-device confirmation is missing; texts: %q", fixture.tester.Texts())
		}
		if err := fixture.tester.Click("Cancel"); err != nil {
			t.Fatalf("cancel this-device clear: %v", err)
		}
		fixture.tester.Frame()
		if device, prune, everywhere := history.calls(); device != 0 || prune != 0 || everywhere != 0 {
			t.Fatalf("cancel called the service: %d/%d/%d", device, prune, everywhere)
		}
		if got := len(history.remaining()); got != 3 {
			t.Fatalf("cancel removed rows: %d remain, want 3", got)
		}

		if err := fixture.tester.Click("Clear this device"); err != nil {
			t.Fatalf("request this-device clear again: %v", err)
		}
		fixture.tester.Frame()
		if err := fixture.tester.Click("Delete"); err != nil {
			t.Fatalf("confirm this-device clear: %v", err)
		}
		fixture.tester.Frame()
		device, prune, everywhere := history.calls()
		if device != 1 || prune != 0 || everywhere != 0 {
			t.Fatalf("this-device clear called %d/%d/%d, want only its own scope", device, prune, everywhere)
		}
		remaining := history.remaining()
		if len(remaining) != 1 || remaining[0].Source != "synced" {
			t.Fatalf("this-device clear kept %+v, want only the synced row", remaining)
		}
		if !fixture.tester.HasText("Deleted 2 entries.") {
			t.Fatalf("the outcome is missing; texts: %q", fixture.tester.Texts())
		}
	})

	t.Run("matching entries", func(t *testing.T) {
		history := newFakeCommandHistory(toolHistoryEntries()...)
		fixture, _ := newToolsFixture(t, history)
		fixture.openSession(t, "prod box")
		fixture.openToolsPanel(t, "prod box")
		fixture.openHistoryTool(t)
		fixture.searchHistory(t, "git")

		if err := fixture.tester.Click("Clear matching entries"); err != nil {
			t.Fatalf("request matching clear: %v", err)
		}
		fixture.tester.Frame()
		if !fixture.tester.HasText("Delete 1 matching entries?") {
			t.Fatalf("the matching confirmation is missing; texts: %q", fixture.tester.Texts())
		}
		if err := fixture.tester.Click("Cancel"); err != nil {
			t.Fatalf("cancel matching clear: %v", err)
		}
		fixture.tester.Frame()
		if device, prune, everywhere := history.calls(); device != 0 || prune != 0 || everywhere != 0 {
			t.Fatalf("cancel called the service: %d/%d/%d", device, prune, everywhere)
		}
		if got := len(history.remaining()); got != 3 {
			t.Fatalf("cancel removed rows: %d remain, want 3", got)
		}

		if err := fixture.tester.Click("Clear matching entries"); err != nil {
			t.Fatalf("request matching clear again: %v", err)
		}
		fixture.tester.Frame()
		if err := fixture.tester.Click("Delete"); err != nil {
			t.Fatalf("confirm matching clear: %v", err)
		}
		fixture.tester.Frame()
		device, prune, everywhere := history.calls()
		if device != 0 || prune != 1 || everywhere != 0 {
			t.Fatalf("matching clear called %d/%d/%d, want only its own scope", device, prune, everywhere)
		}
		if ids := history.prunedIDs(); len(ids) != 1 || strings.Join(ids[0], ",") != "b" {
			t.Fatalf("matching clear pruned %v, want exactly the searched row", ids)
		}
		remaining := history.remaining()
		if len(remaining) != 2 {
			t.Fatalf("matching clear left %+v, want the two non-matching rows", remaining)
		}
	})

	t.Run("everywhere", func(t *testing.T) {
		history := newFakeCommandHistory(toolHistoryEntries()...)
		fixture, _ := newToolsFixture(t, history)
		fixture.openSession(t, "prod box")
		fixture.openToolsPanel(t, "prod box")
		fixture.openHistoryTool(t)

		if err := fixture.tester.Click("Delete everywhere"); err != nil {
			t.Fatalf("request delete-everywhere: %v", err)
		}
		fixture.tester.Frame()
		if !fixture.tester.HasText("Delete history everywhere?") {
			t.Fatalf("the delete-everywhere confirmation is missing; texts: %q", fixture.tester.Texts())
		}
		if err := fixture.tester.Click("Cancel"); err != nil {
			t.Fatalf("cancel delete-everywhere: %v", err)
		}
		fixture.tester.Frame()
		if device, prune, everywhere := history.calls(); device != 0 || prune != 0 || everywhere != 0 {
			t.Fatalf("cancel called the service: %d/%d/%d", device, prune, everywhere)
		}
		if got := len(history.remaining()); got != 3 {
			t.Fatalf("cancel removed rows: %d remain, want 3", got)
		}

		if err := fixture.tester.Click("Delete everywhere"); err != nil {
			t.Fatalf("request delete-everywhere again: %v", err)
		}
		fixture.tester.Frame()
		if err := fixture.tester.Click("Delete"); err != nil {
			t.Fatalf("confirm delete-everywhere: %v", err)
		}
		fixture.tester.Frame()
		device, prune, everywhere := history.calls()
		if device != 0 || prune != 0 || everywhere != 1 {
			t.Fatalf("delete-everywhere called %d/%d/%d, want only its own scope", device, prune, everywhere)
		}
		if got := len(history.remaining()); got != 0 {
			t.Fatalf("delete-everywhere left %d rows, want none", got)
		}
	})
}

// TestToolsHistoryUnavailableStillOpensTheSurface checks the failure path: the
// catalog and the other tools stay usable and the history page names why it
// cannot work.
func TestToolsHistoryUnavailableStillOpensTheSurface(t *testing.T) {
	t.Run("service reports a failure", func(t *testing.T) {
		history := newFakeCommandHistory(toolHistoryEntries()...)
		history.err = errors.New("database is locked")
		fixture, _ := newToolsFixture(t, history)
		fixture.openSession(t, "prod box")
		fixture.openToolsPanel(t, "prod box")

		for _, want := range []string{"Command history", "Files", "Notes"} {
			if !fixture.tester.HasText(want) {
				t.Errorf("the catalog lacks %q despite the history failure; texts: %q", want, fixture.tester.Texts())
			}
		}
		fixture.openHistoryTool(t)
		if !fixture.tester.HasText("Command history is unavailable: database is locked") {
			t.Fatalf("the reason is missing; texts: %q", fixture.tester.Texts())
		}
		if err := fixture.tester.Click("All tools"); err != nil {
			t.Fatalf("return to the catalog: %v", err)
		}
		fixture.tester.Frame()
		if err := fixture.tester.Click("Open Files"); err != nil {
			t.Fatalf("open the placeholder tool: %v", err)
		}
		fixture.tester.Frame()
		if !fixture.tester.HasText("Files from this Session are not on the phone yet.") {
			t.Fatalf("the placeholder tool did not open; texts: %q", fixture.tester.Texts())
		}
	})

	t.Run("no service wired", func(t *testing.T) {
		fixture, _ := newToolsFixture(t, nil)
		fixture.openSession(t, "prod box")
		fixture.openToolsPanel(t, "prod box")
		if !fixture.tester.HasText("Unavailable") {
			t.Fatalf("the catalog does not mark history unavailable; texts: %q", fixture.tester.Texts())
		}
		fixture.openHistoryTool(t)
		if !fixture.tester.HasText("Command history is not available in this build.") {
			t.Fatalf("the reason is missing; texts: %q", fixture.tester.Texts())
		}
		if !fixture.tester.HasText("All tools") {
			t.Fatalf("the surface cannot return to its catalog; texts: %q", fixture.tester.Texts())
		}
	})
}

// toolsPromptFixture is the phone shell over one prompt-driven opener and a
// fake history service.
type toolsPromptFixture struct {
	shell   *Shell
	prompts *Prompts
	tester  *ui.Tester
}

func newToolsPromptFixture(t *testing.T,
	run func(ctx context.Context, prompts *Prompts) (SessionHandle, error)) *toolsPromptFixture {

	t.Helper()
	host := domainvault.Host{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com", Port: 2222,
		Username: "alice", Protocol: domainvault.ProtocolSSH,
	}
	prompts := NewPrompts()
	shell := New(
		WithHosts(&fakeHostStore{hosts: []domainvault.Host{host}}),
		WithSessionOpener(promptOpener{run: func(ctx context.Context) (SessionHandle, error) {
			return run(ctx, prompts)
		}}),
		WithPrompts(prompts),
		WithScreenFactory(fakeScreens),
		WithCommandHistory(newFakeCommandHistory(toolHistoryEntries()...)),
	)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	shell.Select(Hosts)
	tester := ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	tester.Frame()
	return &toolsPromptFixture{shell: shell, prompts: prompts, tester: tester}
}

func (f *toolsPromptFixture) await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; texts: %q", what, f.tester.Texts())
		}
		f.tester.Frame()
		f.tester.Advance(20 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	f.tester.Frame()
}

// forceToolsOpen plants an open surface on the visible Pane, as a state left
// over from before a reconnect would be. It lets a prompt test prove the
// connecting page takes precedence.
func forceToolsOpen(shell *Shell) {
	shell.sessions.mu.Lock()
	defer shell.sessions.mu.Unlock()
	if len(shell.sessions.tabs) == 0 {
		return
	}
	pane := shell.sessions.tabs[0].focusedPane()
	if pane == nil {
		return
	}
	pane.tools.open = true
	pane.tools.tool = toolHistory
	// Leave the load pending, so the surface loads the journal as soon as the
	// Pane is ready, exactly as a fresh open would.
	pane.tools.loaded = false
}

// TestToolsSurfaceNeverBlocksHostKeyPrompt checks that a trust prompt owns the
// page while it is pending. A surface state left open is not drawn until the
// Pane is ready, so the prompt stays visible and its buttons interactive.
func TestToolsSurfaceNeverBlocksHostKeyPrompt(t *testing.T) {
	decided := make(chan appconnection.PromptDecision, 1)
	fixture := newToolsPromptFixture(t, func(ctx context.Context, prompts *Prompts) (SessionHandle, error) {
		decision, err := prompts.HostKey.Confirm(ctx, unknownKey())
		decided <- decision
		if err != nil {
			return nil, err
		}
		if !decision.Trust {
			return nil, errors.New("host key rejected")
		}
		return newFakeHandle("remote ready\r\n"), nil
	})
	if err := fixture.tester.Click("Open prod box"); err != nil {
		t.Fatalf("open the Host: %v", err)
	}
	fixture.tester.Frame()
	fixture.await(t, "the host-key prompt", func() bool {
		return fixture.tester.HasText("Unknown host key")
	})

	forceToolsOpen(fixture.shell)
	fixture.tester.Frame()
	if !fixture.tester.HasText("Unknown host key") || !fixture.tester.HasText("Trust and save") {
		t.Fatalf("the Tools surface covered the host-key prompt; texts: %q", fixture.tester.Texts())
	}
	if fixture.tester.HasText("Command history") || fixture.tester.HasText("Done") {
		t.Fatalf("the Tools surface was drawn over a pending prompt; texts: %q", fixture.tester.Texts())
	}

	if err := fixture.tester.Click("Trust and save"); err != nil {
		t.Fatalf("answer the prompt: %v", err)
	}
	fixture.await(t, "the Tools surface after the prompt", func() bool {
		return fixture.tester.HasText("3 matching entries")
	})
	if decision := <-decided; !decision.Trust {
		t.Fatalf("prompt decision = %+v, want trust", decision)
	}
}

// TestToolsSurfaceNeverBlocksCredentialPrompt is the credential counterpart:
// the password form stays interactive while a surface state is open.
func TestToolsSurfaceNeverBlocksCredentialPrompt(t *testing.T) {
	answered := make(chan appconnection.PasswordDecision, 1)
	fixture := newToolsPromptFixture(t, func(ctx context.Context, prompts *Prompts) (SessionHandle, error) {
		decision, err := prompts.Auth.Password(ctx, appconnection.PasswordRequest{
			HostID: "prod", Hostname: "prod.example.com", Username: "alice",
		})
		answered <- decision
		if err != nil {
			return nil, err
		}
		if decision.Cancelled {
			return nil, errors.New("cancelled")
		}
		return newFakeHandle("signed in\r\n"), nil
	})
	if err := fixture.tester.Click("Open prod box"); err != nil {
		t.Fatalf("open the Host: %v", err)
	}
	fixture.tester.Frame()
	fixture.await(t, "the password prompt", func() bool {
		return fixture.tester.HasText("Password required")
	})

	forceToolsOpen(fixture.shell)
	fixture.tester.Frame()
	if !fixture.tester.HasText("Password required") ||
		!fixture.tester.HasText("Sign in as alice@prod.example.com.") {
		t.Fatalf("the Tools surface covered the credential prompt; texts: %q", fixture.tester.Texts())
	}
	if fixture.tester.HasText("Command history") || fixture.tester.HasText("Done") {
		t.Fatalf("the Tools surface was drawn over a credential prompt; texts: %q", fixture.tester.Texts())
	}

	fixture.tester.Type("hunter2")
	fixture.tester.Frame()
	if err := fixture.tester.Click("Continue"); err != nil {
		t.Fatalf("answer the prompt: %v", err)
	}
	fixture.await(t, "the Tools surface after the prompt", func() bool {
		return fixture.tester.HasText("3 matching entries")
	})
	if decision := <-answered; decision.Password != "hunter2" {
		t.Fatalf("password answer = %q, want hunter2", decision.Password)
	}
}
