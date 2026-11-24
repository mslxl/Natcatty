package state

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// fakeTransport keeps a Session alive until the test closes it.
type fakeTransport struct {
	output    chan []byte
	closed    chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	input   []byte
	resizes [][2]int
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{output: make(chan []byte), closed: make(chan struct{})}
}

func (f *fakeTransport) Read(p []byte) (int, error) {
	select {
	case data := <-f.output:
		return copy(p, data), nil
	case <-f.closed:
		return 0, io.EOF
	}
}

func (f *fakeTransport) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.input = append(f.input, p...)
	return len(p), nil
}

func (f *fakeTransport) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

func (f *fakeTransport) Wait() (int, error) { return 0, nil }

func (f *fakeTransport) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

// fakeFactory records the start specs a graph dials. A reconnect goroutine can
// call Open while a test inspects the record, so every field is guarded.
type fakeFactory struct {
	mu        sync.Mutex
	transport *fakeTransport
	err       error
	spec      appsession.StartSpec
	// opens counts the dials, so a test can tell a reconnect from the first
	// connection.
	opens int
}

func (f *fakeFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spec = spec
	f.opens++
	if f.err != nil {
		return nil, f.err
	}
	return f.transport, nil
}

// lastSpec returns the most recent dial's spec as a race-free snapshot.
func (f *fakeFactory) lastSpec() appsession.StartSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spec
}

// openCount returns the number of dials.
func (f *fakeFactory) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

// setError changes the next dial's failure.
func (f *fakeFactory) setError(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

// fakeVNCTransport carries a pixel display, as a VNC transport does. The
// Session rejects a transport without one, so the connecting and attached VNC
// Pane tests need it.
type fakeVNCTransport struct {
	*fakeTransport
	display *fakeDisplay
}

func (f *fakeVNCTransport) Display() appsession.DisplaySession { return f.display }

func (f *fakeVNCTransport) Close() error {
	_ = f.display.Close()
	return f.fakeTransport.Close()
}

// fakeDisplay is an inert pixel stream for tests.
type fakeDisplay struct {
	events    chan appsession.DisplayEvent
	closeOnce sync.Once
}

func newFakeDisplay() *fakeDisplay {
	return &fakeDisplay{events: make(chan appsession.DisplayEvent)}
}

func (d *fakeDisplay) Events() <-chan appsession.DisplayEvent { return d.events }
func (d *fakeDisplay) SendKey(uint32, bool) error             { return nil }
func (d *fakeDisplay) SendPhysicalKey(uint32, uint32, bool) error {
	return nil
}
func (d *fakeDisplay) SendPointer(int, int, uint8) error                { return nil }
func (d *fakeDisplay) SendCutText(string) error                         { return nil }
func (d *fakeDisplay) RequestUpdate(bool) error                         { return nil }
func (d *fakeDisplay) Screens() []appsession.DisplayScreen              { return nil }
func (d *fakeDisplay) SetScreenCount(int) error                         { return nil }
func (d *fakeDisplay) SetScreenLayout([]appsession.DisplayScreen) error { return nil }

func (d *fakeDisplay) Close() error {
	d.closeOnce.Do(func() { close(d.events) })
	return nil
}

// fakeDisplayFactory opens VNC Sessions with a pixel display and every other
// Kind with an ordinary transport.
type fakeDisplayFactory struct {
	mu   sync.Mutex
	spec appsession.StartSpec
}

func (f *fakeDisplayFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.mu.Lock()
	f.spec = spec
	f.mu.Unlock()
	if spec.Kind == appsession.KindVNC {
		return &fakeVNCTransport{fakeTransport: newFakeTransport(), display: newFakeDisplay()}, nil
	}
	return newFakeTransport(), nil
}

// fakeSurface stands in for the MyGo terminal the UI owns.
type fakeSurface struct {
	closed        bool
	browserOpened bool
	browserOpen   bool
	fontFamily    string
	fontSize      float32
	theme         domaintheme.TerminalTheme
	themed        bool
	inserted      []string
	insertErr     error
	invalidations int
	releases      int
	keyboardGrab  bool
	hibernated    bool
}

func (f *fakeSurface) View(c *ui.Context) ui.Element {
	return ui.Text(c, "terminal-surface").Fill()
}

func (f *fakeSurface) Close() { f.closed = true }

// ReleaseInput records that the shell asked this Surface to let go of guest
// input before it stops rendering.
func (f *fakeSurface) ReleaseInput() { f.releases++ }

// OpenBrowser makes the fake a BrowserOpener so a shell test can prove the
// Vault's browser action reaches the Session surface.
func (f *fakeSurface) OpenBrowser() error {
	f.browserOpened = true
	return nil
}

// BrowserOpen makes the fake a BrowserShown, so a shell test can prove the
// placeholder decision reads the focused Pane's browser state.
func (f *fakeSurface) BrowserOpen() bool { return f.browserOpen }

func (f *fakeSurface) SetTerminalFont(family string, size float32) {
	f.fontFamily, f.fontSize = family, size
}

func (f *fakeSurface) SetTerminalTheme(preset domaintheme.TerminalTheme) {
	f.theme, f.themed = preset, true
}

func (f *fakeSurface) InsertText(text string) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, text)
	return nil
}

func (f *fakeSurface) InvalidateSuggestions() { f.invalidations++ }

// KeyboardGrab makes the fake a KeyboardGrabber so the app's accelerator
// predicate has a focused pane to ask.
func (f *fakeSurface) KeyboardGrab() bool { return f.keyboardGrab }

// Hibernated makes the fake a Surface whose live view can be released while
// its Session stays connected, like the VNC viewer.
func (f *fakeSurface) Hibernated() bool { return f.hibernated }

// testGraph wires a shell State to fake Sessions and surfaces.
type testGraph struct {
	state    *State
	surface  *fakeSurface
	sessions []*appsession.Session
	factory  *fakeFactory
	changes  atomic.Int32
}

func newTestGraph(t *testing.T, opts ...Option) *testGraph {
	t.Helper()
	graph := &testGraph{}
	graph.factory = &fakeFactory{transport: newFakeTransport()}
	service := appsession.NewService(graph.factory)
	t.Cleanup(service.CloseAll)
	graph.surface = &fakeSurface{}
	options := []Option{
		WithRedrawRequest(func() { graph.changes.Add(1) }),
		WithSessions(service, func(session *appsession.Session) (Surface, error) {
			graph.sessions = append(graph.sessions, session)
			return graph.surface, nil
		}),
	}
	options = append(options, opts...)
	graph.state = NewState(options...)
	return graph
}

func TestStateStartsWithoutTabs(t *testing.T) {
	graph := newTestGraph(t)

	if graph.state.TabCount() != 0 || graph.state.ActiveTab() != -1 {
		t.Fatalf("tabs = %d, active = %d, want 0 and -1", graph.state.TabCount(), graph.state.ActiveTab())
	}
}

func TestNewTabOpensALocalShellSessionAndSurface(t *testing.T) {
	graph := newTestGraph(t)

	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if graph.state.TabCount() != 1 || graph.state.ActiveTab() != 0 {
		t.Fatalf("tabs = %d, active = %d, want 1 and 0", graph.state.TabCount(), graph.state.ActiveTab())
	}
	if got := graph.state.ActiveSurface(); got != graph.surface {
		t.Fatalf("active surface = %v, want the created surface", got)
	}
	if len(graph.sessions) != 1 {
		t.Fatalf("created sessions = %d, want 1", len(graph.sessions))
	}

	snapshot, ok := graph.state.ActiveSnapshot()
	if !ok {
		t.Fatal("the active tab has no Session snapshot")
	}
	if snapshot.Kind != appsession.KindLocal {
		t.Errorf("session kind = %q, want %q", snapshot.Kind, appsession.KindLocal)
	}
	if snapshot.Status != appsession.StatusRunning {
		t.Errorf("session status = %q, want %q", snapshot.Status, appsession.StatusRunning)
	}
}

// TestNewTabStartsTheConfiguredLocalShell proves the shell passes the
// preference's argv straight to the Session launcher. The fake factory is the
// launcher here, so no real shell is started.
func TestNewTabStartsTheConfiguredLocalShell(t *testing.T) {
	command := []string{"/opt/fake-shell", "-l", "arg with space"}
	graph := newTestGraph(t, WithLocalShell(func() ([]string, bool) {
		return command, false
	}))

	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if !slices.Equal(graph.factory.lastSpec().Command, command) {
		t.Fatalf("local Session command = %q, want %q", graph.factory.lastSpec().Command, command)
	}
	if graph.factory.lastSpec().Kind != appsession.KindLocal {
		t.Fatalf("Session kind = %q, want local", graph.factory.lastSpec().Kind)
	}
	if got := graph.state.DesktopNotice(); got != DesktopNoticeNone {
		t.Fatalf("desktop notice = %q, want none", got)
	}
}

// TestNewTabFallsBackWhenTheConfiguredShellIsMissing proves an unusable path
// never blocks a Tab: the Session opens the OS login shell and the shell shows
// the warning.
func TestNewTabFallsBackWhenTheConfiguredShellIsMissing(t *testing.T) {
	graph := newTestGraph(t, WithLocalShell(func() ([]string, bool) {
		return nil, true
	}))

	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if len(graph.factory.lastSpec().Command) != 0 {
		t.Fatalf("local Session command = %q, want the OS login shell", graph.factory.lastSpec().Command)
	}
	if got := graph.state.DesktopNotice(); got != DesktopNoticeLocalShellFallback {
		t.Fatalf("desktop notice = %q, want the local-shell fallback", got)
	}
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d, want one", graph.state.TabCount())
	}
}

// TestNewTabClearsTheShellFallbackAfterTheSettingIsFixed proves the warning
// does not outlive the bad path.
func TestNewTabClearsTheShellFallbackAfterTheSettingIsFixed(t *testing.T) {
	missing := true
	graph := newTestGraph(t, WithLocalShell(func() ([]string, bool) {
		if missing {
			return nil, true
		}
		return []string{"/opt/fake-shell"}, false
	}))

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if graph.state.DesktopNotice() != DesktopNoticeLocalShellFallback {
		t.Fatal("the fallback warning was not shown")
	}
	missing = false
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if got := graph.state.DesktopNotice(); got != DesktopNoticeNone {
		t.Fatalf("desktop notice = %q after the fix, want none", got)
	}
	if !slices.Equal(graph.factory.lastSpec().Command, []string{"/opt/fake-shell"}) {
		t.Fatalf("command = %q, want the configured shell", graph.factory.lastSpec().Command)
	}
}

// TestNewTabWithoutACustomShellAsksTheOS proves clearing the preference
// restores the platform login shell.
func TestNewTabWithoutACustomShellAsksTheOS(t *testing.T) {
	graph := newTestGraph(t, WithLocalShell(func() ([]string, bool) { return nil, false }))
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if len(graph.factory.lastSpec().Command) != 0 {
		t.Fatalf("local Session command = %q, want the OS login shell", graph.factory.lastSpec().Command)
	}
}

func TestNewTabWithShellStartsTheNamedProgram(t *testing.T) {
	graph := newTestGraph(t)

	if err := graph.state.NewTabWithShell("pwsh.exe"); err != nil {
		t.Fatalf("NewTabWithShell: %v", err)
	}
	if graph.state.TabCount() != 1 || graph.state.ActiveTab() != 0 {
		t.Fatalf("tabs = %d, active = %d, want 1 and 0", graph.state.TabCount(), graph.state.ActiveTab())
	}
	if spec := graph.factory.lastSpec(); spec.Kind != appsession.KindLocal ||
		len(spec.Command) != 1 || spec.Command[0] != "pwsh.exe" {
		t.Fatalf("start spec = %+v, want a local Session running pwsh.exe", spec)
	}
}

func TestNewTabWithShellFailureAppendsNoTab(t *testing.T) {
	graph := newTestGraph(t)
	graph.factory.setError(errors.New("no such shell"))

	if err := graph.state.NewTabWithShell("missing.exe"); err == nil {
		t.Fatal("a failed shell start reported no error")
	}
	if graph.state.TabCount() != 0 {
		t.Fatalf("a failed shell start left %d tabs", graph.state.TabCount())
	}
}

func TestNewTabAppendsAndClosesActiveTab(t *testing.T) {
	graph := newTestGraph(t)

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if graph.state.TabCount() != 2 || graph.state.ActiveTab() != 1 {
		t.Fatalf("tabs = %d, active = %d, want 2 and 1", graph.state.TabCount(), graph.state.ActiveTab())
	}
	if got := graph.state.TabNumbers(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("tab numbers = %v, want [1 2]", got)
	}

	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false with an active tab")
	}
	if graph.state.TabCount() != 1 || graph.state.ActiveTab() != 0 {
		t.Fatalf("tabs = %d, active = %d, want 1 and 0", graph.state.TabCount(), graph.state.ActiveTab())
	}

	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false with one tab left")
	}
	if graph.state.TabCount() != 0 || graph.state.ActiveTab() != -1 {
		t.Fatalf("tabs = %d, active = %d, want 0 and -1", graph.state.TabCount(), graph.state.ActiveTab())
	}
	if graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned true with no tabs")
	}
}

func TestCloseActiveTabClosesItsSessionAndSurface(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	session := graph.sessions[0]

	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false")
	}
	if !graph.surface.closed {
		t.Error("the tab's surface was not closed")
	}
	select {
	case <-session.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the session did not close")
	}
	if got := session.Snapshot().Status; got != appsession.StatusClosed {
		t.Errorf("session status = %q, want %q", got, appsession.StatusClosed)
	}
}

func TestTabChangesRequestARedraw(t *testing.T) {
	graph := newTestGraph(t)

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if got := graph.changes.Load(); got != 1 {
		t.Fatalf("change requests after NewTab = %d, want 1", got)
	}
	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false")
	}
	if got := graph.changes.Load(); got != 2 {
		t.Fatalf("change requests after CloseActiveTab = %d, want 2", got)
	}
}

func TestCloseActiveTabSelectsPreviousNeighbour(t *testing.T) {
	graph := newTestGraph(t)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	graph.state.SelectTab(2)

	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false")
	}
	if graph.state.TabCount() != 2 || graph.state.ActiveTab() != 1 {
		t.Fatalf("tabs = %d, active = %d, want 2 and 1", graph.state.TabCount(), graph.state.ActiveTab())
	}
}

func TestRequestLocaleReportsTheRequestedTag(t *testing.T) {
	var requested []string
	state := NewState(WithLocaleRequest(func(tag string) {
		requested = append(requested, tag)
	}))

	state.RequestLocale("es")
	state.RequestLocale("zh-TW")

	if len(requested) != 2 || requested[0] != "es" || requested[1] != "zh-TW" {
		t.Fatalf("requested locales = %v, want [es zh-TW]", requested)
	}
}

func TestRequestSettingsReportsTheRequest(t *testing.T) {
	requests := 0
	state := NewState(WithSettingsRequest(func() { requests++ }))

	state.RequestSettings()
	state.RequestSettings()

	if requests != 2 {
		t.Fatalf("settings requests = %d, want 2", requests)
	}
}

func TestVaultIsTheDefaultSurface(t *testing.T) {
	state := NewState()
	if !state.VaultVisible() {
		t.Fatal("the Vault is not visible in a new shell")
	}
}

func TestNewTabHidesTheVaultAndShowVaultReturns(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if graph.state.VaultVisible() {
		t.Fatal("the Vault is still visible after opening a Tab")
	}

	graph.state.ShowVault()
	if !graph.state.VaultVisible() {
		t.Fatal("ShowVault did not show the Vault")
	}

	graph.state.SelectTab(0)
	if graph.state.VaultVisible() {
		t.Fatal("SelectTab did not show the Tab")
	}
}

func TestClosingTheLastTabShowsTheVault(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false")
	}
	if !graph.state.VaultVisible() {
		t.Fatal("the Vault is not visible after closing the last Tab")
	}
}

// TestOpenHostFailsClosedForRemovedProtocolsAndUnknownTransports proves a
// record written by an older build, or hand-edited, is never reinterpreted as
// plain SSH: no Session is opened and the shell reports the unsupported Host.
func TestOpenHostFailsClosedForRemovedProtocolsAndUnknownTransports(t *testing.T) {
	hosts := []domainvault.Host{
		{ID: "old-local", Label: "local", Hostname: "ignored", Protocol: "local"},
		{ID: "old-mosh", Label: "mosh", Hostname: "example.com", Port: 22, Protocol: "mosh"},
		{ID: "old-et", Label: "et", Hostname: "example.com", Port: 22, Protocol: "et"},
		{ID: "bad-transport", Label: "bad", Hostname: "example.com", Port: 22,
			Protocol: domainvault.ProtocolSSH, Transport: "quic"},
	}
	for _, host := range hosts {
		t.Run(string(host.ID), func(t *testing.T) {
			graph := newTestGraph(t)
			err := graph.state.OpenHost(host)
			if !errors.Is(err, ErrUnsupportedHost) {
				t.Fatalf("OpenHost error = %v, want ErrUnsupportedHost", err)
			}
			if pending := graph.state.PendingConnections(); len(pending) != 0 {
				t.Fatalf("pending connections = %q, want none", pending)
			}
			if graph.state.ProblemError() == nil {
				t.Fatal("the shell did not record the unsupported Host")
			}
			graph.state.Flush()
			if graph.state.TabCount() != 0 {
				t.Fatalf("tabs = %d, want 0", graph.state.TabCount())
			}
			if len(graph.sessions) != 0 {
				t.Fatalf("sessions = %d, want 0", len(graph.sessions))
			}
		})
	}
}

func TestOpenHostAddsAnSSHTabOnTheNextFlush(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{
		ID: "host-1", Label: "prod", Hostname: "example.com", Port: 2222,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}

	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	// The Tab exists while the dial runs, so the user can open it and see the
	// connecting state instead of waiting on an indicator they cannot click.
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d before Flush, want the connecting Tab", graph.state.TabCount())
	}
	if pending := graph.state.PendingConnections(); len(pending) != 0 {
		t.Fatalf("pending connections = %q, want none in the strip", pending)
	}
	if !graph.state.TabPending(0) {
		t.Fatal("the Tab does not report that it is still connecting")
	}
	paneID, ok := graph.state.FocusedPaneID(0)
	if !ok {
		t.Fatal("the connecting Tab has no focused Pane")
	}
	if info, ok := graph.state.PaneInfoAt(0, paneID); !ok || !info.Pending ||
		info.Target != "root@example.com:2222" {
		t.Fatalf("PaneInfo = %+v, %v; want a pending Pane reaching the typed endpoint", info, ok)
	}

	waitFor(t, "the SSH Session", func() bool {
		graph.state.Flush()
		return !graph.state.TabPending(0)
	})
	if got := graph.factory.lastSpec(); got.Kind != appsession.KindSSH || got.HostID != "host-1" ||
		got.Hostname != "example.com" || got.Port != 2222 || got.Username != "root" {
		t.Fatalf("factory spec = %+v, want the Host's SSH target", got)
	}
	if graph.factory.lastSpec().Reconnect == nil {
		t.Fatal("SSH Sessions do not carry the reconnect policy")
	}
	if graph.factory.lastSpec().X11Forwarding {
		t.Fatal("X11 forwarding was requested without a Host preference")
	}
	if pending := graph.state.PendingConnections(); len(pending) != 0 {
		t.Fatalf("pending connections = %q after Flush, want none", pending)
	}
	label, number, ok := graph.state.TabLabel(0)
	if !ok || label != "prod" || number != 1 {
		t.Fatalf("tab label = %q, number = %d, %v; want prod and 1", label, number, ok)
	}
	if graph.state.VaultVisible() {
		t.Fatal("the Vault is still visible after an SSH Tab opened")
	}
	snapshot, ok := graph.state.ActiveSnapshot()
	if !ok || snapshot.Kind != appsession.KindSSH || snapshot.Status != appsession.StatusRunning {
		t.Fatalf("snapshot = %+v, %v; want a running SSH Session", snapshot, ok)
	}
	if graph.state.Problem() != "" {
		t.Fatalf("problem = %q after a successful connection", graph.state.Problem())
	}
}

// TestOpenHostShowsAClickableVNCTabWhileConnecting pins the VNC contract the
// pixel viewer needs: the Tab exists and can be selected before the desktop
// handshake finishes, and its Pane names the endpoint it is reaching.
func TestOpenHostShowsAClickableVNCTabWhileConnecting(t *testing.T) {
	service := appsession.NewService(&fakeDisplayFactory{})
	t.Cleanup(service.CloseAll)
	st := NewState(WithSessions(service, func(*appsession.Session) (Surface, error) {
		return &fakeSurface{}, nil
	}))
	if err := st.OpenHost(domainvault.Host{
		ID: "vnc-desk", Label: "desk", Hostname: "desk.local", Port: 5901,
		Username: "viewer", Protocol: domainvault.ProtocolVNC,
	}); err != nil {
		t.Fatalf("OpenHost VNC: %v", err)
	}
	if err := st.OpenHost(domainvault.Host{
		ID: "host-ssh", Label: "prod", Hostname: "prod.local", Port: 22,
		Protocol: domainvault.ProtocolSSH,
	}); err != nil {
		t.Fatalf("OpenHost SSH: %v", err)
	}
	if st.TabCount() != 2 || st.ActiveTab() != 1 {
		t.Fatalf("tabs = %d, active = %d; want both connecting Tabs with the last active",
			st.TabCount(), st.ActiveTab())
	}

	// Both dials are still in flight until Flush, so this selection happens in
	// the connecting window.
	st.SelectTab(0)
	if st.ActiveTab() != 0 {
		t.Fatalf("active tab = %d, want the connecting VNC Tab selectable", st.ActiveTab())
	}
	paneID := mustFocusedPane(t, st, 0)
	info, ok := st.PaneInfoAt(0, paneID)
	if !ok || !info.Pending {
		t.Fatalf("PaneInfo = %+v, %v; want the VNC Pane still connecting", info, ok)
	}
	if info.ByteStream {
		t.Fatal("the connecting VNC Pane reported a byte stream")
	}
	if info.Target != "viewer"+"@"+"desk.local:5901" {
		t.Fatalf("Pane target = %q, want the VNC endpoint", info.Target)
	}
	if !st.TabPending(0) {
		t.Fatal("the VNC Tab does not report that it is connecting")
	}

	waitFor(t, "the VNC Pane", func() bool {
		st.Flush()
		info, ok := st.PaneInfoAt(0, paneID)
		return ok && !info.Pending && info.Surface != nil
	})
	if snapshot, ok := st.ActiveSnapshot(); !ok || snapshot.Kind != appsession.KindVNC {
		t.Fatalf("snapshot = %+v, %v; want the VNC Session", snapshot, ok)
	}
}

func TestOpenHostOpensATelnetHostWithTheTelnetTransport(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{
		ID: "host-telnet", Label: "console", Hostname: "console.local",
		Port: 23, Username: "admin", Protocol: domainvault.ProtocolTelnet,
		// A stray SSH-only preference must not reach a Telnet transport.
		X11Forwarding: true, X11WarningAcknowledged: true,
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the Telnet tab", func() bool {
		graph.state.Flush()
		return tabAttached(graph.state, 0)
	})

	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindTelnet {
		t.Fatalf("factory kind = %q, want %q", spec.Kind, appsession.KindTelnet)
	}
	if spec.Telnet == nil || spec.Telnet.Hostname != "console.local" || spec.Telnet.Port != 23 {
		t.Fatalf("Telnet config = %+v, want console.local:23", spec.Telnet)
	}
	if spec.X11Forwarding {
		t.Fatal("X11 forwarding reached a Telnet transport")
	}
	if spec.Reconnect == nil {
		t.Fatal("a Telnet Session has no manual reconnect policy")
	}
	if spec.Reconnect.ShouldRetry != nil && spec.Reconnect.ShouldRetry(appsession.End{Reason: appsession.EndReasonError}) {
		t.Fatal("a dropped Telnet transport would reconnect automatically")
	}
	snapshot, ok := graph.state.ActiveSnapshot()
	if !ok || snapshot.Kind != appsession.KindTelnet || snapshot.Status != appsession.StatusRunning {
		t.Fatalf("snapshot = %+v, %v; want a running Telnet Session", snapshot, ok)
	}
}

func TestOpenHostCarriesTheAcknowledgedX11Preference(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{
		ID: "host-x11", Label: "gui", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
		X11Forwarding: true, X11WarningAcknowledged: true,
	}

	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the X11 SSH tab", func() bool {
		graph.state.Flush()
		return tabAttached(graph.state, 0)
	})
	if !graph.factory.lastSpec().X11Forwarding {
		t.Fatalf("factory spec X11 = %v, want enabled", graph.factory.lastSpec().X11Forwarding)
	}
}

func TestOpenHostKeepsTheConnectingTabAndShowsTheProblem(t *testing.T) {
	graph := newTestGraph(t)
	graph.factory.setError(&appconnection.Error{
		Kind:  appconnection.KindAuth,
		Cause: appconnection.ErrAuthentication,
	})

	if err := graph.state.OpenHost(domainvault.Host{
		ID: "host-1", Label: "prod", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}

	waitFor(t, "the connection problem", func() bool {
		graph.state.Flush()
		info, ok := graph.state.PaneInfoAt(0, mustFocusedPane(t, graph.state, 0))
		return ok && !info.Pending && info.Err != nil
	})
	if graph.state.Problem() != appconnection.KindAuth {
		t.Fatalf("problem = %q, want the typed authentication failure", graph.state.Problem())
	}
	// The Tab the user could already open stays, showing the failure and its
	// Reconnect control, rather than vanishing under them.
	if graph.state.TabCount() != 1 {
		t.Fatalf("tabs = %d after a failed connection, want the Tab kept", graph.state.TabCount())
	}
	info, ok := graph.state.PaneInfoAt(0, mustFocusedPane(t, graph.state, 0))
	if !ok || info.Err == nil || info.Pending {
		t.Fatalf("PaneInfo = %+v, %v; want a failed Pane", info, ok)
	}
	if info.Target != "root@example.com:22" {
		t.Fatalf("Pane target = %q, want the endpoint it tried", info.Target)
	}
}

// hostGatedFactory holds an SSH dial open while opening every other Kind at
// once, so a test can move a connecting Pane without also stalling the local
// Tab it moves onto.
type hostGatedFactory struct {
	started chan struct{}
	release chan struct{}
}

func (f *hostGatedFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if spec.Kind != appsession.KindSSH {
		return newFakeTransport(), nil
	}
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
		return newFakeTransport(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestMovingAConnectingPaneKeepsItsDialOutcome pins the Pane identity rule: a
// Pane moved to another Tab while its dial runs still receives the Session the
// dial produces, instead of staying pending forever because its old Tab is
// gone.
func TestMovingAConnectingPaneKeepsItsDialOutcome(t *testing.T) {
	factory := &hostGatedFactory{started: make(chan struct{}, 1), release: make(chan struct{})}
	st := newMCPHostState(t, factory)
	if err := st.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitFor(t, "the local Pane", func() bool {
		st.Flush()
		return tabAttached(st, 0)
	})

	if err := st.OpenHost(domainvault.Host{
		ID: "host-1", Label: "prod", Hostname: "example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH,
	}); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	select {
	case <-factory.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the dial never started")
	}
	if st.TabCount() != 2 || !st.TabPending(1) {
		t.Fatalf("tabs = %d, pending = %v; want the connecting Host Tab",
			st.TabCount(), st.TabPending(1))
	}

	hostTab := st.tabs[1]
	movedID := mustFocusedPane(t, st, 1)
	if !st.MovePane(hostTab.ID, movedID, st.tabs[0].ID,
		mustFocusedPane(t, st, 0), domainworkspace.SideRight) {
		t.Fatal("MovePane refused the connecting Pane")
	}
	if st.TabCount() != 1 {
		t.Fatalf("tabs = %d after the move, want the emptied Host Tab gone", st.TabCount())
	}

	close(factory.release)
	waitFor(t, "the moved Pane's Session", func() bool {
		st.Flush()
		value := st.tabs[0].panes[movedID]
		return value != nil && !value.pending && value.surface != nil
	})
}

// mustFocusedPane returns a Tab's focused Pane, failing the test when it has
// none.
func mustFocusedPane(t *testing.T, s *State, tab int) model.PaneID {
	t.Helper()
	id, ok := s.FocusedPaneID(tab)
	if !ok {
		t.Fatalf("Tab %d has no focused Pane", tab)
	}
	return id
}

// tabAttached reports whether the Tab at index exists and its focused Pane
// owns a live Session. Opening a Host creates the Tab before the dial finishes,
// so tests that need the Session wait on this rather than TabCount.
func tabAttached(s *State, index int) bool {
	if index < 0 || index >= s.TabCount() {
		return false
	}
	id, ok := s.FocusedPaneID(index)
	if !ok {
		return false
	}
	value := s.tabs[index].panes[id]
	return value != nil && !value.pending && value.session != nil
}

func waitFor(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestTerminalAppearanceReachesOpenSurfaces proves a live Appearance change
// recolors and restyles the terminals of already open Tabs.
func TestTerminalAppearanceReachesOpenSurfaces(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{
		ID: "host-1", Label: "prod", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the SSH tab", func() bool {
		graph.state.Flush()
		return tabAttached(graph.state, 0)
	})

	graph.state.SetTerminalFont("JetBrains Mono", 15)
	if graph.surface.fontFamily != "JetBrains Mono" || graph.surface.fontSize != 15 {
		t.Errorf("font = %q %v", graph.surface.fontFamily, graph.surface.fontSize)
	}
	preset, _ := domaintheme.LookupTerminalTheme("netcatty-light")
	graph.state.SetTerminalTheme(preset)
	if !graph.surface.themed || graph.surface.theme.ID != preset.ID {
		t.Errorf("theme = %+v", graph.surface.theme)
	}
}

// TestInsertAndSuggestionInvalidationReachOpenSurfaces proves the History
// actions target the focused terminal's own safe insertion path and drop its
// cached popup rows after the journal changes.
func TestInsertAndSuggestionInvalidationReachOpenSurfaces(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{
		ID: "host-1", Label: "prod", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the SSH tab", func() bool {
		graph.state.Flush()
		return tabAttached(graph.state, 0)
	})

	if err := graph.state.InsertIntoFocused("git status"); err != nil {
		t.Fatalf("InsertIntoFocused: %v", err)
	}
	if len(graph.surface.inserted) != 1 || graph.surface.inserted[0] != "git status" {
		t.Fatalf("inserted = %+v, want the command", graph.surface.inserted)
	}
	graph.state.InvalidateSuggestions()
	if graph.surface.invalidations != 1 {
		t.Fatalf("invalidations = %d, want 1", graph.surface.invalidations)
	}

	// A shell that cannot insert multi-line text reports its refusal; the
	// state must not replace it with a typed fallback.
	graph.surface.insertErr = sessionui.ErrInsertUnsafe
	err := graph.state.InsertIntoFocused("touch marker\necho SHOULD_NOT_RUN")
	if !errors.Is(err, sessionui.ErrInsertUnsafe) {
		t.Fatalf("InsertIntoFocused error = %v, want ErrInsertUnsafe", err)
	}
	if len(graph.surface.inserted) != 1 {
		t.Fatalf("refused insert reached the surface: %+v", graph.surface.inserted)
	}
}

// TestSendToFocusedWithoutRunUsesTheSafeInsertionPath pins the side panel's
// Paste action, which calls SendToFocused with run=false: it must reach the
// surface's InsertText refusal instead of writing raw newlines to the shell.
func TestSendToFocusedWithoutRunUsesTheSafeInsertionPath(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}

	graph.surface.insertErr = sessionui.ErrInsertUnsafe
	if graph.state.SendToFocused("touch marker\necho SHOULD_NOT_RUN", false) {
		t.Fatal("SendToFocused accepted the refused multi-line insertion")
	}
	graph.factory.transport.mu.Lock()
	input := append([]byte(nil), graph.factory.transport.input...)
	graph.factory.transport.mu.Unlock()
	if len(input) != 0 {
		t.Fatalf("the refused insertion reached the shell: %q", input)
	}

	graph.surface.insertErr = nil
	if !graph.state.SendToFocused("git status", false) {
		t.Fatal("SendToFocused refused a single-line insertion")
	}
	if len(graph.surface.inserted) != 1 || graph.surface.inserted[0] != "git status" {
		t.Fatalf("inserted = %+v, want git status through the surface", graph.surface.inserted)
	}
	graph.factory.transport.mu.Lock()
	input = append([]byte(nil), graph.factory.transport.input...)
	graph.factory.transport.mu.Unlock()
	if len(input) != 0 {
		t.Fatalf("insertion bypassed the surface and reached the shell: %q", input)
	}
}

// TestSnippetInsertionKeepsTheLineEditorTextAndSafety mirrors the
// autocomplete snippet acceptance at the shell seam: inserting a snippet
// never submits a line, never clears text the terminal holds to the right of
// its cursor (InsertText only writes at the cursor), and a multi-line snippet
// at a target without bracketed paste is refused with ErrInsertUnsafe rather
// than typed as several lines.
func TestSnippetInsertionKeepsTheLineEditorTextAndSafety(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}

	const command = "work@example.com"
	if !graph.state.SendToFocused(command, false) {
		t.Fatal("the single-line snippet insertion was refused")
	}
	if len(graph.surface.inserted) != 1 || graph.surface.inserted[0] != command {
		t.Fatalf("inserted = %+v, want the snippet command unchanged", graph.surface.inserted)
	}
	graph.factory.transport.mu.Lock()
	input := append([]byte(nil), graph.factory.transport.input...)
	graph.factory.transport.mu.Unlock()
	if len(input) != 0 {
		t.Fatalf("the snippet insertion reached the shell as input: %q", input)
	}

	const multiline = "first line\nsecond line"
	graph.surface.insertErr = sessionui.ErrInsertUnsafe
	if graph.state.SendToFocused(multiline, false) {
		t.Fatal("the refused multi-line snippet was accepted")
	}
	if len(graph.surface.inserted) != 1 {
		t.Fatalf("the refused snippet reached the surface: %+v", graph.surface.inserted)
	}
	graph.factory.transport.mu.Lock()
	input = append([]byte(nil), graph.factory.transport.input...)
	graph.factory.transport.mu.Unlock()
	if len(input) != 0 {
		t.Fatalf("the refused snippet reached the shell: %q", input)
	}
}

func TestOpenHostCarriesTheNetworkDeviceModeOnlyForSSH(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{
		ID: "host-switch", Label: "switch", Hostname: "switch.example.com", Port: 22,
		Username: "admin", Protocol: domainvault.ProtocolSSH,
		DeviceType: domainvault.DeviceTypeNetwork,
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the network device tab", func() bool {
		graph.state.Flush()
		return tabAttached(graph.state, 0)
	})
	if !graph.factory.lastSpec().NetworkDevice {
		t.Fatal("the network device mode did not reach the Session")
	}

	// A hand-edited record that pairs the mode with another protocol must not
	// put the Session into raw execution.
	other := newTestGraph(t)
	host.ID = "host-telnet"
	host.Protocol = domainvault.ProtocolTelnet
	host.Port = 23
	if err := other.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost telnet: %v", err)
	}
	waitFor(t, "the Telnet tab", func() bool {
		other.state.Flush()
		return tabAttached(other.state, 0)
	})
	if other.factory.lastSpec().NetworkDevice {
		t.Fatal("the network device mode reached a Telnet Session")
	}
}

func TestOpenHostOpensAMoshHostWithTheMoshTransport(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{
		ID: "host-mosh", Label: "roaming", Hostname: "mosh.example.com",
		Port: 2222, Username: "alice", Protocol: domainvault.ProtocolSSH,
		Transport:      domainvault.TransportMosh,
		MoshServerPath: " /usr/local/bin/mosh-server ",
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the Mosh tab", func() bool {
		graph.state.Flush()
		return tabAttached(graph.state, 0)
	})

	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindMosh {
		t.Fatalf("factory kind = %q, want %q", spec.Kind, appsession.KindMosh)
	}
	if spec.Mosh == nil || spec.Mosh.Hostname != "mosh.example.com" ||
		spec.Mosh.Port != 2222 || spec.Mosh.Username != "alice" ||
		spec.Mosh.ServerPath != "/usr/local/bin/mosh-server" {
		t.Fatalf("Mosh config = %+v", spec.Mosh)
	}
	if spec.Reconnect == nil {
		t.Fatal("a Mosh Session has no reconnect policy")
	}
	snapshot, ok := graph.state.ActiveSnapshot()
	if !ok || snapshot.Kind != appsession.KindMosh || snapshot.Status != appsession.StatusRunning {
		t.Fatalf("snapshot = %+v, %v; want a running Mosh Session", snapshot, ok)
	}
}

func TestOpenHostOpensAnETHostWithTheETTransport(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{
		ID: "host-et", Label: "et", Hostname: "et.example.com",
		Port: 2022, Username: "bob", Protocol: domainvault.ProtocolSSH,
		Transport: domainvault.TransportET, ETPort: 8737,
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the ET tab", func() bool {
		graph.state.Flush()
		return tabAttached(graph.state, 0)
	})

	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindET {
		t.Fatalf("factory kind = %q, want %q", spec.Kind, appsession.KindET)
	}
	if spec.ET == nil || spec.ET.Hostname != "et.example.com" ||
		spec.ET.Port != 2022 || spec.ET.Username != "bob" || spec.ET.ETPort != 8737 {
		t.Fatalf("ET config = %+v", spec.ET)
	}
	if spec.Reconnect == nil {
		t.Fatal("an ET Session has no reconnect policy")
	}
	if spec.ResumeKey == "" {
		t.Fatal("an ET Session has no secret-store resume key")
	}
	panes := graph.state.WorkspacePayload().Panes
	if len(panes) != 1 || panes[0].Session.ResumeKey != spec.ResumeKey {
		t.Fatalf("saved ET Pane = %+v, want resume key %q", panes, spec.ResumeKey)
	}
	snapshot, ok := graph.state.ActiveSnapshot()
	if !ok || snapshot.Kind != appsession.KindET || snapshot.Status != appsession.StatusRunning {
		t.Fatalf("snapshot = %+v, %v; want a running ET Session", snapshot, ok)
	}
}

// TestKeyboardGrabFollowsTheFocusedPane proves the app's accelerator predicate
// only stands down when the active Tab's focused Pane says it grabbed the
// keyboard. A terminal surface that does not implement the method keeps the
// accelerators.
func TestKeyboardGrabFollowsTheFocusedPane(t *testing.T) {
	graph := newTestGraph(t)
	if graph.state.KeyboardGrab() {
		t.Fatal("an empty shell reports a keyboard grab")
	}

	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if graph.state.KeyboardGrab() {
		t.Fatal("a terminal pane reported a keyboard grab")
	}

	graph.surface.keyboardGrab = true
	if !graph.state.KeyboardGrab() {
		t.Fatal("the focused pane's keyboard grab was not reported")
	}

	graph.surface.keyboardGrab = false
	if graph.state.KeyboardGrab() {
		t.Fatal("the shell kept reporting a released grab")
	}
}
