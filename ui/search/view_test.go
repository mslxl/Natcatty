package search

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	appsearch "github.com/mslxl/natcatty/application/search"
)

func overlayView(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		ui.Column(c).Fill().Padding(8).Children(func() {
			ui.Text(c, "Shell")
		})
		Overlay(s)(c)
	}
}

func TestOverlayListsSectionsAndKeyboardOpensTheChosenRow(t *testing.T) {
	var opened []string
	state := newTestState(t, &opened)
	state.Open(ModeQuickSwitcher)
	tester := ui.NewTester(overlayView(state), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	if !tester.HasText("Jump To") {
		t.Fatalf("the overlay did not render: %v", tester.Texts())
	}
	for _, want := range []string{"Hosts", "Sessions", "web", "web session"} {
		if !tester.HasText(want) {
			t.Fatalf("missing %q in %v", want, tester.Texts())
		}
	}

	tester.Type("db")
	tester.Frame()
	if tester.HasText("web session") {
		t.Fatalf("the query did not filter Sessions: %v", tester.Texts())
	}
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if len(opened) != 1 || opened[0] != "h2" {
		t.Fatalf("opened = %v, want h2", opened)
	}
	if state.IsOpen() {
		t.Fatal("the overlay stayed open after Enter")
	}
}

func TestOverlayArrowsMoveAndClamp(t *testing.T) {
	var opened []string
	state := newTestState(t, &opened)
	state.Open(ModeQuickSwitcher)
	tester := ui.NewTester(overlayView(state), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	// Three rows: moving up from the first stays there, and two downs reach
	// the Session. Moving past the end stays on it.
	tester.Key(0, ui.KeyUp)
	tester.Frame()
	tester.Key(0, ui.KeyDown)
	tester.Key(0, ui.KeyDown)
	tester.Key(0, ui.KeyDown)
	tester.Key(0, ui.KeyDown)
	tester.Frame()
	if state.Selected() != 2 {
		t.Fatalf("selected = %d, want the last row", state.Selected())
	}
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if len(opened) != 1 || opened[0] != "s1" {
		t.Fatalf("opened = %v, want s1", opened)
	}
}

func TestOverlayEscapeClosesAndClears(t *testing.T) {
	state := newTestState(t, new([]string))
	state.Open(ModeQuickSwitcher)
	tester := ui.NewTester(overlayView(state), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	tester.Type("web")
	tester.Frame()

	tester.Key(0, ui.KeyEscape)
	tester.Frame()
	if state.IsOpen() {
		t.Fatal("Escape did not close the overlay")
	}
	if state.Query() != "" {
		t.Fatalf("query = %q after Escape, want empty", state.Query())
	}
	if tester.HasText("Jump To") {
		t.Fatal("the closed overlay still renders")
	}
}

func TestOverlayEmptyAndNoResultStates(t *testing.T) {
	empty := New(WithSource(func(Mode, string) []appsearch.Record { return nil }))
	empty.Open(ModeQuickSwitcher)
	tester := ui.NewTester(overlayView(empty), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	if !tester.HasText("Nothing to switch to yet") {
		t.Fatalf("missing the empty state: %v", tester.Texts())
	}

	// The palette's empty state names commands; the Quick Switcher's copy
	// would tell the user to add a Host.
	palette := New(WithSource(func(Mode, string) []appsearch.Record { return nil }))
	palette.Open(ModeCommandPalette)
	tester = ui.NewTester(overlayView(palette), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	if !tester.HasText("No commands available") {
		t.Fatalf("missing the palette empty state: %v", tester.Texts())
	}
	if tester.HasText("Nothing to switch to yet") {
		t.Fatalf("the palette shows the Quick Switcher's empty copy: %v", tester.Texts())
	}

	state := newTestState(t, new([]string))
	state.Open(ModeQuickSwitcher)
	state.QueryPtr()
	*state.QueryPtr() = "zzz"
	tester = ui.NewTester(overlayView(state), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	if !tester.HasText("No matches") {
		t.Fatalf("missing the no-result state: %v", tester.Texts())
	}
	if !tester.HasText("Nothing matches “zzz”.") {
		t.Fatalf("missing the no-result detail: %v", tester.Texts())
	}
}

func TestCommandPaletteGroupsByCategoryAndExecutes(t *testing.T) {
	var opened []string
	state := newTestState(t, &opened)
	state.Open(ModeCommandPalette)
	tester := ui.NewTester(overlayView(state), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	for _, want := range []string{"Tabs", "Application", "New Tab", "Settings"} {
		if !tester.HasText(want) {
			t.Fatalf("missing %q in %v", want, tester.Texts())
		}
	}
	tester.Type("settings")
	tester.Frame()
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if len(opened) != 1 || opened[0] != "app.settings" {
		t.Fatalf("opened = %v, want app.settings", opened)
	}
}

// TestOverlayFitsBothWindowSizesAndLanguages checks every visible label stays
// inside the window at the minimum and default sizes, in English and Chinese,
// and writes the frames for visual review.
func TestOverlayFitsBothWindowSizesAndLanguages(t *testing.T) {
	outDir := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "natcatty-search-screenshots")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range [][2]int{{1100, 720}, {720, 480}} {
			state := newTestState(t, new([]string))
			state.Open(ModeQuickSwitcher)
			tester := ui.NewTester(overlayView(state), size[0], size[1])
			tester.SetLocale(locale)
			tester.Frame()

			labels := []string{"Jump To", "Jump", "Hosts", "Sessions", "web", "db", "web session"}
			if locale != "en" {
				labels = []string{"跳转到", "主机", "会话", "web", "db", "web session"}
			}
			for _, label := range labels {
				rect, ok := tester.Find(label)
				if !ok {
					continue
				}
				if rect.X < 0 || rect.Y < 0 ||
					rect.X+rect.W > float32(size[0]) || rect.Y+rect.H > float32(size[1]) {
					t.Fatalf("%s %dx%d: %q at %+v is outside the window",
						locale, size[0], size[1], label, rect)
				}
			}

			path := filepath.Join(outDir, "search-"+locale+"-"+itoa(size[0])+"x"+itoa(size[1])+".png")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(file, tester.Image()); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			t.Logf("screenshot: %s", path)
		}
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
