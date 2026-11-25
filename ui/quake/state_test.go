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
	"github.com/mslxl/natcatty/domain/model"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/motion"
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
	mu         sync.Mutex
	transports []*testTransport
	err        error
}

func (f *testFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	transport := newTestTransport()
	f.transports = append(f.transports, transport)
	return transport, nil
}

// transportsSnapshot copies the transports a test may inspect while Session
// goroutines still run.
func (f *testFactory) transportsSnapshot() []*testTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*testTransport(nil), f.transports...)
}

// testOpener is the consumer-owned SessionOpener port as the app implements
// it: it opens through the shared Session service. It counts opens so a test
// can prove hiding and showing the panel never starts a second shell, and it
// records the Host each remote Tab opened so the label and lifecycle remain
// observable.
type testOpener struct {
	mu        sync.Mutex
	service   *appsession.Service
	opens     int
	err       error
	hostOpens []domainvault.Host
	hostErr   error
}

func (o *testOpener) OpenLocal() (*appsession.Session, error) {
	o.mu.Lock()
	o.opens++
	err := o.err
	o.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return o.service.OpenLocal(context.Background())
}

func (o *testOpener) OpenHost(host domainvault.Host) (*appsession.Session, error) {
	o.mu.Lock()
	o.hostOpens = append(o.hostOpens, host)
	err := o.hostErr
	o.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return o.service.Open(context.Background(), appsession.StartSpec{
		Kind:     appsession.KindSSH,
		Label:    host.Label,
		HostID:   host.ID,
		Hostname: host.Hostname,
		Port:     host.Port,
		Username: host.Username,
	})
}

func (o *testOpener) opened() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opens
}

func (o *testOpener) hostsOpened() []domainvault.Host {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]domainvault.Host(nil), o.hostOpens...)
}

func (o *testOpener) setHostErr(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hostErr = err
}

// testHostSource is the consumer-owned HostSource port: a fixed list a test
// controls.
type testHostSource struct {
	hosts []domainvault.Host
}

func (s *testHostSource) Hosts() []domainvault.Host { return s.hosts }

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
	if opener.opened() != 1 {
		t.Fatalf("opens = %d, want one local Shell", opener.opened())
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
	if opener.opened() != 1 {
		t.Fatalf("opens = %d after a second show, want the original shell", opener.opened())
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
	if opener.opened() != 2 {
		t.Fatalf("opens = %d, want two local shells", opener.opened())
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
	transports := factory.transportsSnapshot()
	if len(transports) != 2 {
		t.Fatalf("transports = %d, want one per Tab", len(transports))
	}
	remainingTransport, closingTransport := transports[0], transports[1]

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
	if opener.opened() != 2 {
		t.Fatalf("opens = %d, want a new local Shell after the workspace emptied", opener.opened())
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

// TestMotionPullsTheLivePreference proves the Panel reads the motion setting
// on every call rather than caching the value it was constructed with.
func TestMotionPullsTheLivePreference(t *testing.T) {
	motion := domainpreferences.Defaults().Appearance.Motion
	motion.Enabled = false
	state := NewState(&testOpener{}, func(*appsession.Session) (Surface, error) {
		return &recordingSurface{}, nil
	}, WithMotion(func() domainpreferences.Motion { return motion }))
	t.Cleanup(state.Shutdown)
	if got := state.Motion(); got != motion {
		t.Fatalf("Motion = %+v, want %+v", got, motion)
	}

	motion.Enabled = true
	motion.Speed = domainpreferences.MotionFast
	if got := state.Motion(); got != motion {
		t.Fatalf("Motion cached its first value: %+v, want %+v", got, motion)
	}
}

// TestMotionDefaultsToOnWithoutAWiring keeps an unwired Quake state's motion
// on, so a Panel built without the app cannot silently disable animation.
func TestMotionDefaultsToOnWithoutAWiring(t *testing.T) {
	state := NewState(&testOpener{}, func(*appsession.Session) (Surface, error) {
		return &recordingSurface{}, nil
	})
	t.Cleanup(state.Shutdown)
	if got, want := state.Motion(), domainpreferences.Defaults().Appearance.Motion; got != want {
		t.Fatalf("unwired Motion = %+v, want %+v", got, want)
	}
}

// TestTabEventsRecordTheLatestTransition pins the Quake event stream the bar
// and the body consume: only Tab kinds exist, each carries the Tab identity
// and direction, and an active close names the replacement that takes its
// place rather than the Tab that just left.
func TestTabEventsRecordTheLatestTransition(t *testing.T) {
	state, _, _ := newTestState(t)
	if _, ok := state.MotionEvent(); ok {
		t.Fatal("a new Quake workspace recorded an event")
	}
	mustEvent := func(what string, kind motion.Kind, tabID model.TabID, target model.TabID, direction int) {
		t.Helper()
		event, ok := state.MotionEvent()
		if !ok {
			t.Fatalf("%s recorded no event", what)
		}
		if event.Kind != kind || event.TabID != tabID ||
			event.TargetTabID != target || event.Direction != direction {
			t.Fatalf("%s event = %+v, want kind %v Tab %q target %q direction %d",
				what, event, kind, tabID, target, direction)
		}
		if event.At.IsZero() {
			t.Fatalf("%s event has no timestamp", what)
		}
	}

	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	first, _ := state.MotionEvent()
	mustEvent("Activate", motion.TabOpen, quakeTabID(1), "", 0)
	if first.Seq != 1 {
		t.Fatalf("the first event Seq = %d, want 1", first.Seq)
	}

	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	second, _ := state.MotionEvent()
	mustEvent("AddLocalTab", motion.TabOpen, quakeTabID(2), "", 0)
	if second.Seq != first.Seq+1 {
		t.Fatalf("the second event Seq = %d, want %d", second.Seq, first.Seq+1)
	}

	state.SelectTab(0)
	mustEvent("SelectTab", motion.TabSwitch, quakeTabID(1), "", -1)

	// Closing the active first Tab of two moves to its right neighbour.
	if !state.CloseTab(0) {
		t.Fatal("CloseTab returned false")
	}
	mustEvent("CloseTab active first", motion.TabClose, quakeTabID(1), quakeTabID(2), 1)
	if tab, ok := state.Active(); !ok || tab.Number != 2 {
		t.Fatalf("active Tab after the close = %+v, %v; want number 2", tab, ok)
	}

	// Closing the sole Tab names no replacement: the direction is the
	// stream's neutral default and the body has no incoming Tab to switch to.
	if !state.CloseTab(0) {
		t.Fatal("CloseTab returned false")
	}
	mustEvent("CloseTab last", motion.TabClose, quakeTabID(2), "", 1)
	if state.TabCount() != 0 || state.ActiveIndex() != -1 {
		t.Fatalf("tabs = %d, active = %d; want an empty workspace", state.TabCount(), state.ActiveIndex())
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
	if opener.opened() != 2 {
		t.Fatalf("opens = %d, want one new shell per app run", opener.opened())
	}
	transports := factory.transportsSnapshot()
	if len(transports) != 2 || transports[0] == transports[1] {
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
	if len(factory.transportsSnapshot()) != 1 {
		t.Fatalf("transports = %d, want the failed surface's Session", len(factory.transportsSnapshot()))
	}
	waitTransportClosed(t, factory.transportsSnapshot()[0])
}

// flushUntil applies completed Host opens until condition holds. Dialing runs
// off the UI thread, so a test waits for the outcome instead of assuming
// AddHostTab finished synchronously.
func flushUntil(t *testing.T, state *State, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		state.Flush()
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
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

// hostsForTest returns two saved Hosts with distinct labels and endpoints.
func hostsForTest() []domainvault.Host {
	return []domainvault.Host{
		{ID: "host-alpha", Label: "Alpha", Hostname: "alpha.example", Port: 22, Username: "ops"},
		{ID: "host-beta", Label: "Beta", Hostname: "beta.example", Port: 2222, Username: "root"},
	}
}

// TestPickerListsVaultHostsAndCancelLeavesTabsUntouched covers the picker's
// contents and the cancel contract: opening and closing it changes neither the
// Tab list nor the active Tab.
func TestPickerListsVaultHostsAndCancelLeavesTabsUntouched(t *testing.T) {
	state, _, _ := newTestState(t)
	source := &testHostSource{hosts: hostsForTest()}
	state.hosts = source
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	before := state.Tabs()
	active := state.ActiveIndex()

	if got := state.Hosts(); len(got) != 2 || got[0].ID != "host-alpha" || got[1].ID != "host-beta" {
		t.Fatalf("Hosts() = %+v, want the source's two Hosts", got)
	}
	state.OpenPicker()
	if !state.PickerOpen() {
		t.Fatal("OpenPicker did not open the picker")
	}
	state.ClosePicker()
	if state.PickerOpen() {
		t.Fatal("ClosePicker left the picker open")
	}
	if state.ActiveIndex() != active || len(state.Tabs()) != len(before) {
		t.Fatalf("cancelling changed the workspace: active %d, %d Tabs", state.ActiveIndex(), state.TabCount())
	}
	for index := range before {
		if state.Tabs()[index].Label != before[index].Label || state.Tabs()[index].Active != before[index].Active {
			t.Fatalf("Tab %d changed after cancel: %+v, want %+v", index, state.Tabs()[index], before[index])
		}
	}
}

// TestAddHostTabOpensThroughTheSharedOpenerAndLabelsTheTab covers a Host
// selection: the opener receives the Host, the new Tab carries its label and
// identity, and it becomes active.
func TestAddHostTabOpensThroughTheSharedOpenerAndLabelsTheTab(t *testing.T) {
	state, opener, _ := newTestState(t)
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	host := hostsForTest()[1]

	if err := state.AddHostTab(host); err != nil {
		t.Fatalf("AddHostTab: %v", err)
	}
	// The connection runs off the UI thread and its Tab appears on the next
	// Flush.
	flushUntil(t, state, "the Host Tab", func() bool { return state.TabCount() == 2 })
	if len(opener.hostsOpened()) != 1 || opener.hostsOpened()[0].ID != host.ID {
		t.Fatalf("opener Hosts = %+v, want the selected Host", opener.hostsOpened())
	}
	if state.TabCount() != 2 {
		t.Fatalf("Tabs = %d, want the local Shell and the Host", state.TabCount())
	}
	tabs := state.Tabs()
	if tabs[1].Label != "Beta" || tabs[1].HostID != host.ID {
		t.Fatalf("Host Tab = %+v, want label Beta and the Host identity", tabs[1])
	}
	if state.ActiveIndex() != 1 || !tabs[1].Active {
		t.Fatalf("active = %d, want the new Host Tab", state.ActiveIndex())
	}
	tabsAfter := state.Tabs()
	if tabsAfter[0].Label != "" || tabsAfter[0].HostID != "" {
		t.Fatalf("the local Tab was labelled as a Host: %+v", tabsAfter[0])
	}
}

// TestRemoteTabsSelectAndClose proves remote Tabs switch like local ones, end
// their own Session and Surface on close, and hide the Panel when the last one
// closes.
func TestRemoteTabsSelectAndClose(t *testing.T) {
	state, opener, factory := newTestState(t)
	hides := 0
	state.empty = func() { hides++ }
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	hosts := hostsForTest()
	if err := state.AddHostTab(hosts[0]); err != nil {
		t.Fatalf("first AddHostTab: %v", err)
	}
	if err := state.AddHostTab(hosts[1]); err != nil {
		t.Fatalf("second AddHostTab: %v", err)
	}
	flushUntil(t, state, "both Host Tabs", func() bool { return state.TabCount() == 3 })
	hostsOpened, transports := opener.hostsOpened(), factory.transportsSnapshot()
	if len(hostsOpened) != 2 || len(transports) != 3 {
		t.Fatalf("Host opens = %d, transports = %d, want two Hosts and three Sessions",
			len(hostsOpened), len(transports))
	}
	// Host opens complete off the UI thread, so the two Tabs may append in
	// either completion order. Find Alpha by label rather than assuming it.
	alphaIndex, betaIndex := -1, -1
	for _, tab := range state.Tabs() {
		switch tab.Label {
		case "Alpha":
			alphaIndex = tab.Index
		case "Beta":
			betaIndex = tab.Index
		}
	}
	if alphaIndex < 0 || betaIndex < 0 {
		t.Fatalf("the Host Tabs are missing: %+v", state.Tabs())
	}
	alphaSurface := state.tabs[alphaIndex].Surface.(*recordingSurface)

	// Switching to Alpha changes only the active index.
	state.SelectTab(alphaIndex)
	if state.ActiveIndex() != alphaIndex || state.Tabs()[alphaIndex].Label != "Alpha" {
		t.Fatalf("selecting the remote Tab left active %d", state.ActiveIndex())
	}

	// Closing it ends its Session and Surface, not the other remote Tab's.
	if !state.CloseTab(alphaIndex) {
		t.Fatal("CloseTab reported no change for a remote Tab")
	}
	if alphaSurface.closed != 1 {
		t.Fatalf("closed remote Surface closes = %d, want 1", alphaSurface.closed)
	}
	// Exactly one Host transport ends; the other Host still owns a Session.
	// Closing a Session ends its transport asynchronously.
	hostTransports := factory.transportsSnapshot()[1:]
	deadline := time.Now().Add(2 * time.Second)
	for {
		closed := 0
		for _, transport := range hostTransports {
			if transportClosed(transport) {
				closed++
			}
		}
		if closed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("closing Alpha closed %d Host transports, want 1", closed)
		}
		time.Sleep(time.Millisecond)
	}
	if state.TabCount() != 2 {
		t.Fatalf("closing one remote Tab left %d Tabs: %+v", state.TabCount(), state.Tabs())
	}
	if remaining := state.Tabs()[1]; remaining.Label != "Beta" {
		t.Fatalf("the remaining remote Tab = %+v, want Beta", remaining)
	}

	// Closing the remaining remote Tab and then the local one hides the Panel.
	if !state.CloseTab(1) {
		t.Fatal("CloseTab reported no change for the last remote Tab")
	}
	if !state.CloseTab(0) {
		t.Fatal("CloseTab reported no change for the local Tab")
	}
	if hides != 1 {
		t.Fatalf("hide requests = %d, want the last close to hide the Panel", hides)
	}
}

// TestDismissOverlayClosesThePickerFirst proves the close-request order: the
// first request closes an open picker and reports consumption, the next one is
// the Panel's own.
func TestDismissOverlayClosesThePickerFirst(t *testing.T) {
	state, _, _ := newTestState(t)
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	state.OpenPicker()

	if !state.DismissOverlay() {
		t.Fatal("the first close request did not consume an open picker")
	}
	if state.PickerOpen() {
		t.Fatal("the first close request left the picker open")
	}
	if state.TabCount() != 1 {
		t.Fatalf("dismissing the picker changed the Tabs: %d", state.TabCount())
	}
	if state.DismissOverlay() {
		t.Fatal("the second close request was consumed with no overlay open")
	}
}

// TestFailedHostOpenSurfacesAProblemAndAddsNoTab covers a Host that cannot
// connect: the Tab list stays unchanged and the view can explain the failure.
// The failure arrives asynchronously, so it is applied by Flush.
func TestFailedHostOpenSurfacesAProblemAndAddsNoTab(t *testing.T) {
	state, opener, _ := newTestState(t)
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	opener.setHostErr(errors.New("no route to Host"))
	if err := state.AddHostTab(hostsForTest()[0]); err != nil {
		t.Fatalf("AddHostTab reported a configuration error: %v", err)
	}
	flushUntil(t, state, "the failed Host open", func() bool { return state.Problem() != nil })
	if state.TabCount() != 1 {
		t.Fatalf("a failed Host open left %d Tabs, want the original local Shell", state.TabCount())
	}
}

// TestFailedHostSurfaceClosesTheSession covers the async Host path's surface
// failure: the connected Session is closed instead of leaking, and no broken
// Tab is appended.
func TestFailedHostSurfaceClosesTheSession(t *testing.T) {
	factory := &testFactory{}
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	opener := &testOpener{service: service}
	wantErr := errors.New("no terminal")
	state := NewState(opener, func(*appsession.Session) (Surface, error) {
		return nil, wantErr
	})
	t.Cleanup(state.Shutdown)

	if err := state.AddHostTab(hostsForTest()[0]); err != nil {
		t.Fatalf("AddHostTab: %v", err)
	}
	flushUntil(t, state, "the surface failure", func() bool { return state.Problem() != nil })
	if !errors.Is(state.Problem(), wantErr) {
		t.Fatalf("Problem() = %v, want %v", state.Problem(), wantErr)
	}
	if state.TabCount() != 0 {
		t.Fatalf("a failed Host surface left %d Tabs", state.TabCount())
	}
	waitTransportClosed(t, factory.transportsSnapshot()[0])
}

// TestTabAtAndTabForSessionLocateTheSameTabs proves the integration seam the
// tray and notification routing use: index lookup exposes the live Tab, and
// Session lookup returns the TabInfo for the Tab that owns it.
func TestTabAtAndTabForSessionLocateTheSameTabs(t *testing.T) {
	state, _, _ := newTestState(t)
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := state.AddHostTab(hostsForTest()[0]); err != nil {
		t.Fatalf("AddHostTab: %v", err)
	}
	flushUntil(t, state, "the Host Tab", func() bool { return state.TabCount() == 2 })
	first, ok := state.TabAt(0)
	if !ok || first.Session == nil || first.Surface == nil {
		t.Fatal("TabAt(0) did not return the first live Tab")
	}
	info, ok := state.TabForSession(first.Session.ID())
	if !ok {
		t.Fatal("TabForSession did not find the first Tab")
	}
	if info.Index != 0 || info.Number != first.Number || info.Label != first.Label {
		t.Fatalf("TabForSession = %+v, want the first Tab", info)
	}
	if _, ok := state.TabForSession("missing"); ok {
		t.Fatal("TabForSession found a Tab for an unknown Session")
	}
	if _, ok := state.TabAt(-1); ok {
		t.Fatal("TabAt(-1) returned a Tab")
	}
	if _, ok := state.TabAt(state.TabCount()); ok {
		t.Fatal("TabAt past the end returned a Tab")
	}
}

// TestFocusSessionSelectsOnlyTheOwningTab covers a notification click for a
// Quake Session: the Tab that owns it becomes active and no other Tab moves.
func TestFocusSessionSelectsOnlyTheOwningTab(t *testing.T) {
	state, _, _ := newTestState(t)
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	first, _ := state.Active()
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	if state.ActiveIndex() != 1 {
		t.Fatalf("active = %d, want the new second Tab", state.ActiveIndex())
	}
	if !state.FocusSession(first.Session.ID()) {
		t.Fatal("FocusSession did not find the first Tab's Session")
	}
	if state.ActiveIndex() != 0 {
		t.Fatalf("active = %d after FocusSession, want the first Tab", state.ActiveIndex())
	}
	if state.TabCount() != 2 {
		t.Fatalf("FocusSession changed the Tab list: %d", state.TabCount())
	}
	if state.FocusSession("missing") {
		t.Fatal("FocusSession found an unknown Session")
	}
}

// TestSessionCountCountsOnlyLiveTabs covers the quit count: a running Quake
// Session is counted, and Shutdown (which closes every Tab) drops the count to
// zero.
func TestSessionCountCountsOnlyLiveTabs(t *testing.T) {
	state, _, _ := newTestState(t)
	if state.SessionCount() != 0 {
		t.Fatalf("a new workspace counts %d Sessions", state.SessionCount())
	}
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := state.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	if got := state.SessionCount(); got != 2 {
		t.Fatalf("SessionCount = %d, want 2 running local Shells", got)
	}
	state.SelectTab(0)
	if !state.CloseTab(0) {
		t.Fatal("CloseTab reported no change for the first Tab")
	}
	if got := state.SessionCount(); got != 1 {
		t.Fatalf("SessionCount = %d after closing a Tab, want 1", got)
	}
	state.Shutdown()
	if state.SessionCount() != 0 {
		t.Fatalf("Shutdown left %d counted Sessions", state.SessionCount())
	}
}
