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
	// BatchID groups the rows one user action created. Apply-to-all answers
	// are scoped to a batch and a conflict type; an empty value is one row.
	BatchID string
	// SkipUnchanged enables the size + whole-second mtime check while a merged
	// directory is traversed. It defaults to true; set a pointer to false to
	// turn it off.
	SkipUnchanged *bool
	// SourceLabel and DestLabel name the endpoints in the Transfer center.
	SourceLabel string
	DestLabel   string
	// DisplayName overrides the row label.
	DisplayName string
	// SourceKey and DestKey override the derived queue lanes. They exist for
	// callers that know a Session identity the filesystem's Backing omits.
	SourceKey domainsftp.ResourceKey
	DestKey   domainsftp.ResourceKey
	// SourceHost and DestHost name the saved Hosts behind the endpoints; an
	// empty value is the local device. Enqueue derives them from the
	// filesystem's Backing when a caller leaves them empty, and the queue
	// persists them so a reconnect can rebuild the request.
	SourceHost string
	DestHost   string
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

	// names reserves a destination path chosen at plan time (a duplicate
	// name). claims on each entry reserve top-level paths and directory
	// subtrees before a worker starts. They prevent two Transfers from staging
	// and renaming into one path.
	names            map[string]domainsftp.TaskID
	conflictDefaults map[string]map[string]ConflictPolicy

	persistMu          sync.Mutex
	lastCheckpointSave time.Time
}

type queueEntry struct {
	task    domainsftp.Task
	spec    *TaskSpec
	running bool
	cancel  context.CancelFunc
	// claims are the top-level destination paths this row owns. A directory
	// claim covers its whole subtree.
	claims []pathClaim
	// conflict is the live answer channel while the row waits as attention.
	conflict chan ConflictResolution
}

// Errors returned by ResolveConflict.
var (
	// ErrConflictNotPending reports an answer for a row that is not waiting on
	// a conflict, for example after a cancel already settled it.
	ErrConflictNotPending = errors.New("transfer queue: no conflict is waiting")
	// ErrConflictActionInvalid reports an action this conflict cannot take,
	// such as merge into a file.
	ErrConflictActionInvalid = errors.New("transfer queue: conflict action does not apply")
)

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
		service:          service,
		now:              time.Now,
		concurrency:      domainsftp.DefaultConcurrency,
		retryDelay:       400 * time.Millisecond,
		tasks:            map[domainsftp.TaskID]*queueEntry{},
		active:           map[domainsftp.ResourceKey]int{},
		names:            map[string]domainsftp.TaskID{},
		conflictDefaults: map[string]map[string]ConflictPolicy{},
		ctx:              ctx,
		cancel:           cancel,
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
	skipUnchanged := spec.SkipUnchanged == nil || *spec.SkipUnchanged
	claims := queueClaims(spec)
	task := domainsftp.Task{
		ID:            newTaskID(),
		Kind:          kind,
		Status:        domainsftp.TaskQueued,
		DisplayName:   spec.DisplayName,
		Sources:       append([]string(nil), spec.Sources...),
		DestDir:       spec.DestDir,
		Move:          spec.Move,
		Conflict:      string(spec.Conflict),
		BatchID:       spec.BatchID,
		SkipUnchanged: skipUnchanged,
		ResourceKeys:  keys,
		SourceLabel:   spec.SourceLabel,
		DestLabel:     spec.DestLabel,
		SourceHost:    queueHostID(spec.SourceHost, spec.SourceKey, spec.Source),
		DestHost:      queueHostID(spec.DestHost, spec.DestKey, spec.Dest),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	q.mu.Lock()
	if err := q.reserveClaimsLocked(task.ID, spec.Dest, kind, claims); err != nil {
		q.mu.Unlock()
		return "", err
	}
	copied := specCopy(spec)
	q.tasks[task.ID] = &queueEntry{task: task, spec: &copied, claims: claims}
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
	// A row restored by Reattach may have lost its claims with the old
	// process, or another Transfer may have started in the meantime. Reclaim
	// the paths before admitting the row again. Chosen duplicate names are
	// pinned in Targets, so they are part of the set.
	if len(entry.claims) == 0 {
		entry.claims = resumeClaims(entry)
	}
	if err := q.reserveClaimsLocked(id, entry.spec.Dest, kindOfSpec(*entry.spec), entry.claims); err != nil {
		q.mu.Unlock()
		return err
	}
	entry.task.Status = domainsftp.TaskQueued
	entry.task.PauseRequested = false
	entry.task.CancelRequested = false
	entry.task.ReconnectRequired = false
	entry.task.Epoch++
	entry.task.Speed = 0
	// Speed measures the resumed attempt, not time spent paused.
	entry.task.StartedAt = time.Time{}
	// A directory resume starts from its checkpoint prefix, so progress and
	// the honest byte count continue rather than dropping to zero. A single
	// file has no directory checkpoint and replays from the beginning.
	entry.task.BytesDone = 0
	entry.task.FilesDone = 0
	if checkpoint := entry.task.DirectoryResumeCheckpoint; checkpoint != nil && checkpoint.Valid() {
		entry.task.FilesDone = checkpoint.CompletedEntries
		entry.task.BytesDone = entry.task.CheckpointBytesDone
	}
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

// Reattach binds live filesystems to a Transfer restored after a restart or
// cut off by a lost transport. It keeps the directory checkpoint and pinned
// targets, clears the reconnect requirement, and moves a failed row back to
// interrupted so Resume can continue it. Retry keeps its replay-from-start
// meaning.
func (q *Queue) Reattach(id domainsftp.TaskID, spec TaskSpec) error {
	if q == nil {
		return domainsftp.ErrTaskNotFound
	}
	if spec.Source == nil || spec.Dest == nil || len(spec.Sources) == 0 || spec.DestDir == "" {
		return domainsftp.ErrTaskUnavailable
	}
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil {
		q.mu.Unlock()
		return domainsftp.ErrTaskNotFound
	}
	if entry.running || entry.task.Status == domainsftp.TaskCompleted ||
		entry.task.Status == domainsftp.TaskCancelled {
		q.mu.Unlock()
		return domainsftp.ErrTaskReattachNotNeeded
	}
	if entry.spec != nil && !entry.task.ReconnectRequired {
		q.mu.Unlock()
		return domainsftp.ErrTaskReattachNotNeeded
	}
	copied := specCopy(spec)
	entry.spec = &copied
	entry.claims = queueClaims(copied)
	entry.task.SkipUnchanged = copied.SkipUnchanged == nil || *copied.SkipUnchanged
	if copied.BatchID != "" {
		entry.task.BatchID = copied.BatchID
	}
	if copied.Conflict != "" {
		entry.task.Conflict = string(copied.Conflict)
	}
	// A row persisted before the endpoints carried their Host keeps the
	// identity the reconnecting caller supplied, so the next restart can
	// resolve it without guessing the direction.
	if entry.task.SourceHost == "" {
		entry.task.SourceHost = copied.SourceHost
	}
	if entry.task.DestHost == "" {
		entry.task.DestHost = copied.DestHost
	}
	entry.task.ReconnectRequired = false
	entry.task.Error = ""
	entry.task.Err = nil
	entry.task.Speed = 0
	if entry.task.Status == domainsftp.TaskFailed || entry.task.Status == domainsftp.TaskInterrupted {
		entry.task.Status = domainsftp.TaskInterrupted
	}
	entry.task.Epoch++
	entry.task.UpdatedAt = q.now()
	snapshot := entry.task.Clone()
	q.mu.Unlock()

	q.emit(snapshot)
	q.persist()
	q.pump()
	return nil
}

// HostReattach is the live side of one Host reconnect. Host names the saved
// Host whose SFTP connection was just established, Remote is that Host's
// fresh filesystem, and Peers maps any other connected Hosts to their live
// filesystems. OpenLocal opens a local filesystem for a row whose other
// endpoint is the local device.
type HostReattach struct {
	Host   string
	Remote FileSystem
	Peers  map[string]FileSystem
	// OpenLocal opens the local peer once, shared by every rebound row. A nil
	// opener leaves rows with a local peer reconnect-required.
	OpenLocal func(context.Context) (FileSystem, error)
}

// ReattachHost binds live filesystems to the interrupted rows that used the
// Host just reconnected, keeping each row's durable request and directory
// checkpoint so Resume continues rather than replays. It binds every matching
// row. A row whose other endpoint has no live filesystem is left alone, so
// the reconnect prompt stays truthful. It returns how many rows it rebound
// and the first local-open failure that stopped a row.
func (q *Queue) ReattachHost(ctx context.Context, attach HostReattach) (int, error) {
	if q == nil || attach.Host == "" || attach.Remote == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var (
		local      FileSystem
		localErr   error
		localTried bool
		firstErr   error
	)
	peer := func(host string) (FileSystem, bool) {
		if host != "" {
			// A copy inside one Host has that Host on both endpoints.
			if host == attach.Host {
				return attach.Remote, true
			}
			files := attach.Peers[host]
			return files, files != nil
		}
		if !localTried {
			localTried = true
			if attach.OpenLocal == nil {
				localErr = domainsftp.ErrTaskUnavailable
			} else {
				local, localErr = attach.OpenLocal(ctx)
			}
		}
		if localErr != nil {
			if firstErr == nil {
				firstErr = localErr
			}
			return nil, false
		}
		if local == nil {
			return nil, false
		}
		return local, true
	}

	bound := 0
	for _, task := range q.Tasks() {
		// Only rows a reconnect can continue: a restart downgrades unfinished
		// work to interrupted, and a lost transport settles it as failed with
		// the reconnect flag set. An ordinary failure has nothing to continue,
		// so it must not be revived by an unrelated reconnect.
		if task.Status != domainsftp.TaskInterrupted && !task.ReconnectRequired {
			continue
		}
		sourceHost, destHost, usesHost := taskHostEndpoints(task, attach.Host)
		if !usesHost {
			continue
		}
		var source, dest FileSystem
		var ok bool
		if sourceHost == attach.Host {
			source = attach.Remote
			dest, ok = peer(destHost)
		} else {
			dest = attach.Remote
			source, ok = peer(sourceHost)
		}
		if !ok {
			continue
		}
		spec := TaskSpec{
			Kind:        task.Kind,
			Source:      source,
			Dest:        dest,
			Sources:     append([]string(nil), task.Sources...),
			DestDir:     task.DestDir,
			Move:        task.Move,
			Conflict:    ConflictPolicy(task.Conflict),
			BatchID:     task.BatchID,
			SourceLabel: task.SourceLabel,
			DestLabel:   task.DestLabel,
			SourceHost:  sourceHost,
			DestHost:    destHost,
		}
		skipUnchanged := task.SkipUnchanged
		spec.SkipUnchanged = &skipUnchanged
		if err := q.Reattach(task.ID, spec); err == nil {
			bound++
		}
	}
	return bound, firstErr
}

// taskHostEndpoints resolves which endpoint of a durable row is the Host that
// reconnected. Rows persisted before the endpoints recorded their Host fall
// back to the request's queue lanes.
func taskHostEndpoints(task domainsftp.Task, host string) (sourceHost, destHost string, ok bool) {
	sourceHost, destHost = task.SourceHost, task.DestHost
	if sourceHost == "" && destHost == "" {
		if sourceHost, destHost, ok = legacyHostEndpoints(task); !ok {
			return "", "", false
		}
	}
	if sourceHost != host && destHost != host {
		return "", "", false
	}
	return sourceHost, destHost, true
}

// legacyHostEndpoints infers the endpoints of a row persisted before
// SourceHost/DestHost existed. ResourceKeys are stored in request order
// (source, destination) and skip a duplicate lane, so two lanes map directly.
// One lane means both endpoints share that Backing, and the Transfer's
// direction says which side the Host is on: an upload moves a local source to
// a Host, a download the reverse. A session lane names no saved Host, so it is
// never guessed to be local.
func legacyHostEndpoints(task domainsftp.Task) (sourceHost, destHost string, ok bool) {
	switch len(task.ResourceKeys) {
	case 0:
		return "", "", false
	case 1:
		lane, known := legacyLaneHost(task.ResourceKeys[0])
		if !known || lane == "" {
			return "", "", false
		}
		switch task.Kind {
		case domainsftp.OpUpload:
			return "", lane, true
		case domainsftp.OpDownload:
			return lane, "", true
		case domainsftp.OpCopy, domainsftp.OpMove:
			return lane, lane, true
		default:
			return "", "", false
		}
	default:
		sourceHost, sourceOK := legacyLaneHost(task.ResourceKeys[0])
		destHost, destOK := legacyLaneHost(task.ResourceKeys[1])
		if !sourceOK || !destOK {
			return "", "", false
		}
		return sourceHost, destHost, true
	}
}

// legacyLaneHost maps one queue lane onto its saved Host, or "" for the local
// device. ok is false for a lane that names neither, such as a Session.
func legacyLaneHost(key domainsftp.ResourceKey) (host string, ok bool) {
	if key == domainsftp.LocalResourceKey() {
		return "", true
	}
	if strings.HasPrefix(string(key), "host:") {
		return strings.TrimPrefix(string(key), "host:"), true
	}
	return "", false
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
	// A row whose transport was lost, or one restored from a previous
	// process, has no live filesystems to replay against. Retrying it would
	// send the request down the same dead stream and fail at 0% with no
	// explanation, so it waits for a reconnect instead.
	if entry.task.ReconnectRequired || entry.spec == nil {
		q.mu.Unlock()
		return domainsftp.ErrTaskUnavailable
	}
	// A failed row freed its paths when it settled, so claim them again before
	// admitting the replay; another Transfer may have taken them meanwhile.
	// Retry replays from scratch, so it does not keep the previous attempt's
	// pinned duplicate targets.
	entry.claims = queueClaims(*entry.spec)
	if err := q.reserveClaimsLocked(id, entry.spec.Dest, kindOfSpec(*entry.spec), entry.claims); err != nil {
		q.mu.Unlock()
		return err
	}
	entry.task.Status = domainsftp.TaskQueued
	entry.task.PauseRequested = false
	entry.task.CancelRequested = false
	entry.task.ReconnectRequired = false
	entry.task.Epoch++
	entry.task.Speed = 0
	// Retry is a full replay: the original's Retry does not inherit a paused
	// directory's checkpoint.
	entry.task.DirectoryResumeCheckpoint = nil
	entry.task.CheckpointBytesDone = 0
	entry.task.Targets = nil
	entry.task.ReplaceTargets = nil
	entry.task.SkipTargets = nil
	entry.task.PendingConflict = nil
	entry.task.Skipped = 0
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
		// Cancel is a hard stop. Clearing the resume state here matches a
		// running row that settles as cancelled, so Retry always replays from
		// the beginning.
		entry.task.DirectoryResumeCheckpoint = nil
		entry.task.CheckpointBytesDone = 0
		entry.task.Targets = nil
		entry.task.ReplaceTargets = nil
		entry.task.SkipTargets = nil
		entry.task.PendingConflict = nil
		entry.task.Skipped = 0
		q.releaseReservationsLocked(id)
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

	var err error
	for attempt := 0; attempt <= 1; attempt++ {
		// Re-read the durable resume state per attempt: an automatic retry
		// after a transient failure continues from the files the previous
		// attempt completed instead of replaying them.
		checkpoint, targets, baseBytes := q.resumeState(id)
		replaceTargets, skipTargets := q.attemptPolicies(id)
		skipUnchanged := q.skipUnchanged(id)
		progress := func(update Progress) {
			q.recordProgress(id, update, baseBytes)
		}
		result, copyErr := q.service.Copy(ctx, CopyRequest{
			// The durable Task ID seeds every staging file name, so a resumed
			// attempt reuses the staging file an interrupted process left.
			StageKey:       string(id),
			Source:         spec.Source,
			Dest:           spec.Dest,
			Sources:        spec.Sources,
			DestDir:        spec.DestDir,
			Move:           spec.Move,
			Kind:           spec.Kind,
			Conflict:       spec.Conflict,
			Progress:       progress,
			Checkpoint:     checkpoint,
			KnownTargets:   targets,
			ReplaceTargets: replaceTargets,
			SkipTargets:    skipTargets,
			SkipUnchanged:  skipUnchanged,
			OnConflict: func(conflict domainsftp.Conflict) ConflictResolution {
				return q.waitConflict(ctx, id, conflict)
			},
			ReserveName: func(path string, directory bool) bool {
				return q.reserveName(id, path, directory)
			},
			OnTarget: func(source, target string) {
				q.recordTarget(id, source, target)
			},
			KeepReplacement: func(copyErr error) bool {
				return q.keepReplacement(id, copyErr)
			},
		})
		err = copyErr
		if len(result.Skipped) > 0 {
			q.recordSkipped(id, len(result.Skipped))
		}
		if result.ResumeReset {
			// The copy put the original destination back, so the checkpoint
			// no longer describes it; a retry or resume must start over.
			q.clearResumeState(id)
		}
		if err != nil && ctx.Err() != nil && KindOf(err) != KindCancelled {
			// A shutdown or cancel while the row waited for a conflict answer
			// surfaces as the stop action's exists error; the context is the
			// real cause and must settle the row as cancelled, not failed.
			err = &Error{Kind: KindCancelled, Op: spec.Kind, Path: spec.DestDir, Cause: ctx.Err()}
		}
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

// resumeState snapshots the checkpoint and pinned targets for one attempt.
func (q *Queue) resumeState(id domainsftp.TaskID) (*domainsftp.DirectoryCheckpoint,
	map[string]string, int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.tasks[id]
	if entry == nil {
		return nil, nil, 0
	}
	var checkpoint *domainsftp.DirectoryCheckpoint
	if entry.task.DirectoryResumeCheckpoint != nil {
		copied := entry.task.DirectoryResumeCheckpoint.Clone()
		checkpoint = &copied
	}
	var targets map[string]string
	if entry.task.Targets != nil {
		targets = make(map[string]string, len(entry.task.Targets))
		for source, target := range entry.task.Targets {
			targets[source] = target
		}
	}
	return checkpoint, targets, entry.task.CheckpointBytesDone
}

// attemptPolicies snapshots the per-source conflict answers and the
// skip-unchanged setting for one copy attempt.
func (q *Queue) attemptPolicies(id domainsftp.TaskID) (map[string]bool, map[string]bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.tasks[id]
	if entry == nil {
		return nil, nil
	}
	replace := copyBoolMap(entry.task.ReplaceTargets)
	skip := copyBoolMap(entry.task.SkipTargets)
	return replace, skip
}

// skipUnchanged reports whether the row's merged directories use the
// size + whole-second mtime check. A row saved before the field existed
// defaults to enabled, matching the original setting.
func (q *Queue) skipUnchanged(id domainsftp.TaskID) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.tasks[id]
	if entry == nil {
		return true
	}
	if entry.spec != nil && entry.spec.SkipUnchanged == nil && !entry.task.SkipUnchanged {
		// The row was created before the setting was recorded; the queue's
		// default is enabled.
		return true
	}
	return entry.task.SkipUnchanged
}

func copyBoolMap(source map[string]bool) map[string]bool {
	if source == nil {
		return nil
	}
	out := make(map[string]bool, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// recordSkipped adds the entries this attempt left untouched to the row's
// visible skipped count.
func (q *Queue) recordSkipped(id domainsftp.TaskID, count int) {
	if count <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.tasks[id]
	if entry == nil || entry.task.CancelRequested {
		return
	}
	entry.task.Skipped += count
}

// waitConflict parks the running row as attention and waits for one answer.
// The engine calls it from the worker goroutine; ResolveConflict answers it.
// A context cancellation (cancel, pause, shutdown, lost transport) returns
// stop so the engine unwinds and settle records the real cause.
func (q *Queue) waitConflict(ctx context.Context, id domainsftp.TaskID,
	conflict domainsftp.Conflict) ConflictResolution {
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil {
		q.mu.Unlock()
		return ConflictResolution{Action: ConflictStop}
	}
	if action, ok := q.conflictDefaults[taskBatchKey(entry.task)][conflict.TypeKey()]; ok {
		q.mu.Unlock()
		return ConflictResolution{Action: action}
	}
	conflict.ApplyToAllCount = q.applyToAllCountLocked(entry.task, conflict)
	entry.task.Status = domainsftp.TaskAttention
	entry.task.PendingConflict = &conflict
	entry.task.Speed = 0
	entry.task.UpdatedAt = q.now()
	waiter := make(chan ConflictResolution, 1)
	entry.conflict = waiter
	snapshot := entry.task.Clone()
	q.mu.Unlock()

	q.emit(snapshot)
	q.persist()

	var answer ConflictResolution
	select {
	case answer = <-waiter:
	case <-ctx.Done():
		answer = ConflictResolution{Action: ConflictStop}
	}

	q.mu.Lock()
	if entry.conflict == waiter {
		entry.conflict = nil
	}
	entry.task.PendingConflict = nil
	if answer.Action == ConflictReplace && conflict.SourcePath != "" {
		if entry.task.ReplaceTargets == nil {
			entry.task.ReplaceTargets = map[string]bool{}
		}
		entry.task.ReplaceTargets[conflict.SourcePath] = true
	}
	if answer.Action == ConflictSkip && conflict.SourcePath != "" {
		if entry.task.SkipTargets == nil {
			entry.task.SkipTargets = map[string]bool{}
		}
		entry.task.SkipTargets[conflict.SourcePath] = true
	}
	if !entry.task.CancelRequested && !entry.task.PauseRequested && ctx.Err() == nil {
		entry.task.Status = domainsftp.TaskTransferring
		entry.task.Error = ""
		entry.task.Err = nil
	}
	entry.task.UpdatedAt = q.now()
	settled := entry.task.Clone()
	q.mu.Unlock()

	q.emit(settled)
	q.persist()
	return answer
}

// applyToAllCountLocked estimates how many conflicts one answer could cover:
// the rest of this request plus the other live rows in the same batch that do
// not already wait on a different destination type. An answer is keyed by the
// conflict type, so a sibling waiting on another type is not covered.
func (q *Queue) applyToAllCountLocked(task domainsftp.Task, conflict domainsftp.Conflict) int {
	count := 1 + conflict.Remaining
	batch := taskBatchKey(task)
	for id, other := range q.tasks {
		if id == task.ID || other.task.Terminal() || taskBatchKey(other.task) != batch {
			continue
		}
		if other.task.Status == domainsftp.TaskAttention && other.task.PendingConflict != nil &&
			other.task.PendingConflict.TypeKey() != conflict.TypeKey() {
			continue
		}
		count++
	}
	return count
}

// ResolveConflict answers the destination conflict a row is waiting on. With
// applyToAll, later conflicts of the same type in the same batch take the same
// action without asking again, and any sibling already waiting with that type
// is answered too. Without it, only the row that asked is answered; its
// siblings keep their own prompts. Stop cancels the whole batch, matching the
// original.
func (q *Queue) ResolveConflict(id domainsftp.TaskID, action ConflictPolicy, applyToAll bool) error {
	if q == nil {
		return ErrConflictNotPending
	}
	if !action.Valid() || action == ConflictAsk {
		return ErrConflictActionInvalid
	}
	now := q.now()
	var (
		cancels   []context.CancelFunc
		snapshots []domainsftp.Task
	)
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil {
		q.mu.Unlock()
		return domainsftp.ErrTaskNotFound
	}
	conflict := entry.task.PendingConflict
	if conflict == nil || entry.task.Status != domainsftp.TaskAttention || entry.conflict == nil {
		q.mu.Unlock()
		return ErrConflictNotPending
	}
	if action == ConflictMerge && !conflict.CanMerge() {
		q.mu.Unlock()
		return ErrConflictActionInvalid
	}
	if action == ConflictReplace && !conflict.CanReplace() {
		q.mu.Unlock()
		return ErrConflictActionInvalid
	}
	batch := taskBatchKey(entry.task)
	if applyToAll {
		if q.conflictDefaults[batch] == nil {
			q.conflictDefaults[batch] = map[string]ConflictPolicy{}
		}
		q.conflictDefaults[batch][conflict.TypeKey()] = action
	}
	answer := ConflictResolution{Action: action}
	// Answer the row that asked. A buffered send never blocks the control path.
	if entry.conflict != nil {
		select {
		case entry.conflict <- answer:
		default:
		}
	}
	if applyToAll {
		// An apply-to-all answer covers the siblings already waiting with the
		// same conflict type. Without it every sibling keeps its own prompt.
		for _, other := range q.tasks {
			if other == entry || other.task.Terminal() || taskBatchKey(other.task) != batch {
				continue
			}
			if other.task.Status == domainsftp.TaskAttention && other.task.PendingConflict != nil &&
				other.task.PendingConflict.TypeKey() == conflict.TypeKey() && other.conflict != nil {
				select {
				case other.conflict <- answer:
				default:
				}
			}
		}
	}
	if action == ConflictStop {
		// A stop means stop this batch, not only this entry. A running row is
		// asked to stop; one that has not started is settled at once.
		for _, other := range q.tasks {
			if other.task.Terminal() || taskBatchKey(other.task) != batch {
				continue
			}
			if other.running {
				if other.task.CancelRequested {
					continue
				}
				other.task.CancelRequested = true
				other.task.PauseRequested = false
				other.task.Epoch++
				other.task.UpdatedAt = now
				if other.cancel != nil {
					cancels = append(cancels, other.cancel)
				}
				continue
			}
			other.task.Status = domainsftp.TaskCancelled
			other.task.CancelRequested = false
			other.task.PauseRequested = false
			other.task.Speed = 0
			other.task.Error = ""
			other.task.Err = nil
			other.task.PendingConflict = nil
			other.task.FinishedAt = now
			other.task.UpdatedAt = now
			other.task.DirectoryResumeCheckpoint = nil
			other.task.CheckpointBytesDone = 0
			other.task.Targets = nil
			other.task.ReplaceTargets = nil
			other.task.SkipTargets = nil
			other.task.Skipped = 0
			q.releaseReservationsLocked(other.task.ID)
			snapshots = append(snapshots, other.task.Clone())
		}
	}
	q.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, snapshot := range snapshots {
		q.emit(snapshot)
	}
	q.persist()
	q.pump()
	return nil
}

// reserveName claims one plan-time destination name for a row. It reports
// false when another live row owns the exact path or a directory subtree that
// contains it.
func (q *Queue) reserveName(id domainsftp.TaskID, path string, directory bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.tasks[id]
	if entry == nil || entry.spec == nil {
		return false
	}
	claim := newPathClaim(entry.spec.Dest, path, directory)
	if owner, ok := q.names[claim.key]; ok && owner != id && q.reservationLiveLocked(owner) {
		return false
	}
	for otherID, other := range q.tasks {
		if otherID == id || !reservationActive(other.task.Status) {
			continue
		}
		for _, existing := range other.claims {
			if claimsOverlap(claim, existing) {
				return false
			}
		}
	}
	q.names[claim.key] = id
	// Keep the plan-time claim with the row as well, so a later Enqueue's
	// overlap check sees directory subtrees and not only exact names.
	upgraded := false
	for index := range entry.claims {
		if entry.claims[index].key == claim.key {
			entry.claims[index].directory = entry.claims[index].directory || directory
			upgraded = true
			break
		}
	}
	if !upgraded {
		entry.claims = append(entry.claims, claim)
	}
	return true
}

// reserveClaimsLocked rejects a row whose top-level destination is already
// owned by a live row. The caller holds q.mu.
func (q *Queue) reserveClaimsLocked(id domainsftp.TaskID, dest FileSystem,
	kind domainsftp.OperationKind, claims []pathClaim) error {
	for _, claim := range claims {
		if owner, ok := q.names[claim.key]; ok && owner != id && q.reservationLiveLocked(owner) {
			return failure(KindReserved, kind, claim.path, nil)
		}
		for otherID, other := range q.tasks {
			if otherID == id || !reservationActive(other.task.Status) {
				continue
			}
			for _, existing := range other.claims {
				if claimsOverlap(claim, existing) {
					return failure(KindReserved, kind, claim.path, nil)
				}
			}
		}
	}
	return nil
}

// reservationLiveLocked reports whether a name's owner still holds its path.
func (q *Queue) reservationLiveLocked(id domainsftp.TaskID) bool {
	entry := q.tasks[id]
	return entry != nil && reservationActive(entry.task.Status)
}

// releaseReservationsLocked drops one row's claims and plan-time names. The
// caller holds q.mu.
func (q *Queue) releaseReservationsLocked(id domainsftp.TaskID) {
	if entry := q.tasks[id]; entry != nil {
		entry.claims = nil
	}
	for key, owner := range q.names {
		if owner == id {
			delete(q.names, key)
		}
	}
}

// reservationActive reports the statuses that still hold or may soon hold a
// destination. Attention waits before writing but owns the path it asked
// about, so a second writer would race it. An interrupted row has no worker
// and is not active; it must re-reserve when resumed.
func reservationActive(status domainsftp.TaskStatus) bool {
	switch status {
	case domainsftp.TaskPending, domainsftp.TaskQueued, domainsftp.TaskTransferring,
		domainsftp.TaskPausing, domainsftp.TaskPaused, domainsftp.TaskAttention:
		return true
	}
	return false
}

// pathClaim is one normalized destination reservation. A directory claim
// covers its whole subtree; a file claim covers only its exact path.
type pathClaim struct {
	key       string
	path      string
	backing   string
	directory bool
	separator string
}

func newPathClaim(files FileSystem, path string, directory bool) pathClaim {
	canonical := files.Canonical(path)
	separator := "/"
	if files.Style() == StyleWindows {
		separator = `\`
		canonical = strings.ToLower(canonical)
	}
	canonical = trimClaimPath(canonical, separator)
	backing := files.Backing()
	if backing == "" {
		backing = "local"
	}
	return pathClaim{
		key:       backing + "\x00" + canonical,
		path:      canonical,
		backing:   backing,
		directory: directory,
		separator: separator,
	}
}

// trimClaimPath removes trailing separators except for a filesystem root, so
// "/a/b/" and "/a/b" name one path.
func trimClaimPath(path, separator string) string {
	if path == separator {
		return path
	}
	trimmed := strings.TrimRight(path, separator)
	if trimmed == "" {
		return separator
	}
	return trimmed
}

// claimsOverlap reports whether two reservations cannot be held by different
// writers. Exact paths always collide; a directory collides with anything
// below it, in either direction.
func claimsOverlap(a, b pathClaim) bool {
	if a.backing != b.backing {
		return false
	}
	if a.key == b.key {
		return true
	}
	if a.directory && claimWithin(a.path, a.separator, b.path) {
		return true
	}
	if b.directory && claimWithin(b.path, b.separator, a.path) {
		return true
	}
	return false
}

// claimWithin reports whether child is a strict descendant of parent.
func claimWithin(parent, separator, child string) bool {
	if parent == "" || child == "" || parent == child {
		return false
	}
	if !strings.HasSuffix(parent, separator) {
		parent += separator
	}
	return strings.HasPrefix(child, parent)
}

// queueClaims resolves the top-level destination each source reserves before
// its worker starts. It needs only the paths: the plan upgrades a claim to a
// subtree once it knows the source is a directory, so enqueueing never does a
// transport round trip on the caller's (UI) thread. A duplicate policy has no
// fixed name until its plan runs, so it reserves nothing here; the plan claims
// the chosen name instead.
func queueClaims(spec TaskSpec) []pathClaim {
	if spec.Conflict == ConflictDuplicate || spec.Source == nil || spec.Dest == nil {
		return nil
	}
	claims := make([]pathClaim, 0, len(spec.Sources))
	for _, source := range spec.Sources {
		target := spec.Dest.Join(spec.DestDir, spec.Source.Base(source))
		claims = append(claims, newPathClaim(spec.Dest, target, false))
	}
	return claims
}

// resumeClaims rebuilds a row's reservations when it is admitted again after a
// failure. A duplicate row's chosen names are pinned in Targets, so they are
// claimed too even though a duplicate has no fixed top-level destination.
func resumeClaims(entry *queueEntry) []pathClaim {
	claims := queueClaims(*entry.spec)
	seen := make(map[string]bool, len(claims))
	for _, claim := range claims {
		seen[claim.key] = true
	}
	for _, target := range entry.task.Targets {
		claim := newPathClaim(entry.spec.Dest, target, false)
		if seen[claim.key] {
			continue
		}
		seen[claim.key] = true
		claims = append(claims, claim)
	}
	return claims
}

// taskBatchKey groups one user action's rows. A row without an explicit batch
// is its own batch.
func taskBatchKey(task domainsftp.Task) string {
	if task.BatchID != "" {
		return task.BatchID
	}
	return string(task.ID)
}

// recordTarget persists the destination root resolved for one top-level
// source, so a resume continues in the same tree. A pause may arrive while the
// plan is resolving, so the target is recorded even then; a cancel clears it
// at settle.
func (q *Queue) recordTarget(id domainsftp.TaskID, source, target string) {
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil || entry.task.CancelRequested {
		q.mu.Unlock()
		return
	}
	if entry.task.Targets == nil {
		entry.task.Targets = map[string]string{}
	}
	changed := entry.task.Targets[source] != target
	entry.task.Targets[source] = target
	q.mu.Unlock()
	if changed {
		q.persist()
	}
}

// keepReplacement decides whether a failed replacement keeps its partial tree
// and backup. A pause and a lost transport can resume that tree; a cancel or
// ordinary failure restores the original.
func (q *Queue) keepReplacement(id domainsftp.TaskID, err error) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.tasks[id]
	if entry == nil {
		return false
	}
	return entry.task.PauseRequested || KindOf(err) == KindConnection
}

// clearResumeState drops a checkpoint and its pinned targets after the copy
// rolled the destination back to the original.
func (q *Queue) clearResumeState(id domainsftp.TaskID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.tasks[id]
	if entry == nil {
		return
	}
	entry.task.DirectoryResumeCheckpoint = nil
	entry.task.CheckpointBytesDone = 0
	entry.task.Targets = nil
	entry.task.ReplaceTargets = nil
	entry.task.SkipTargets = nil
	entry.task.FilesDone = 0
	entry.task.BytesDone = 0
	entry.task.CurrentFile = ""
}

// recordProgress updates the row and its average speed. It ignores late
// events after a pause or cancel latch, so a stopping stream cannot advance
// the visible bytes.
func (q *Queue) recordProgress(id domainsftp.TaskID, update Progress, baseBytes int64) {
	q.mu.Lock()
	entry := q.tasks[id]
	if entry == nil || entry.task.PauseRequested || entry.task.CancelRequested {
		q.mu.Unlock()
		return
	}
	now := q.now()
	changedCheckpoint := false
	if update.Checkpoint != nil && update.Checkpoint.Valid() {
		copied := update.Checkpoint.Clone()
		entry.task.DirectoryResumeCheckpoint = &copied
		entry.task.CheckpointBytesDone = update.CheckpointBytes
		changedCheckpoint = true
	} else if entry.task.DirectoryResumeCheckpoint != nil {
		// A request that is no longer a directory cannot use the old prefix;
		// keeping it would let freezeAtCheckpoint report work that was never
		// written.
		entry.task.DirectoryResumeCheckpoint = nil
		entry.task.CheckpointBytesDone = 0
		changedCheckpoint = true
	}
	entry.task.BytesDone = update.BytesDone
	entry.task.BytesTotal = update.BytesTotal
	entry.task.FilesDone = update.FilesDone
	entry.task.FilesTotal = update.FilesTotal
	entry.task.CurrentFile = update.Current
	if !entry.task.StartedAt.IsZero() {
		// Speed measures this attempt only: a resumed prefix must not read as
		// work done in the current second.
		moved := update.BytesDone - baseBytes
		if moved > 0 {
			elapsed := now.Sub(entry.task.StartedAt).Seconds()
			if elapsed >= 0.001 {
				entry.task.Speed = int64(float64(moved) / elapsed)
			}
		}
	}
	entry.task.UpdatedAt = now
	snapshot := entry.task.Clone()
	persist := changedCheckpoint && now.Sub(q.lastCheckpointSave) >= time.Second
	if persist {
		q.lastCheckpointSave = now
	}
	q.mu.Unlock()
	q.emit(snapshot)
	if persist {
		// Keep a long directory Transfer resumable across a crash without
		// rewriting the whole history on every 256 KiB.
		q.persist()
	}
}

// resetForRetry clears the per-attempt counters before the automatic retry.
func (q *Queue) resetForRetry(id domainsftp.TaskID) {
	q.mu.Lock()
	entry := q.tasks[id]
	if entry != nil {
		entry.task.Attempts++
		entry.task.BytesDone = entry.task.CheckpointBytesDone
		entry.task.FilesDone = 0
		if checkpoint := entry.task.DirectoryResumeCheckpoint; checkpoint != nil {
			entry.task.FilesDone = checkpoint.CompletedEntries
		}
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
	// A worker that settles is no longer waiting on an answer.
	entry.conflict = nil
	entry.task.PendingConflict = nil
	cancelled := entry.task.CancelRequested ||
		(!entry.task.PauseRequested && err != nil && KindOf(err) == KindCancelled)
	entry.task.ReconnectRequired = false
	switch {
	case cancelled:
		entry.task.Status = domainsftp.TaskCancelled
		entry.task.Error = ""
		entry.task.Err = nil
		entry.task.PauseRequested = false
		// Cancel is a hard stop: a later Retry replays from the beginning.
		entry.task.DirectoryResumeCheckpoint = nil
		entry.task.CheckpointBytesDone = 0
		entry.task.Targets = nil
		entry.task.ReplaceTargets = nil
		entry.task.SkipTargets = nil
	case entry.task.PauseRequested:
		entry.task.Status = domainsftp.TaskPaused
		entry.task.Error = ""
		entry.task.Err = nil
		freezeAtCheckpoint(&entry.task)
	case err != nil:
		entry.task.Status = domainsftp.TaskFailed
		kind := KindOf(err)
		entry.task.Error = string(kind)
		entry.task.Err = err
		// A lost transport cannot be replayed over the same dead stream. The
		// row explains that it needs a reconnect, and Retry refuses until a
		// fresh transport exists.
		entry.task.ReconnectRequired = kind == KindConnection
		freezeAtCheckpoint(&entry.task)
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
		entry.task.DirectoryResumeCheckpoint = nil
		entry.task.CheckpointBytesDone = 0
		entry.task.Targets = nil
		entry.task.ReplaceTargets = nil
		entry.task.SkipTargets = nil
	}
	if entry.task.Terminal() {
		entry.task.FinishedAt = now
	}
	// The worker is gone, but a paused or interrupted row will continue into
	// the destinations it already claimed, so it keeps them until it is
	// terminal. Releasing here would let another Transfer take a paused
	// duplicate's chosen name.
	if entry.task.Terminal() {
		q.releaseReservationsLocked(id)
	}
	// Apply-to-all answers end with the batch's last live row.
	batch := taskBatchKey(entry.task)
	if _, ok := q.conflictDefaults[batch]; ok {
		live := false
		for otherID, other := range q.tasks {
			if otherID == id || other.task.Terminal() || taskBatchKey(other.task) != batch {
				continue
			}
			live = true
			break
		}
		if !live {
			delete(q.conflictDefaults, batch)
		}
	}
	entry.task.Speed = 0
	entry.task.UpdatedAt = now
	snapshot := entry.task.Clone()
	q.mu.Unlock()

	q.emit(snapshot)
	q.persist()
	q.pump()
}

// freezeAtCheckpoint drops the partial file's bytes from a stopped row. The
// checkpoint covers whole entries only, so the visible progress matches what
// a resume will actually skip.
func freezeAtCheckpoint(task *domainsftp.Task) {
	if task.DirectoryResumeCheckpoint == nil || !task.DirectoryResumeCheckpoint.Valid() {
		return
	}
	task.FilesDone = task.DirectoryResumeCheckpoint.CompletedEntries
	task.BytesDone = task.CheckpointBytesDone
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

// queueHostID resolves the saved Host behind one endpoint. An explicit value
// wins; otherwise the endpoint's queue lane supplies it, matching the lane the
// request uses. Only a Host lane names a saved Host: a local or session lane
// has none, so a later reconnect cannot mistake it for the local device.
func queueHostID(explicit string, lane domainsftp.ResourceKey, files FileSystem) string {
	if explicit != "" {
		return explicit
	}
	key := lane
	if key == "" {
		key = resourceKeyFor(files)
	}
	if strings.HasPrefix(string(key), "host:") {
		return strings.TrimPrefix(string(key), "host:")
	}
	return ""
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
	if KindOf(err) == KindConnection || isConnectionError(err) {
		return true
	}
	// A failed-but-unclassified message can still be a momentary network
	// blip; these tokens are the original's retry hint beyond the explicit
	// connection vocabulary.
	message := strings.ToLower(err.Error())
	for _, token := range []string{
		"network", "temporarily unavailable", "try again",
	} {
		if strings.Contains(message, token) {
			return true
		}
	}
	return false
}
