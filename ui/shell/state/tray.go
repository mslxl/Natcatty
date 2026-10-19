package state

import (
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

// TabStatus is the aggregate transport state of a Tab's Sessions, as the
// system tray shows it.
type TabStatus string

const (
	// TabConnected reports at least one open transport and no failure.
	TabConnected TabStatus = "connected"
	// TabConnecting reports a dial or a reconnect in progress.
	TabConnecting TabStatus = "connecting"
	// TabDisconnected reports a failed or ended transport.
	TabDisconnected TabStatus = "disconnected"
)

// TabSummary is one Tab's tray entry. A Tab may host several Sessions; the
// summary aggregates their transport state and choosing it selects the Tab.
type TabSummary struct {
	TabID    model.TabID
	TabIndex int
	Detached bool
	Number   int
	Label    string
	Status   TabStatus
}

// TabSummaries returns the tray's Tab entries in Workspace order, followed by
// detached Tabs. It must run on the UI thread.
func (s *State) TabSummaries() []TabSummary {
	summaries := make([]TabSummary, 0, len(s.tabs)+len(s.detached))
	for index, tab := range s.tabs {
		summaries = append(summaries, summaryFor(tab, index, false))
	}
	for _, tab := range s.detached {
		summaries = append(summaries, summaryFor(tab, -1, true))
	}
	return summaries
}

func summaryFor(tab *Tab, index int, detached bool) TabSummary {
	return TabSummary{
		TabID:    tab.ID,
		TabIndex: index,
		Detached: detached,
		Number:   tab.TitleNumber,
		Label:    tab.Label,
		Status:   aggregateStatus(tab),
	}
}

// aggregateStatus reports the worst transport state in the Tab: a failure
// outranks a dial, and a dial outranks an open transport.
func aggregateStatus(tab *Tab) TabStatus {
	status := TabConnected
	sawSession := false
	for _, pane := range tab.panes {
		sawSession = true
		switch paneStatus(pane) {
		case TabDisconnected:
			return TabDisconnected
		case TabConnecting:
			status = TabConnecting
		}
	}
	if !sawSession {
		return TabConnecting
	}
	return status
}

func paneStatus(pane *pane) TabStatus {
	if pane.pending || pane.session == nil {
		return TabConnecting
	}
	switch snapshot := pane.session.Snapshot(); {
	case snapshot.Connection == appsession.ConnectionStatusError:
		return TabDisconnected
	case snapshot.Status == appsession.StatusExited:
		return TabDisconnected
	case snapshot.Connection == appsession.ConnectionStatusConnecting,
		snapshot.Connection == appsession.ConnectionStatusReconnecting:
		return TabConnecting
	default:
		return TabConnected
	}
}

// SessionLabel returns the label of the Tab or Pane that owns id. The tray and
// notifications use it to name a Session without exposing IDs.
func (s *State) SessionLabel(id model.SessionID) (string, bool) {
	for _, tab := range s.tabs {
		if label, ok := labelForSession(tab, id); ok {
			return label, true
		}
	}
	for _, tab := range s.detached {
		if label, ok := labelForSession(tab, id); ok {
			return label, true
		}
	}
	return "", false
}

func labelForSession(tab *Tab, id model.SessionID) (string, bool) {
	for _, pane := range tab.panes {
		if pane.session == nil || pane.session.ID() != id {
			continue
		}
		if len(tab.panes) > 1 && tab.Label != "" && pane.descriptor.Label != "" {
			return tab.Label + " · " + pane.descriptor.Label, true
		}
		if tab.Label != "" {
			return tab.Label, true
		}
		if pane.descriptor.Label != "" {
			return pane.descriptor.Label, true
		}
		return "", true
	}
	return "", false
}
