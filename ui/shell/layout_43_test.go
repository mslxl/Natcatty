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
// below the client, so the last language option was unreachable. Ticket 61
// tracks this cross-cutting shell defect; this test keeps it fixed for 43.
func TestSidebarLanguageOptionsFitAtMinimumClient(t *testing.T) {
	const width, height = 704, 421
	var requested []string
	shellState := state.NewState(state.WithLocaleRequest(func(tag string) {
		requested = append(requested, tag)
	}))
	tester := ui.NewTester(View(shellState), width, height)
	tester.SetLocale("en")
	tester.Frame()
	for _, tag := range locales.Tags() {
		label := i18n.Autonym(tag)
		rect, ok := tester.Find(label)
		if !ok || rect.W <= 0 || rect.H <= 0 {
			t.Fatalf("the sidebar lost the %s language option: %+v present=%v: %v",
				label, rect, ok, tester.Texts())
		}
		// The options must stay in the sidebar's own content box, not merely
		// somewhere inside the client: wrapping into the content pane would
		// still be unreachable as a language control.
		if rect.X < shellPadding-0.5 || rect.X+rect.W > sidebarWidth+0.5 ||
			rect.Y < -0.5 || rect.Y+rect.H > height+0.5 {
			t.Fatalf("the %s language option is clipped in the sidebar at %dx%d: %+v",
				label, width, height, rect)
		}
		if err := tester.Click(label); err != nil {
			t.Fatalf("the %s language option is not clickable: %v", label, err)
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
