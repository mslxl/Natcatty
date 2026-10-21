package state

import (
	"context"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

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
	targets := make([]model.SessionID, 0, len(order))
	for _, id := range order {
		if s.broadcastTargets[id] {
			targets = append(targets, id)
		}
	}
	s.broadcastSessions = present
	s.broadcastTargetIDs = targets
	if removed > 0 {
		s.broadcastNotice += removed
		// Results and per-Session statuses described the old target set.
		s.broadcastResult = appsession.SendResult{}
		s.composeResult = appsession.SendResult{}
		if s.composeBroadcast {
			// Stop a composed broadcast before its next line so a Session
			// that just left the main window receives no further chunks.
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
				Label:     value.descriptor.Label,
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
	// stale, and answers any notice about removed targets.
	s.broadcastResult = appsession.SendResult{}
	s.composeResult = appsession.SendResult{}
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
	s.composeResult = appsession.SendResult{}
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
	if s.composeSending {
		s.composeSeq++
		s.composeSending = false
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

// SetComposeDraft replaces the draft. The selftest uses it to prove the
// preview and send path without typing into a native text field.
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

// ComposeTargets reports where Send would deliver: the active broadcast set,
// or the focused Session.
func (s *State) ComposeTargets() (ids []model.SessionID, broadcast bool) {
	s.refreshBroadcastState()
	s.inputMu.Lock()
	broadcasting := s.broadcasting
	selected := s.selectedBroadcastTargetIDsLocked()
	s.inputMu.Unlock()
	if broadcasting {
		return selected, true
	}
	if id, ok := s.FocusedSessionID(); ok {
		return []model.SessionID{id}, false
	}
	return nil, false
}

// ComposeSending reports a line-by-line send still in progress.
func (s *State) ComposeSending() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeSending
}

// ComposeResult returns the most recent compose delivery, if any.
func (s *State) ComposeResult() (appsession.SendResult, bool) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeResult, len(s.composeResult.Deliveries) > 0
}

// SendCompose sends the bound draft to the focused Session, or to the
// broadcast target set while broadcast is active. It reports whether the send
// was accepted (a non-empty transformed draft with at least one target), not
// whether every target delivered. The draft clears when the send is accepted,
// as the original compose bar cleared its field immediately; the per-target
// result reports partial failure afterwards.
func (s *State) SendCompose() bool {
	return s.sendCompose()
}

// SendComposeDraft sends editor text that may be ahead of the bound draft.
// OnTextKey sees the text typed with the key before the next frame writes the
// bound value, so Enter must send what the key carries. It reports whether the
// send was accepted, which lets the caller clear the editor.
func (s *State) SendComposeDraft(draft string) bool {
	s.inputMu.Lock()
	s.composeDraft = draft
	s.inputMu.Unlock()
	return s.sendCompose()
}

func (s *State) sendCompose() bool {
	if s.sessions == nil {
		return false
	}
	s.refreshBroadcastState()
	s.inputMu.Lock()
	if s.composeSending {
		s.inputMu.Unlock()
		return false
	}
	preview := appsession.TransformCompose(s.composeDraft, s.composeOptionsLocked())
	if preview.Empty() {
		s.inputMu.Unlock()
		return false
	}
	broadcasting := s.broadcasting
	var targets []model.SessionID
	if broadcasting {
		targets = s.selectedBroadcastTargetIDsLocked()
	}
	lineByLine := preview.LineMode == appsession.ComposeLineByLine
	chunks := preview.Chunks
	if !lineByLine {
		chunks = chunks[:1]
	}
	async := lineByLine && len(chunks) > 1
	s.inputMu.Unlock()

	if !broadcasting {
		if id, ok := s.FocusedSessionID(); ok {
			targets = []model.SessionID{id}
		}
	}
	if len(targets) == 0 {
		return false
	}

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
		go func() {
			result := s.sessions.SendChunksUntil(ctx, targets, chunks, appsession.ComposeLineDelay)
			s.finishCompose(seq, result)
		}()
		return true
	}
	result := s.sessions.SendInput(targets, chunks[0])
	s.finishCompose(0, result)
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
