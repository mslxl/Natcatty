package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/internal/styletest"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestNavColumnFollowsTheOriginal keeps the original's Settings layout: the
// header row holds only the title, and a 224-wide nav column holds the search
// at its top, then the areas, then resetting everything at its foot.
func TestNavColumnFollowsTheOriginal(t *testing.T) {
	styletest.Each(t, func(t *testing.T) {
		m := locales.In("en")
		prefs := NewPreferencesState(newFakePreferences())
		tester := ui.NewTester(ViewWithSettings(Config{Preferences: prefs}), 1100, 720)
		tester.SetLocale("en")
		tester.Frame()

		title, ok := tester.Find(m.NavSettings())
		if !ok {
			t.Fatalf("no title: %v", tester.Texts())
		}
		search, ok := tester.Find(m.SettingsSearchPlaceholder())
		if !ok {
			t.Fatalf("no search field: %v", tester.Texts())
		}
		// The column's content box: Classic pads the column by 12; the
		// macOS panel floats 8 in from the window and pads itself by 8.
		left, right := float32(12), float32(224-12)
		if kit.CurrentStyle().Native() {
			left, right = 16, 224-8
		}
		if search.Y <= title.Y+title.H || search.X < left-0.5 || search.X+search.W > right+0.5 {
			t.Fatalf("the search %+v is not at the top of the nav column", search)
		}
		reset, ok := tester.Find(m.SettingsResetAll())
		if !ok {
			t.Fatalf("no reset-all: %v", tester.Texts())
		}
		if reset.Y < 600 || reset.X+reset.W > 224 {
			t.Fatalf("reset-all %+v is not at the nav column's foot", reset)
		}
		previous := search
		for _, area := range Areas() {
			item, ok := tester.Find(AreaTitle(m, area))
			if !ok {
				t.Fatalf("no nav item for %q", area)
			}
			if item.X < left-0.5 || item.X+item.W > 224 || item.Y <= previous.Y {
				t.Fatalf("nav item %q at %+v is out of the column's order", area, item)
			}
			previous = item
		}
	})
}
