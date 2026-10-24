package state

import (
	"context"
	"sync"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// Operation is a snapshot of the running or last finished file operation.
type Operation struct {
	Kind       domainsftp.OperationKind
	Current    string
	FilesDone  int
	FilesTotal int
	BytesDone  int64
	BytesTotal int64
	Running    bool
	Cancelled  bool
	// Status is the queue row's lifecycle state when the operation is
	// queue-backed; a direct operation leaves it empty.
	Status domainsftp.TaskStatus
	// Speed is the last measured bytes per second for a queue-backed
	// operation.
	Speed int64
	// Err is the failure, or nil. A cancelled operation reports
	// KindCancelled here as well as Cancelled.
	Err error

	cancel context.CancelFunc
}

// Metadata reports whether the operation changes entries without moving file
// contents. Such an operation has no byte or file totals to show.
func (o Operation) Metadata() bool {
	switch o.Kind {
	case domainsftp.OpMkdir, domainsftp.OpCreateFile, domainsftp.OpRename, domainsftp.OpChmod:
		return true
	}
	return false
}

// Percent returns the byte progress in 0..100, or 0 when the size is not yet
// known.
func (o Operation) Percent() int {
	if o.BytesTotal <= 0 {
		if o.FilesTotal > 0 {
			return o.FilesDone * 100 / o.FilesTotal
		}
		return 0
	}
	percent := int(o.BytesDone * 100 / o.BytesTotal)
	if percent > 100 {
		percent = 100
	}
	return percent
}

// ETASeconds mirrors the queue's estimate for the operation bar: only a
// transferring row with a trustworthy speed and a remaining byte count has an
// honest ETA.
func (o Operation) ETASeconds() int {
	if o.Status != domainsftp.TaskTransferring || o.Speed < 1024 {
		return 0
	}
	remaining := o.BytesTotal - o.BytesDone
	if remaining <= 0 {
		return 0
	}
	seconds := (remaining + o.Speed - 1) / o.Speed
	if seconds <= 0 || seconds > 48*3600 {
		return 0
	}
	return int(seconds)
}

// operationState is the mutable operation behind the snapshot.
type operationState struct {
	mu     sync.Mutex
	kind   domainsftp.OperationKind
	state  Operation
	cancel context.CancelFunc
	// taskID links a queue-backed operation to its global Transfer row.
	taskID domainsftp.TaskID
	// refresh lists the Tabs to reload when the operation ends.
	refresh []*Tab
	// retry restarts the request after an overwrite confirmation.
	retry func()
	// cleanup releases a resource the operation opened, such as the local
	// filesystem behind an OS file drop.
	cleanup func()
}

func (o *operationState) snapshot() Operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := o.state
	out.Kind = o.kind
	return out
}

func (o *operationState) setProgress(progress apptransfer.Progress) {
	o.mu.Lock()
	o.state.Current = progress.Current
	o.state.FilesDone = progress.FilesDone
	o.state.FilesTotal = progress.FilesTotal
	o.state.BytesDone = progress.BytesDone
	o.state.BytesTotal = progress.BytesTotal
	o.mu.Unlock()
}

// Snapshot returns the visible operation, or nil when none has started.
func (s *State) Snapshot() *Operation {
	if s.operation == nil {
		return nil
	}
	snapshot := s.operation.snapshot()
	return &snapshot
}

// Busy reports whether a file operation is running. Another operation is
// refused while one is in flight.
func (s *State) Busy() bool {
	if s.operation == nil {
		return false
	}
	s.operation.mu.Lock()
	defer s.operation.mu.Unlock()
	return s.operation.state.Running
}

// DismissOperation clears a finished operation's result.
func (s *State) DismissOperation() {
	if s.operation == nil {
		return
	}
	if s.Busy() {
		return
	}
	s.operation = nil
	s.RequestRedraw()
}

// CancelOperation interrupts the running operation. The copy engine checks
// cancellation between chunks and removes a partially written file.
func (s *State) CancelOperation() {
	if s.operation == nil {
		return
	}
	s.operation.mu.Lock()
	cancel := s.operation.cancel
	s.operation.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// beginOperation reserves the single operation slot.
func (s *State) beginOperation(kind domainsftp.OperationKind) (*operationState, context.Context, bool) {
	if s.Busy() {
		s.notice = "busy"
		s.RequestRedraw()
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(s.ctx)
	op := &operationState{
		kind:   kind,
		cancel: cancel,
		state: Operation{
			Kind:    kind,
			Running: true,
		},
	}
	op.cancel = cancel
	s.operation = op
	s.RequestRedraw()
	return op, ctx, true
}

// enqueueOperation hands the request to the global Transfer queue. The
// operation bar mirrors the queue row through applyQueueTask, so pause,
// retry, and cancel stay in the Transfer center while the pane copy behaves
// as before. retry re-runs the request after the overwrite confirmation;
// cleanup releases a resource the queue now owns.
func (s *State) enqueueOperation(op *operationState, spec apptransfer.TaskSpec,
	refresh []*Tab, retry func(), cleanup func()) bool {
	taskID, err := s.transferQueue.Enqueue(spec)
	if err != nil {
		s.releaseOperation(op)
		s.notice = "operation-failed"
		s.RequestRedraw()
		return false
	}
	s.attachQueuedOperation(op, taskID, refresh, retry, cleanup)
	return true
}

// attachQueuedOperation links an already-enqueued row to the operation bar.
// The operation's context is released because the queue owns cancellation
// now.
func (s *State) attachQueuedOperation(op *operationState, taskID domainsftp.TaskID,
	refresh []*Tab, retry func(), cleanup func()) {
	op.mu.Lock()
	previous := op.cancel
	op.taskID = taskID
	op.refresh = refresh
	op.retry = retry
	op.cleanup = cleanup
	queue := s.transferQueue
	op.cancel = func() { _ = queue.Cancel(taskID) }
	op.mu.Unlock()
	if previous != nil {
		previous()
	}
}

// finishOperation publishes the outcome and refreshes the affected Tabs.
func (s *State) finishOperation(op *operationState, err error, refresh ...*Tab) {
	s.finishOperationOutcome(op, err,
		err != nil && apptransfer.KindOf(err) == apptransfer.KindCancelled, refresh...)
}

// finishOperationOutcome is finishOperation for a row whose cancellation is
// carried by its status rather than an error: a queued row cancelled before
// it ran settles with no error.
func (s *State) finishOperationOutcome(op *operationState, err error, cancelled bool, refresh ...*Tab) {
	op.mu.Lock()
	cleanup := op.cleanup
	op.cleanup = nil
	op.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
	op.mu.Lock()
	op.state.Running = false
	op.state.Err = err
	op.state.Cancelled = cancelled
	op.mu.Unlock()
	if cancelled {
		s.notice = "operation-cancelled"
	} else if err != nil {
		s.notice = "operation-failed"
	} else {
		s.notice = "operation-done"
	}
	for _, tab := range refresh {
		if tab != nil && tab.FS != nil && !tab.closed {
			s.loadAt(tab, tab.Path, false)
		}
	}
	s.RequestRedraw()
}

// Transfer copies the focused pane's selection to the other pane. Upload and
// download label the direction; the copy engine is the same.
func (s *State) Transfer(side Side) {
	s.transferTo(side, side.Other(), false)
}

// MoveToOther moves the focused pane's selection to the other pane.
func (s *State) MoveToOther(side Side) {
	s.transferTo(side, side.Other(), true)
}

// transferTo copies or moves between two sides.
func (s *State) transferTo(from, to Side, move bool) {
	source := s.Tab(from)
	dest := s.Tab(to)
	if source == nil || dest == nil || source.FS == nil || dest.FS == nil {
		s.notice = "pane-unavailable"
		s.RequestRedraw()
		return
	}
	paths := source.SelectedPaths()
	if len(paths) == 0 {
		s.notice = "nothing-selected"
		s.RequestRedraw()
		return
	}
	destDir := dest.Destination()
	if destDir == "" {
		s.notice = "pane-unavailable"
		s.RequestRedraw()
		return
	}
	if problem := copyDestinationProblem(source.FS, dest.FS, paths, destDir, move); problem != "" {
		s.notice = problem
		s.RequestRedraw()
		return
	}
	kind := transferKind(source, dest)
	s.startCopy(source, dest, paths, destDir, move, kind, apptransfer.ConflictStop, s.refreshAfter(source, dest, move))
}

// transferKind labels a transfer by the panes it connects.
func transferKind(source, dest *Tab) domainsftp.OperationKind {
	switch {
	case source.Kind == KindLocal && dest.Kind == KindRemote:
		return domainsftp.OpUpload
	case source.Kind == KindRemote && dest.Kind == KindLocal:
		return domainsftp.OpDownload
	default:
		return domainsftp.OpCopy
	}
}

// refreshAfter returns the Tabs to reload once a transfer finishes: the
// destination always, and the source after a move.
func (s *State) refreshAfter(source, dest *Tab, move bool) []*Tab {
	if move {
		return []*Tab{dest, source}
	}
	return []*Tab{dest}
}

// startCopy runs a copy request and offers replacement when the destination
// exists.
func (s *State) startCopy(source, dest *Tab, paths []string, destDir string, move bool, kind domainsftp.OperationKind, conflict apptransfer.ConflictPolicy, refresh []*Tab) bool {
	if s.Busy() {
		s.notice = "busy"
		s.RequestRedraw()
		return false
	}
	op, ctx, ok := s.beginOperation(kind)
	if !ok {
		return false
	}
	if s.transferQueue != nil {
		retry := func() {
			s.startCopy(source, dest, paths, destDir, move, kind, apptransfer.ConflictReplace, refresh)
		}
		return s.enqueueOperation(op, apptransfer.TaskSpec{
			Kind:        kind,
			Source:      source.FS,
			Dest:        dest.FS,
			Sources:     paths,
			DestDir:     destDir,
			Move:        move,
			Conflict:    conflict,
			SourceLabel: source.Title(),
			DestLabel:   dest.Title(),
		}, refresh, retry, nil)
	}
	request := apptransfer.CopyRequest{
		Source:   source.FS,
		Dest:     dest.FS,
		Sources:  append([]string(nil), paths...),
		DestDir:  destDir,
		Move:     move,
		Kind:     kind,
		Conflict: conflict,
		Progress: func(progress apptransfer.Progress) {
			op.setProgress(progress)
			s.RequestRedraw()
		},
	}
	go func() {
		_, err := s.service.Copy(ctx, request)
		s.post(func() {
			if err != nil && apptransfer.KindOf(err) == apptransfer.KindExists {
				// A conflict is a question, not a failure. Release the
				// operation slot so the confirmation prompt is the only thing
				// asking the user, and confirming starts a fresh operation.
				s.releaseOperation(op)
				s.askOverwrite(s.focused, paths, func() {
					s.startCopy(source, dest, paths, destDir, move, kind, apptransfer.ConflictReplace, refresh)
				})
				return
			}
			s.finishOperation(op, err, refresh...)
		})
	}()
	return true
}

// sameDirectory reports whether every source already lives in dir.
func sameDirectory(source, dest apptransfer.FileSystem, dir string, paths []string) bool {
	for _, path := range paths {
		if !apptransfer.SameLocation(source, source.Parent(path), dest, dir) {
			return false
		}
	}
	return true
}

// copyDestinationProblem reports why a copy or move cannot target dir.
// Copying a folder into itself would rediscover the new tree and recurse
// until the walk bound; the original blocks it too. Backing identity and
// canonical paths make this hold across two Tabs on one local machine or one
// Host, while different Hosts keep separate trees.
func copyDestinationProblem(source, dest apptransfer.FileSystem, paths []string, dir string, move bool) string {
	for _, path := range paths {
		if apptransfer.PathWithin(source, path, dest, dir) {
			return string(apptransfer.KindSelfCopy)
		}
	}
	if move && sameDirectory(source, dest, dir, paths) {
		return "same-directory"
	}
	return ""
}

// CopySelection remembers the focused pane's selection for a later paste.
func (s *State) CopySelection(side Side) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	paths := tab.SelectedPaths()
	if len(paths) == 0 {
		s.notice = "nothing-selected"
		s.RequestRedraw()
		return
	}
	s.clipboard = &clipboard{Side: side, Tab: tab, FS: tab.FS, Paths: paths, Cut: false}
	s.notice = "copied"
	s.RequestRedraw()
}

// CutSelection remembers the focused pane's selection, to be moved by the
// next paste.
func (s *State) CutSelection(side Side) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	paths := tab.SelectedPaths()
	if len(paths) == 0 {
		s.notice = "nothing-selected"
		s.RequestRedraw()
		return
	}
	s.clipboard = &clipboard{Side: side, Tab: tab, FS: tab.FS, Paths: paths, Cut: true}
	s.notice = "cut"
	s.RequestRedraw()
}

// Paste copies or moves the clipboard into the focused pane's destination.
func (s *State) Paste(side Side) {
	if s.clipboard == nil {
		s.notice = "clipboard-empty"
		s.RequestRedraw()
		return
	}
	dest := s.Tab(side)
	if dest == nil || dest.FS == nil {
		s.notice = "pane-unavailable"
		s.RequestRedraw()
		return
	}
	clip := s.clipboard
	destDir := dest.Destination()
	kind := domainsftp.OpCopy
	conflict := apptransfer.ConflictStop
	same := apptransfer.SameBacking(clip.FS, dest.FS)
	if problem := copyDestinationProblem(clip.FS, dest.FS, clip.Paths, destDir, clip.Cut); problem != "" {
		s.notice = problem
		s.RequestRedraw()
		return
	}
	if same && sameDirectory(clip.FS, dest.FS, destDir, clip.Paths) {
		// A copy back into the same folder becomes "(copy N)" rather than
		// overwriting the source. A cut already returned above.
		conflict = apptransfer.ConflictDuplicate
	}
	source := &Tab{FS: clip.FS, Kind: KindLocal}
	if dest.Kind == KindRemote {
		source.Kind = KindRemote
	}
	if same {
		source.Kind = dest.Kind
	}
	refresh := []*Tab{dest}
	if clip.Cut && clip.Tab != nil {
		// A move changes the source listing too.
		refresh = append(refresh, clip.Tab)
	}
	if s.startCopy(source, dest, clip.Paths, destDir, clip.Cut, kind, conflict, refresh) && clip.Cut {
		s.clipboard = nil
	}
}

// HasClipboard reports whether a paste is possible.
func (s *State) HasClipboard() bool { return s.clipboard != nil }

// DropFiles copies OS paths into a side's destination directory. This is the
// OS-to-pane drop; the source is always a fresh local filesystem.
func (s *State) DropFiles(side Side, targetDir string, paths []string) {
	s.dropFiles(side, targetDir, paths, apptransfer.ConflictStop, true)
}

// dropFiles runs an OS-to-pane copy with the requested conflict policy. When
// prompt is set, an existing destination opens the same replacement prompt as
// a toolbar transfer; confirming retries with ConflictReplace.
func (s *State) dropFiles(side Side, targetDir string, paths []string, conflict apptransfer.ConflictPolicy, prompt bool) {
	dest := s.Tab(side)
	if dest == nil || dest.FS == nil || len(paths) == 0 {
		s.notice = "pane-unavailable"
		s.RequestRedraw()
		return
	}
	if targetDir == "" {
		targetDir = dest.Destination()
	}
	kind := domainsftp.OpUpload
	if dest.Kind == KindLocal {
		kind = domainsftp.OpCopy
	}
	op, ctx, ok := s.beginOperation(kind)
	if !ok {
		return
	}
	go func() {
		local, err := s.service.OpenLocal(ctx)
		if err == nil {
			if problem := copyDestinationProblem(local, dest.FS, paths, targetDir, false); problem != "" {
				_ = local.Close()
				s.post(func() {
					s.releaseOperation(op)
					s.notice = problem
					s.RequestRedraw()
				})
				return
			}
			if s.transferQueue != nil {
				// The queue owns the local filesystem until the row settles;
				// a conflict prompt releases it before the retry re-opens one.
				retry := func() {
					s.dropFiles(side, targetDir, paths, apptransfer.ConflictReplace, false)
				}
				taskID, enqueueErr := s.transferQueue.Enqueue(apptransfer.TaskSpec{
					Kind:        kind,
					Source:      local,
					Dest:        dest.FS,
					Sources:     paths,
					DestDir:     targetDir,
					Conflict:    conflict,
					SourceLabel: "local",
					DestLabel:   dest.Title(),
				})
				s.post(func() {
					if s.operation != op {
						if enqueueErr == nil {
							_ = s.transferQueue.Cancel(taskID)
						}
						_ = local.Close()
						return
					}
					if enqueueErr != nil {
						_ = local.Close()
						s.releaseOperation(op)
						s.notice = "operation-failed"
						return
					}
					s.attachQueuedOperation(op, taskID, []*Tab{dest}, retry,
						func() { _ = local.Close() })
					// Snapshots posted before the attach found no taskID and
					// were dropped. Re-read the row so a copy that already
					// finished still settles the operation bar.
					if task, ok := s.transferQueue.Task(taskID); ok {
						s.applyQueueTask(task)
					}
				})
				return
			}
			_, err = s.service.Copy(ctx, apptransfer.CopyRequest{
				Source: local, Dest: dest.FS, Sources: paths, DestDir: targetDir,
				Kind: kind, Conflict: conflict,
				Progress: func(progress apptransfer.Progress) {
					op.setProgress(progress)
					s.RequestRedraw()
				},
			})
			_ = local.Close()
		}
		s.post(func() {
			if err != nil && prompt && apptransfer.KindOf(err) == apptransfer.KindExists {
				s.releaseOperation(op)
				s.askOverwrite(side, paths, func() {
					s.dropFiles(side, targetDir, paths, apptransfer.ConflictReplace, false)
				})
				return
			}
			s.finishOperation(op, err, dest)
		})
	}()
}

// releaseOperation abandons a failed attempt whose follow-up is a prompt, so
// the prompt is the only thing asking the user and the result bar stays clean.
func (s *State) releaseOperation(op *operationState) {
	op.mu.Lock()
	cancel := op.cancel
	cleanup := op.cleanup
	op.cleanup = nil
	op.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cleanup != nil {
		cleanup()
	}
	if s.operation == op {
		s.operation = nil
	}
}

// askOverwrite opens the replacement confirmation. retry starts the operation
// again with ConflictReplace when the user confirms.
func (s *State) askOverwrite(side Side, paths []string, retry func()) {
	s.dialog = &Dialog{
		Kind:  DialogOverwrite,
		Side:  side,
		Paths: append([]string(nil), paths...),
		retry: retry,
	}
	s.notice = "overwrite-required"
	s.RequestRedraw()
}

// DragPayload is what one pane drags onto another.
type DragPayload struct {
	Side  Side
	FS    apptransfer.FileSystem
	Paths []string
}

// BeginDrag builds the payload for the focused pane's selection.
func (s *State) BeginDrag(side Side) *DragPayload {
	tab := s.Tab(side)
	if tab == nil || tab.FS == nil {
		return nil
	}
	paths := tab.SelectedPaths()
	if len(paths) == 0 {
		return nil
	}
	return &DragPayload{Side: side, FS: tab.FS, Paths: paths}
}

// DragFor builds the payload for a drag gesture that starts on one row. A row
// that is part of the selection drags the whole selection; any other row
// drags only itself.
func (s *State) DragFor(side Side, entry domainsftp.Entry) *DragPayload {
	tab := s.Tab(side)
	if tab == nil || tab.FS == nil || entry.Name == ".." {
		return nil
	}
	paths := tab.SelectedPaths()
	if !tab.Selection[entry.Path] {
		paths = []string{entry.Path}
	}
	if len(paths) == 0 {
		paths = []string{entry.Path}
	}
	return &DragPayload{Side: side, FS: tab.FS, Paths: paths}
}

// DropPane completes a pane-to-pane drop. A drop onto the source pane moves;
// a cross-pane drop copies.
func (s *State) DropPane(side Side, targetDir string, payload *DragPayload) {
	if payload == nil || len(payload.Paths) == 0 {
		return
	}
	dest := s.Tab(side)
	if dest == nil || dest.FS == nil {
		s.notice = "pane-unavailable"
		s.RequestRedraw()
		return
	}
	if targetDir == "" {
		targetDir = dest.Destination()
	}
	move := payload.Side == side
	same := apptransfer.SameBacking(payload.FS, dest.FS)
	if problem := copyDestinationProblem(payload.FS, dest.FS, payload.Paths, targetDir, move); problem != "" {
		s.notice = problem
		s.RequestRedraw()
		return
	}
	kind := domainsftp.OpCopy
	if !same {
		if dest.Kind == KindRemote {
			kind = domainsftp.OpUpload
		} else {
			kind = domainsftp.OpDownload
		}
	}
	source := &Tab{FS: payload.FS, Kind: KindLocal}
	if dest.Kind == KindRemote {
		source.Kind = KindRemote
	}
	if same {
		source.Kind = dest.Kind
	}
	s.startCopy(source, dest, payload.Paths, targetDir, move, kind, apptransfer.ConflictStop, s.refreshAfter(source, dest, move))
}

// DeleteSelection removes the focused pane's selection. The view calls it
// only after the confirmation dialog.
func (s *State) DeleteSelection(side Side) {
	tab := s.Tab(side)
	if tab == nil || tab.FS == nil {
		return
	}
	paths := tab.SelectedPaths()
	if len(paths) == 0 {
		s.notice = "nothing-selected"
		s.RequestRedraw()
		return
	}
	op, ctx, ok := s.beginOperation(domainsftp.OpDelete)
	if !ok {
		return
	}
	files := tab.FS
	go func() {
		err := s.service.Delete(ctx, files, paths, true)
		s.post(func() { s.finishOperation(op, err, tab) })
	}()
}

// runOneShot runs a one-shot operation and refreshes its Tab when it ends.
func (s *State) runOneShot(kind domainsftp.OperationKind, tab *Tab, work func(ctx context.Context) error) {
	if tab == nil || tab.FS == nil {
		s.notice = "pane-unavailable"
		s.RequestRedraw()
		return
	}
	op, ctx, ok := s.beginOperation(kind)
	if !ok {
		return
	}
	go func() {
		err := work(ctx)
		s.post(func() { s.finishOperation(op, err, tab) })
	}()
}
