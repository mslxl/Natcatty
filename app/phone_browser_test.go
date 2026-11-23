package app

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo"

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

// TestPhoneBrowserSessionNameIsStableAndPerHost proves two Hosts never share a
// cookie jar and a Host keeps its jar across opens.
func TestPhoneBrowserSessionNameIsStableAndPerHost(t *testing.T) {
	if phoneBrowserSessionName("prod") != phoneBrowserSessionName("prod") {
		t.Fatal("a Host's session name is not stable")
	}
	if phoneBrowserSessionName("prod") == phoneBrowserSessionName("web") {
		t.Fatal("two Hosts share a session name")
	}
	for _, name := range []string{phoneBrowserSessionName("prod"), phoneBrowserSessionName("a/b c")} {
		if name == "" || strings.ContainsAny(name, `/\ :`) {
			t.Fatalf("session name %q is not a safe on-disk name", name)
		}
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
