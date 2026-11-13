package quake

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// testTransport keeps a Session alive until the test closes it. It is the
// only transport a Quake test needs: the workspace never reads or writes
// terminal bytes.
type testTransport struct {
	closed chan struct{}
	once   sync.Once
}

func newTestTransport() *testTransport {
	return &testTransport{closed: make(chan struct{})}
}

func (t *testTransport) Read([]byte) (int, error) {
	<-t.closed
	return 0, io.EOF
}
func (t *testTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *testTransport) Resize(int, int) error       { return nil }
func (t *testTransport) Wait() (int, error)          { return 0, nil }
func (t *testTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

// testFactory opens a fresh fake transport per Session, so a test can prove
// that closing one Tab ends that Tab's Session and no other.
type testFactory struct {
	transports []*testTransport
	err        error
}

func (f *testFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	if f.err != nil {
		return nil, f.err
	}
	transport := newTestTransport()
	f.transports = append(f.transports, transport)
	return transport, nil
}

// testOpener is the consumer-owned SessionOpener port as the app implements
// it: it opens through the shared Session service. It counts opens so a test
// can prove hiding and showing the panel never starts a second shell.
type testOpener struct {
	service *appsession.Service
	opens   int
	err     error
}

func (o *testOpener) OpenLocal() (*appsession.Session, error) {
	o.opens++
	if o.err != nil {
		return nil, o.err
	}
	return o.service.OpenLocal(context.Background())
}

// recordingSurface records what the state asked of it.
type recordingSurface struct {
	closed      int
	fontFamily  string
	fontSize    float32
	cursorStyle domainpreferences.CursorStyle
	cursorBlink bool
	theme       domaintheme.TerminalTheme
	themeSet    bool
}

func (s *recordingSurface) View(c *ui.Context) ui.Element {
	return ui.Text(c, "quake-terminal").Fill()
}

func (s *recordingSurface) Close() { s.closed++ }

func (s *recordingSurface) SetTerminalFont(family string, size float32) {
	s.fontFamily, s.fontSize = family, size
}

func (s *recordingSurface) SetTerminalCursor(style domainpreferences.CursorStyle, blink bool) {
	s.cursorStyle, s.cursorBlink = style, blink
}

func (s *recordingSurface) SetTerminalTheme(preset domaintheme.TerminalTheme) {
	s.theme, s.themeSet = preset, true
}

func newTestState(t *testing.T) (*State, *testOpener, *testFactory) {
	t.Helper()
	factory := &testFactory{}
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	opener := &testOpener{service: service}
	state := NewState(opener, func(*appsession.Session) (Surface, error) {
		return &recordingSurface{}, nil
	})
	t.Cleanup(state.Shutdown)
	return state, opener, factory
}

// TestActivateOpensOneLocalShellAndReusesItAcrossShows pins the persistence
// contract that matters to the user: the first show creates a local Shell,
// and every later show keeps the same Session and Surface.
func TestActivateOpensOneLocalShellAndReusesItAcrossShows(t *testing.T) {
	state, opener, _ := newTestState(t)
	if state.TabCount() != 0 {
		t.Fatalf("a new Quake workspace has %d Tabs, want none", state.TabCount())
	}

	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if opener.opens != 1 {
		t.Fatalf("opens = %d, want one local Shell", opener.opens)
	}
	first, ok := state.Active()
	if !ok || first.Session == nil || first.Surface == nil {
		t.Fatal("Activate did not leave an active Tab with a Session and Surface")
	}
	if state.TabCount() != 1 {
		t.Fatalf("Tabs = %d, want 1", state.TabCount())
	}

	// Hiding and showing the Panel calls Activate again; it must not open
	// another shell or replace the surface.
	if err := state.Activate(); err != nil {
		t.Fatalf("second Activate: %v", err)
	}
	second, _ := state.Active()
	if opener.opens != 1 {
		t.Fatalf("opens = %d after a second show, want the original shell", opener.opens)
	}
	if second.Session != first.Session || second.Surface != first.Surface {
		t.Fatal("a second show replaced the Session or Surface instead of reusing it")
	}
}

// TestAddLocalTabAppendsAndActivates proves the + action opens a fresh local
// Shell beside the existing Tab and makes it active.
func TestAddLocalTabAppendsAndActivates(t *testing.T) {
	state, opener, _ := newTestState(t)
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	if opener.opens != 2 {
		t.Fatalf("opens = %d, want two local shells", opener.opens)
	}
	tabs := state.Tabs()
	if len(tabs) != 2 {
		t.Fatalf("Tabs = %d, want 2", len(tabs))
	}
	if tabs[0].Number != 1 || tabs[1].Number != 2 {
		t.Fatalf("Tab numbers = %d, %d, want 1, 2", tabs[0].Number, tabs[1].Number)
	}
	if state.ActiveIndex() != 1 || !tabs[1].Active || tabs[0].Active {
		t.Fatalf("active = %d, want the new second Tab", state.ActiveIndex())
	}
}

// TestSelectTabChangesOnlyTheQuakeWorkspace covers switching the active Tab.
func TestSelectTabChangesOnlyTheQuakeWorkspace(t *testing.T) {
	state, _, _ := newTestState(t)
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	second, _ := state.Active()

	state.SelectTab(0)
	if state.ActiveIndex() != 0 {
		t.Fatalf("active = %d, want 0", state.ActiveIndex())
	}
	tabs := state.Tabs()
	if !tabs[0].Active || tabs[1].Active {
		t.Fatalf("active flags = %v, %v, want only the first", tabs[0].Active, tabs[1].Active)
	}
	first, _ := state.Active()
	if first.Session == second.Session {
		t.Fatal("selecting the first Tab did not change the active Session")
	}

	// An out of range selection is a no-op.
	state.SelectTab(9)
	if state.ActiveIndex() != 0 {
		t.Fatalf("an invalid selection changed active to %d", state.ActiveIndex())
	}
}

// TestCloseTabEndsItsSessionAndSurfaceAndKeepsActiveValid proves a closed Tab
// releases its live terminal and the remaining Tab becomes active.
func TestCloseTabEndsItsSessionAndSurfaceAndKeepsActiveValid(t *testing.T) {
	state, _, factory := newTestState(t)
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	state.SelectTab(0)
	remainingTab, _ := state.Active()
	closingSurface := state.tabs[1].Surface.(*recordingSurface)
	if len(factory.transports) != 2 {
		t.Fatalf("transports = %d, want one per Tab", len(factory.transports))
	}
	remainingTransport, closingTransport := factory.transports[0], factory.transports[1]

	if !state.CloseTab(1) {
		t.Fatal("CloseTab reported no change for a valid index")
	}
	if state.TabCount() != 1 {
		t.Fatalf("Tabs = %d, want 1", state.TabCount())
	}
	if state.ActiveIndex() != 0 {
		t.Fatalf("active = %d, want the remaining Tab", state.ActiveIndex())
	}
	remaining, _ := state.Active()
	if remaining.Session != remainingTab.Session {
		t.Fatal("closing the inactive Tab changed the active Session")
	}

	// The closed Tab's Surface and Session end with it; the remaining Tab's
	// Session stays live.
	if closingSurface.closed != 1 {
		t.Fatalf("closed Surface close count = %d, want 1", closingSurface.closed)
	}
	waitTransportClosed(t, closingTransport)
	if transportClosed(remainingTransport) {
		t.Fatal("closing the inactive Tab ended the remaining Session")
	}
}

// TestClosingTheLastTabRequestsHide proves the workspace reports emptiness so
// the app can hide the Panel; a later Activate starts a new local Shell.
func TestClosingTheLastTabRequestsHide(t *testing.T) {
	state, opener, _ := newTestState(t)
	hides := 0
	state.empty = func() { hides++ }
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !state.CloseTab(0) {
		t.Fatal("CloseTab reported no change for the only Tab")
	}
	if hides != 1 {
		t.Fatalf("hide requests = %d, want 1 after closing the last Tab", hides)
	}
	if state.TabCount() != 0 {
		t.Fatalf("Tabs = %d, want 0", state.TabCount())
	}
	if _, ok := state.Active(); ok {
		t.Fatal("an empty workspace still reports an active Tab")
	}
	if state.CloseTab(0) {
		t.Fatal("closing an empty workspace reported a change")
	}
	if hides != 1 {
		t.Fatalf("hide requests = %d, want the no-op close not to hide again", hides)
	}

	// The next show recreates the local Shell.
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate after the last close: %v", err)
	}
	if opener.opens != 2 {
		t.Fatalf("opens = %d, want a new local Shell after the workspace emptied", opener.opens)
	}
}

// TestShutdownClosesEverySessionWithoutHiding covers app quit: Quake Tabs end
// their Sessions, but quitting never asks the Panel to hide.
func TestShutdownClosesEverySessionWithoutHiding(t *testing.T) {
	state, _, _ := newTestState(t)
	hides := 0
	state.empty = func() { hides++ }
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	first := state.tabs[0].Surface.(*recordingSurface)
	second := state.tabs[1].Surface.(*recordingSurface)

	state.Shutdown()
	if first.closed != 1 || second.closed != 1 {
		t.Fatalf("surfaces closed = %d, %d, want both", first.closed, second.closed)
	}
	if hides != 0 {
		t.Fatalf("shutdown requested %d hides, want none", hides)
	}
	if state.TabCount() != 0 {
		t.Fatalf("shutdown left %d Tabs", state.TabCount())
	}
}

// TestFreshStateStartsWithNoTabs proves Quake state is memory-only: a new
// State does not restore any previous Tab and opens a new local Shell.
func TestFreshStateStartsWithNoTabs(t *testing.T) {
	factory := &testFactory{}
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	opener := &testOpener{service: service}

	first := NewState(opener, func(*appsession.Session) (Surface, error) {
		return &recordingSurface{}, nil
	})
	if err := first.Activate(); err != nil {
		t.Fatalf("first Activate: %v", err)
	}
	old, _ := first.Active()
	first.Shutdown()

	second := NewState(opener, func(*appsession.Session) (Surface, error) {
		return &recordingSurface{}, nil
	})
	t.Cleanup(second.Shutdown)
	if second.TabCount() != 0 {
		t.Fatalf("a restarted Quake workspace restored %d Tabs, want none", second.TabCount())
	}
	if err := second.Activate(); err != nil {
		t.Fatalf("second Activate: %v", err)
	}
	fresh, _ := second.Active()
	if fresh.Session == old.Session {
		t.Fatal("a restarted workspace reused the previous Session")
	}
	if opener.opens != 2 {
		t.Fatalf("opens = %d, want one new shell per app run", opener.opens)
	}
	if len(factory.transports) != 2 || factory.transports[0] == factory.transports[1] {
		t.Fatalf("a restarted workspace did not get a fresh Session transport")
	}
}

// TestFailedOpenSurfacesAProblemAndAddsNoTab covers a local Shell that cannot
// start: the Panel must not show a broken Tab, and the view can explain why.
func TestFailedOpenSurfacesAProblemAndAddsNoTab(t *testing.T) {
	opener := &testOpener{err: errors.New("no local shell")}
	state := NewState(opener, func(*appsession.Session) (Surface, error) {
		return &recordingSurface{}, nil
	})
	t.Cleanup(state.Shutdown)

	if err := state.Activate(); err == nil {
		t.Fatal("Activate hid a failed local Shell")
	}
	if state.TabCount() != 0 {
		t.Fatalf("a failed open left %d Tabs", state.TabCount())
	}
	if state.Problem() == nil {
		t.Fatal("a failed open recorded no problem for the view")
	}
}

// TestSurfaceFailureClosesTheSession proves a surface that cannot build does
// not leak its Session.
func TestSurfaceFailureClosesTheSession(t *testing.T) {
	factory := &testFactory{}
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	opener := &testOpener{service: service}
	wantErr := errors.New("no terminal")
	state := NewState(opener, func(*appsession.Session) (Surface, error) {
		return nil, wantErr
	})
	t.Cleanup(state.Shutdown)

	if err := state.Activate(); !errors.Is(err, wantErr) {
		t.Fatalf("Activate error = %v, want %v", err, wantErr)
	}
	if state.TabCount() != 0 {
		t.Fatalf("a failed surface left %d Tabs", state.TabCount())
	}
	if len(factory.transports) != 1 {
		t.Fatalf("transports = %d, want the failed surface's Session", len(factory.transports))
	}
	waitTransportClosed(t, factory.transports[0])
}

func transportClosed(t *testTransport) bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}

// waitTransportClosed waits for a Session's command loop to end its transport,
// with a hard deadline instead of assuming the close is synchronous.
func waitTransportClosed(t *testing.T, transport *testTransport) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !transportClosed(transport) {
		if time.Now().After(deadline) {
			t.Fatal("a Session did not close its transport")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestTerminalSettingsReachEverySurface proves an open Quake terminal follows
// the live terminal scheme, font, and cursor without being recreated.
func TestTerminalSettingsReachEverySurface(t *testing.T) {
	state, _, _ := newTestState(t)
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	first := state.tabs[0].Surface.(*recordingSurface)
	second := state.tabs[1].Surface.(*recordingSurface)

	theme := domaintheme.TerminalTheme{Name: "Quake Test"}
	state.SetTerminalTheme(theme)
	state.SetTerminalFont("Test Mono", 15)
	state.SetTerminalCursor(domainpreferences.CursorBar, false)

	for index, surface := range []*recordingSurface{first, second} {
		if !surface.themeSet || surface.theme.Name != theme.Name {
			t.Fatalf("surface %d theme = %+v, want the live scheme", index, surface.theme)
		}
		if surface.fontFamily != "Test Mono" || surface.fontSize != 15 {
			t.Fatalf("surface %d font = %q %.1f, want Test Mono 15", index, surface.fontFamily, surface.fontSize)
		}
		if surface.cursorStyle != domainpreferences.CursorBar || surface.cursorBlink {
			t.Fatalf("surface %d cursor = %q blink=%v, want bar without blink", index, surface.cursorStyle, surface.cursorBlink)
		}
	}
}
