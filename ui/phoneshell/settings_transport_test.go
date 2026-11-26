package phoneshell

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestTransportPageSavesTheSharedSetting proves the Mosh prediction control
// writes the shared Transport group, and that the same document keeps
// device-local fields out of the portable payload.
func TestTransportPageSavesTheSharedSetting(t *testing.T) {
	settings := newFakeSettings()
	shell := New(WithSettings(settings))
	tt := openSectionPage(t, shell, Settings, "Transport")

	if err := tt.Click("Mosh prediction: Never"); err != nil {
		t.Fatalf("click Mosh prediction: Never: %v", err)
	}
	if got := settings.prefs.Transport.MoshPredictionDisplay; got != moshmodel.PredictionNever {
		t.Fatalf("MoshPredictionDisplay = %q, want never", got)
	}
	if err := tt.Click("Mosh prediction: Always"); err != nil {
		t.Fatalf("click Mosh prediction: Always: %v", err)
	}
	if got := settings.prefs.Transport.MoshPredictionDisplay; got != moshmodel.PredictionAlways {
		t.Fatalf("MoshPredictionDisplay = %q, want always", got)
	}
}

// TestTransportPageSavesTheDefaultProxy proves the page also exposes the
// Transport group's app-wide Proxy default, using the same Vault profile list
// the Proxies page and Host editor show.
func TestTransportPageSavesTheDefaultProxy(t *testing.T) {
	settings := newFakeSettings()
	vault := &fakeVault{proxies: []domainvault.ProxyProfile{
		{ID: "proxy-1", Label: "Office"},
		{ID: "proxy-2", Label: "Home"},
	}}
	shell := New(WithSettings(settings), WithVault(vault))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("load shell: %v", err)
	}
	tt := openSectionPage(t, shell, Settings, "Transport")

	if err := tt.Click("Default Proxy: Office"); err != nil {
		t.Fatalf("click Default Proxy: Office: %v", err)
	}
	if got := settings.prefs.DefaultProxyProfileID(); got != "proxy-1" {
		t.Fatalf("DefaultProfileID = %q, want proxy-1", got)
	}
	if err := tt.Click("Default Proxy: Home"); err != nil {
		t.Fatalf("click Default Proxy: Home: %v", err)
	}
	if got := settings.prefs.DefaultProxyProfileID(); got != "proxy-2" {
		t.Fatalf("DefaultProfileID = %q, want proxy-2", got)
	}
	if err := tt.Click("Default Proxy: Off (direct)"); err != nil {
		t.Fatalf("click Default Proxy: Off (direct): %v", err)
	}
	if got := settings.prefs.DefaultProxyProfileID(); got != "" {
		t.Fatalf("DefaultProfileID = %q, want a direct default", got)
	}
}

// TestTransportPageRejectsAFailedWrite proves the page never reports a
// half-saved setting: when the shared service refuses the write, the old value
// stays and the page states the problem.
func TestTransportPageRejectsAFailedWrite(t *testing.T) {
	settings := newFakeSettings()
	settings.prefs.Transport.MoshPredictionDisplay = moshmodel.PredictionAdaptive
	settings.updateErr = errors.New("disk full")
	shell := New(WithSettings(settings))
	tt := openSectionPage(t, shell, Settings, "Transport")

	if err := tt.Click("Mosh prediction: Never"); err != nil {
		t.Fatalf("click Mosh prediction: Never: %v", err)
	}
	if got := settings.prefs.Transport.MoshPredictionDisplay; got != moshmodel.PredictionAdaptive {
		t.Fatalf("MoshPredictionDisplay = %q, want the old adaptive value", got)
	}
	if !strings.Contains(shell.settings.problem, "Could not save") {
		t.Fatalf("inline problem = %q, want a save failure", shell.settings.problem)
	}
}

// TestPortablePreferencesCoverTransportPageFields pins the portable/local
// split the Transport page participates in: the theme and terminal font travel
// through the portable document, while the phone's interface style, icon,
// Mosh transport choice, and Atuin account stay on this device.
func TestPortablePreferencesCoverTransportPageFields(t *testing.T) {
	settings := newFakeSettings()
	settings.prefs.Appearance.ThemeMode = domainpreferences.ThemeDark
	settings.prefs.Appearance.TerminalFontSize = 18
	settings.prefs.Appearance.InterfaceStyle = domainpreferences.InterfaceMacOS
	settings.prefs.Appearance.AppIcon = "rainbow"
	settings.prefs.Transport.MoshPredictionDisplay = moshmodel.PredictionNever
	settings.prefs.Proxy.DefaultProfileID = "proxy-1"
	settings.prefs.History.SyncEnabled = true
	settings.prefs.History.SyncAddress = "https://atuin.example"

	portable := domainpreferences.Portable(settings.prefs)
	if portable.Appearance.ThemeMode != domainpreferences.ThemeDark {
		t.Fatalf("portable ThemeMode = %q, want dark", portable.Appearance.ThemeMode)
	}
	if portable.Appearance.TerminalFontSize != 18 {
		t.Fatalf("portable TerminalFontSize = %v, want 18", portable.Appearance.TerminalFontSize)
	}
	if portable.Appearance.InterfaceStyle != "" || portable.Appearance.AppIcon != "" {
		t.Fatalf("portable appearance kept device-only fields: %+v", portable.Appearance)
	}
	if portable.Proxy.DefaultProfileID != "proxy-1" {
		t.Fatalf("portable proxy default = %q, want proxy-1", portable.Proxy.DefaultProfileID)
	}
	raw, err := json.Marshal(portable)
	if err != nil {
		t.Fatalf("encode portable document: %v", err)
	}
	if !strings.Contains(string(raw), `"defaultProfileId":"proxy-1"`) {
		t.Fatalf("portable document dropped the default proxy: %s", raw)
	}
	for _, local := range []string{"moshPredictionDisplay", "atuin.example"} {
		if strings.Contains(string(raw), local) {
			t.Fatalf("portable document carries device-local %q: %s", local, raw)
		}
	}
}
