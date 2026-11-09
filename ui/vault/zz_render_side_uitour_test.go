//go:build uitour

package vault

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainvault "github.com/mslxl/natcatty/domain/vault"
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

// TestRenderSideUITour renders the Scripts and Notes side tools with fakes.
func TestRenderSideUITour(t *testing.T) {
	state, _, runner, _ := newSnippetView(t, nil,
		domainvault.Snippet{ID: "s1", Label: "Disk usage", Command: "df -h", Package: "ops"},
		domainvault.Snippet{ID: "s2", Label: "Restart", Command: "systemctl restart {{service:nginx}}"},
	)
	runner.hasHost = true
	renderSide(t, "scripts", NewSidePanels(state, nil).Scripts(), nil)

	notes, _ := newNoteProductionState(t)
	notes.BeginCreateNote()
	notes.NoteForm().Title = "Runbook"
	notes.NoteForm().Content = "# Restart\n\nRun **deploy**."
	if err := notes.SaveNote(); err != nil {
		t.Fatal(err)
	}
	renderSide(t, "notes", NewSidePanels(notes, nil).Notes(), nil)
}
