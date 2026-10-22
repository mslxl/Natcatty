//go:build selftest

package vault

import "github.com/egoist/mygo/ui"

// SelftestWatchExportLayout sets fn to receive the secrets toggle's and the
// scrolling export panel's laid-out bounds on each build, and returns a
// function that restores the previous observer. It exists only in selftest
// builds, so the self-test can assert the panel clears the scroll bar without
// widening the production API.
func SelftestWatchExportLayout(fn func(secrets, viewport ui.Rect, gutter float32)) (restore func()) {
	previous := exportLayoutProbe
	exportLayoutProbe = func(layout exportFieldLayout) {
		fn(layout.secrets, layout.viewport, layout.gutter)
	}
	return func() { exportLayoutProbe = previous }
}
