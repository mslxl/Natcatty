package state

import (
	"errors"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// WithQueue enables the global Transfer queue behind the surface's copy
// actions and exposes it to the Transfer center. Without a queue the surface
// keeps its one-operation behavior, which the tests and detached surfaces
// rely on.
func WithQueue(queue *apptransfer.Queue) Option {
	return func(s *State) {
		if queue == nil {
			return
		}
		s.transferQueue = queue
		queue.Observe(func(task domainsftp.Task) {
			s.post(func() { s.applyQueueTask(task) })
		})
	}
}

// Queue returns the global Transfer queue, or nil when the surface owns no
// queue.
func (s *State) Queue() *apptransfer.Queue { return s.transferQueue }

// QueueEnabled reports whether pane transfers are scheduled by the global
// queue.
func (s *State) QueueEnabled() bool { return s.transferQueue != nil }

// QueueTasks returns every visible Transfer row.
func (s *State) QueueTasks() []domainsftp.Task {
	if s.transferQueue == nil {
		return nil
	}
	return s.transferQueue.Tasks()
}

// TransferStats returns the scheduler counters, or the zero value when the
// surface has no queue.
func (s *State) TransferStats() apptransfer.QueueStats {
	if s.transferQueue == nil {
		return apptransfer.QueueStats{}
	}
	return s.transferQueue.Stats()
}

// TransferConcurrency returns the per-resource file limit.
func (s *State) TransferConcurrency() int {
	if s.transferQueue == nil {
		return domainsftp.DefaultConcurrency
	}
	return s.transferQueue.Concurrency()
}

// CenterOpen reports whether the Transfer center is shown.
func (s *State) CenterOpen() bool { return s.centerOpen }

// OpenCenter shows the global Transfer center. Reopening it starts from the
// rows' current state, so it does not repeat a control refusal the user has
// since navigated away from; a stale notice must not outlive the condition
// that produced it.
func (s *State) OpenCenter() {
	if s.transferQueue == nil {
		s.notice = "transfer-unavailable"
		s.RequestRedraw()
		return
	}
	s.centerError = ""
	s.centerOpen = true
	s.RequestRedraw()
}

// CloseCenter hides the Transfer center.
func (s *State) CloseCenter() {
	if s.centerOpen {
		s.centerOpen = false
		s.RequestRedraw()
	}
}

// ToggleCenter opens or closes the Transfer center.
func (s *State) ToggleCenter() {
	if s.centerOpen {
		s.CloseCenter()
		return
	}
	s.OpenCenter()
}

// reattachHost binds a just-connected Host's filesystem to every interrupted
// row that used it, so the Transfer center's Resume continues from the
// checkpoint instead of asking for a reconnect that already happened. It runs
// on the UI thread: the peer snapshot must see the panes the user does. The
// queue work runs off the UI thread, and a failure leaves the rows
// reconnect-required rather than pretending they can continue.
func (s *State) reattachHost(host model.HostID, remote apptransfer.FileSystem) {
	if s.transferQueue == nil || s.service == nil || remote == nil || host == "" {
		return
	}
	peers := map[string]apptransfer.FileSystem{}
	for _, side := range Sides() {
		for _, tab := range s.panes[side].Tabs {
			if tab == nil || tab.closed || tab.Kind != KindRemote || tab.FS == nil {
				continue
			}
			peers[string(tab.HostID)] = tab.FS
		}
	}
	queue := s.transferQueue
	openLocal := s.service.OpenLocal
	ctx := s.ctx
	go func() {
		// A partial bind still changed rows, so redraw either way; a row that
		// could not bind keeps the reconnect prompt.
		bound, _ := queue.ReattachHost(ctx, apptransfer.HostReattach{
			Host:      string(host),
			Remote:    remote,
			Peers:     peers,
			OpenLocal: openLocal,
		})
		s.post(func() {
			// The reattach removes the condition a refused Resume reported, so
			// its banner must go with it; a bind that changed nothing keeps
			// the notice truthful.
			if bound > 0 {
				s.centerError = ""
			}
			s.RequestRedraw()
		})
	}()
}

// PauseTransfer pauses one queue row.
func (s *State) PauseTransfer(id domainsftp.TaskID) {
	s.controlTransfer(id, func(queue *apptransfer.Queue) error { return queue.Pause(id) })
}

// ResumeTransfer resumes one queue row.
func (s *State) ResumeTransfer(id domainsftp.TaskID) {
	s.controlTransfer(id, func(queue *apptransfer.Queue) error { return queue.Resume(id) })
}

// RetryTransfer replays one queue row.
func (s *State) RetryTransfer(id domainsftp.TaskID) {
	s.controlTransfer(id, func(queue *apptransfer.Queue) error { return queue.Retry(id) })
}

// CancelTransfer cancels one queue row.
func (s *State) CancelTransfer(id domainsftp.TaskID) {
	s.controlTransfer(id, func(queue *apptransfer.Queue) error { return queue.Cancel(id) })
}

func (s *State) controlTransfer(id domainsftp.TaskID, action func(*apptransfer.Queue) error) {
	if s.transferQueue == nil {
		s.notice = "transfer-unavailable"
		s.RequestRedraw()
		return
	}
	err := action(s.transferQueue)
	switch {
	case err == nil:
		s.centerError = ""
	case err == domainsftp.ErrTaskUnavailable:
		s.centerError = "transfer-reconnect-required"
	case err == domainsftp.ErrTaskNotRetryable:
		s.centerError = "transfer-not-retryable"
	default:
		s.centerError = "transfer-control-failed"
	}
	s.RequestRedraw()
}

// CenterError returns the last Transfer-center control problem code, or "".
func (s *State) CenterError() string { return s.centerError }

// applyQueueTask mirrors a queue snapshot onto the operation bar. Progress
// updates redraw; an attention row opens the conflict prompt; a terminal row
// finishes the operation with the same refresh handling as a direct copy.
func (s *State) applyQueueTask(task domainsftp.Task) {
	op := s.queueOperations[task.ID]
	if op == nil {
		return
	}
	op.mu.Lock()
	if op.taskID != task.ID {
		op.mu.Unlock()
		return
	}
	op.state.Status = task.Status
	op.state.Speed = task.Speed
	op.state.BytesDone = task.BytesDone
	op.state.BytesTotal = task.BytesTotal
	op.state.FilesDone = task.FilesDone
	op.state.FilesTotal = task.FilesTotal
	op.state.Skipped = task.Skipped
	op.state.Current = task.Name()
	op.state.Running = !task.Terminal()
	op.mu.Unlock()

	if task.Status == domainsftp.TaskAttention && task.PendingConflict != nil {
		op.mu.Lock()
		prompted := op.prompted
		op.prompted = true
		op.mu.Unlock()
		if !prompted {
			s.queueConflictPrompt(task.ID)
		}
		s.RequestRedraw()
		return
	}
	op.mu.Lock()
	op.prompted = false
	op.mu.Unlock()
	if !task.Terminal() {
		s.RequestRedraw()
		return
	}
	delete(s.queueOperations, task.ID)

	op.mu.Lock()
	refresh := op.refresh
	op.mu.Unlock()
	s.finishOperationOutcome(op, task.Err,
		task.Status == domainsftp.TaskCancelled, refresh...)
}

// queueConflictPrompt queues one attention row for the conflict dialog. More
// than one Transfer can reach a conflict at once, so the prompts are shown one
// at a time and each keeps its own answer.
func (s *State) queueConflictPrompt(id domainsftp.TaskID) {
	if id == "" || id == s.conflictShowing {
		return
	}
	for _, queued := range s.promptQueue {
		if queued == id {
			return
		}
	}
	s.promptQueue = append(s.promptQueue, id)
	s.showNextConflictPrompt()
}

// showNextConflictPrompt opens the next queued conflict that is still waiting
// for an answer. A row that was resolved by another answer or cancelled while
// queued is skipped instead of showing a stale prompt.
func (s *State) showNextConflictPrompt() {
	if s.dialog != nil || s.transferQueue == nil {
		return
	}
	for len(s.promptQueue) > 0 {
		id := s.promptQueue[0]
		s.promptQueue = s.promptQueue[1:]
		task, ok := s.transferQueue.Task(id)
		if !ok || task.Status != domainsftp.TaskAttention || task.PendingConflict == nil {
			continue
		}
		conflict := *task.PendingConflict
		s.conflictShowing = id
		s.showConflictPrompt(conflict, id, func(action apptransfer.ConflictPolicy, applyAll bool) {
			s.resolveQueuedConflict(id, action, applyAll)
		})
		return
	}
}

// showConflictPrompt opens the shared five-action conflict dialog. A direct
// request passes an empty TaskID; a queued row passes its durable identity so
// the answer reaches the row that asked.
func (s *State) showConflictPrompt(conflict domainsftp.Conflict, taskID domainsftp.TaskID,
	resolve func(action apptransfer.ConflictPolicy, applyAll bool)) {
	copied := conflict
	s.dialog = &Dialog{
		Kind:     DialogConflict,
		Side:     s.focused,
		Conflict: &copied,
		TaskID:   taskID,
		resolve:  resolve,
	}
	s.notice = "conflict-required"
	s.RequestRedraw()
}

// resolveQueuedConflict forwards the chosen action to the queue. An answer
// that no longer matches a waiting row is reported in the center rather than
// silently ignored.
func (s *State) resolveQueuedConflict(id domainsftp.TaskID, action apptransfer.ConflictPolicy, applyAll bool) {
	if s.transferQueue == nil {
		return
	}
	err := s.transferQueue.ResolveConflict(id, action, applyAll)
	switch {
	case err == nil:
		// Keep an unrelated reconnect notice; only the conflict error is stale.
		if s.centerError == "transfer-conflict-failed" {
			s.centerError = ""
		}
	case errors.Is(err, apptransfer.ErrConflictNotPending) ||
		errors.Is(err, apptransfer.ErrConflictActionInvalid):
		s.centerError = "transfer-conflict-failed"
	default:
		s.centerError = "transfer-control-failed"
	}
	s.RequestRedraw()
}
