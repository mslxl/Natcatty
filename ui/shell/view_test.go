package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/shell/state"
)

func TestShellShowsVaultAndSettingsNavigation(t *testing.T) {
	shellState := state.NewState()
	tt := ui.NewTester(View(shellState), 900, 600)

	if !tt.HasText("Vault") {
		t.Errorf("shell does not show Vault navigation, texts %q", tt.Texts())
	}
	if !tt.HasText("Settings") {
		t.Errorf("shell does not show Settings navigation, texts %q", tt.Texts())
	}
	if !tt.HasText("Your Hosts, keys, and notes will appear here.") {
		t.Errorf("shell does not show the Vault surface, texts %q", tt.Texts())
	}
}

func TestShellSettingsNavigationRequestsTheSettingsWindow(t *testing.T) {
	var requests int
	shellState := state.NewState(state.WithSettingsRequest(func() { requests++ }))
	tt := ui.NewTester(View(shellState), 900, 600)

	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("settings requests = %d after clicking Settings, want 1", requests)
	}
}

func TestShellSwitchesAllFiveLocalesWithoutRestart(t *testing.T) {
	shellState := state.NewState()
	tt := ui.NewTester(View(shellState), 900, 600)

	tests := []struct {
		tag      string
		settings string
	}{
		{"en", "Settings"},
		{"es", "Configuración"},
		{"ru", "Настройки"},
		{"zh-CN", "设置"},
		{"zh-TW", "設定"},
	}
	for _, test := range tests {
		tt.SetLocale(test.tag)
		if !tt.HasText(test.settings) {
			t.Errorf("locale %s does not show %q, texts %q", test.tag, test.settings, tt.Texts())
		}
	}
}

func TestShellShowsWorkTabs(t *testing.T) {
	shellState := state.NewState()
	tt := ui.NewTester(View(shellState), 900, 600)

	shellState.NewTab()
	shellState.NewTab()
	tt.Frame()
	for _, want := range []string{"Tab 1", "Tab 2"} {
		if !tt.HasText(want) {
			t.Errorf("shell does not show %q, texts %q", want, tt.Texts())
		}
	}

	shellState.CloseActiveTab()
	tt.Frame()
	if tt.HasText("Tab 2") {
		t.Errorf("shell still shows closed Tab 2, texts %q", tt.Texts())
	}
	if !tt.HasText("Tab 1") {
		t.Errorf("shell does not show Tab 1 after closing Tab 2, texts %q", tt.Texts())
	}
}

func TestShellLanguageControlRequestsLocale(t *testing.T) {
	var requested []string
	shellState := state.NewState(state.WithLocaleRequest(func(tag string) {
		requested = append(requested, tag)
	}))
	tt := ui.NewTester(View(shellState), 900, 600)

	if err := tt.Click("español"); err != nil {
		t.Fatal(err)
	}
	if len(requested) != 1 || requested[0] != "es" {
		t.Fatalf("requested locales = %v, want [es]", requested)
	}
}
