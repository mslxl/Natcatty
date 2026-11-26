//go:build !natcatty_smoke

package phoneshell

import "github.com/egoist/mygo/ui"

// smokeRoute is the hook a natcatty_smoke build fills with the terminal
// diagnostic. A normal phone build has no route for it.
func smokeRoute(*ui.Context, *ui.Route) bool { return false }
