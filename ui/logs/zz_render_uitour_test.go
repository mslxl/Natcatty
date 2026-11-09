//go:build uitour

package logs

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// TestRenderUITour writes offscreen PNGs of Logs with fake records, and of the
// clear confirmation and the empty state, for a visual review.
func TestRenderUITour(t *testing.T) {
	out := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if out == "" {
		t.Skip("set NATCATTY_SCREENSHOT_DIR")
	}
	write := func(tt *ui.Tester, name string) {
		file, err := os.Create(filepath.Join(out, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := png.Encode(file, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	themed := func(dark bool, view func(*ui.Context)) func(*ui.Context) {
		preset := domaintheme.ResolvePreset(dark, domaintheme.DefaultLightTheme, domaintheme.DefaultDarkTheme)
		theme := uitheme.UI(dark, preset, uitheme.Options{})
		return func(c *ui.Context) { c.SetTheme(theme); view(c) }
	}
	state := NewState(seededSource(), nil)
	tt := ui.NewTester(themed(false, View(state)), 880, 680)
	tt.SetLocale("en")
	tt.Frame()
	write(tt, "logs-records")
	state.RequestClear()
	tt.Frame()
	write(tt, "logs-clear-confirm")
	state.CancelClear()
	tt.Frame()

	// An unset start opens a calendar and stays unset until a day is chosen.
	if err := tt.Click("From"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	write(tt, "logs-calendar-unset")
	if *state.From() != "" {
		t.Fatalf("opening the calendar set the start to %q", *state.From())
	}
	if err := tt.Click("12"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if *state.From() == "" || !strings.HasSuffix(*state.From(), "-12") {
		t.Fatalf("choosing the 12th set the start to %q", *state.From())
	}

	// A set start shows the regional date field; clicking it opens the calendar.
	state.SetDateRange("2026-11-01", "")
	tt.Frame()
	write(tt, "logs-dates")
	if err := tt.Click("From"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	write(tt, "logs-calendar")

	empty := NewState(&fakeSource{}, nil)
	te := ui.NewTester(themed(true, View(empty)), 880, 680)
	te.SetLocale("zh-CN")
	te.Frame()
	write(te, "logs-empty-zh-dark")
}
