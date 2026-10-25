package state

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainvault "github.com/mslxl/natcatty/domain/vault"
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

type fakeFactory struct {
	transport *fakeTransport
	err       error
	spec      appsession.StartSpec
}

func (f *fakeFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.spec = spec
	if f.err != nil {
		return nil, f.err
	}
	return f.transport, nil
}

// fakeSurface stands in for the MyGo terminal the UI owns.
type fakeSurface struct {
	closed     bool
	fontFamily string
	fontSize   float32
	theme      domaintheme.TerminalTheme
	themed     bool
}

func (f *fakeSurface) View(c *ui.Context) ui.Element {
	return ui.Text(c, "terminal-surface").Fill()
}

func (f *fakeSurface) Close() { f.closed = true }

func (f *fakeSurface) SetTerminalFont(family string, size float32) {
	f.fontFamily, f.fontSize = family, size
}

func (f *fakeSurface) SetTerminalTheme(preset domaintheme.TerminalTheme) {
	f.theme, f.themed = preset, true
}

// testGraph wires a shell State to fake Sessions and surfaces.
type testGraph struct {
	state    *State
	surface  *fakeSurface
	sessions []*appsession.Session
	factory  *fakeFactory
	changes  atomic.Int32
}

func newTestGraph(t *testing.T) *testGraph {
	t.Helper()
	graph := &testGraph{}
	graph.factory = &fakeFactory{transport: newFakeTransport()}
	service := appsession.NewService(graph.factory)
	t.Cleanup(service.CloseAll)
	graph.surface = &fakeSurface{}
	graph.state = NewState(
		WithRedrawRequest(func() { graph.changes.Add(1) }),
		WithSessions(service, func(session *appsession.Session) (Surface, error) {
			graph.sessions = append(graph.sessions, session)
			return graph.surface, nil
		}),
	)
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

func TestOpenHostAddsAnSSHTabOnTheNextFlush(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{
		ID: "host-1", Label: "prod", Hostname: "example.com", Port: 2222,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}

	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	// The dial is in flight until Flush applies the outcome, so the shell can
	// show its connecting indicator.
	if pending := graph.state.PendingConnections(); len(pending) != 1 || pending[0] != "prod" {
		t.Fatalf("pending connections = %q, want prod", pending)
	}
	// The dial goroutine only queues its outcome; Tabs change in Flush.
	if graph.state.TabCount() != 0 {
		t.Fatalf("tabs = %d before Flush, want 0", graph.state.TabCount())
	}

	waitFor(t, "the SSH tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})
	if got := graph.factory.spec; got.Kind != appsession.KindSSH || got.HostID != "host-1" ||
		got.Hostname != "example.com" || got.Port != 2222 || got.Username != "root" {
		t.Fatalf("factory spec = %+v, want the Host's SSH target", got)
	}
	if graph.factory.spec.Reconnect == nil {
		t.Fatal("SSH Sessions do not carry the reconnect policy")
	}
	if graph.factory.spec.X11Forwarding {
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
		return graph.state.TabCount() == 1
	})

	spec := graph.factory.spec
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
		return graph.state.TabCount() == 1
	})
	if !graph.factory.spec.X11Forwarding {
		t.Fatalf("factory spec X11 = %v, want enabled", graph.factory.spec.X11Forwarding)
	}
}

func TestOpenHostReportsATypedConnectionProblemWithoutATab(t *testing.T) {
	graph := newTestGraph(t)
	graph.factory.err = &appconnection.Error{
		Kind:  appconnection.KindAuth,
		Cause: appconnection.ErrAuthentication,
	}

	if err := graph.state.OpenHost(domainvault.Host{
		ID: "host-1", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}

	waitFor(t, "the connection problem", func() bool {
		graph.state.Flush()
		return graph.state.Problem() == appconnection.KindAuth
	})
	if graph.state.TabCount() != 0 {
		t.Fatalf("tabs = %d after a failed connection, want 0", graph.state.TabCount())
	}
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
		return graph.state.TabCount() == 1
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
		return graph.state.TabCount() == 1
	})
	if !graph.factory.spec.NetworkDevice {
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
		return other.state.TabCount() == 1
	})
	if other.factory.spec.NetworkDevice {
		t.Fatal("the network device mode reached a Telnet Session")
	}
}

func TestOpenHostOpensAMoshHostWithTheMoshTransport(t *testing.T) {
	graph := newTestGraph(t)
	host := domainvault.Host{
		ID: "host-mosh", Label: "roaming", Hostname: "mosh.example.com",
		Port: 2222, Username: "alice", Protocol: domainvault.ProtocolMosh,
		MoshServerPath: " /usr/local/bin/mosh-server ",
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the Mosh tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})

	spec := graph.factory.spec
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
		Port: 2022, Username: "bob", Protocol: domainvault.ProtocolET, ETPort: 8737,
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the ET tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})

	spec := graph.factory.spec
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
	snapshot, ok := graph.state.ActiveSnapshot()
	if !ok || snapshot.Kind != appsession.KindET || snapshot.Status != appsession.StatusRunning {
		t.Fatalf("snapshot = %+v, %v; want a running ET Session", snapshot, ok)
	}
}
