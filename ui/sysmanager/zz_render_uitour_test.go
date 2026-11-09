//go:build uitour

package sysmanager

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// TestRenderUITour writes offscreen PNGs of the System manager with a fake
// service, live and connecting, inside a card like the shell's.
func TestRenderUITour(t *testing.T) {
	out := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if out == "" {
		t.Skip("set NATCATTY_SCREENSHOT_DIR")
	}
	render := func(name string, dark bool, snapshot appsysmanager.Snapshot) {
		service := newFakeService()
		service.snapshot = snapshot
		state := newTestState(service, newFakePreferences())
		state.Activate()
		preset := domaintheme.ResolvePreset(dark, domaintheme.DefaultLightTheme, domaintheme.DefaultDarkTheme)
		theme := uitheme.UI(dark, preset, uitheme.Options{})
		view := View(state)
		tt := ui.NewTester(func(c *ui.Context) {
			c.SetTheme(theme)
			t := c.Theme()
			ui.Column(c).Fill().Padding(8).Background(t.Background).Children(func() {
				ui.Column(c).Fill().Padding(8).Background(t.Surface).Border(1, t.Border).Radius(10).Clip().
					Children(func() { view(c) })
			})
		}, 1100, 720)
		tt.SetLocale("en")
		tt.Frame()
		tt.Frame()
		file, err := os.Create(filepath.Join(out, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := png.Encode(file, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	connecting := newFakeService().snapshot
	connecting.Tabs[appsysmanager.TabOverview] = appsysmanager.TabState{
		Tab: appsysmanager.TabOverview, Status: appsysmanager.StatusConnecting}
	render("system-live-light", false, demoSnapshot())
	render("system-live-dark", true, demoSnapshot())
	render("system-connecting-light", false, connecting)
}
