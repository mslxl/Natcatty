//go:build ios || android

package app

import (
	"context"
	"path/filepath"

	"github.com/egoist/mygo"

	fileapplock "github.com/mslxl/natcatty/adapters/applock"
	appapplock "github.com/mslxl/natcatty/application/applock"
)

// newPhoneAppLock opens the phone's App Lock settings next to the Vault and
// loads them before the shell is shown, so an enabled App Lock starts locked
// before the first frame. The file format and the state machine are the same
// ones the desktop uses; only the gate that renders them is the phone's.
//
// The redraw hook repaints every phone window when the gate closes (an idle
// lock) or opens (an unlock), so no user action is needed to see the change.
func newPhoneAppLock(ctx context.Context, userData string) (*appapplock.Service, error) {
	service := appapplock.NewService(
		fileapplock.Open(filepath.Join(userData, "app-lock-settings.json")),
		appapplock.WithRedrawRequest(func() {
			for _, window := range mygo.Windows() {
				window.Invalidate()
			}
		}),
	)
	if err := service.Load(ctx); err != nil {
		return nil, err
	}
	return service, nil
}
