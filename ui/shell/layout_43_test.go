package shell

import (
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// TestSidebarLanguageOptionsFitAtMinimumClient is the ticket-43 regression for
// the shell sidebar at the app's enforced minimum window: a 720x480 window has
// a 704x421 client area. At 0fd22027 the Language group ended at y=540, 28 DIPs
// below the client, so the last language option was unreachable. The picker
// is now a button in the window's Tab bar opening a menu of every language;
// the button must stay inside the bar and every language must be choosable
// from it.
func TestLanguagePickerFitsAtMinimumClient(t *testing.T) {
	const width, height = 704, 421
	var requested []string
	shellState := state.NewState(state.WithLocaleRequest(func(tag string) {
		requested = append(requested, tag)
	}))
	tester := ui.NewTester(View(shellState), width, height)
	tester.SetLocale("en")
	tester.Frame()
	picker := locales.In("en").NavLanguage()
	rect, ok := tester.Find(picker)
	if !ok || rect.W <= 0 || rect.H <= 0 {
		t.Fatalf("the Tab bar lost the language picker: %+v present=%v: %v",
			rect, ok, tester.Texts())
	}
	// The picker must stay in the Tab bar's own box, not merely somewhere
	// inside the client.
	if rect.X < -0.5 || rect.X+rect.W > width+0.5 ||
		rect.Y < -0.5 || rect.Y+rect.H > topBarHeight+0.5 {
		t.Fatalf("the language picker is clipped in the Tab bar at %dx%d: %+v",
			width, height, rect)
	}
	for _, tag := range locales.Tags() {
		label := i18n.Autonym(tag)
		if err := tester.Click(picker); err != nil {
			t.Fatalf("the language picker is not clickable: %v", err)
		}
		if err := tester.ChooseMenuItem(label); err != nil {
			t.Fatalf("the %s language is not in the picker's menu: %v", label, err)
		}
		tester.Frame()
	}
	// Clicking the active language is a no-op; every other option must reach
	// the shell's locale request.
	want := locales.Tags()[1:]
	if len(requested) != len(want) {
		t.Fatalf("requested locales = %v, want all %v", requested, want)
	}
	for index, tag := range want {
		if requested[index] != tag {
			t.Fatalf("requested locales = %v, want %v", requested, want)
		}
	}
}
