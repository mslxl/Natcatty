package settings

// The External MCP endpoint surface: the master switch defaults to off, the
// Streamable HTTP endpoint is a second opt-in, and the listen address and port
// are editable. These tests drive the same Settings state the app builds.

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

func mcpEndpointFixture(t *testing.T, enabled, httpEnabled bool) *fakeMCPService {
	t.Helper()
	endpoint := domainpreferences.Defaults().ExternalMCP
	endpoint.Enabled = enabled
	endpoint.HTTPEnabled = httpEnabled
	return &fakeMCPService{endpoint: endpoint, endpointSet: true}
}

func TestMCPEndpointStartsWithTheServerOffAndOnlyTheSwitch(t *testing.T) {
	service := mcpEndpointFixture(t, false, false)
	state := NewMCPState(service)
	m := locales.In("en")
	tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	for _, text := range []string{m.SettingsMCPEnabled(), m.SettingsMCPEnableHint()} {
		if !tester.HasText(text) {
			t.Fatalf("the disabled endpoint does not show %q: %q", text, tester.Texts())
		}
	}
	for _, text := range []string{m.SettingsMCPMode(), m.SettingsMCPHTTPEnabled(), m.SettingsMCPHTTPListenAddress()} {
		if tester.HasText(text) {
			t.Fatalf("the disabled endpoint still shows %q: %q", text, tester.Texts())
		}
	}

	if err := tester.Click(m.SettingsMCPEnabled()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !service.Endpoint().Enabled {
		t.Fatal("the master switch did not reach the preferences")
	}
	for _, text := range []string{m.SettingsMCPMode(), m.SettingsMCPHTTPEnabled()} {
		if !tester.HasText(text) {
			t.Fatalf("the enabled endpoint does not show %q: %q", text, tester.Texts())
		}
	}
	if tester.HasText(m.SettingsMCPEnableHint()) {
		t.Fatalf("the enabled endpoint still shows the disabled hint: %q", tester.Texts())
	}
}

func TestMCPHTTPEndpointEditsTheListenAddressAndPort(t *testing.T) {
	service := mcpEndpointFixture(t, true, true)
	state := NewMCPState(service)
	m := locales.In("en")
	tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 1400)
	tester.SetLocale("en")
	tester.Frame()

	for _, text := range []string{
		m.SettingsMCPHTTPListenAddress(), m.SettingsMCPHTTPPort(),
		m.SettingsMCPHTTPAddressHint(),
		domainpreferences.DefaultExternalMCPHTTPListenAddress,
	} {
		if !tester.HasText(text) {
			t.Fatalf("the HTTP endpoint does not show %q: %q", text, tester.Texts())
		}
	}
	if tester.HasText(m.SettingsMCPHTTPExposed(domainpreferences.DefaultExternalMCPHTTPListenAddress)) {
		t.Fatalf("the loopback endpoint was reported as exposed: %q", tester.Texts())
	}

	// The address input keeps its draft and applies a usable address. A
	// non-loopback bind turns on the domain's custom-bind opt-in.
	if err := tester.Click(m.SettingsMCPHTTPListenAddress()); err != nil {
		t.Fatalf("focus the listen address: %v", err)
	}
	tester.Key(ui.Cmd, ui.KeyA)
	tester.Type("192.168.1.5")
	tester.Frame()
	endpoint := service.Endpoint()
	if endpoint.HTTPListenAddress != "192.168.1.5" || !endpoint.HTTPAllowCustomBind {
		t.Fatalf("custom bind = %+v, want 192.168.1.5 with the opt-in", endpoint)
	}
	if !tester.HasText(m.SettingsMCPHTTPExposed("192.168.1.5")) {
		t.Fatalf("the exposed warning is missing: %q", tester.Texts())
	}

	state.SetHTTPPort(51234)
	tester.Frame()
	if got := service.Endpoint().HTTPPort; got != 51234 {
		t.Fatalf("port = %d, want 51234", got)
	}

	if err := tester.Click(m.SettingsMCPHTTPEnabled()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if service.Endpoint().HTTPEnabled {
		t.Fatal("the HTTP switch did not reach the preferences")
	}
	if tester.HasText(m.SettingsMCPHTTPListenAddress()) {
		t.Fatalf("a disabled HTTP endpoint still shows its bind: %q", tester.Texts())
	}
}

func TestMCPHTTPListenAddressKeepsTypingUntilItIsUsable(t *testing.T) {
	service := mcpEndpointFixture(t, true, true)
	state := NewMCPState(service)
	m := locales.In("en")
	tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 1400)
	tester.SetLocale("en")
	tester.Frame()

	state.SetHTTPListenAddress("not-an-address")
	tester.Frame()
	if !state.HTTPAddressInvalid() {
		t.Fatal("an unusable address was not reported")
	}
	if got := service.Endpoint().HTTPListenAddress; got != domainpreferences.DefaultExternalMCPHTTPListenAddress {
		t.Fatalf("an unusable address was saved as %q", got)
	}
	if !tester.HasText(m.SettingsMCPHTTPAddressInvalid()) {
		t.Fatalf("the invalid-address hint is missing: %q", tester.Texts())
	}

	state.SetHTTPListenAddress("0.0.0.0")
	tester.Frame()
	if state.HTTPAddressInvalid() {
		t.Fatal("the usable wildcard address is still reported invalid")
	}
	if got := service.Endpoint().HTTPListenAddress; got != "0.0.0.0" {
		t.Fatalf("wildcard address = %q", got)
	}
	if !tester.HasText(m.SettingsMCPHTTPExposed("0.0.0.0")) {
		t.Fatalf("the wildcard exposure warning is missing: %q", tester.Texts())
	}
}

func TestMCPEndpointSaveFailureIsLocalized(t *testing.T) {
	service := mcpEndpointFixture(t, false, false)
	service.endpointErr = errors.New("disk is full")
	state := NewMCPState(service)
	m := locales.In("en")
	tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	if err := tester.Click(m.SettingsMCPEnabled()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if service.Endpoint().Enabled {
		t.Fatal("a failed save enabled the server")
	}
	if !tester.HasText(m.SettingsMCPProblemEndpoint()) {
		t.Fatalf("the save failure is not localized: %q", tester.Texts())
	}
}
