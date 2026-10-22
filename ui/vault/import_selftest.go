//go:build selftest

package vault

import "github.com/egoist/mygo/ui"

// SelftestWatchImportLayout sets fn to receive the master-password input's
// and the scrolling import panel's laid-out bounds on each build, and returns
// a function that restores the previous observer. It exists only in selftest
// builds, so the self-test can assert the field clears the scroll bar without
// widening the production API.
func SelftestWatchImportLayout(fn func(master, viewport ui.Rect, gutter float32)) (restore func()) {
	previous := importLayoutProbe
	importLayoutProbe = func(layout importFieldLayout) {
		fn(layout.master, layout.viewport, layout.gutter)
	}
	return func() { importLayoutProbe = previous }
}
