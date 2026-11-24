package session

import "github.com/mslxl/natcatty/domain/model"

// SetNotificationHook replaces the program-notification hook after NewService.
// The phone composition root needs it because it builds the Session service
// before the shell that owns the Tab and Pane a click must open.
//
// Call it before any Session is opened: Open copies the hook into the Session,
// so the read loop never takes the service lock to publish a notification. A
// call after a Session started leaves that Session's copy unchanged.
func (s *Service) SetNotificationHook(fn func(model.SessionID, TerminalNotification)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onNotification = fn
	s.mu.Unlock()
}
