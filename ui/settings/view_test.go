package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestViewLocalizes(t *testing.T) {
	tt := ui.NewTester(View, 600, 400)

	tt.SetLocale("ru")
	if !tt.HasText("Настройки") {
		t.Errorf("Russian settings window does not show Настройки, texts %q", tt.Texts())
	}
}
