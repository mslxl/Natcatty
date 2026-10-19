package session

import (
	"time"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// ClipboardAccess reports the OSC 52 policy this Session enforces. The
// terminal surface consults it for every clipboard request, so a Settings
// change applies to live Sessions as it did in the original.
func (s *Session) ClipboardAccess() domainterminal.ClipboardAccess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clipboardAccess
}

// setClipboardAccess applies a Settings change to a live Session.
func (s *Session) setClipboardAccess(policy domainterminal.ClipboardAccess) {
	if !policy.Valid() {
		return
	}
	s.mu.Lock()
	s.clipboardAccess = policy
	s.mu.Unlock()
	s.tap.setClipboardPolicy(policy)
}

// CancelTransfer closes an active file-transfer gate from the UI. It reports
// whether a transfer was active.
func (s *Session) CancelTransfer() bool {
	if !s.tap.cancelTransfer(time.Now()) {
		return false
	}
	s.publishTap(time.Now())
	return true
}

// CompleteTransfer closes an active gate after a transfer engine finished.
// The native app does not ship an engine; this is the seam for one.
func (s *Session) CompleteTransfer() bool {
	if !s.tap.completeTransfer(time.Now()) {
		return false
	}
	s.publishTap(time.Now())
	return true
}

// AcknowledgeActivity clears the recent-output activity marker when the user
// has seen the Session. A running command or transfer keeps the indicator on.
func (s *Session) AcknowledgeActivity() {
	if !s.tap.acknowledgeActivity() {
		return
	}
	s.publishTap(time.Now())
}

// handleInput passes input through the tap. source is true for input typed in
// this Session's own terminal: only that input is observed by the input hook,
// so a broadcast delivery cannot recursively fan out again. gated reports that
// an active transfer consumed the input.
func (s *Session) handleInput(data []byte, source bool) (forwarded []byte, changed bool, gated bool) {
	now := time.Now()
	forwarded, changed, transfer := s.tap.handleInput(data, now)
	// The tap returns no bytes only while a transfer consumes normal input.
	// A Ctrl+C abort returns the five CAN bytes, so it is not gated.
	gated = len(data) > 0 && len(forwarded) == 0
	if changed {
		s.publishTap(now)
	}
	if transfer != nil && s.onTransfer != nil {
		s.onTransfer(s.id, *transfer)
	}
	if source && len(forwarded) > 0 && s.onInput != nil {
		s.onInput(s.id, forwarded)
	}
	// Encode only the bytes that leave the Session. The tap, input hook, and
	// broadcast fan-out above operate on the user's UTF-8 text.
	if len(forwarded) > 0 {
		forwarded = s.encodeInput(forwarded)
	}
	return forwarded, changed, gated
}

func (s *Session) publishTap(now time.Time) {
	state := s.tap.state(now)
	s.update(func(snapshot *Snapshot) {
		snapshot.CWD = state.CWD
		snapshot.Title = state.Title
		snapshot.Command = state.Command
		snapshot.Busy = state.Busy
		snapshot.LastActivity = state.LastActivity
		snapshot.Transfer = state.Transfer
		snapshot.Notification = state.Notification
		snapshot.Clipboard = state.Clipboard
	})
}

// scheduleActivityClear publishes Busy=false once output has been quiet for
// the activity window. Each new chunk supersedes the previous timer.
func (s *Session) scheduleActivityClear() {
	s.mu.Lock()
	s.activitySeq++
	seq := s.activitySeq
	if s.activityTimer != nil {
		s.activityTimer.Stop()
	}
	s.activityTimer = time.AfterFunc(activityWindow+100*time.Millisecond, func() {
		s.mu.Lock()
		if seq != s.activitySeq {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		s.update(func(snapshot *Snapshot) {
			snapshot.Busy = false
		})
	})
	s.mu.Unlock()
}

func (s *Session) stopActivityTimer() {
	s.mu.Lock()
	s.activitySeq++
	if s.activityTimer != nil {
		s.activityTimer.Stop()
		s.activityTimer = nil
	}
	s.mu.Unlock()
}
