package phoneshell

import (
	"strings"

	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// RestoredSession is one Session the phone can open from a desktop workspace
// restore payload. PaneID names the remote Pane it came from, and Title is the
// label the phone shows until the Session reports its own.
type RestoredSession struct {
	PaneID     string
	Title      string
	Descriptor domainworkspace.SessionDescriptor
}

// FlattenRestored flattens a workspace restore payload into the phone's flat
// Workspace: one Session per remote Pane, in Tab order and then pane order.
//
// The desktop Workspace can hold a Tab with a split tree; the phone cannot, so
// each leaf becomes its own Session (one Tab, one Pane). Local and serial
// Panes are dropped because the phone has no transport for them, exactly as the
// Hosts list drops their Host records.
func FlattenRestored(payload domainworkspace.Payload) []RestoredSession {
	payload = domainworkspace.SanitizePayload(payload)
	descriptors := make(map[string]domainworkspace.SessionDescriptor, len(payload.Panes))
	for _, pane := range payload.Panes {
		descriptors[string(pane.ID)] = pane.Session
	}
	sessions := make([]RestoredSession, 0, len(payload.Panes))
	for _, tab := range payload.Tabs {
		for _, paneID := range domainworkspace.CollectPaneIDs(tab.Root) {
			descriptor, ok := descriptors[string(paneID)]
			if !ok || !phoneCanRestore(descriptor) {
				continue
			}
			sessions = append(sessions, RestoredSession{
				PaneID:     string(paneID),
				Title:      restoredTitle(descriptor),
				Descriptor: descriptor,
			})
		}
	}
	return sessions
}

// phoneCanRestore reports whether a descriptor names a Session the phone can
// open. A Local shell would run on the device and a serial device has no phone
// adapter; both stay on the desktop.
func phoneCanRestore(descriptor domainworkspace.SessionDescriptor) bool {
	if !descriptor.Valid() {
		return false
	}
	switch descriptor.Kind {
	case domainworkspace.SessionSSH, domainworkspace.SessionTelnet,
		domainworkspace.SessionMosh, domainworkspace.SessionET:
		return true
	default:
		return false
	}
}

// restoredTitle names one flattened Session from its own Pane. A split-era
// Tab title describes several panes ("web/db"), which would mislabel each
// Session, so it is never used; the Pane's name or Host does that instead.
func restoredTitle(descriptor domainworkspace.SessionDescriptor) string {
	for _, candidate := range []string{descriptor.CustomName, descriptor.Label, descriptor.Hostname} {
		if title := strings.TrimSpace(candidate); title != "" {
			return title
		}
	}
	return "Session"
}
