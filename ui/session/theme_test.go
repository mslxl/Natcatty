package sessionui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

func TestTerminalThemeOptionMapsTheSelectedScheme(t *testing.T) {
	preset, ok := domaintheme.LookupTerminalTheme("netcatty-dark")
	if !ok {
		t.Fatal("netcatty-dark missing")
	}
	surface := &Surface{options: Options{Theme: func() domaintheme.TerminalTheme {
		return preset
	}}}
	theme := surface.terminalTheme()
	if theme == nil {
		t.Fatal("theme option produced no MyGo theme")
	}
	if theme.Background != ui.Hex("#0d1117") || theme.Foreground != ui.Hex("#c9d1d9") {
		t.Errorf("colors = %v / %v", theme.Background, theme.Foreground)
	}
	if theme.Palette[1] != ui.Hex("#ff7b72") {
		t.Errorf("red = %v", theme.Palette[1])
	}
}

func TestTerminalThemeOptionIsOptional(t *testing.T) {
	if got := (&Surface{}).terminalTheme(); got != nil {
		t.Errorf("missing option = %+v", got)
	}
	empty := &Surface{options: Options{Theme: func() domaintheme.TerminalTheme {
		return domaintheme.TerminalTheme{}
	}}}
	if got := empty.terminalTheme(); got != nil {
		t.Errorf("empty scheme = %+v", got)
	}
}
