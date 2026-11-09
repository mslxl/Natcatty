//go:build uitour

package monitoring

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// TestRenderUITour writes offscreen PNGs of Monitoring with fake panels and
// hosts, inside a card like the shell's, for a visual review.
func TestRenderUITour(t *testing.T) {
	out := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if out == "" {
		t.Skip("set NATCATTY_SCREENSHOT_DIR")
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	hosts := []domainvault.Host{
		testHost("h1", "Alpha", "alpha.example", "prod", "web"),
		testHost("h2", "Beta", "beta.example", "dev", "db"),
	}
	render := func(name string, dark bool, monitor domainpreferences.Monitor, start bool) {
		service := newFakeService()
		service.snapshot = liveSnapshot(now, hosts...)
		state := NewState(service, &fakeHosts{items: hosts}, newFakePrefs(monitor), nil,
			WithClock(func() time.Time { return now }))
		defer state.Close()
		state.Activate()
		if start {
			state.Start()
		}
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
	panel := domainpreferences.Monitor{Panels: []domainpreferences.MonitorPanel{{
		ID: "ops", Name: "Operations", HostIDs: []string{"h1", "h2"},
		View: domainpreferences.MonitorViewCharts, Interval: 5, Range: 30,
		Sort: domainpreferences.MonitorSortManual,
	}}}
	render("monitor-empty-light", false, domainpreferences.Monitor{}, false)
	render("monitor-panel-light", false, panel, true)
	render("monitor-panel-dark", true, panel, true)
}
