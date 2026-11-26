package phoneshell

import domainmodel "github.com/mslxl/natcatty/domain/model"

// SessionIdentity is implemented by a Session handle that knows which
// application Session it owns. A notification click names a Tab and Pane by
// their persisted identities, but the notification itself arrives as a
// SessionID, so the shell needs this one fact to find the owning page.
type SessionIdentity interface {
	SessionID() domainmodel.SessionID
}

// SessionRoute names the Tab and Pane a notification click should show. Both
// identities are the persisted Workspace identities, not a runtime SessionID:
// they survive a relaunch, which is what lets a click that started the process
// land on the restored Pane.
type SessionRoute struct {
	Tab  string
	Pane domainmodel.PaneID
}

// RouteForSession finds the Tab and Pane whose open handle owns id. It reports
// false when no Pane holds that Session: a notification from a Session that
// already ended has nowhere useful to go.
func (s *Shell) RouteForSession(id domainmodel.SessionID) (SessionRoute, bool) {
	if s == nil || s.sessions == nil || id == "" {
		return SessionRoute{}, false
	}
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	for _, tab := range s.sessions.tabs {
		for _, pane := range tab.panes {
			identified, ok := pane.handle.(SessionIdentity)
			if !ok {
				continue
			}
			if identified.SessionID() == id {
				return SessionRoute{Tab: tab.id, Pane: pane.id}, true
			}
		}
	}
	return SessionRoute{}, false
}

// ShowSessionRoute is the notification click's navigation. It selects the
// Sessions section, shows the route's Tab, and focuses its Pane, so the one
// live terminal view moves to that Session.
//
// A route delivered before Load and Show completed is buffered, exactly like a
// deep link that launched the app: the workspace restore has not created the
// Tab yet, and matching against nothing would lose the click. Ready applies the
// buffered routes after the restore.
func (s *Shell) ShowSessionRoute(route SessionRoute) bool {
	if s == nil || route.Tab == "" {
		return false
	}
	if !s.ready {
		s.pendingNotificationRoutes = append(s.pendingNotificationRoutes, route)
		return true
	}
	return s.applySessionRoute(route)
}

// applySessionRoute lands on one Tab and Pane. It never opens a Session: the
// Tab was restored or already open when the notification was shown, so the
// click only reveals it. A Tab that closed in the meantime brings the user to
// the Sessions section without inventing a page.
func (s *Shell) applySessionRoute(route SessionRoute) bool {
	s.sessions.mu.Lock()
	tab := s.sessions.find(route.Tab)
	exists := tab != nil
	// A notification can outlive the Pane it named: the user may have closed
	// that Pane before tapping. Resolve both facts under the same lock so the
	// branch below does not race a concurrent close.
	paneExists := exists && route.Pane != "" && tab.paneOf(route.Pane) != nil
	s.sessions.mu.Unlock()
	if !exists {
		// The notification names a Tab the Workspace no longer has, such as one
		// the user closed before relaunching. Open Sessions rather than leave
		// the user on whatever section they were in.
		s.Select(Sessions)
		return false
	}
	// Focus the named Pane only while it still exists. When it has closed, the
	// click still lands on the surviving Tab (the user is not stranded), but it
	// must not resurrect the Session or invent a replacement Pane: the Tab
	// keeps whatever focus remains. The focus move persists the shared
	// Workspace's focus path, so a relaunch restores the Pane the user was sent
	// to.
	if paneExists {
		s.sessions.focusPane(route.Tab, string(route.Pane))
	}

	s.Select(Sessions)
	router := s.sections[Sessions].router
	path := "/session/" + route.Tab
	if router.Path() != path {
		router.Push(path)
	}
	// Attach or resume the Pane now when the app is in the foreground. A click
	// that arrived while backgrounded is resumed by Foreground, which reads the
	// same route.
	s.syncLiveView()
	return true
}
