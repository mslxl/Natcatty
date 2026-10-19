// Package desktop holds the use cases behind the operating-system integration:
// tray and close-to-tray behavior, deep links and command-line Host targets,
// notifications, the global toggle hotkey, and launch-at-login autostart. It
// depends only on consumer-owned ports, so the composition root can supply
// MyGo, fakes, or a headless host.
package desktop

import "strings"

// SettingsVersion is the on-disk desktop settings version. A record without
// this version predates the feature (or is an empty object), so it loads as
// defaults instead of turning every switch off.
const SettingsVersion = 1

// Settings is the persisted desktop integration configuration. The original
// app stored these values under separate keys; the rewrite keeps one versioned
// record.
type Settings struct {
	Version int `json:"version"`

	// ShowTrayIcon controls whether Natcatty keeps its notification-area
	// icon. It defaults to true.
	ShowTrayIcon bool `json:"showTrayIcon"`
	// CloseToTray hides the main window instead of closing it while the tray
	// icon is available. It defaults to true, as in the original app.
	CloseToTray bool `json:"closeToTray"`
	// GlobalHotkeyEnabled turns the system-wide toggle shortcut on or off.
	GlobalHotkeyEnabled bool `json:"globalHotkeyEnabled"`
	// ToggleWindowHotkey is the accelerator that shows, focuses, or hides the
	// main window. The display default is the original's "Ctrl + `" outside
	// macOS and "⌃ + `" on macOS.
	ToggleWindowHotkey string `json:"toggleWindowHotkey"`
	// AutoStart is the last known launch-at-login state. The operating system
	// remains the source of truth and the service re-reads it on load.
	AutoStart bool `json:"autoStart"`
	// SSHDeepLinks accepts ssh:// links and command-line Host targets.
	SSHDeepLinks bool `json:"sshDeepLinks"`

	// Notification toggles default to on and match the events the app can
	// report: a Session ended, a connection failed, and a Port Forwarding Rule
	// failed.
	NotifySessionEnded     bool `json:"notifySessionEnded"`
	NotifyConnectionFailed bool `json:"notifyConnectionFailed"`
	NotifyForwardingFailed bool `json:"notifyForwardingFailed"`
}

// Capabilities reports which integrations the current host can provide.
type Capabilities struct {
	Tray          bool
	Notifications bool
	AutoStart     bool
	GlobalHotkey  bool
}

// Event identifies an application event that can produce a notification.
type Event string

const (
	// EventSessionEnded reports a Session whose transport ended on its own.
	EventSessionEnded Event = "session-ended"
	// EventConnectionFailed reports a terminal transport failure.
	EventConnectionFailed Event = "connection-failed"
	// EventForwardingFailed reports a Port Forwarding Rule that entered its
	// error state.
	EventForwardingFailed Event = "forwarding-failed"
)

// Notification is a typed notification request. The UI layer localizes the
// title and body; this package never contains user-visible text.
type Notification struct {
	Event  Event
	Target string
}

// Defaults returns the original app's defaults for goos.
func Defaults(goos string) Settings {
	return Settings{
		Version:                SettingsVersion,
		ShowTrayIcon:           true,
		CloseToTray:            true,
		GlobalHotkeyEnabled:    true,
		ToggleWindowHotkey:     DefaultToggleHotkey(goos),
		AutoStart:              false,
		SSHDeepLinks:           true,
		NotifySessionEnded:     true,
		NotifyConnectionFailed: true,
		NotifyForwardingFailed: true,
	}
}

// DefaultToggleHotkey returns the original platform default as a display
// string. Accelerator translates the macOS symbols for MyGo.
func DefaultToggleHotkey(goos string) string {
	if goos == "darwin" {
		return "⌃ + `"
	}
	return "Ctrl + `"
}

// Normalize returns settings with the required defaults and trims the hotkey.
// A zero Version means the record was not written by this feature, so the
// defaults are returned.
func Normalize(settings Settings, goos string) Settings {
	if settings.Version == 0 {
		return Defaults(goos)
	}
	settings.Version = SettingsVersion
	settings.ToggleWindowHotkey = strings.TrimSpace(settings.ToggleWindowHotkey)
	if settings.ToggleWindowHotkey == "" {
		settings.ToggleWindowHotkey = DefaultToggleHotkey(goos)
	}
	return settings
}

// Accelerator translates an original-style display accelerator into the form
// MyGo parses. It keeps the stored value unchanged so the original defaults
// stay recognizable.
func Accelerator(display string) string {
	replacer := strings.NewReplacer(
		"⌃", "Ctrl",
		"⌘", "Super",
		"⌥", "Alt",
		"⇧", "Shift",
		"+", "+",
	)
	display = replacer.Replace(display)
	// Original strings use " + " between every component. The parser accepts
	// spaces, so no further rewriting is needed.
	return strings.TrimSpace(display)
}

// NotificationEnabled reports whether event may produce a notification.
func (s Settings) NotificationEnabled(event Event) bool {
	toggle, ok := s.notificationToggle(event)
	return ok && *toggle
}

// notificationToggle returns the stored switch for event. One switch table is
// shared by reads and writes so a new event cannot update one and miss the
// other.
func (s *Settings) notificationToggle(event Event) (*bool, bool) {
	switch event {
	case EventSessionEnded:
		return &s.NotifySessionEnded, true
	case EventConnectionFailed:
		return &s.NotifyConnectionFailed, true
	case EventForwardingFailed:
		return &s.NotifyForwardingFailed, true
	}
	return nil, false
}
