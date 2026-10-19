package app

import (
	"fmt"
	"runtime"

	"github.com/egoist/mygo"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
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

// notificationAllowed applies the original OSC notification modes. The
// unfocused mode suppresses notifications only while the main window is
// visible and focused.
func notificationAllowed(mode domainterminal.NotificationMode, focused bool) bool {
	switch mode {
	case domainterminal.NotificationOff:
		return false
	case domainterminal.NotificationUnfocused:
		return !focused
	default:
		return true
	}
}

// showProgramNotification displays an OSC 9/777/99 notification. The title and
// body are exactly what the terminal displays; the tap has already stripped
// control characters and capped their length.
func showProgramNotification(notification appsession.TerminalNotification, onClick func()) error {
	if !mygo.NotificationsSupported() {
		return nil
	}
	title := notification.Title
	if title == "" {
		title = "Netcatty"
	}
	shown := mygo.NewNotification(mygo.NotificationOptions{
		ID:    fmt.Sprintf("program:%d:%s", notification.At.UnixNano(), title),
		Title: title,
		Body:  notification.Body,
		Group: "natcatty",
	})
	if onClick != nil {
		shown.OnClick(onClick)
	}
	return shown.Show()
}

// deliverProgramNotification applies the user's mode and focus policy on the
// UI thread, where the main window's focus state is current.
func deliverProgramNotification(mode domainterminal.NotificationMode,
	notification appsession.TerminalNotification, focused func() bool, onClick func()) {
	if mode == domainterminal.NotificationOff {
		return
	}
	mygo.RunOnMain(func() {
		if notificationAllowed(mode, focused != nil && focused()) {
			_ = showProgramNotification(notification, onClick)
		}
	})
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
