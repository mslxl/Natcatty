package plugin

// EventType is a member of the closed, versioned host event vocabulary.
// Plugins subscribe to concrete types through the events.subscribe
// permission; an unknown name never becomes an event.
type EventType string

const (
	// EventHostChanged reports a Host record change.
	EventHostChanged EventType = "host.changed"
	// EventSessionState reports a Session lifecycle change.
	EventSessionState EventType = "session.state"
	// EventTerminalExit reports that a Session's terminal exited.
	EventTerminalExit EventType = "terminal.exit"
	// EventVaultChanged reports a vault record change.
	EventVaultChanged EventType = "vault.changed"
	// EventPluginLifecycle reports plugin activation and unload changes.
	EventPluginLifecycle EventType = "plugin.lifecycle"
	// EventNotificationAction reports that the user chose a notification
	// action. The payload carries the notification and action IDs.
	EventNotificationAction EventType = "notification.action"
	// EventSettingsChanged reports a host settings change.
	EventSettingsChanged EventType = "settings.changed"
	// EventLocaleChanged reports a host locale change.
	EventLocaleChanged EventType = "locale_changed"
	// EventServiceChanged reports a cross-plugin service binding change: an
	// optional injection bound or unbound, or a dynamic service appeared or
	// disappeared. It is targeted at the affected consumer.
	EventServiceChanged EventType = "service.changed"
	// EventGap is a host-generated event emitted by a subscription when its
	// bounded queue dropped older events of one type.
	EventGap EventType = "events.gap"
)

var eventTypes = []EventType{
	EventHostChanged,
	EventSessionState,
	EventTerminalExit,
	EventVaultChanged,
	EventPluginLifecycle,
	EventNotificationAction,
	EventSettingsChanged,
	EventLocaleChanged,
	EventServiceChanged,
	EventGap,
}

// EventTypes returns the closed event vocabulary in stable order.
func EventTypes() []EventType {
	return append([]EventType(nil), eventTypes...)
}

// EventTypeByName reports whether name belongs to the closed vocabulary.
func EventTypeByName(name string) (EventType, bool) {
	for _, event := range eventTypes {
		if string(event) == name {
			return event, true
		}
	}
	return "", false
}

// IsKnownEventType reports whether name is a declared event type.
func IsKnownEventType(name string) bool {
	_, ok := EventTypeByName(name)
	return ok
}
