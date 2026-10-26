package browser

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/domain/model"
)

type fakeTunnel struct {
	dial func(ctx context.Context, network, address string) (net.Conn, error)
	done chan struct{}

	mu        sync.Mutex
	discarded int
}

func newFakeTunnel() *fakeTunnel {
	return &fakeTunnel{
		done: make(chan struct{}),
		dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("unexpected dial")
		},
	}
}

func (t *fakeTunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return t.dial(ctx, network, address)
}

func (t *fakeTunnel) Done() <-chan struct{} { return t.done }

func (t *fakeTunnel) Discard() {
	t.mu.Lock()
	t.discarded++
	t.mu.Unlock()
}

type fakeConnector struct {
	mu      sync.Mutex
	tunnels []*fakeTunnel
	err     error
	calls   int
}

func (c *fakeConnector) Connect(context.Context, model.HostID) (Tunnel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	tunnel := newFakeTunnel()
	c.tunnels = append(c.tunnels, tunnel)
	return tunnel, nil
}

func (c *fakeConnector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *fakeConnector) last() *fakeTunnel {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tunnels) == 0 {
		return nil
	}
	return c.tunnels[len(c.tunnels)-1]
}

func subscribePanel(t *testing.T, panel *Panel) <-chan State {
	t.Helper()
	updates := make(chan State, 128)
	panel.Subscribe(func(state State) {
		select {
		case updates <- state:
		default:
		}
	})
	return updates
}

func waitState(t *testing.T, updates <-chan State, what string, want func(State) bool) State {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case state := <-updates:
			if want(state) {
				return state
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func startPanel(t *testing.T, connector Connector) (*Panel, <-chan State, State) {
	t.Helper()
	panel := New(Config{HostID: "host-1", Label: "web-1", Connector: connector})
	updates := subscribePanel(t, panel)
	panel.Start()
	state := waitState(t, updates, "the dedicated tunnel", func(state State) bool {
		return state.Route == RouteConnected
	})
	return panel, updates, state
}

func TestPanelOpensOnADedicatedTunnelAndNavigatesItsTabs(t *testing.T) {
	connector := &fakeConnector{}
	panel, updates, state := startPanel(t, connector)
	defer panel.Close()

	if connector.count() != 1 {
		t.Fatalf("Connect calls = %d, want 1", connector.count())
	}
	if len(state.Tabs) != 1 || state.ActiveID != state.Tabs[0].ID {
		t.Fatalf("initial state = %+v", state)
	}
	tabID := state.ActiveID

	gatewayURL, err := panel.Navigate(tabID, "localhost:3000/admin")
	if err != nil {
		t.Fatalf("Navigate localhost: %v", err)
	}
	if !strings.HasPrefix(gatewayURL, "http://127.0.0.2:") || !strings.HasSuffix(gatewayURL, "/admin") {
		t.Fatalf("gateway URL = %q", gatewayURL)
	}
	state = waitState(t, updates, "the localhost tab URL", func(state State) bool {
		return state.Tabs[0].URL == "http://localhost:3000/admin"
	})
	if state.Tabs[0].Loading != true {
		t.Fatalf("navigating tab loading = %v", state.Tabs[0].Loading)
	}

	if _, err := panel.Navigate(tabID, "example.com/docs"); err != nil {
		t.Fatalf("Navigate public host: %v", err)
	}
	state = waitState(t, updates, "the HTTPS tab URL", func(state State) bool {
		return state.Tabs[0].URL == "https://example.com/docs"
	})
	if state.Tabs[0].Title != "example.com" {
		t.Fatalf("tab title = %q", state.Tabs[0].Title)
	}

	second, err := panel.NewTab()
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	state = waitState(t, updates, "the second tab", func(state State) bool {
		return len(state.Tabs) == 2 && state.ActiveID == second
	})
	if _, err := panel.Navigate(second, "https://docs.example.com/"); err != nil {
		t.Fatalf("Navigate second tab: %v", err)
	}
	if !panel.Activate(tabID) {
		t.Fatal("Activate first tab = false")
	}
	state = panel.State()
	if state.ActiveID != tabID || state.Tabs[0].URL != "https://example.com/docs" {
		t.Fatalf("after Activate state = %+v", state)
	}
	if !panel.CloseTab(second) {
		t.Fatal("CloseTab = false")
	}
	state = panel.State()
	if len(state.Tabs) != 1 || state.ActiveID != tabID {
		t.Fatalf("after CloseTab state = %+v", state)
	}
}

func TestPanelStopBeforeACommitDropsTheProvisionalLabel(t *testing.T) {
	connector := &fakeConnector{}
	panel, updates, state := startPanel(t, connector)
	defer panel.Close()
	tabID := state.ActiveID

	gatewayURL, err := panel.Navigate(tabID, "https://docs.example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	panel.PageChanged(tabID, PageState{URL: gatewayURL, Title: "Docs", Loading: false})
	state = waitState(t, updates, "the committed page", func(state State) bool {
		return state.Tabs[0].Title == "Docs" && !state.Tabs[0].Loading
	})
	panel.SetPageFlags(tabID, true, true, false)
	waitState(t, updates, "the committed navigation flags", func(state State) bool {
		return state.Tabs[0].CanGoBack && state.Tabs[0].CanGoForward
	})

	if _, err := panel.Navigate(tabID, "https://other.example/slow"); err != nil {
		t.Fatal(err)
	}
	state = panel.State()
	if state.Tabs[0].URL != "https://other.example/slow" || state.Tabs[0].Title != "other.example" ||
		!state.Tabs[0].Loading {
		t.Fatalf("the provisional navigation = %+v", state.Tabs[0])
	}
	if state.Tabs[0].CanGoBack || state.Tabs[0].CanGoForward {
		t.Fatalf("the provisional navigation kept the previous history flags: %+v", state.Tabs[0])
	}
	if !panel.PageStopped(tabID) {
		t.Fatal("Stopped before a commit = false, want the provisional tab restored")
	}
	state = panel.State()
	if state.Tabs[0].URL != "https://docs.example.com/a" || state.Tabs[0].Title != "Docs" {
		t.Fatalf("the stopped tab kept %+v, want the committed docs.example.com page",
			state.Tabs[0])
	}
	// The restored page is the one the tab actually shows, so its history
	// flags are the committed ones, not the provisional navigation's.
	if !state.Tabs[0].CanGoBack || !state.Tabs[0].CanGoForward {
		t.Fatalf("the restored tab lost its committed history flags: %+v", state.Tabs[0])
	}
	if state.Tabs[0].Loading {
		t.Fatal("the stopped tab is still loading")
	}
}

func TestPanelStopRestoresTheCommittedTabIcon(t *testing.T) {
	icons, err := NewSiteIcons(&memoryIconStore{items: []domainbrowser.SiteIcon{{
		HostID: "host-1", Origin: "https://docs.example.com",
		Mode: domainbrowser.IconModeAuto, DataURL: testPNGDataURL,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	panel := New(Config{
		HostID: "host-1", Label: "web-1", Connector: &fakeConnector{}, Icons: icons,
	})
	updates := subscribePanel(t, panel)
	panel.Start()
	state := waitState(t, updates, "the dedicated tunnel", func(state State) bool {
		return state.Route == RouteConnected
	})
	defer panel.Close()
	tabID := state.ActiveID

	gatewayURL, err := panel.Navigate(tabID, "https://docs.example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	panel.PageChanged(tabID, PageState{URL: gatewayURL, Title: "Docs", Loading: false})
	waitState(t, updates, "the committed docs icon", func(state State) bool {
		return state.Tabs[0].IconDataURL == testPNGDataURL
	})

	if _, err := panel.Navigate(tabID, "https://other.example/slow"); err != nil {
		t.Fatal(err)
	}
	if icon := panel.State().Tabs[0].IconDataURL; icon != "" {
		t.Fatalf("the provisional tab kept icon %q", icon)
	}
	if !panel.PageStopped(tabID) {
		t.Fatal("Stopped before a commit = false, want the provisional tab restored")
	}
	if icon := panel.State().Tabs[0].IconDataURL; icon != testPNGDataURL {
		t.Fatalf("the stopped tab icon = %q, want the committed docs icon", icon)
	}
}

func TestPanelStopAfterACommitKeepsTheCommittedAddress(t *testing.T) {
	connector := &fakeConnector{}
	panel, updates, state := startPanel(t, connector)
	defer panel.Close()
	tabID := state.ActiveID

	gatewayURL, err := panel.Navigate(tabID, "https://other.example/slow")
	if err != nil {
		t.Fatal(err)
	}
	// A navigation event is a commit even when no title has arrived yet.
	panel.PageChanged(tabID, PageState{URL: gatewayURL, Loading: true})
	waitState(t, updates, "the committed navigation", func(state State) bool {
		return state.Tabs[0].URL == "https://other.example/slow"
	})
	if panel.PageStopped(tabID) {
		t.Fatal("Stopped after a commit restored a page the tab had already navigated away from")
	}
	state = panel.State()
	if state.Tabs[0].URL != "https://other.example/slow" || state.Tabs[0].Title != "other.example" {
		t.Fatalf("the committed tab = %+v", state.Tabs[0])
	}
}

func TestPanelRefusesBadAddressesAndNavigationWithoutATunnel(t *testing.T) {
	connector := &fakeConnector{}
	panel, updates, state := startPanel(t, connector)
	tabID := state.ActiveID

	if _, err := panel.Navigate(tabID, "javascript:alert(1)"); !errors.Is(err, domainbrowser.ErrAddressScheme) {
		t.Fatalf("javascript error = %v", err)
	}
	if _, err := panel.Navigate(tabID, "https://user:pass@example.com/"); !errors.Is(err, domainbrowser.ErrAddressCredentials) {
		t.Fatalf("credentials error = %v", err)
	}
	if _, err := panel.Navigate("missing", "example.com"); !errors.Is(err, ErrNoTab) {
		t.Fatalf("missing tab error = %v", err)
	}

	close(connector.last().done)
	waitState(t, updates, "the tunnel loss", func(state State) bool {
		return state.Route == RouteDisconnected && state.Failure == FailureTunnel
	})
	if _, err := panel.Navigate(tabID, "example.com"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("navigate while disconnected error = %v", err)
	}
}

func TestPanelDisconnectStopsBrowsingAndRetryUsesANewTunnel(t *testing.T) {
	connector := &fakeConnector{}
	panel, updates, state := startPanel(t, connector)
	defer panel.Close()
	first := connector.last()
	tabID := state.ActiveID
	if _, err := panel.Navigate(tabID, "localhost:8080/"); err != nil {
		t.Fatal(err)
	}

	close(first.done)
	state = waitState(t, updates, "the disconnect", func(state State) bool {
		return state.Route == RouteDisconnected && state.Failure == FailureTunnel
	})
	if len(state.Tabs) != 1 || state.Tabs[0].URL != "http://localhost:8080/" || state.Tabs[0].Loading {
		t.Fatalf("disconnected state = %+v", state)
	}

	if panel.Retry() != true {
		t.Fatal("Retry after disconnect = false")
	}
	state = waitState(t, updates, "the retried tunnel", func(state State) bool {
		return state.Route == RouteConnected
	})
	if connector.count() != 2 {
		t.Fatalf("Connect calls after retry = %d, want 2", connector.count())
	}
	if connector.last() == first {
		t.Fatal("Retry reused the failed tunnel")
	}
	if panel.Retry() {
		t.Fatal("Retry while connected = true")
	}
	first.mu.Lock()
	discarded := first.discarded
	first.mu.Unlock()
	if discarded == 0 {
		t.Fatal("the failed tunnel was not discarded")
	}
}

func TestPanelConnectFailureIsVisibleAndRetryCanRecover(t *testing.T) {
	connector := &fakeConnector{err: errors.New("host key refused")}
	panel := New(Config{HostID: "host-1", Label: "web-1", Connector: connector})
	defer panel.Close()
	updates := subscribePanel(t, panel)
	panel.Start()

	state := waitState(t, updates, "the connect failure", func(state State) bool {
		return state.Route == RouteDisconnected && state.Failure == FailureConnect
	})
	if !strings.Contains(state.Detail, "host key refused") {
		t.Fatalf("failure detail = %q", state.Detail)
	}
	connector.mu.Lock()
	connector.err = nil
	connector.mu.Unlock()
	if !panel.Retry() {
		t.Fatal("Retry after connect failure = false")
	}
	waitState(t, updates, "the recovered tunnel", func(state State) bool {
		return state.Route == RouteConnected
	})
}

func TestPanelMapsPageEventsBackToTheRealAddress(t *testing.T) {
	connector := &fakeConnector{}
	panel, updates, state := startPanel(t, connector)
	defer panel.Close()
	tabID := state.ActiveID
	gatewayURL, err := panel.Navigate(tabID, "https://example.com/start")
	if err != nil {
		t.Fatal(err)
	}

	panel.PageChanged(tabID, PageState{
		URL: gatewayURL + "/redirected#part", Title: "Console",
		Loading: false, CanGoBack: true, CanGoForward: false,
	})
	state = waitState(t, updates, "the page title", func(state State) bool {
		return state.Tabs[0].Title == "Console"
	})
	if state.Tabs[0].URL != "https://example.com/start/redirected#part" {
		t.Fatalf("mapped URL = %q", state.Tabs[0].URL)
	}
	if !state.Tabs[0].CanGoBack || state.Tabs[0].CanGoForward || state.Tabs[0].Loading {
		t.Fatalf("page flags = %+v", state.Tabs[0])
	}

	panel.PageFailed(tabID, "name resolution failed")
	state = waitState(t, updates, "the page failure", func(state State) bool {
		return state.Notice == FailurePage
	})
	if state.NoticeMsg != "name resolution failed" {
		t.Fatalf("notice = %q", state.NoticeMsg)
	}
	panel.PageChanged(tabID, PageState{URL: gatewayURL, Loading: false})
	waitState(t, updates, "the notice to clear", func(state State) bool {
		return state.Notice == FailureNone
	})

	panel.SetPageFlags(tabID, true, true, true)
	state = panel.State()
	if !state.Tabs[0].CanGoBack || !state.Tabs[0].CanGoForward || !state.Tabs[0].Loading {
		t.Fatalf("SetPageFlags state = %+v", state.Tabs[0])
	}
}

func TestPanelEnforcesTheOriginalTabLimit(t *testing.T) {
	connector := &fakeConnector{}
	panel, _, _ := startPanel(t, connector)
	defer panel.Close()
	for i := 1; i < MaxTabs; i++ {
		if _, err := panel.NewTab(); err != nil {
			t.Fatalf("NewTab %d: %v", i, err)
		}
	}
	if _, err := panel.NewTab(); !errors.Is(err, ErrTabLimit) {
		t.Fatalf("NewTab past limit error = %v", err)
	}
}

func TestPanelCloseDiscardsTheTunnel(t *testing.T) {
	connector := &fakeConnector{}
	panel, _, _ := startPanel(t, connector)
	tunnel := connector.last()
	panel.Close()
	tunnel.mu.Lock()
	discarded := tunnel.discarded
	tunnel.mu.Unlock()
	if discarded == 0 {
		t.Fatal("Close did not discard the dedicated tunnel")
	}
	panel.Close()
	if state := panel.State(); state.Route == RouteConnected {
		t.Fatalf("closed panel state = %+v", state)
	}
}
