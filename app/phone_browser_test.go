package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// TestPhoneBrowserCapabilityIsProbedOnce proves the shell's per-frame read
// does not repeat native work: the probe runs on the first Capability call and
// its answer is cached.
func TestPhoneBrowserCapabilityIsProbedOnce(t *testing.T) {
	calls := 0
	provider := &phoneBrowserProvider{probe: func() phoneshell.BrowserCapability {
		calls++
		return phoneshell.BrowserCapability{Supported: true}
	}}
	for i := 0; i < 5; i++ {
		if capability := provider.Capability(); !capability.Supported {
			t.Fatalf("capability %d = %+v, want supported", i, capability)
		}
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d, want one", calls)
	}
}

// TestPhoneBrowserCapabilityAlwaysStatesAReason keeps a hidden entry
// explainable: an unsupported capability without its own sentence still gets
// one.
func TestPhoneBrowserCapabilityAlwaysStatesAReason(t *testing.T) {
	provider := &phoneBrowserProvider{probe: func() phoneshell.BrowserCapability {
		return phoneshell.BrowserCapability{}
	}}
	capability := provider.Capability()
	if capability.Supported {
		t.Fatal("a zero capability reported supported")
	}
	if strings.TrimSpace(capability.Reason) == "" {
		t.Fatal("an unsupported capability states no reason")
	}
}

// TestPhoneBrowserOpenFailsClosedWithoutCapability proves Open cannot be used
// to bypass the capability gate, and that the refusal carries the device's
// reason.
func TestPhoneBrowserOpenFailsClosedWithoutCapability(t *testing.T) {
	const reason = "mygo: this device's WebView has no profiles"
	provider := &phoneBrowserProvider{probe: func() phoneshell.BrowserCapability {
		return phoneshell.BrowserCapability{Reason: reason}
	}}
	_, err := provider.Open(domainvault.Host{
		ID: "prod", Protocol: domainvault.ProtocolSSH,
	}, &mygo.Window{})
	if err == nil {
		t.Fatal("Open succeeded without the capability")
	}
	if !strings.Contains(err.Error(), reason) {
		t.Fatalf("Open error = %q, want it to carry %q", err, reason)
	}
}

// TestPhoneBrowserOpenRefusesNonForwardingAndWindowlessHosts pins the two
// prerequisites Open checks before a session, panel, or lease exists.
func TestPhoneBrowserOpenRefusesNonForwardingAndWindowlessHosts(t *testing.T) {
	provider := &phoneBrowserProvider{probe: func() phoneshell.BrowserCapability {
		return phoneshell.BrowserCapability{Supported: true}
	}}
	_, err := provider.Open(domainvault.Host{
		ID: "console", Protocol: domainvault.ProtocolTelnet,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot carry a Browser") {
		t.Fatalf("Telnet Open error = %v, want a forwarding refusal", err)
	}
	_, err = provider.Open(domainvault.Host{
		ID: "prod", Protocol: domainvault.ProtocolSSH,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "window is not open") {
		t.Fatalf("windowless Open error = %v, want the missing window", err)
	}
}

// TestPhoneBrowserConnectorUsesTheHostRouteAndRefusesNoChannel pins the lease
// path's gates: a Telnet Host has no channel, an SSH Host reaches the shared
// Connection Manager, and a Vault read failure stays that error.
func TestPhoneBrowserConnectorUsesTheHostRouteAndRefusesNoChannel(t *testing.T) {
	ctx := context.Background()
	vault := &fakeRouteVault{hosts: map[model.HostID]domainvault.Host{
		"prod":   {ID: "prod", Hostname: "prod.example.com", Port: 22, Protocol: domainvault.ProtocolSSH},
		"telnet": {ID: "telnet", Hostname: "console.example.com", Port: 23, Protocol: domainvault.ProtocolTelnet},
	}}
	connector := phoneBrowserConnector{vault: vault}

	_, err := connector.Connect(ctx, "telnet")
	if err == nil || !strings.Contains(err.Error(), "cannot host a browser tunnel") {
		t.Fatalf("Telnet Connect error = %v, want a channel refusal", err)
	}
	_, err = connector.Connect(ctx, "prod")
	if err == nil || !strings.Contains(err.Error(), "no connection manager") {
		t.Fatalf("SSH Connect error = %v, want it to reach the Connection Manager", err)
	}
	_, err = connector.Connect(ctx, "missing")
	if err == nil {
		t.Fatal("a missing Host did not fail")
	}

	_, err = (phoneBrowserConnector{}).Connect(ctx, "prod")
	if err == nil || !strings.Contains(err.Error(), "no Vault") {
		t.Fatalf("vaultless Connect error = %v, want no Vault", err)
	}
}

// TestPhoneBrowserPageIsUntrusted pins the WebView boundary: no preload, no
// trusted origin, no developer tools, an isolated session, and Untrusted set.
func TestPhoneBrowserPageIsUntrusted(t *testing.T) {
	opts := phoneBrowserPageOptions(nil)
	if !opts.Untrusted {
		t.Fatal("the Host Browser page is not marked untrusted")
	}
	if opts.DevTools != mygo.DevToolsDisabled {
		t.Fatalf("DevTools = %v, want disabled", opts.DevTools)
	}
	if opts.PreloadScript != "" {
		t.Fatalf("the page carries a preload script: %q", opts.PreloadScript)
	}
	if len(opts.TrustedOrigins) != 0 {
		t.Fatalf("the page trusts origins: %v", opts.TrustedOrigins)
	}
	session := &mygo.Session{}
	withSession := phoneBrowserPageOptions(session)
	if withSession.Session != session {
		t.Fatal("the page does not use the Host's own website data store")
	}
}

// TestPhoneBrowserSessionNameIsStableAndPerSite proves the isolation
// boundary: two Hosts never share a store for the same site, one Host's two
// sites never share one, and a Host keeps a site's store across opens and
// reconnects because the key never uses the gateway's ephemeral port.
func TestPhoneBrowserSessionNameIsStableAndPerSite(t *testing.T) {
	const appSite = "https://app.example.com"
	const mailSite = "https://mail.example.com"
	if phoneBrowserSessionName("prod", appSite) != phoneBrowserSessionName("prod", appSite) {
		t.Fatal("a Host and site's session name is not stable")
	}
	if phoneBrowserSessionName("prod", appSite) == phoneBrowserSessionName("web", appSite) {
		t.Fatal("two Hosts share a session for the same site")
	}
	if phoneBrowserSessionName("prod", appSite) == phoneBrowserSessionName("prod", mailSite) {
		t.Fatal("one Host's two sites share a session")
	}
	// The same site spelled with another case or a default port is one store.
	for _, equivalent := range []string{"https://APP.example.com", "https://app.example.com:443"} {
		if phoneBrowserSessionName("prod", equivalent) != phoneBrowserSessionName("prod", appSite) {
			t.Fatalf("equivalent origin %q got another session", equivalent)
		}
	}
	for _, name := range []string{
		phoneBrowserSessionName("prod", appSite),
		phoneBrowserSessionName("a/b c", appSite),
	} {
		if name == "" || strings.ContainsAny(name, `/\ :`) {
			t.Fatalf("session name %q is not a safe on-disk name", name)
		}
	}
}

// TestPhoneBrowserPagesUseTheirOwnSiteStore drives the page factory seam: two
// pages of one Host and site share a store, the same Host's other site gets
// another, and another Host never sees either.
func TestPhoneBrowserPagesUseTheirOwnSiteStore(t *testing.T) {
	const appSite = "https://app.example.com"
	const mailSite = "https://mail.example.com"

	var mu sync.Mutex
	var sessions []string
	newView := func(options mygo.WebViewOptions) (*mygo.WebView, error) {
		mu.Lock()
		if options.Page.Session != nil {
			sessions = append(sessions, options.Page.Session.Name())
		} else {
			sessions = append(sessions, "")
		}
		mu.Unlock()
		// A real WebView needs a native window, so the factory stops after
		// recording the options it was asked for.
		return nil, errFakeWebView
	}

	browserFor := func(host model.HostID) *phoneBrowser {
		return &phoneBrowser{
			hostID:   host,
			core:     appbrowser.New(appbrowser.Config{HostID: host}),
			newView:  newView,
			sessions: make(map[string]*mygo.Session),
		}
	}

	prod := browserFor("prod")
	_, _ = prod.NewPage(appSite)
	_, _ = prod.NewPage("https://APP.example.com:443")
	_, _ = prod.NewPage(mailSite)
	web := browserFor("web")
	_, _ = web.NewPage(appSite)

	mu.Lock()
	got := append([]string(nil), sessions...)
	mu.Unlock()
	want := []string{
		phoneBrowserSessionName("prod", appSite),
		phoneBrowserSessionName("prod", appSite),
		phoneBrowserSessionName("prod", mailSite),
		phoneBrowserSessionName("web", appSite),
	}
	if len(got) != len(want) {
		t.Fatalf("page sessions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("page %d session = %q, want %q", i, got[i], want[i])
		}
	}
	if got[0] == got[2] {
		t.Fatal("one Host's two sites shared a store")
	}
	if got[0] == got[3] {
		t.Fatal("two Hosts shared a store for the same site")
	}
}

// errFakeWebView lets the page-factory test stop after recording the options:
// a real WebView needs a native window.
var errFakeWebView = errors.New("test: no native web view")

// TestPhoneBrowserGatewayMapsToTheSiteSession proves a download or favicon
// request that arrives as a loopback gateway URL reads the cookies of the
// site whose page started it. The real panel maps the URL back to the remote
// origin, and that origin selects the same store the page uses.
func TestPhoneBrowserGatewayMapsToTheSiteSession(t *testing.T) {
	panel := appbrowser.New(appbrowser.Config{
		HostID:    "prod",
		Connector: phoneBrowserTestConnector{},
	})
	updates := make(chan appbrowser.State, 8)
	panel.Subscribe(func(state appbrowser.State) {
		select {
		case updates <- state:
		default:
		}
	})
	panel.Start()
	defer panel.Close()
	state := waitPhoneBrowserState(t, updates, func(state appbrowser.State) bool {
		return state.Route == appbrowser.RouteConnected
	})
	gatewayURL, err := panel.Navigate(state.ActiveID, "https://app.example.com/files/report.pdf")
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	browser := &phoneBrowser{
		hostID:   "prod",
		core:     panel,
		sessions: make(map[string]*mygo.Session),
	}
	got := browser.sessionForGateway(gatewayURL)
	if got == nil {
		t.Fatal("the gateway URL mapped to no session")
	}
	want := phoneBrowserSessionName("prod", "https://app.example.com")
	if got.Name() != want {
		t.Fatalf("gateway session = %q, want the page's session %q", got.Name(), want)
	}
	if foreign := browser.sessionForSite("https://mail.example.com"); foreign.Name() == got.Name() {
		t.Fatal("another site mapped to the same session")
	}
}

// phoneBrowserTestConnector satisfies the shared panel's connector without a
// transport: the test never fetches through the gateway.
type phoneBrowserTestConnector struct{}

func (phoneBrowserTestConnector) Connect(context.Context, model.HostID) (appbrowser.Tunnel, error) {
	return phoneBrowserTestTunnel{}, nil
}

type phoneBrowserTestTunnel struct{}

func (phoneBrowserTestTunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, address)
}
func (phoneBrowserTestTunnel) Done() <-chan struct{} { return nil }
func (phoneBrowserTestTunnel) Discard()              {}

// waitPhoneBrowserState waits on the panel's own state stream with a hard
// deadline, so the asynchronous connect is never a fixed sleep.
func waitPhoneBrowserState(t *testing.T, updates <-chan appbrowser.State, cond func(appbrowser.State) bool) appbrowser.State {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case state := <-updates:
			if cond(state) {
				return state
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for the panel state")
		}
	}
}

// TestPhoneBrowserExportUsesTheSystemSavePath proves a finished download is
// handed to MyGo's save/share path and that a cancellation is reported as
// such, never as a failure.
func TestPhoneBrowserExportUsesTheSystemSavePath(t *testing.T) {
	var gotPath, gotName string
	browser := &phoneBrowser{saveCopy: func(path, name string) (string, error) {
		gotPath, gotName = path, name
		return "/shared/report.pdf", nil
	}}
	download := appbrowser.Download{
		ID: "download-1", Filename: "report.pdf",
		SavePath: "/app/downloads/report.pdf", Status: appbrowser.DownloadCompleted,
	}
	path, err := browser.ExportDownload(download)
	if err != nil {
		t.Fatalf("ExportDownload: %v", err)
	}
	if path != "/shared/report.pdf" {
		t.Fatalf("export path = %q, want the picker's path", path)
	}
	if gotPath != download.SavePath || gotName != download.Filename {
		t.Fatalf("save copy got %q/%q, want %q/%q", gotPath, gotName, download.SavePath, download.Filename)
	}

	browser.saveCopy = func(string, string) (string, error) { return "", nil }
	if path, err := browser.ExportDownload(download); err != nil || path != "" {
		t.Fatalf("a cancelled export = %q, %v; want an empty path and no error", path, err)
	}

	unfinished := download
	unfinished.Status = appbrowser.DownloadProgressing
	if _, err := browser.ExportDownload(unfinished); err == nil {
		t.Fatal("an unfinished download was exported")
	}
}

// fakeSandboxPage records the two handlers the sandbox installs.
type fakeSandboxPage struct {
	permission    func(mygo.PermissionRequest) bool
	windowOpen    func(mygo.WindowOpenRequest) *mygo.WindowOptions
	permissionSet bool
	windowSet     bool
}

func (p *fakeSandboxPage) SetPermissionHandler(fn func(mygo.PermissionRequest) bool) {
	p.permission = fn
	p.permissionSet = true
}

func (p *fakeSandboxPage) SetWindowOpenHandler(fn func(mygo.WindowOpenRequest) *mygo.WindowOptions) {
	p.windowOpen = fn
	p.windowSet = true
}

// TestPhoneBrowserPageDeniesPermissionsAndPopups proves the native capabilities
// are removed: a permission request is refused and window.open gets no page.
func TestPhoneBrowserPageDeniesPermissionsAndPopups(t *testing.T) {
	page := &fakeSandboxPage{}
	hardenPhoneBrowserPage(page)
	if !page.permissionSet || !page.windowSet {
		t.Fatal("the sandbox did not install both handlers")
	}
	if page.permission(mygo.PermissionRequest{}) {
		t.Fatal("the page was granted a permission")
	}
	if page.windowOpen(mygo.WindowOpenRequest{}) != nil {
		t.Fatal("window.open was allowed to create a page")
	}
}
