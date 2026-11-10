package session

import (
	"time"

	"github.com/mslxl/natcatty/domain/model"
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
	forwarded, changed, started, completions, transfer := s.tap.handleInput(data, now)
	// The tap returns no bytes only while a transfer consumes normal input.
	// A Ctrl+C abort returns the five CAN bytes, so it is not gated.
	gated = len(data) > 0 && len(forwarded) == 0
	if changed {
		s.publishTap(now)
	}
	if started {
		// A deferred check belongs to the command just replaced.
		s.stopPromptTimer()
	}
	// The tap released its lock before returning, so the completion hook never
	// runs under the tap mutex. The tap finalizes each command exactly once,
	// so concurrent input, output, and close cannot produce a second event.
	for _, completion := range completions {
		s.publishCommandCompletion(completion)
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

// CommandEvent is one command execution captured from a Session. It is emitted
// only after the command finishes or the Session ends, never at Enter. The
// event carries the identity the composition root needs to derive the row's
// Host context and origin, plus the fields the tap observed. Unknown values
// are zero: an empty CWD or Shell, a zero FinishedAt for an execution the
// shell never reported finishing (duration unknown), and HasExitCode false
// when no exit code was reported. CaptureTier is 1 when the installed shell
// hook reported the command, 2 for native OSC 133/7, and 3 for typed input
// plus prompt detection.
type CommandEvent struct {
	SessionID  model.SessionID
	Scope      string
	HostID     model.HostID
	HostLabel  string
	Kind       Kind
	Hostname   string
	Username   string
	Command    string
	CWD        string
	Shell      string
	StartedAt  time.Time
	FinishedAt time.Time
	ExitCode   int
	// Transient is true when the Session chose the Host-Atuin path. The app
	// must keep the completion in memory for suggestions and never persist or
	// sync it: the Host's Atuin database is the only authority for this Host.
	Transient   bool
	HasExitCode bool
	CaptureTier int
}

// publishCommandCompletion delivers one captured command to the app's hook.
// The Session adds its identity and the scope it can resolve without the app:
// "local", "host:<id>", or "serial:<port>". An unsaved Host's scope needs the
// captured origin, so it is left empty for the app to complete.
func (s *Session) publishCommandCompletion(capture capturedCommand) {
	// A shell hook captures a programmatic wrapper as if the user typed it.
	// Wrapper completions are the app's execution machinery, not history; a
	// concurrent user command never carries an internal marker and is kept.
	// The marker is no longer needed once its own capture has been dropped.
	if s.internalCommand(capture.command) {
		s.forgetInternalMarkers(capture.command)
		return
	}
	if s.onCommandCompletion == nil {
		return
	}
	s.onCommandCompletion(CommandEvent{
		SessionID:   s.id,
		Scope:       commandScope(s.spec),
		HostID:      s.spec.HostID,
		HostLabel:   s.spec.Label,
		Kind:        s.spec.Kind,
		Hostname:    s.spec.Hostname,
		Username:    s.spec.Username,
		Command:     capture.command,
		CWD:         capture.cwd,
		Shell:       capture.shell,
		StartedAt:   capture.startedAt,
		FinishedAt:  capture.finishedAt,
		ExitCode:    capture.exitCode,
		Transient:   s.hostAtuinActive,
		HasExitCode: capture.hasExitCode,
		CaptureTier: capture.captureTier,
	})
}

func (s *Session) publishTap(now time.Time) {
	state := s.tap.state(now)
	s.update(func(snapshot *Snapshot) {
		snapshot.CWD = state.CWD
		snapshot.Title = state.Title
		snapshot.Command = state.Command
		snapshot.Input = state.Input
		snapshot.Busy = state.Busy
		snapshot.LastActivity = state.LastActivity
		snapshot.Transfer = state.Transfer
		snapshot.Notification = state.Notification
		snapshot.Clipboard = state.Clipboard
		snapshot.Stats = state.Stats
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

// promptRecheckDelay leaves the prompt fallback's guard window behind before
// the deferred check, so coarse timer resolution cannot land inside it.
const promptRecheckDelay = promptWindow + 10*time.Millisecond

// schedulePromptRecheck settles a command whose last chunk left it running
// with output but did not end in a recognizable prompt, as when the renderer
// hides the prompt behind control sequences. Nothing may follow the chunk, so
// one check after the guard window finishes a prompt-like tail. A new
// submission or a newer deferral supersedes the previous timer, exactly like
// the activity timer.
func (s *Session) schedulePromptRecheck() {
	s.mu.Lock()
	s.promptSeq++
	seq := s.promptSeq
	if s.promptTimer != nil {
		s.promptTimer.Stop()
	}
	s.promptTimer = time.AfterFunc(promptRecheckDelay, func() {
		s.mu.Lock()
		if seq != s.promptSeq {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		now := time.Now()
		if completion := s.tap.resolvePrompt(now); completion != nil {
			s.publishTap(now)
			s.publishCommandCompletion(*completion)
		}
	})
	s.mu.Unlock()
}

func (s *Session) stopPromptTimer() {
	s.mu.Lock()
	s.promptSeq++
	if s.promptTimer != nil {
		s.promptTimer.Stop()
		s.promptTimer = nil
	}
	s.mu.Unlock()
}
