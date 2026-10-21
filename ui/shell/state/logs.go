package state

import (
	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// FocusSession makes the Tab that owns the live Session active and gives the
// Session's Pane focus. A Session living in a detached window is reattached
// first, so the user lands on it in the main window rather than on a hidden
// surface. It reports false when no Tab owns the Session.
func (s *State) FocusSession(id model.SessionID) bool {
	if id == "" {
		return false
	}
	for index, tab := range s.tabs {
		if paneID, ok := tab.livePaneWithSession(id); ok {
			s.focusPane(index, tab, paneID)
			return true
		}
	}
	for _, tab := range s.detached {
		if paneID, ok := tab.livePaneWithSession(id); ok {
			if !s.ReattachDetachedTab(tab.ID) {
				return false
			}
			for index, reattached := range s.tabs {
				if reattached == tab {
					s.focusPane(index, reattached, paneID)
					return true
				}
			}
		}
	}
	return false
}

// FocusHost makes the Tab that owns a live Session for hostID active. It is
// the "focus the related terminal" half of a history or log row action: a
// Session that is still open is reused instead of dialing the Host again.
func (s *State) FocusHost(hostID model.HostID) bool {
	if hostID == "" {
		return false
	}
	for index, tab := range s.tabs {
		if paneID, ok := tab.livePaneWithHost(hostID); ok {
			s.focusPane(index, tab, paneID)
			return true
		}
	}
	for _, tab := range s.detached {
		if paneID, ok := tab.livePaneWithHost(hostID); ok {
			if !s.ReattachDetachedTab(tab.ID) {
				return false
			}
			for index, reattached := range s.tabs {
				if reattached == tab {
					s.focusPane(index, reattached, paneID)
					return true
				}
			}
		}
	}
	return false
}

// OpenHistoryTarget opens or focuses the terminal related to a recorded log or
// history row. A live Session wins, then a live Session for the same Host,
// then a local shell for a local record, and finally the Host looked up in the
// Vault. ErrHostUnavailable reports a Host that has been deleted, which the
// caller shows as a localized problem instead of inventing a terminal.
func (s *State) OpenHistoryTarget(sessionID model.SessionID, hostID model.HostID) error {
	if s.FocusSession(sessionID) {
		return nil
	}
	if hostID == "" {
		if s.FocusLocal() {
			return nil
		}
		return s.NewTab()
	}
	if s.FocusHost(hostID) {
		return nil
	}
	if s.resolveHost == nil {
		return ErrHostUnavailable
	}
	host, ok := s.resolveHost(s.ctx, hostID)
	if !ok {
		return ErrHostUnavailable
	}
	return s.OpenHost(host)
}

// FocusLocal makes a live local shell Tab active when one exists. A local
// history row then reuses the running shell, matching the Host rule.
func (s *State) FocusLocal() bool {
	for index, tab := range s.tabs {
		for _, paneID := range tab.paneIDs() {
			value := tab.panes[paneID]
			if value == nil || value.pending || value.session == nil {
				continue
			}
			if value.descriptor.Kind == domainworkspace.SessionLocal {
				s.focusPane(index, tab, paneID)
				return true
			}
		}
	}
	for _, tab := range s.detached {
		for _, paneID := range tab.paneIDs() {
			value := tab.panes[paneID]
			if value == nil || value.pending || value.session == nil ||
				value.descriptor.Kind != domainworkspace.SessionLocal {
				continue
			}
			if !s.ReattachDetachedTab(tab.ID) {
				return false
			}
			for index, reattached := range s.tabs {
				if reattached == tab {
					s.focusPane(index, reattached, paneID)
					return true
				}
			}
		}
	}
	return false
}

func (t *Tab) livePaneWithSession(id model.SessionID) (model.PaneID, bool) {
	for _, paneID := range t.paneIDs() {
		value := t.panes[paneID]
		if value != nil && !value.pending && value.session != nil &&
			value.session.ID() == id {
			return paneID, true
		}
	}
	return "", false
}

func (t *Tab) livePaneWithHost(hostID model.HostID) (model.PaneID, bool) {
	for _, paneID := range t.paneIDs() {
		value := t.panes[paneID]
		if value != nil && !value.pending && value.session != nil &&
			value.descriptor.HostID == hostID {
			return paneID, true
		}
	}
	return "", false
}

// focusPane makes tab active, focuses paneID, hides the Vault, and persists
// the selection.
func (s *State) focusPane(index int, tab *Tab, paneID model.PaneID) {
	s.active = index
	tab.setFocus(paneID)
	tab.Dirty = false
	s.vaultVisible = false
	s.logsVisible = false
	s.save()
	s.RequestRedraw()
}
