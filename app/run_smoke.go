//go:build launchsmoke

package app

import (
	"fmt"
	"os"
	"time"

	"github.com/egoist/mygo"
)

// launchSmokeReady is printed once the main window exists. The launch smoke
// test looks for it to prove that readiness and window creation succeeded, not
// merely that the process exited.
const launchSmokeReady = "natcatty: launch smoke ready"

// RunSmoke starts the app, reports readiness, and quits shortly after. It is
// built only with the launchsmoke tag so no test-only behavior ships in the
// normal app.
func RunSmoke() error {
	return run(func(win hostWindow) {
		if !win.IsVisible() {
			fmt.Fprintln(os.Stderr, "launch smoke: the main window is not visible")
			mygo.App.Quit()
			return
		}
		fmt.Println(launchSmokeReady)
		time.AfterFunc(500*time.Millisecond, mygo.App.Quit)
	})
}
