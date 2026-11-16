package state

import (
	"context"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

// ComposeConnection is the seam compose uses to inspect a target's connection,
// ask it to dial again, and wait until it can take input. A nil field falls
// back to the live Session in the main window; tests supply fakes so a
// reconnect can be observed without a transport.
type ComposeConnection struct {
	// Status reports the target's lifecycle and transport state.
	Status func(model.SessionID) (appsession.Status, appsession.ConnectionStatus, bool)
	// Reconnect requests an immediate retry. It reports whether the request
	// was accepted.
	Reconnect func(model.SessionID) bool
	// Ready reports that the target can receive input again.
	Ready func(model.SessionID) bool
}

// WithComposeConnection replaces the live-Session connection seam used by a
// compose submit. Fields left nil still read the live Session.
func WithComposeConnection(seam ComposeConnection) Option {
	return func(s *State) { s.composeConnection = seam }
}

// composeReconnectTimeout is the longest a compose submit waits for a
// disconnected target before leaving the draft in the bar for another try.
const composeReconnectTimeout = 30 * time.Second

// composeReconnectPoll is the readiness poll cadence. Every iteration reads
// the target's real connection state and the deadline ends the wait, so this
// is not a fixed-delay synchronization.
const composeReconnectPoll = 20 * time.Millisecond

// BroadcastSessionInfo describes one Session that can receive broadcast input.
// Label is the Pane's label; an empty label means a local Session whose title
// the view localizes from Number. Status is the most recent delivery result
// while a broadcast panel is open.
type BroadcastSessionInfo struct {
	ID        model.SessionID
	Label     string
	Number    int
	TabNumber int
	Focused   bool
	Selected  bool
	Running   bool
	Status    appsession.DeliveryStatus
	HasStatus bool
}

// selectedBroadcastTargetSetLocked copies the selected IDs. The caller holds
// inputMu.
func (s *State) selectedBroadcastTargetSetLocked() map[model.SessionID]bool {
	selected := make(map[model.SessionID]bool, len(s.broadcastTargets))
	for id, value := range s.broadcastTargets {
		if value {
			selected[id] = true
		}
	}
	return selected
}

// selectedBroadcastTargetIDsLocked lists the delivery recipients: selected
// Sessions that are still represented in the main window, in Tab and Pane
// order. The caller holds inputMu, so it never reads UI-thread Tab state.
func (s *State) selectedBroadcastTargetIDsLocked() []model.SessionID {
	return append([]model.SessionID(nil), s.broadcastTargetIDs...)
}

// refreshBroadcastState reconciles the input-thread delivery snapshot with
// the main window's Tabs. It runs on the UI thread after every structural
// change. A selected Session that was detached or closed leaves the target
// set and cannot receive typed or composed broadcast input; its ID is never a
// hidden recipient. The removal is reported as a visible notice.
func (s *State) refreshBroadcastState() {
	present := make(map[model.SessionID]bool, len(s.tabs))
	order := make([]model.SessionID, 0, len(s.tabs))
	for _, tab := range s.tabs {
		if tab == nil {
			continue
		}
		for _, paneID := range tab.paneIDs() {
			value := tab.panes[paneID]
			if value == nil || value.session == nil {
				continue
			}
			id := value.session.ID()
			if present[id] {
				continue
			}
			present[id] = true
			order = append(order, id)
		}
	}

	s.inputMu.Lock()
	removed := 0
	for id := range s.broadcastTargets {
		if !present[id] {
			delete(s.broadcastTargets, id)
			removed++
		}
	}
	// An explicit compose target that left the main window is pruned the same
	// way: it must never stay a hidden recipient. If nothing explicit remains,
	// compose follows the default again.
	composeRemoved := 0
	for id := range s.composeTargets {
		if !present[id] {
			delete(s.composeTargets, id)
			composeRemoved++
		}
	}
	targets := make([]model.SessionID, 0, len(order))
	for _, id := range order {
		if s.broadcastTargets[id] {
			targets = append(targets, id)
		}
	}
	s.broadcastSessions = present
	s.broadcastTargetIDs = targets
	s.mainSessionIDs = order
	if removed > 0 {
		s.broadcastNotice += removed
		// Results and per-Session statuses described the old target set.
		s.broadcastResult = appsession.SendResult{}
		if !s.composeExplicit {
			// Compose was following the default, so its previous result
			// described a set that no longer exists.
			s.composeResult = appsession.SendResult{}
			if s.composeBroadcast {
				// Stop a composed broadcast before its next line so a Session
				// that just left the main window receives no further chunks.
				s.cancelComposeSendLocked()
			}
		}
	}
	if composeRemoved > 0 {
		s.composeNotice += composeRemoved
		s.composeResult = appsession.SendResult{}
		if len(s.composeTargets) == 0 {
			s.composeExplicit = false
		}
		if s.composeSending {
			// The captured chunk target list still names the pruned Session.
			s.cancelComposeSendLocked()
		}
	}
	s.inputMu.Unlock()
}

// BroadcastSessions lists the running Sessions in the main window in Tab and
// split-tree order. Only Sessions that can receive input are included.
func (s *State) BroadcastSessions() []BroadcastSessionInfo {
	s.refreshBroadcastState()
	s.inputMu.Lock()
	selected := s.selectedBroadcastTargetSetLocked()
	statuses := make(map[model.SessionID]appsession.DeliveryStatus, len(s.broadcastResult.Deliveries))
	hasStatuses := make(map[model.SessionID]bool, len(s.broadcastResult.Deliveries))
	for _, delivery := range s.broadcastResult.Deliveries {
		statuses[delivery.SessionID] = delivery.Status
		hasStatuses[delivery.SessionID] = true
	}
	s.inputMu.Unlock()

	var infos []BroadcastSessionInfo
	for index, tab := range s.tabs {
		focusedPane, focusedID := tab.focusedPane()
		for _, paneID := range tab.paneIDs() {
			value := tab.panes[paneID]
			if value == nil || value.session == nil {
				continue
			}
			id := value.session.ID()
			running := value.session.Snapshot().Status == appsession.StatusRunning
			if !running && !selected[id] && !hasStatuses[id] {
				continue
			}
			info := BroadcastSessionInfo{
				ID:        id,
				Label:     s.connectionLabel(value),
				Number:    value.descriptor.Number,
				TabNumber: tab.Number,
				// Only the active Tab holds terminal focus; every other Tab's
				// own FocusPath is just where focus returns when selected.
				Focused: focusedPane != nil && focusedID == paneID &&
					index == s.active && !s.vaultVisible,
				Selected: selected[id],
				Running:  running,
			}
			if hasStatuses[id] {
				info.Status = statuses[id]
				info.HasStatus = true
			}
			infos = append(infos, info)
		}
	}
	return infos
}

// SessionByID returns the live Session with id in the main window. The shell
// owns the Session; callers use it for production input paths such as typing.
func (s *State) SessionByID(id model.SessionID) (*appsession.Session, bool) {
	for _, tab := range s.tabs {
		for _, value := range tab.panes {
			if value != nil && value.session != nil && value.session.ID() == id {
				return value.session, true
			}
		}
	}
	return nil, false
}

// FocusedSessionID returns the Session of the focused Pane in the active Tab.
func (s *State) FocusedSessionID() (model.SessionID, bool) {
	tab, _ := s.activeTab()
	if tab == nil {
		return "", false
	}
	value, _ := tab.focusedPane()
	if value == nil || value.session == nil {
		return "", false
	}
	return value.session.ID(), true
}

// FocusedHostID returns the Vault Host of the focused Session, or false for a
// local shell or when no Session is focused.
func (s *State) FocusedHostID() (model.HostID, bool) {
	id, ok := s.FocusedSessionID()
	if !ok {
		return "", false
	}
	session, ok := s.SessionByID(id)
	if !ok {
		return "", false
	}
	hostID := session.HostID()
	if hostID == "" {
		return "", false
	}
	return hostID, true
}

// BroadcastPanelOpen reports whether the target-selection panel is showing.
func (s *State) BroadcastPanelOpen() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.broadcastPanel
}

// OpenBroadcastPanel shows the panel. The first open selects every other
// running Session, matching the original workspace broadcast default of
// excluding the source.
func (s *State) OpenBroadcastPanel() {
	s.inputMu.Lock()
	alreadyOpen := s.broadcastPanel
	s.broadcastPanel = true
	needDefault := len(s.broadcastTargets) == 0
	s.inputMu.Unlock()
	if !alreadyOpen && needDefault {
		for _, info := range s.BroadcastSessions() {
			if info.Running && !info.Focused {
				s.SetBroadcastTarget(info.ID, true)
			}
		}
	}
	s.RequestRedraw()
}

// CloseBroadcastPanel hides the panel without changing an active broadcast.
func (s *State) CloseBroadcastPanel() {
	s.inputMu.Lock()
	s.broadcastPanel = false
	s.inputMu.Unlock()
	s.RequestRedraw()
}

// ToggleBroadcastPanel shows or hides the target panel.
func (s *State) ToggleBroadcastPanel() {
	if s.BroadcastPanelOpen() {
		s.CloseBroadcastPanel()
		return
	}
	s.OpenBroadcastPanel()
}

// SetBroadcastTarget selects or deselects one Session.
func (s *State) SetBroadcastTarget(id model.SessionID, selected bool) {
	if id == "" {
		return
	}
	s.inputMu.Lock()
	if selected {
		s.broadcastTargets[id] = true
	} else {
		delete(s.broadcastTargets, id)
	}
	// A target-set change makes the previous delivery and compose results
	// stale, and answers any notice about removed targets. An explicitly
	// chosen compose set did not change, so its result stays meaningful.
	s.broadcastResult = appsession.SendResult{}
	if !s.composeExplicit {
		s.composeResult = appsession.SendResult{}
	}
	s.broadcastNotice = 0
	s.inputMu.Unlock()
	s.refreshBroadcastState()
	s.RequestRedraw()
}

// ToggleBroadcastTarget flips one Session's selection.
func (s *State) ToggleBroadcastTarget(id model.SessionID) {
	for _, info := range s.BroadcastSessions() {
		if info.ID == id {
			s.SetBroadcastTarget(id, !info.Selected)
			return
		}
	}
}

// BroadcastTargets returns the selected recipients represented in the main
// window, in Tab and Pane order. An ended Session whose Tab is still open
// stays a recipient: delivery reports its failure in a visible row. Sessions
// in detached windows are never recipients.
func (s *State) BroadcastTargets() []model.SessionID {
	s.refreshBroadcastState()
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.selectedBroadcastTargetIDsLocked()
}

// BroadcastTargetCount returns how many main-window Sessions are selected.
func (s *State) BroadcastTargetCount() int { return len(s.BroadcastTargets()) }

// BroadcastNotice returns how many selected targets left the main window in
// the latest structural change. The panel shows it so pruning is never silent.
func (s *State) BroadcastNotice() int {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.broadcastNotice
}

// BroadcastCanStart reports whether the selection contains a live target
// other than the focused Session. Fan-out excludes the typed source, so a
// selection of only the focused Session would send nowhere.
func (s *State) BroadcastCanStart() bool {
	for _, info := range s.BroadcastSessions() {
		if info.Selected && info.Running && !info.Focused {
			return true
		}
	}
	return false
}

// StartBroadcast activates the selected target set. It reports false when no
// live Session other than the focused source is selected.
func (s *State) StartBroadcast() bool {
	if !s.BroadcastCanStart() {
		return false
	}
	s.inputMu.Lock()
	s.broadcasting = true
	s.broadcastResult = appsession.SendResult{}
	if !s.composeExplicit {
		// Compose follows the default, which just became the broadcast set.
		s.composeResult = appsession.SendResult{}
	}
	s.broadcastNotice = 0
	s.inputMu.Unlock()
	s.RequestRedraw()
	return true
}

// StopBroadcast deactivates broadcast immediately. Typing in every Session
// then reaches only that Session again; any selection is kept for a later
// start.
func (s *State) StopBroadcast() {
	s.inputMu.Lock()
	changed := s.broadcasting
	s.broadcasting = false
	if changed {
		// An explicitly chosen compose set did not follow the broadcast latch,
		// so stopping broadcast leaves its targets and result alone.
		if !s.composeExplicit {
			// Compose now targets the focused Session rather than the old
			// broadcast set, so its previous result no longer describes Send.
			s.composeResult = appsession.SendResult{}
			if s.composeBroadcast {
				// Stop reaches an in-flight composed broadcast too: it cancels
				// before the next line instead of letting the remaining chunks
				// through.
				s.cancelComposeSendLocked()
			}
		}
	}
	s.inputMu.Unlock()
	if changed {
		s.RequestRedraw()
	}
}

// Broadcasting reports whether typed input is being fanned out.
func (s *State) Broadcasting() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.broadcasting
}

// BroadcastResult returns the most recent fan-out result. It is meaningful
// while the panel is open, including after Stop.
func (s *State) BroadcastResult() (appsession.SendResult, bool) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.broadcastResult, len(s.broadcastResult.Deliveries) > 0
}

// NoteSessionInput is the Session input hook. While broadcast is active it
// sends the same bytes, after the source's own tap, to every selected target
// except the source. It runs on the input goroutine and is safe there.
func (s *State) NoteSessionInput(id model.SessionID, data []byte) {
	if len(data) == 0 || s.sessions == nil {
		return
	}
	s.inputMu.Lock()
	if !s.broadcasting || !s.broadcastSessions[id] {
		// The source must be a Session of the main window. Typing in a
		// detached window never fans out through the main target set, and
		// the recipients below are only main-window Sessions.
		s.inputMu.Unlock()
		return
	}
	selected := s.selectedBroadcastTargetIDsLocked()
	targets := selected[:0]
	for _, target := range selected {
		if target != id {
			targets = append(targets, target)
		}
	}
	s.inputMu.Unlock()
	if len(targets) == 0 {
		return
	}
	result := s.sessions.SendInput(targets, data)
	s.inputMu.Lock()
	s.broadcastResult = result
	s.inputMu.Unlock()
	s.RequestRedraw()
}

// OpenCompose shows the compose bar. A draft from an earlier open is kept.
func (s *State) OpenCompose() {
	s.inputMu.Lock()
	s.composeOpen = true
	s.composeBroadcast = false
	s.composeResult = appsession.SendResult{}
	s.inputMu.Unlock()
	s.RequestRedraw()
}

// CloseCompose cancels the compose bar and discards the draft, as the
// original's Escape did.
func (s *State) CloseCompose() {
	s.inputMu.Lock()
	s.cancelComposeSendLocked()
	s.composeOpen = false
	s.composeDraft = ""
	s.composeBroadcast = false
	s.composeResult = appsession.SendResult{}
	s.inputMu.Unlock()
	s.RequestRedraw()
}

// cancelComposeSendLocked stops an asynchronous composed send before its next
// line. Its completion is stale: it cannot report a result or touch a draft
// typed after the cancellation. The caller holds inputMu.
func (s *State) cancelComposeSendLocked() {
	if s.composeCancel != nil {
		s.composeCancel()
		s.composeCancel = nil
	}
	if s.composeSending || s.composeReconnecting {
		s.composeSeq++
		s.composeSending = false
		s.composeReconnecting = false
	}
	s.composeBroadcast = false
}

// ComposeOpen reports whether the compose bar is showing.
func (s *State) ComposeOpen() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeOpen
}

// ComposeDraft returns the editable draft for the TextArea. It is UI-thread
// state.
func (s *State) ComposeDraft() *string {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return &s.composeDraft
}

// ComposeTrim returns the trim option for binding.
func (s *State) ComposeTrim() *bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return &s.composeTrim
}

// ComposeDecodeEscapes returns the escape-decoding option for binding.
func (s *State) ComposeDecodeEscapes() *bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return &s.composeDecodeEscapes
}

// ComposeLineMode returns the current line mode.
func (s *State) ComposeLineMode() appsession.ComposeLineMode {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeLineMode
}

// SetComposeLineMode changes how a multi-line draft reaches the shell.
func (s *State) SetComposeLineMode(mode appsession.ComposeLineMode) {
	if mode != appsession.ComposeLineByLine {
		mode = appsession.ComposePaste
	}
	s.inputMu.Lock()
	s.composeLineMode = mode
	s.inputMu.Unlock()
}

// SetComposeDraft replaces the draft. Tests use it to prove the preview and
// send path without typing into a native text field.
func (s *State) SetComposeDraft(draft string) {
	s.inputMu.Lock()
	s.composeDraft = draft
	s.inputMu.Unlock()
	s.RequestRedraw()
}

func (s *State) composeOptionsLocked() appsession.ComposeOptions {
	return appsession.ComposeOptions{
		Trim:          s.composeTrim,
		DecodeEscapes: s.composeDecodeEscapes,
		LineMode:      s.composeLineMode,
	}
}

// ComposePreview returns the transformed draft the bar previews.
func (s *State) ComposePreview() appsession.ComposePreview {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return appsession.TransformCompose(s.composeDraft, s.composeOptionsLocked())
}

// defaultComposeTargets is what Send delivers without an explicit compose
// choice: the broadcast set while broadcasting, else the focused Session.
// broadcast reports whether the set comes from the broadcast latch.
func (s *State) defaultComposeTargets() (ids []model.SessionID, broadcast bool) {
	s.inputMu.Lock()
	broadcasting := s.broadcasting
	selected := append([]model.SessionID(nil), s.broadcastTargetIDs...)
	s.inputMu.Unlock()
	if broadcasting {
		return selected, true
	}
	if id, ok := s.FocusedSessionID(); ok {
		return []model.SessionID{id}, false
	}
	return nil, false
}

// ComposeTargets reports where Send would deliver. An explicit choice in the
// compose bar wins; without one the default is the broadcast set while
// broadcasting, else the focused Session, which keeps the old behavior.
// broadcast reports whether the recipients are the broadcast latch's set, so
// only that case mirrors the delivery into the broadcast panel.
func (s *State) ComposeTargets() (ids []model.SessionID, broadcast bool) {
	s.refreshBroadcastState()
	s.inputMu.Lock()
	explicit := s.composeExplicit
	if explicit {
		ids = make([]model.SessionID, 0, len(s.composeTargets))
		for _, id := range s.mainSessionIDs {
			if s.composeTargets[id] {
				ids = append(ids, id)
			}
		}
	}
	s.inputMu.Unlock()
	if explicit {
		return ids, false
	}
	return s.defaultComposeTargets()
}

// ComposeTargetExplicit reports whether the compose bar holds a target choice
// of its own rather than following the broadcast/focus default.
func (s *State) ComposeTargetExplicit() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeExplicit
}

// ComposeTargetSelected reports whether id is one of the effective compose
// recipients, which is what the compose target menu checks.
func (s *State) ComposeTargetSelected(id model.SessionID) bool {
	ids, _ := s.ComposeTargets()
	for _, target := range ids {
		if target == id {
			return true
		}
	}
	return false
}

// SetComposeTarget selects or deselects one Session for Compose. The first
// choice seeds the explicit set from what Send would deliver now, so refining
// a default (dropping A from an A+B broadcast) keeps the other recipients.
// It never changes the toolbar broadcast selection.
func (s *State) SetComposeTarget(id model.SessionID, selected bool) {
	if id == "" {
		return
	}
	current, _ := s.ComposeTargets()
	s.inputMu.Lock()
	if !s.composeExplicit {
		s.composeTargets = make(map[model.SessionID]bool, len(current))
		for _, target := range current {
			s.composeTargets[target] = true
		}
		s.composeExplicit = true
	}
	if selected {
		s.composeTargets[id] = true
	} else {
		delete(s.composeTargets, id)
	}
	s.composeResult = appsession.SendResult{}
	s.composeNotice = 0
	s.inputMu.Unlock()
	s.RequestRedraw()
}

// ResetComposeTargets drops the explicit choice so Compose follows the
// broadcast/focus default again.
func (s *State) ResetComposeTargets() {
	s.inputMu.Lock()
	s.composeTargets = make(map[model.SessionID]bool)
	s.composeExplicit = false
	s.composeResult = appsession.SendResult{}
	s.composeNotice = 0
	s.inputMu.Unlock()
	s.RequestRedraw()
}

// ComposeTargetDiverges reports that an explicit compose set differs from the
// active broadcast selection. The bar shows it so a toolbar broadcast change
// is visibly not rewriting the chosen recipients. Without an active
// broadcast the focused Session is not a set worth warning about: the user
// chose the recipients deliberately.
func (s *State) ComposeTargetDiverges() bool {
	if !s.ComposeTargetExplicit() {
		return false
	}
	s.inputMu.Lock()
	broadcasting := s.broadcasting
	s.inputMu.Unlock()
	if !broadcasting {
		return false
	}
	ids, _ := s.ComposeTargets()
	return !sameSessionSet(ids, s.BroadcastTargets())
}

// ComposeNotice returns how many explicit compose targets left the main
// window in the latest structural change, so pruning is never silent.
func (s *State) ComposeNotice() int {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeNotice
}

// sameSessionSet compares two recipient lists without regard to order.
func sameSessionSet(left, right []model.SessionID) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[model.SessionID]bool, len(left))
	for _, id := range left {
		seen[id] = true
	}
	for _, id := range right {
		if !seen[id] {
			return false
		}
	}
	return true
}

// ComposeSending reports a send still in progress: the line-by-line sender or
// a queued send that has just been released by a reconnect.
func (s *State) ComposeSending() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeSending
}

// ComposeReconnecting reports that a submit is waiting for a disconnected
// target to come back. The draft stays in the editor during this wait.
func (s *State) ComposeReconnecting() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeReconnecting
}

// ComposeResult returns the most recent compose delivery, if any.
func (s *State) ComposeResult() (appsession.SendResult, bool) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeResult, len(s.composeResult.Deliveries) > 0
}

// composeSendOutcome distinguishes a rejected submit from one accepted now and
// one accepted but waiting for a reconnect. Only the first two clear the
// editor; a queued submit keeps the composed text visible.
type composeSendOutcome int

const (
	composeSendRejected composeSendOutcome = iota
	composeSendAccepted
	composeSendQueued
)

// SendCompose sends the bound draft to the compose bar's target set: an
// explicit choice when one was made, otherwise the broadcast set while
// broadcasting or the focused Session. It reports whether the send was
// accepted (a non-empty transformed draft with at least one target), not
// whether every target delivered. A disconnected target is reconnected first;
// the send is accepted then but delivered when it is ready. The draft clears
// when the send is accepted, as the original compose bar cleared its field
// immediately, except while a reconnect is pending so the text is not lost.
func (s *State) SendCompose() bool {
	return s.sendCompose() != composeSendRejected
}

// SendComposeDraft sends editor text that may be ahead of the bound draft.
// OnTextKey sees the text typed with the key before the next frame writes the
// bound value, so Enter must send what the key carries. It reports whether the
// editor should clear now: a reconnect wait keeps the text in place, and the
// send path clears it once the target is ready.
func (s *State) SendComposeDraft(draft string) bool {
	s.inputMu.Lock()
	s.composeDraft = draft
	s.inputMu.Unlock()
	return s.sendCompose() == composeSendAccepted
}

func (s *State) sendCompose() composeSendOutcome {
	if s.sessions == nil {
		return composeSendRejected
	}
	s.refreshBroadcastState()
	s.inputMu.Lock()
	if s.composeSending || s.composeReconnecting {
		s.inputMu.Unlock()
		return composeSendRejected
	}
	preview := appsession.TransformCompose(s.composeDraft, s.composeOptionsLocked())
	if preview.Empty() {
		s.inputMu.Unlock()
		return composeSendRejected
	}
	draft := s.composeDraft
	s.inputMu.Unlock()

	// Send follows the compose bar's own target set. The broadcast latch only
	// supplies the default when the bar holds no explicit choice.
	targets, broadcasting := s.ComposeTargets()
	if len(targets) == 0 {
		return composeSendRejected
	}

	// A target that is still connecting is not sent to yet: the bytes would be
	// gated or lost. Request an immediate retry where the Session allows one,
	// then hold the draft until every waiting target can take input.
	if waiting := s.composeReconnectTargets(targets); len(waiting) > 0 {
		return s.queueComposeAfterReconnect(targets, waiting, preview, broadcasting, draft)
	}

	s.dispatchComposeNow(targets, preview, broadcasting)
	return composeSendAccepted
}

// dispatchComposeNow sends a direct compose submit and clears the draft.
func (s *State) dispatchComposeNow(targets []model.SessionID, preview appsession.ComposePreview, broadcasting bool) {
	lineByLine := preview.LineMode == appsession.ComposeLineByLine
	chunks := preview.Chunks
	if !lineByLine {
		chunks = chunks[:1]
	}
	async := lineByLine && len(chunks) > 1

	s.inputMu.Lock()
	s.composeDraft = ""
	s.composeBroadcast = broadcasting
	var seq uint64
	var ctx context.Context
	if async {
		seq = s.composeSeq + 1
		s.composeSeq = seq
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		s.composeCancel = cancel
		s.composeSending = true
	}
	s.inputMu.Unlock()

	if async {
		go s.sendComposedChunks(ctx, seq, targets, preview)
		return
	}
	// Paste mode lets each target's peer state decide whether a multiline
	// draft travels as one bracketed paste. A single line-by-line chunk keeps
	// the raw path.
	var result appsession.SendResult
	if preview.LineMode == appsession.ComposePaste {
		result = s.sessions.SendCompose(targets, preview)
	} else {
		result = s.sessions.SendInput(targets, chunks[0])
	}
	s.finishCompose(0, result)
}

// composeWaitTarget is one disconnected recipient. The Session pointer is
// resolved on the UI thread before the wait starts, so the polling goroutine
// never reads the Tabs.
type composeWaitTarget struct {
	id      model.SessionID
	session *appsession.Session
}

// composeReconnectTargets requests an immediate retry for each running target
// whose transport is not active and returns the ones the submit must wait for.
// A target already reconnecting is waited for without a second request. An
// active target is never touched, so a busy but connected Session keeps its
// old behavior.
func (s *State) composeReconnectTargets(targets []model.SessionID) []composeWaitTarget {
	var waiting []composeWaitTarget
	for _, id := range targets {
		target := composeWaitTarget{id: id}
		if session, ok := s.SessionByID(id); ok {
			target.session = session
		}
		status, connection, ok := s.composeWaitStatus(target)
		if !ok || status != appsession.StatusRunning {
			continue
		}
		switch connection {
		case appsession.ConnectionStatusActive:
			continue
		case appsession.ConnectionStatusReconnecting:
			waiting = append(waiting, target)
		default:
			if s.composeWaitReconnect(target) {
				waiting = append(waiting, target)
			}
		}
	}
	return waiting
}

// queueComposeAfterReconnect records the captured draft and dispatches it only
// after every waiting target is ready. The composed text stays in the editor
// for the whole wait, so surfacing the reconnect never loses it.
func (s *State) queueComposeAfterReconnect(targets []model.SessionID, waiting []composeWaitTarget, preview appsession.ComposePreview, broadcasting bool, draft string) composeSendOutcome {
	s.inputMu.Lock()
	if s.composeSending || s.composeReconnecting {
		s.inputMu.Unlock()
		return composeSendRejected
	}
	seq := s.composeSeq + 1
	s.composeSeq = seq
	ctx, cancel := context.WithCancel(context.Background())
	s.composeCancel = cancel
	s.composeReconnecting = true
	s.composeBroadcast = broadcasting
	s.inputMu.Unlock()
	s.RequestRedraw()

	go s.waitForComposeConnection(ctx, seq, targets, waiting, preview, broadcasting, draft)
	return composeSendQueued
}

// waitForComposeConnection waits on the real transport state with a hard
// deadline. Cancellation (Escape, Close, StopBroadcast) ends it without a
// result.
func (s *State) waitForComposeConnection(ctx context.Context, seq uint64, targets []model.SessionID, waiting []composeWaitTarget, preview appsession.ComposePreview, broadcasting bool, draft string) {
	deadline := time.Now().Add(composeReconnectTimeout)
	ticker := time.NewTicker(composeReconnectPoll)
	defer ticker.Stop()
	for {
		if s.composeTargetsReady(waiting) {
			break
		}
		if !s.composeWaitPossible(waiting) || !time.Now().Before(deadline) {
			s.abandonComposeReconnect(seq)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
	s.dispatchQueuedCompose(ctx, seq, targets, preview, broadcasting, draft)
}

// dispatchQueuedCompose sends the captured submit after the wait. Only the
// captured text is sent; text typed while waiting is left in the editor.
func (s *State) dispatchQueuedCompose(ctx context.Context, seq uint64, targets []model.SessionID, preview appsession.ComposePreview, broadcasting bool, draft string) {
	s.inputMu.Lock()
	if seq != s.composeSeq || !s.composeOpen || ctx.Err() != nil {
		s.inputMu.Unlock()
		return
	}
	if s.composeDraft == draft {
		s.composeDraft = ""
	}
	s.composeReconnecting = false
	s.composeCancel = nil
	// Hold the re-entry guard until the queued bytes are handed to the
	// Sessions, so a second Enter cannot duplicate them.
	s.composeSending = true
	s.composeBroadcast = broadcasting
	s.inputMu.Unlock()
	s.RequestRedraw()

	s.sendComposedChunks(ctx, seq, targets, preview)
}

// abandonComposeReconnect ends an unsuccessful wait. The draft stays in the
// editor so the user can retry once the Session is back.
func (s *State) abandonComposeReconnect(seq uint64) {
	s.inputMu.Lock()
	if seq != s.composeSeq || !s.composeOpen {
		s.inputMu.Unlock()
		return
	}
	s.composeReconnecting = false
	s.composeCancel = nil
	s.inputMu.Unlock()
	s.RequestRedraw()
}

// sendComposedChunks is the off-UI-thread send for a queue released by a
// reconnect. The context stops a line-by-line draft before its next line.
func (s *State) sendComposedChunks(ctx context.Context, seq uint64, targets []model.SessionID, preview appsession.ComposePreview) {
	var result appsession.SendResult
	switch {
	case preview.LineMode == appsession.ComposeLineByLine:
		result = s.sessions.SendChunksUntil(ctx, targets, preview.Chunks, appsession.ComposeLineDelay)
	case ctx.Err() != nil:
		// Canceled after the wait: deliver nothing.
	case preview.LineMode == appsession.ComposePaste:
		result = s.sessions.SendCompose(targets, preview)
	case len(preview.Chunks) > 0:
		result = s.sessions.SendInput(targets, preview.Chunks[0])
	}
	s.finishCompose(seq, result)
}

// composeWaitStatus reads one waiting target's lifecycle and transport state.
// The test seam wins; otherwise the already-resolved Session supplies both.
func (s *State) composeWaitStatus(target composeWaitTarget) (appsession.Status, appsession.ConnectionStatus, bool) {
	if s.composeConnection.Status != nil {
		return s.composeConnection.Status(target.id)
	}
	if target.session == nil {
		return "", "", false
	}
	snapshot := target.session.Snapshot()
	return snapshot.Status, snapshot.Connection, true
}

// composeWaitReconnect asks one target to dial again immediately.
func (s *State) composeWaitReconnect(target composeWaitTarget) bool {
	if s.composeConnection.Reconnect != nil {
		return s.composeConnection.Reconnect(target.id)
	}
	if target.session == nil {
		return false
	}
	return target.session.ReconnectNow()
}

// composeWaitReady reports that one target can receive input again.
func (s *State) composeWaitReady(target composeWaitTarget) bool {
	if s.composeConnection.Ready != nil {
		return s.composeConnection.Ready(target.id)
	}
	if target.session == nil {
		return false
	}
	snapshot := target.session.Snapshot()
	return snapshot.Status == appsession.StatusRunning && snapshot.Connection == appsession.ConnectionStatusActive
}

func (s *State) composeTargetsReady(targets []composeWaitTarget) bool {
	for _, target := range targets {
		if !s.composeWaitReady(target) {
			return false
		}
	}
	return true
}

// composeWaitPossible reports that every target still exists and is running.
// A target that ended during the wait cannot become ready, so the submit
// stops early instead of waiting for the deadline.
func (s *State) composeWaitPossible(targets []composeWaitTarget) bool {
	for _, target := range targets {
		status, _, ok := s.composeWaitStatus(target)
		if !ok || status != appsession.StatusRunning {
			return false
		}
	}
	return true
}

// finishCompose records an accepted send's result. seq identifies an
// asynchronous send: after CloseCompose or StopBroadcast, a stale completion
// neither reports a result nor changes any later draft.
func (s *State) finishCompose(seq uint64, result appsession.SendResult) {
	s.inputMu.Lock()
	if seq != 0 && (seq != s.composeSeq || !s.composeOpen) {
		s.inputMu.Unlock()
		return
	}
	if seq != 0 {
		s.composeCancel = nil
		s.composeSending = false
	}
	s.composeResult = result
	// A compose send to the broadcast set is still broadcast input: the
	// target panel shows the same per-Session delivery status as typed input.
	if s.composeBroadcast {
		s.broadcastResult = result
	}
	s.composeBroadcast = false
	s.inputMu.Unlock()
	s.RequestRedraw()
}

// InsertIntoFocused puts text into the focused Pane's line editor without
// submitting it. It returns the surface's error unchanged, so the composition
// root can tell an unsafe multi-line insertion from a missing Session. A
// Surface that cannot insert is reported as unavailable rather than falling
// back to typing, which could submit a line.
func (s *State) InsertIntoFocused(text string) error {
	surface := s.ActiveSurface()
	if surface == nil {
		return ErrNoActiveTab
	}
	inserter, ok := surface.(TextInserter)
	if !ok {
		return ErrNoTextInserter
	}
	if err := inserter.InsertText(text); err != nil {
		return err
	}
	s.RequestRedraw()
	return nil
}

// SendToFocused types text into the focused Session: run submits it with
// Enter, otherwise it is inserted into the line editor without submitting.
// The insertion goes through the same safe path as the History Insert action,
// so a multi-line command is refused when the shell cannot paste it rather
// than typing newlines that would execute it. It reports false when no Session
// is focused or the Session did not take the input.
func (s *State) SendToFocused(text string, run bool) bool {
	if !run {
		return s.InsertIntoFocused(text) == nil
	}
	if s.sessions == nil {
		return false
	}
	id, ok := s.FocusedSessionID()
	if !ok {
		return false
	}
	preview := appsession.TransformCompose(text, appsession.ComposeOptions{NoAutoRun: false})
	if preview.Empty() {
		return false
	}
	return s.sessions.SendInput([]model.SessionID{id}, preview.Chunks[0]).Complete()
}
