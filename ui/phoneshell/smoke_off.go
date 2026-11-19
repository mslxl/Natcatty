//go:build !natcatty_smoke

package phoneshell

import "github.com/egoist/mygo/ui"

// smokePages and smokeRoute are the hooks a natcatty_smoke build fills with
// the terminal diagnostic. A normal phone build has neither a route nor a
// page for it.
func smokePages(Section) []page { return nil }

func smokeRoute(*ui.Context, *ui.Route) bool { return false }
