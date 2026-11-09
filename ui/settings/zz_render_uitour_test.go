//go:build uitour

package settings

import (
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// TestRenderUITour writes offscreen PNGs of every Settings area over fakes,
// for a visual review. It never touches real stores or the OS keychain.
func TestRenderUITour(t *testing.T) {
	out := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if out == "" {
		t.Skip("set NATCATTY_SCREENSHOT_DIR")
	}
	shortcuts, _ := newShortcutFixture(t)
	prefs := newFakePreferences()
	config := Config{
		Lock:     NewAppLockState(&fakeAppLock{}),
		Desktop:  NewDesktopState(newFakeDesktop()),
		Terminal: NewTerminalState(newFakeTerminal()),
		Themes:   NewThemesState(&fakeThemeCatalog{}),
		Preferences: NewPreferencesState(prefs, WithIconArtwork(func(variant string) []byte {
			data, _ := os.ReadFile(filepath.Join("..", "..", "app", "icons", "variants", variant+".png"))
			return data
		})),
		Associations: NewAssociationsState(prefs),
		Shortcuts:    shortcuts,
		Sync:         NewSyncState(newFakeSyncService()),
		MCP:          NewMCPState(&fakeMCPService{}),
		Licenses:     NewLicensesState(legalFixture()),
	}
	areas := []Area{AreaAppearance, AreaTerminal, AreaTransport, AreaLayout, AreaSFTPEditor,
		AreaLogs, AreaSystem, AreaSecurity, AreaSync, AreaShortcuts, AreaMCP, AreaAbout}
	for _, dark := range []bool{false, true} {
		preset := domaintheme.ResolvePreset(dark, domaintheme.DefaultLightTheme, domaintheme.DefaultDarkTheme)
		theme := uitheme.UI(dark, preset, uitheme.Options{})
		search := NewSearchState()
		config.Search = search
		view := ViewWithSettings(config)
		// NATCATTY_SCREENSHOT_HEIGHT renders taller pages, to see a long area whole.
		height := 700
		if value, err := strconv.Atoi(os.Getenv("NATCATTY_SCREENSHOT_HEIGHT")); err == nil {
			height = value
		}
		tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(theme); view(c) }, 1000, height)
		for _, locale := range []string{"en", "zh-CN"} {
			tt.SetLocale(locale)
			for _, area := range areas {
				search.SetArea(area)
				tt.Frame()
				tt.Frame()
				mode := "light"
				if dark {
					mode = "dark"
				}
				file, err := os.Create(filepath.Join(out, mode+"-"+locale+"-"+string(area)+".png"))
				if err != nil {
					t.Fatal(err)
				}
				if err := png.Encode(file, tt.Image()); err != nil {
					t.Fatal(err)
				}
				file.Close()
			}
		}
	}
}
