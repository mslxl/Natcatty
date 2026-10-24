package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// QueueRepository persists the visible Transfer history. A nil repository
// keeps the queue in memory.
type QueueRepository interface {
	LoadTasks(ctx context.Context) ([]domainsftp.Task, error)
	SaveTasks(ctx context.Context, tasks []domainsftp.Task) error
}

// TaskSpec is one enqueued Transfer. The filesystems and cancel resources
// stay in this process; TaskSpec itself is never persisted. A task restored
// after a restart therefore has no spec and must be re-opened before it can
// run again.
type TaskSpec struct {
	Kind   domainsftp.OperationKind
	Source FileSystem
	Dest   FileSystem
	// Sources are the entries to copy and DestDir is their common target.
	Sources []string
	DestDir string
	Move    bool
	// Conflict is the policy to apply. Empty means ConflictStop.
	Conflict ConflictPolicy
	// SourceLabel and DestLabel name the endpoints in the Transfer center.
	SourceLabel string
	DestLabel   string
	// DisplayName overrides the row label.
	DisplayName string
	// SourceKey and DestKey override the derived queue lanes. They exist for
	// callers that know a Session identity the filesystem's Backing omits.
	SourceKey domainsftp.ResourceKey
	DestKey   domainsftp.ResourceKey
}

// QueueStats is a point-in-time scheduler summary.
type QueueStats struct {
	// Active is the number of workers running now.
	Active int
	// PeakActive is the largest number of workers seen since the queue
	// started. It proves a configured limit held.
	PeakActive int
	// Queued is the number of rows waiting for a resource slot.
	Queued int
}

// Queue is the durable global Transfer queue. It schedules copy work per
// resource lane, tracks per-file progress and ETA, and exposes pause,
// resume, retry, and cancel controls that outlive any pane.
type Queue struct {
	service *Service
	repo    QueueRepository
	now     func() time.Time
	// concurrency is both the per-resource limit and the scheduler's
	// admission limit, matching the original scheduler.
	concurrency int
	retryDelay  time.Duration

	mu      sync.Mutex
	started bool
	closed  bool
	tasks   map[domainsftp.TaskID]*queueEntry
	order   []domainsftp.TaskID
	active  map[domainsftp.ResourceKey]int
	running int
	peak    int
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	observers []func(domainsftp.Task)

	persistMu sync.Mutex
}

type queueEntry struct {
	task    domainsftp.Task
	spec    *TaskSpec
	running bool
	cancel  context.CancelFunc
}

// QueueOption customizes a Queue.
type QueueOption func(*Queue)

// WithQueueRepository persists the visible Transfer history.
func WithQueueRepository(repository QueueRepository) QueueOption {
	return func(q *Queue) { q.repo = repository }
}

// WithQueueConcurrency sets the per-resource limit. Values outside 1-16
// fall back to the default, as the original setting does.
func WithQueueConcurrency(value int) QueueOption {
	return func(q *Queue) { q.concurrency = domainsftp.ResolveConcurrency(value) }
}

// WithQueueRetryDelay overrides the pause before the single automatic retry
// of a transient failure. Tests shorten it; production keeps 400 ms.
func WithQueueRetryDelay(delay time.Duration) QueueOption {
	return func(q *Queue) {
		if delay >= 0 {
			q.retryDelay = delay
		}
	}
}

// WithQueueClock overrides the clock used for task timestamps.
func WithQueueClock(now func() time.Time) QueueOption {
	return func(q *Queue) {
		if now != nil {
			q.now = now
		}
	}
}

// NewQueue builds a queue over the copy service.
func NewQueue(service *Service, options ...QueueOption) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		service:     service,
		now:         time.Now,
		concurrency: domainsftp.DefaultConcurrency,
		retryDelay:  400 * time.Millisecond,
		tasks:       map[domainsftp.TaskID]*queueEntry{},
		active:      map[domainsftp.ResourceKey]int{},
		ctx:         ctx,
		cancel:      cancel,
	}
	for _, option := range options {
		option(q)
	}
	return q
}

// Concurrency returns the per-resource file limit.
func (q *Queue) Concurrency() int { return q.concurrency }

// Observe registers a listener called for every lifecycle and progress
// snapshot. Listeners run on the worker goroutine; UI callers must post to
// their own thread.
func (q *Queue) Observe(listener func(domainsftp.Task)) {
	if q == nil || listener == nil {
		return
	}
	q.mu.Lock()
	q.observers = append(q.observers, listener)
	q.mu.Unlock()
}

// Start loads the persisted history, downgrades unfinished rows to
// interrupted, prunes terminal history, and begins scheduling. It is safe to
// call once; a second call does nothing.
func (q *Queue) Start(ctx context.Context) error {
	if q == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return nil
	}
	// Replace the placeholder context so Shutdown of the owning window also
	// stops the workers.
	q.cancel()
	q.ctx, q.cancel = context.WithCancel(ctx)
	q.mu.Unlock()

	var loaded []domainsftp.Task
	var loadErr error
	if q.repo != nil {
		loaded, loadErr = q.repo.LoadTasks(ctx)
	}

	q.mu.Lock()
	loaded = domainsftp.DowngradeInterrupted(loaded)
	loaded = domainsftp.PruneTasks(loaded, q.now())
	for _, task := range loaded {
		if task.ID == "" {
			continue
		}
		if _, exists := q.tasks[task.ID]; exists {
			continue
		}
		q.tasks[task.ID] = &queueEntry{task: task.Clone()}
		q.order = append(q.order, task.ID)
	}
	q.started = true
	q.mu.Unlock()

	if loadErr != nil {
		// Keep the damaged history on disk instead of replacing it with the
		// empty in-memory result of the failed load.
		return loadErr
	}
	q.persist()
	return nil
}

// Shutdown stops every worker and releases the queue. It is idempotent.
func (q *Queue) Shutdown() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	cancel := q.cancel
	q.mu.Unlock()
	cancel()
	q.wg.Wait()
}

// Enqueue adds a Transfer and schedules it immediately when a resource slot
// is free.
func (q *Queue) Enqueue(spec TaskSpec) (domainsftp.TaskID, error) {
	if q == nil {
		return "", errors.New("transfer queue: no queue")
	}
	if spec.Source == nil || spec.Dest == nil {
		return "", failure(KindInternal, kindOfSpec(spec), spec.DestDir,
			errors.New("sftp: missing filesystem"))
	}
	if len(spec.Sources) == 0 || spec.DestDir == "" {
		return "", failure(KindInternal, kindOfSpec(spec), spec.DestDir,
			errors.New("sftp: empty copy request"))
	}
	kind := kindOfSpec(spec)
	keys := queueResourceKeys(spec)
	now := q.now()
	task := domainsftp.Task{
		ID:           newTaskID(),
		Kind:         kind,
		Status:       domainsftp.TaskQueued,
		DisplayName:  spec.DisplayName,
		Sources:      append([]string(nil), spec.Sources...),
		DestDir:      spec.DestDir,
		Move:         spec.Move,
		Conflict:     string(spec.Conflict),
		ResourceKeys: keys,
		SourceLabel:  spec.SourceLabel,
		DestLabel:    spec.DestLabel,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	q.mu.Lock()
	copied := specCopy(spec)
	q.tasks[task.ID] = &queueEntry{task: task, spec: &copied}
	q.order = append(q.order, task.ID)
	q.mu.Unlock()

	q.emit(task)
	q.persist()
	q.pump()
	return task.ID, nil
}

// Tasks returns every visible row, oldest first.
func (q *Queue) Tasks() []domainsftp.Task {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	out := make([]domainsftp.Task, 0, len(q.order))
	for _, id := range q.order {
		if entry := q.tasks[id]; entry != nil {
			out = append(out, entry.task.Clone())
		}
	}
	q.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Task returns one row.
func (q *Queue) Task(id domainsftp.TaskID) (domainsftp.Task, bool) {
	if q == nil {
		return domainsftp.Task{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.tasks[id]
	if entry == nil {
		return domainsftp.Task{}, false
	}
	return entry.task.Clone(), true
}

// Stats returns the scheduler counters.
func (q *Queue) Stats() QueueStats {
	if q == nil {
		return QueueStats{}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	stats := QueueStats{Active: q.running, PeakActive: q.peak}
	for _, id := range q.order {
		entry := q.tasks[id]
		if entry != nil && (entry.task.Status == domainsftp.TaskQueued ||
			entry.task.Status == domainsftp.TaskPending) {
			stats.Queued++
		}
	}
	return stats
}

// Active returns how many workers occupy one resource lane.
func (q *Queue) Active(key domainsftp.ResourceKey) int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.active[key]
}

// Pause stops a queued Transfer or asks a running one to stop at the next
// chunk boundary.
func (q *Queue) Pause(id domainsftp.TaskID) error {
	var cancel context.CancelFunc
	var snapshot domainsftp.Task
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil {
		q.mu.Unlock()
		return domainsftp.ErrTaskNotFound
	}
	if entry.task.CancelRequested {
		q.mu.Unlock()
		return nil
	}
	switch {
	case entry.running:
		entry.task.PauseRequested = true
		entry.task.Status = domainsftp.TaskPausing
		entry.task.Speed = 0
		entry.task.Epoch++
		entry.task.UpdatedAt = q.now()
		cancel = entry.cancel
		snapshot = entry.task.Clone()
	case entry.task.Status.CanPause():
		entry.task.PauseRequested = true
		entry.task.Status = domainsftp.TaskPaused
		entry.task.Speed = 0
		entry.task.Epoch++
		entry.task.UpdatedAt = q.now()
		snapshot = entry.task.Clone()
	default:
		q.mu.Unlock()
		return nil
	}
	q.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	q.emit(snapshot)
	q.persist()
	return nil
}

// Resume re-queues a paused or interrupted Transfer. A Transfer restored
// from a previous process has no live filesystem handles and reports
// ErrTaskUnavailable until it is reconnected.
func (q *Queue) Resume(id domainsftp.TaskID) error {
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil {
		q.mu.Unlock()
		return domainsftp.ErrTaskNotFound
	}
	status := entry.task.Status
	if status == domainsftp.TaskQueued || status == domainsftp.TaskPending {
		q.mu.Unlock()
		return nil
	}
	if !status.CanResume() {
		q.mu.Unlock()
		return domainsftp.ErrTaskNotRetryable
	}
	if entry.spec == nil {
		q.mu.Unlock()
		return domainsftp.ErrTaskUnavailable
	}
	entry.task.Status = domainsftp.TaskQueued
	entry.task.PauseRequested = false
	entry.task.CancelRequested = false
	entry.task.ReconnectRequired = false
	entry.task.Epoch++
	entry.task.Speed = 0
	entry.task.BytesDone = 0
	entry.task.FilesDone = 0
	entry.task.CurrentFile = ""
	entry.task.Error = ""
	entry.task.Err = nil
	entry.task.FinishedAt = time.Time{}
	entry.task.UpdatedAt = q.now()
	snapshot := entry.task.Clone()
	q.mu.Unlock()

	q.emit(snapshot)
	q.persist()
	q.pump()
	return nil
}

// Retry replays a failed, cancelled, interrupted, or attention Transfer from
// the beginning. It requires the live filesystems, so a row loaded after a
// restart first needs its panes reconnected.
func (q *Queue) Retry(id domainsftp.TaskID) error {
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil {
		q.mu.Unlock()
		return domainsftp.ErrTaskNotFound
	}
	if !entry.task.Status.CanRetry() || entry.running {
		q.mu.Unlock()
		return domainsftp.ErrTaskNotRetryable
	}
	if entry.spec == nil {
		q.mu.Unlock()
		return domainsftp.ErrTaskUnavailable
	}
	entry.task.Status = domainsftp.TaskQueued
	entry.task.PauseRequested = false
	entry.task.CancelRequested = false
	entry.task.ReconnectRequired = false
	entry.task.Epoch++
	entry.task.Speed = 0
	entry.task.BytesDone = 0
	entry.task.FilesDone = 0
	entry.task.CurrentFile = ""
	entry.task.Attempts = 0
	entry.task.Error = ""
	entry.task.Err = nil
	entry.task.StartedAt = time.Time{}
	entry.task.FinishedAt = time.Time{}
	entry.task.UpdatedAt = q.now()
	snapshot := entry.task.Clone()
	q.mu.Unlock()

	q.emit(snapshot)
	q.persist()
	q.pump()
	return nil
}

// Cancel stops a Transfer. A queued or paused row settles immediately; a
// running one stops at its next chunk boundary.
func (q *Queue) Cancel(id domainsftp.TaskID) error {
	var cancel context.CancelFunc
	var snapshot domainsftp.Task
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil {
		q.mu.Unlock()
		return domainsftp.ErrTaskNotFound
	}
	if entry.task.Terminal() {
		q.mu.Unlock()
		return nil
	}
	entry.task.CancelRequested = true
	entry.task.PauseRequested = false
	entry.task.Epoch++
	entry.task.UpdatedAt = q.now()
	if entry.running {
		cancel = entry.cancel
	} else {
		entry.task.Status = domainsftp.TaskCancelled
		entry.task.Speed = 0
		entry.task.Err = nil
		entry.task.Error = ""
		entry.task.FinishedAt = entry.task.UpdatedAt
	}
	snapshot = entry.task.Clone()
	q.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	q.emit(snapshot)
	q.persist()
	return nil
}

// pump starts every runnable queued Transfer whose resource lanes have a
// free slot.
func (q *Queue) pump() {
	if q == nil {
		return
	}
	var started []domainsftp.Task
	q.mu.Lock()
	if !q.started || q.closed {
		q.mu.Unlock()
		return
	}
	for {
		id := q.nextRunnableLocked()
		if id == "" {
			break
		}
		entry := q.tasks[id]
		entry.running = true
		entry.task.Status = domainsftp.TaskTransferring
		entry.task.Error = ""
		entry.task.Err = nil
		now := q.now()
		if entry.task.StartedAt.IsZero() {
			entry.task.StartedAt = now
		}
		entry.task.UpdatedAt = now
		for _, key := range entry.task.ResourceKeys {
			q.active[key]++
		}
		q.running++
		if q.running > q.peak {
			q.peak = q.running
		}
		ctx, cancel := context.WithCancel(q.ctx)
		entry.cancel = cancel
		started = append(started, entry.task.Clone())
		q.wg.Add(1)
		go q.run(ctx, id)
	}
	q.mu.Unlock()
	for _, task := range started {
		q.emit(task)
	}
	if len(started) > 0 {
		q.persist()
	}
}

// nextRunnableLocked returns the oldest queued row whose lanes all have a
// free slot. Lane admission uses the same 1-16 limit as the file setting,
// so one Host never exceeds the configured concurrency.
func (q *Queue) nextRunnableLocked() domainsftp.TaskID {
	for _, id := range q.order {
		entry := q.tasks[id]
		if entry == nil || entry.running || entry.spec == nil {
			continue
		}
		if entry.task.Status != domainsftp.TaskQueued || entry.task.PauseRequested ||
			entry.task.CancelRequested {
			continue
		}
		admitted := true
		for _, key := range entry.task.ResourceKeys {
			if q.active[key] >= q.concurrency {
				admitted = false
				break
			}
		}
		if admitted {
			return id
		}
	}
	return ""
}

// run executes one Transfer, retrying once after a transient network
// failure, then settles the row.
func (q *Queue) run(ctx context.Context, id domainsftp.TaskID) {
	defer q.wg.Done()
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil || entry.spec == nil {
		q.mu.Unlock()
		q.settle(id, failure(KindInternal, domainsftp.OpCopy, "", domainsftp.ErrTaskUnavailable))
		return
	}
	spec := specCopy(*entry.spec)
	q.mu.Unlock()

	progress := func(update Progress) {
		q.recordProgress(id, update)
	}
	var err error
	for attempt := 0; attempt <= 1; attempt++ {
		_, err = q.service.Copy(ctx, CopyRequest{
			Source:   spec.Source,
			Dest:     spec.Dest,
			Sources:  spec.Sources,
			DestDir:  spec.DestDir,
			Move:     spec.Move,
			Kind:     spec.Kind,
			Conflict: spec.Conflict,
			Progress: progress,
		})
		if err == nil || KindOf(err) == KindCancelled || ctx.Err() != nil {
			break
		}
		if attempt == 1 || !isTransientQueueError(err) {
			break
		}
		q.resetForRetry(id)
		timer := time.NewTimer(q.retryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			err = &Error{Kind: KindCancelled, Op: spec.Kind, Path: spec.DestDir, Cause: ctx.Err()}
		}
	}
	q.settle(id, err)
}

// recordProgress updates the row and its average speed. It ignores late
// events after a pause or cancel latch, so a stopping stream cannot advance
// the visible bytes.
func (q *Queue) recordProgress(id domainsftp.TaskID, update Progress) {
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil || entry.task.PauseRequested || entry.task.CancelRequested {
		q.mu.Unlock()
		return
	}
	now := q.now()
	entry.task.BytesDone = update.BytesDone
	entry.task.BytesTotal = update.BytesTotal
	entry.task.FilesDone = update.FilesDone
	entry.task.FilesTotal = update.FilesTotal
	entry.task.CurrentFile = update.Current
	if !entry.task.StartedAt.IsZero() && update.BytesDone > 0 {
		elapsed := now.Sub(entry.task.StartedAt).Seconds()
		if elapsed >= 0.001 {
			entry.task.Speed = int64(float64(update.BytesDone) / elapsed)
		}
	}
	entry.task.UpdatedAt = now
	snapshot := entry.task.Clone()
	q.mu.Unlock()
	q.emit(snapshot)
}

// resetForRetry clears the per-attempt counters before the automatic retry.
func (q *Queue) resetForRetry(id domainsftp.TaskID) {
	q.mu.Lock()
	entry := q.tasks[id]
	if entry != nil {
		entry.task.Attempts++
		entry.task.BytesDone = 0
		entry.task.FilesDone = 0
		entry.task.CurrentFile = ""
		entry.task.Speed = 0
		entry.task.UpdatedAt = q.now()
	}
	q.mu.Unlock()
}

// settle publishes the worker's outcome and releases its resource slots.
func (q *Queue) settle(id domainsftp.TaskID, err error) {
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil {
		q.mu.Unlock()
		return
	}
	entry.running = false
	entry.cancel = nil
	for _, key := range entry.task.ResourceKeys {
		if q.active[key] <= 1 {
			delete(q.active, key)
		} else {
			q.active[key]--
		}
	}
	q.running--
	now := q.now()
	cancelled := entry.task.CancelRequested ||
		(!entry.task.PauseRequested && err != nil && KindOf(err) == KindCancelled)
	switch {
	case cancelled:
		entry.task.Status = domainsftp.TaskCancelled
		entry.task.Error = ""
		entry.task.Err = nil
		entry.task.PauseRequested = false
	case entry.task.PauseRequested:
		entry.task.Status = domainsftp.TaskPaused
		entry.task.Error = ""
		entry.task.Err = nil
	case err != nil:
		entry.task.Status = domainsftp.TaskFailed
		entry.task.Error = string(KindOf(err))
		entry.task.Err = err
	default:
		entry.task.Status = domainsftp.TaskCompleted
		if entry.task.BytesTotal > 0 {
			entry.task.BytesDone = entry.task.BytesTotal
		}
		if entry.task.FilesTotal > 0 {
			entry.task.FilesDone = entry.task.FilesTotal
		}
		entry.task.Error = ""
		entry.task.Err = nil
	}
	if entry.task.Terminal() {
		entry.task.FinishedAt = now
	}
	entry.task.Speed = 0
	entry.task.UpdatedAt = now
	snapshot := entry.task.Clone()
	q.mu.Unlock()

	q.emit(snapshot)
	q.persist()
	q.pump()
}

// emit publishes one snapshot to every observer, outside the queue lock.
func (q *Queue) emit(task domainsftp.Task) {
	q.mu.Lock()
	observers := make([]func(domainsftp.Task), len(q.observers))
	copy(observers, q.observers)
	q.mu.Unlock()
	for _, observer := range observers {
		observer(task.Clone())
	}
}

// persist writes the visible rows. It takes its snapshot under the persist
// lock so an older concurrent save cannot overwrite a newer one.
func (q *Queue) persist() {
	if q == nil || q.repo == nil {
		return
	}
	q.persistMu.Lock()
	defer q.persistMu.Unlock()
	tasks := q.Tasks()
	_ = q.repo.SaveTasks(context.Background(), tasks)
}

// queueResourceKeys derives the lanes a Transfer occupies. A Host's key
// wins over a Session key, matching the original grouping; two local
// endpoints share "local".
func queueResourceKeys(spec TaskSpec) []domainsftp.ResourceKey {
	source := spec.SourceKey
	if source == "" {
		source = resourceKeyFor(spec.Source)
	}
	dest := spec.DestKey
	if dest == "" {
		dest = resourceKeyFor(spec.Dest)
	}
	keys := make([]domainsftp.ResourceKey, 0, 2)
	seen := map[domainsftp.ResourceKey]bool{}
	for _, key := range []domainsftp.ResourceKey{source, dest} {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		keys = append(keys, domainsftp.LocalResourceKey())
	}
	return keys
}

// resourceKeyFor maps a filesystem's Backing onto a queue lane. The remote
// adapter names its Host "remote:<id>"; an unknown backing is its own lane
// so unrelated transports cannot starve one another.
func resourceKeyFor(files FileSystem) domainsftp.ResourceKey {
	if files == nil {
		return ""
	}
	backing := files.Backing()
	switch {
	case backing == "", backing == "local":
		return domainsftp.LocalResourceKey()
	case strings.HasPrefix(backing, "remote:"):
		return domainsftp.HostResourceKey(strings.TrimPrefix(backing, "remote:"))
	case strings.HasPrefix(backing, "host:"), strings.HasPrefix(backing, "session:"):
		return domainsftp.ResourceKey(backing)
	default:
		return domainsftp.ResourceKey(backing)
	}
}

// kindOfSpec resolves an empty kind the same way the copy engine does.
func kindOfSpec(spec TaskSpec) domainsftp.OperationKind {
	if spec.Kind.Valid() {
		return spec.Kind
	}
	if spec.Move {
		return domainsftp.OpMove
	}
	return domainsftp.OpCopy
}

// specCopy deep-copies the slices a worker reads.
func specCopy(spec TaskSpec) TaskSpec {
	out := spec
	out.Sources = append([]string(nil), spec.Sources...)
	return out
}

// newTaskID returns an opaque process-unique identifier.
func newTaskID() domainsftp.TaskID {
	var data [8]byte
	if _, err := rand.Read(data[:]); err == nil {
		return domainsftp.TaskID(hex.EncodeToString(data[:]))
	}
	return domainsftp.TaskID(fmt.Sprintf("task-%d", time.Now().UnixNano()))
}

// isTransientQueueError mirrors the original's network-blip classification:
// a session failure or a recognizable reset/timeout message gets the single
// automatic retry; permission, missing-path, and conflict failures do not.
func isTransientQueueError(err error) bool {
	if err == nil || KindOf(err) == KindCancelled {
		return false
	}
	if KindOf(err) == KindConnection {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, token := range []string{
		"econnreset", "econnrefused", "etimedout", "epipe", "broken pipe",
		"socket hang up", "connection reset", "network", "temporarily unavailable",
		"try again",
	} {
		if strings.Contains(message, token) {
			return true
		}
	}
	return false
}
