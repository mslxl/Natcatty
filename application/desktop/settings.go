// Package desktop holds the use cases behind the operating-system integration:
// tray and close-to-tray behavior, deep links and command-line Host targets,
// notifications, the global hotkey modes, and launch-at-login autostart. It
// depends only on consumer-owned ports, so the composition root can supply
// MyGo, fakes, or a headless host.
package desktop

import (
	"fmt"
	"strings"
)

// SettingsVersion is the on-disk desktop settings version. A record without
// this version predates the feature (or is an empty object), so it loads as
// defaults instead of turning every switch off.
const SettingsVersion = 1

// HotkeyMode is what the global shortcut does when pressed. The zero value is
// not a mode: records without the field normalize to the Quake default.
type HotkeyMode string

const (
	// HotkeyModeNone releases the accelerator to the system: nothing is
	// registered, so another application may claim it.
	HotkeyModeNone HotkeyMode = "none"
	// HotkeyModeToggle shows, focuses, or hides the main window, the original
	// behavior.
	HotkeyModeToggle HotkeyMode = "toggle"
	// HotkeyModeQuake toggles the Dropdown Panel with the Quake terminal
	// Workspace.
	HotkeyModeQuake HotkeyMode = "quake"
	// HotkeyModeTopdown toggles the Dropdown Panel with the full application
	// shell.
	HotkeyModeTopdown HotkeyMode = "topdown"
)

// DefaultHotkeyMode is the mode a new or pre-field record loads with.
const DefaultHotkeyMode = HotkeyModeQuake

// QuakeSource selects what the Quake Dropdown Panel shows: the Quake
// Workspace it owns, or the main window's active Tab borrowed temporarily for
// rendering. The zero value is not a source: records without the field
// normalize to Own.
type QuakeSource string

const (
	// QuakeSourceOwn shows the Dropdown Panel's own flat terminal Workspace.
	QuakeSourceOwn QuakeSource = "own"
	// QuakeSourceFollow follows the main window's active Tab. The Tab stays in
	// the main Workspace; only the window that renders it changes.
	QuakeSourceFollow QuakeSource = "follow"
)

// DefaultQuakeSource is the source a new or pre-field record loads with.
const DefaultQuakeSource = QuakeSourceOwn

// Valid reports whether source is one of the two sources the settings and
// service understand. An unknown value from disk normalizes to
// DefaultQuakeSource; a setter rejects it.
func (s QuakeSource) Valid() bool {
	switch s {
	case QuakeSourceOwn, QuakeSourceFollow:
		return true
	}
	return false
}

// HeightUnit is how a Dropdown Panel height is expressed. The zero value is
// not a unit: records without the field normalize to percent.
type HeightUnit string

const (
	// HeightUnitPercent sizes the Panel as a share of the work area.
	HeightUnitPercent HeightUnit = "percent"
	// HeightUnitPX sizes the Panel in DIPs.
	HeightUnitPX HeightUnit = "px"
)

// DefaultHeightUnit is the unit a new or pre-field record loads with.
const DefaultHeightUnit = HeightUnitPercent

// The configured default heights. Quake takes half the work area; top-down
// shows the complete application and takes 80%.
const (
	DefaultQuakeHeight   = 50
	DefaultTopdownHeight = 80
)

// The accepted configured height ranges. They are checked by the setters and
// defensively clamped by Normalize for persisted records.
const (
	MinPercentHeight = 10
	MaxPercentHeight = 100
	MinPixelHeight   = 120
	MaxPixelHeight   = 10000
)

// Valid reports whether unit is one of the two units the settings and service
// understand. An unknown value from disk normalizes to DefaultHeightUnit; a
// setter rejects it.
func (u HeightUnit) Valid() bool {
	switch u {
	case HeightUnitPercent, HeightUnitPX:
		return true
	}
	return false
}

// HeightBounds returns the accepted configured range for unit in that unit's
// own terms: percent points or DIPs. ok is false for an unknown unit.
func HeightBounds(unit HeightUnit) (minimum, maximum int, ok bool) {
	switch unit {
	case HeightUnitPercent:
		return MinPercentHeight, MaxPercentHeight, true
	case HeightUnitPX:
		return MinPixelHeight, MaxPixelHeight, true
	}
	return 0, 0, false
}

// ValidateHeight returns nil when value is an allowed configured height for
// unit. An unknown unit or an out-of-range value is a validation error,
// matching the setters.
func ValidateHeight(value int, unit HeightUnit) error {
	minimum, maximum, ok := HeightBounds(unit)
	if !ok {
		return &Error{Kind: KindValidation, Cause: fmt.Errorf("unknown height unit %q", unit)}
	}
	if value < minimum || value > maximum {
		return &Error{Kind: KindValidation, Cause: fmt.Errorf(
			"height %d is outside %d-%d for unit %q", value, minimum, maximum, unit)}
	}
	return nil
}

// Valid reports whether mode is one of the four modes the settings and service
// understand. An unknown value from disk normalizes to DefaultHotkeyMode; a
// setter rejects it.
func (m HotkeyMode) Valid() bool {
	switch m {
	case HotkeyModeNone, HotkeyModeToggle, HotkeyModeQuake, HotkeyModeTopdown:
		return true
	}
	return false
}

// RegistersHotkey reports whether mode claims the global accelerator. None
// returns it to the system, so no callback is registered at all.
func (m HotkeyMode) RegistersHotkey() bool {
	return m != HotkeyModeNone
}

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
	// HotkeyMode selects what the global shortcut does. It defaults to the
	// Quake dropdown. The old globalHotkeyEnabled key is intentionally gone:
	// JSON decoding ignores it and an old record loads the default mode.
	HotkeyMode HotkeyMode `json:"hotkeyMode"`
	// QuakeSource selects what the Quake dropdown renders: its own Workspace
	// or the main window's active Tab. It defaults to own.
	QuakeSource QuakeSource `json:"quakeSource"`
	// QuakeHeight and QuakeHeightUnit size the Quake dropdown, as a share of
	// the work area or in DIPs. They default to 50 percent.
	QuakeHeight     int        `json:"quakeHeight"`
	QuakeHeightUnit HeightUnit `json:"quakeHeightUnit"`
	// TopdownHeight and TopdownHeightUnit size the top-down dropdown
	// independently of Quake. They default to 80 percent, because the
	// complete application needs more room than the terminal.
	TopdownHeight     int        `json:"topdownHeight"`
	TopdownHeightUnit HeightUnit `json:"topdownHeightUnit"`
	// HideOnBlur hides the Dropdown Panel when it loses focus. It defaults to
	// true; the store overlays the default for records written before the
	// field existed, so an explicit false survives a reload.
	HideOnBlur bool `json:"hideOnBlur"`
	// HideOnEscape hides the Dropdown Panel when Escape reaches it. It
	// defaults to true, with the same presence handling as HideOnBlur.
	HideOnEscape bool `json:"hideOnEscape"`
	// HideOnClickOutside hides the Dropdown Panel when the user clicks outside
	// it. Platforms that report that gesture as a focus loss reuse the blur
	// signal: there the switch shares HideOnBlur's behavior, and turning
	// HideOnBlur off makes the shared trigger unavailable too. v1 does not
	// install a global mouse listener.
	HideOnClickOutside bool `json:"hideOnClickOutside"`
	// ToggleWindowHotkey is the accelerator the mode above claims. The display
	// default is the original's "Ctrl + `" outside macOS and "⌃ + `" on macOS.
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
	// Dropdown reports whether the host can show the Quake and top-down
	// Dropdown Panel modes. None and toggle remain usable when it is false.
	Dropdown bool
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
	// Session is the opaque identity of the Session the event belongs to. The
	// composition root uses it to route the notification's click to the window
	// and Tab that own the Session; this package never interprets it. It is
	// empty for an event that is not tied to a Session.
	Session string
}

// Defaults returns the original app's defaults for goos.
func Defaults(goos string) Settings {
	return Settings{
		Version:                SettingsVersion,
		ShowTrayIcon:           true,
		CloseToTray:            true,
		HotkeyMode:             DefaultHotkeyMode,
		QuakeSource:            DefaultQuakeSource,
		QuakeHeight:            DefaultQuakeHeight,
		QuakeHeightUnit:        DefaultHeightUnit,
		TopdownHeight:          DefaultTopdownHeight,
		TopdownHeightUnit:      DefaultHeightUnit,
		HideOnBlur:             true,
		HideOnEscape:           true,
		HideOnClickOutside:     true,
		ToggleWindowHotkey:     DefaultToggleHotkey(goos),
		AutoStart:              false,
		SSHDeepLinks:           true,
		NotifySessionEnded:     true,
		NotifyConnectionFailed: true,
		NotifyForwardingFailed: true,
	}
}

// DropdownSupported reports whether goos can host the Dropdown Panel. MyGo
// implements panel windows on the three desktop platforms; every other target
// still compiles and runs, but Quake and top-down stay disabled there.
func DropdownSupported(goos string) bool {
	switch goos {
	case "darwin", "windows", "linux":
		return true
	}
	return false
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
// defaults are returned. An unknown or missing hotkey mode falls back to the
// Quake default without touching any other desktop setting. Unknown height
// units return to percent, and configured heights take their field's default
// when missing or clamp into the unit's accepted range.
func Normalize(settings Settings, goos string) Settings {
	if settings.Version == 0 {
		return Defaults(goos)
	}
	settings.Version = SettingsVersion
	if !settings.HotkeyMode.Valid() {
		settings.HotkeyMode = DefaultHotkeyMode
	}
	if !settings.QuakeSource.Valid() {
		settings.QuakeSource = DefaultQuakeSource
	}
	if !settings.QuakeHeightUnit.Valid() {
		settings.QuakeHeightUnit = DefaultHeightUnit
	}
	if !settings.TopdownHeightUnit.Valid() {
		settings.TopdownHeightUnit = DefaultHeightUnit
	}
	// A missing height decodes as zero, which is also out of every accepted
	// range. Treat zero as "missing" and fill the field's default; any other
	// out-of-range persisted value is defensively clamped. The default-true
	// hide switches are filled by the storage layer, which can tell a missing
	// JSON key from an explicit false.
	settings.QuakeHeight = normalizeHeight(settings.QuakeHeight, settings.QuakeHeightUnit, DefaultQuakeHeight)
	settings.TopdownHeight = normalizeHeight(settings.TopdownHeight, settings.TopdownHeightUnit, DefaultTopdownHeight)
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

// normalizeHeight fills a missing height with fallback and clamps any other
// persisted value into unit's accepted range. unit must already be valid.
func normalizeHeight(value int, unit HeightUnit, fallback int) int {
	minimum, maximum, ok := HeightBounds(unit)
	if !ok {
		return fallback
	}
	if value == 0 {
		value = fallback
	}
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
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
