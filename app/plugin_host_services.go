package app

import (
	"context"
	"errors"
	"runtime"

	"github.com/egoist/mygo"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/ui/locales"
)

// pluginClipboard adapts the MyGo clipboard to the host-service port. The
// clipboard API returns empty text when nothing is copied, which is a valid
// empty string rather than an error.
type pluginClipboard struct{}

var _ appplugin.Clipboard = pluginClipboard{}

func (pluginClipboard) ReadText(context.Context) (string, error) {
	return mygo.Clipboard.ReadText(), nil
}

func (pluginClipboard) WriteText(_ context.Context, text string) error {
	mygo.Clipboard.WriteText(text)
	return nil
}

// pluginHostSettings exposes a small, read-only, key-scoped namespace. Plugins
// cannot enumerate keys and there is no write path.
type pluginHostSettings struct {
	locale  func() string
	version string
}

var _ appplugin.HostSettings = pluginHostSettings{}

func (s pluginHostSettings) Get(_ context.Context, key string) (string, bool, error) {
	switch key {
	case "app.locale":
		if s.locale == nil {
			return "", false, nil
		}
		return s.locale(), true, nil
	case "app.version":
		if s.version == "" {
			return "", false, nil
		}
		return s.version, true, nil
	case "app.platform":
		return runtime.GOOS + "/" + runtime.GOARCH, true, nil
	default:
		return "", false, nil
	}
}

// pluginNotificationSink shows plugin notifications through MyGo. Actionable
// notifications open a dialog when the user clicks the notification; the
// chosen action is routed back through the manager as a host event. Post never
// blocks a guest call on the UI thread.
type pluginNotificationSink struct {
	manager func() *appplugin.Manager
	locale  func() string
}

var _ appplugin.NotificationSink = (*pluginNotificationSink)(nil)

func (s *pluginNotificationSink) Post(_ context.Context, notification appplugin.Notification) error {
	if !mygo.NotificationsSupported() {
		return errors.New("plugin notifications: desktop notifications are unavailable on this platform")
	}
	mygo.RunOnMain(func() {
		shown := mygo.NewNotification(mygo.NotificationOptions{
			ID:    "plugin:" + notification.ID,
			Title: notification.Title,
			Body:  notification.Body,
			Group: "natcatty",
		})
		if len(notification.Actions) > 0 {
			shown.OnClick(func() { s.chooseAction(notification) })
		}
		_ = shown.Show()
	})
	return nil
}

// chooseAction runs on the UI thread from the notification click handler.
func (s *pluginNotificationSink) chooseAction(notification appplugin.Notification) {
	m := locales.In("en")
	if s.locale != nil {
		m = locales.In(s.locale())
	}
	buttons := make([]string, 0, len(notification.Actions)+1)
	for _, action := range notification.Actions {
		buttons = append(buttons, action.Label)
	}
	buttons = append(buttons, m.SettingsPluginsCancel())
	result, err := mygo.Dialog.Message(mygo.MessageOptions{
		Type:          mygo.MessageQuestion,
		Title:         notification.Title,
		Message:       notification.Body,
		Buttons:       buttons,
		DefaultButton: 0,
		CancelButton:  len(notification.Actions),
	})
	if err != nil || result.Button < 0 || result.Button >= len(notification.Actions) {
		return
	}
	if manager := s.manager(); manager != nil {
		_ = manager.NotificationAction(notification.ID, notification.Actions[result.Button].ID)
	}
}
