//go:build uitour

package settings

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domain "github.com/mslxl/natcatty/domain/preferences"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// renderSide writes a side panel tool to NATCATTY_SCREENSHOT_DIR in both
// themes, at the panel's width on the terminal's background.
func renderSide(t *testing.T, name string, view func(*ui.Context), settle func(*ui.Tester)) {
	t.Helper()
	out := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if out == "" {
		t.Skip("set NATCATTY_SCREENSHOT_DIR")
	}
	for _, dark := range []bool{false, true} {
		preset := domaintheme.ResolvePreset(dark, domaintheme.DefaultLightTheme, domaintheme.DefaultDarkTheme)
		theme := uitheme.UI(dark, preset, uitheme.Options{})
		tt := ui.NewTester(func(c *ui.Context) {
			c.SetTheme(theme)
			ui.Column(c).Fill().Background(c.Theme().Background).Children(func() { view(c) })
		}, 360, 620)
		tt.SetLocale("en")
		tt.Frame()
		if settle != nil {
			settle(tt)
		}
		tt.Frame()
		mode := "light"
		if dark {
			mode = "dark"
		}
		file, err := os.Create(filepath.Join(out, "side-"+name+"-"+mode+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, tt.Image()); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
}

// TestRenderSideUITour renders the Theme side tool with a fake catalog.
func TestRenderSideUITour(t *testing.T) {
	catalog := &fakeThemeCatalog{themes: []domaintheme.TerminalTheme{
		{ID: domain.DefaultTerminalTheme, Name: "Default Scheme"},
		{ID: "solarized", Name: "Solarized Dark"},
		{ID: "paper", Name: "Paper Light", Custom: true},
	}}
	look := &fakeLook{settings: domain.Defaults()}
	renderSide(t, "theme", NewThemePanel(NewThemesState(catalog), look).View(), nil)
}
