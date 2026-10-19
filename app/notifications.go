package app

import (
	"fmt"
	"runtime"

	"github.com/egoist/mygo"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	"github.com/mslxl/natcatty/ui/locales"
)

// nativeNotifier turns typed desktop events into localized MyGo
// notifications. MyGo registers the click handler with the system; clicking a
// notification raises the main window.
type nativeNotifier struct {
	locale  func() string
	onClick func()
}

func (n *nativeNotifier) Supported() bool {
	return mygo.NotificationsSupported()
}

func (n *nativeNotifier) Notify(request appdesktop.Notification) error {
	m := locales.In(n.locale())
	target := request.Target
	if target == "" {
		target = m.NotificationUnknownTarget()
	}
	var title, body string
	switch request.Event {
	case appdesktop.EventSessionEnded:
		title = m.NotificationSessionEndedTitle()
		body = m.NotificationSessionEndedBody(target)
	case appdesktop.EventConnectionFailed:
		title = m.NotificationConnectionFailedTitle()
		body = m.NotificationConnectionFailedBody(target)
	case appdesktop.EventForwardingFailed:
		title = m.NotificationForwardingFailedTitle()
		body = m.NotificationForwardingFailedBody(target)
	default:
		return fmt.Errorf("notification: unknown event %q", request.Event)
	}
	notification := mygo.NewNotification(mygo.NotificationOptions{
		ID:    string(request.Event) + ":" + target,
		Title: title,
		Body:  body,
		Group: "natcatty",
	})
	if n.onClick != nil {
		notification.OnClick(n.onClick)
	}
	return notification.Show()
}

// autoLauncher adapts MyGo's login-item API. MyGo strips its own login marker
// from os.Args; WasOpenedAtLogin reports whether the system started the app.
// The original app offers this on macOS and Windows only: Linux packages have
// no first-party autostart hook, so reporting support there would show a
// toggle that does nothing.
type autoLauncher struct{}

func (autoLauncher) Supported() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	default:
		return false
	}
}

func (autoLauncher) Enabled() (bool, error) { return mygo.App.OpenAtLogin(), nil }
func (autoLauncher) SetEnabled(enabled bool) error {
	return mygo.App.SetOpenAtLogin(enabled)
}

// hotkeyRegistrar adapts MyGo's GlobalShortcut module.
type hotkeyRegistrar struct{}

func (hotkeyRegistrar) Register(accelerator string, fn func()) error {
	return mygo.GlobalShortcut.Register(accelerator, fn)
}

func (hotkeyRegistrar) Unregister(accelerator string) {
	mygo.GlobalShortcut.Unregister(accelerator)
}
