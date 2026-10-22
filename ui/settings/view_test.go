package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestViewLocalizes(t *testing.T) {
	tt := ui.NewTester(View(NewAppLockState(&fakeAppLock{})), 600, 400)

	tt.SetLocale("ru")
	if !tt.HasText("Настройки") {
		t.Errorf("Russian settings window does not show Настройки, texts %q", tt.Texts())
	}
}

// viewAt renders one area directly, as a user reaches it from the sidebar.
func viewAt(area Area, config Config) func(*ui.Context) {
	search := NewSearchState()
	search.SetArea(area)
	config.Search = search
	return ViewWithSettings(config)
}
