//go:build uitour

package transfer

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
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

// TestRenderSideUITour renders the SFTP side tool over temporary directories.
func TestRenderSideUITour(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(remote, "deploy.sh"), "#!/bin/sh")
	writeFile(t, filepath.Join(remote, "README.md"), "readme")
	state := transferstate.NewState(newViewService(local, remote))
	t.Cleanup(state.Shutdown)
	state.ShowRemote(transferstate.Left, "h1", "prod-web-01")
	renderSide(t, "sftp", SidePanel(state, 360), func(tt *ui.Tester) {
		frameUntil(t, tt, "the remote listing", func() bool {
			tab := state.Tab(transferstate.Left)
			return tab != nil && tab.FS != nil && !tab.Loading
		})
	})
}
