package state

import (
	apptransfer "github.com/mslxl/natcatty/application/transfer"
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

// OpenCenter shows the global Transfer center.
func (s *State) OpenCenter() {
	if s.transferQueue == nil {
		s.notice = "transfer-unavailable"
		s.RequestRedraw()
		return
	}
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
// updates redraw; a terminal row finishes the operation with the same refresh
// and conflict handling as a direct copy.
func (s *State) applyQueueTask(task domainsftp.Task) {
	op := s.operation
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
	op.state.Current = task.Name()
	op.state.Running = !task.Terminal()
	op.mu.Unlock()

	if !task.Terminal() {
		s.RequestRedraw()
		return
	}

	op.mu.Lock()
	retry := op.retry
	refresh := op.refresh
	op.mu.Unlock()
	if task.Err != nil && apptransfer.KindOf(task.Err) == apptransfer.KindExists && retry != nil {
		s.releaseOperation(op)
		s.askOverwrite(s.focused, task.Sources, retry)
		return
	}
	s.finishOperationOutcome(op, task.Err,
		task.Status == domainsftp.TaskCancelled, refresh...)
}
