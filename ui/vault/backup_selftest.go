//go:build selftest

package vault

import "github.com/egoist/mygo/ui"

// SelftestWatchBackupLayout sets fn to receive the first row's restore action,
// the scrolling list's bounds, and the gutter reserved for the overlay scroll
// bar on each build, and returns a function that restores the previous
// observer. It exists only in selftest builds.
func SelftestWatchBackupLayout(fn func(action, viewport ui.Rect, gutter float32)) (restore func()) {
	previous := backupLayoutProbe
	backupLayoutProbe = func(layout backupPanelLayout) {
		fn(layout.action, layout.viewport, layout.gutter)
	}
	return func() { backupLayoutProbe = previous }
}
