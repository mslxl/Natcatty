package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/phoneshell"
)

// phonePage opens one section page and returns its tester once the route
// push has settled. A push slides the new page in; advancing the tester's
// clock puts it where a user would see it before the test taps inside it.
func phonePage(t *testing.T, shell *phoneshell.Shell, sec phoneshell.Section, title string) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(shell.View(sec), 390, 844)
	if err := tt.Click(title); err != nil {
		t.Fatalf("click %q: %v", title, err)
	}
	tt.Advance(600 * time.Millisecond)
	return tt
}
