package transfer

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// TaskID identifies one queued Transfer for its whole life, including across
// a restart. It is opaque to the UI.
type TaskID string

// TaskStatus is a Transfer's lifecycle state. The values are stable
// identifiers used by the queue, persistence, and tests.
type TaskStatus string

const (
	// TaskPending is a Transfer that was created but is not queued yet.
	TaskPending TaskStatus = "pending"
	// TaskQueued waits for a resource slot.
	TaskQueued TaskStatus = "queued"
	// TaskTransferring is actively copying.
	TaskTransferring TaskStatus = "transferring"
	// TaskPausing waits for the copy to stop at a chunk boundary.
	TaskPausing TaskStatus = "pausing"
	// TaskPaused is stopped and can be resumed.
	TaskPaused TaskStatus = "paused"
	// TaskAttention needs the user, for example an unresolvable conflict.
	TaskAttention TaskStatus = "attention"
	// TaskInterrupted lost its live transport, usually after a restart.
	// Reconnecting and retrying continues it.
	TaskInterrupted TaskStatus = "interrupted"
	// TaskCompleted finished successfully.
	TaskCompleted TaskStatus = "completed"
	// TaskFailed ended with an error.
	TaskFailed TaskStatus = "failed"
	// TaskCancelled was stopped by the user.
	TaskCancelled TaskStatus = "cancelled"
)

// Valid reports whether the status is one the queue knows.
func (s TaskStatus) Valid() bool {
	switch s {
	case TaskPending, TaskQueued, TaskTransferring, TaskPausing, TaskPaused,
		TaskAttention, TaskInterrupted, TaskCompleted, TaskFailed, TaskCancelled:
		return true
	}
	return false
}

// Terminal reports whether the status is final: no further control action
// can move the Transfer.
func (s TaskStatus) Terminal() bool {
	switch s {
	case TaskCompleted, TaskFailed, TaskCancelled:
		return true
	}
	return false
}

// Active reports whether a worker currently owns the Transfer. A pausing
// Transfer still has a worker draining its last chunk.
func (s TaskStatus) Active() bool {
	return s == TaskTransferring || s == TaskPausing
}

// CanPause reports whether Pause applies.
func (s TaskStatus) CanPause() bool {
	switch s {
	case TaskPending, TaskQueued, TaskTransferring:
		return true
	}
	return false
}

// CanResume reports whether Resume applies. A pausing Transfer still owns its
// worker and its canceled context, so Resume does not apply until it settles
// as paused.
func (s TaskStatus) CanResume() bool {
	switch s {
	case TaskPaused, TaskInterrupted:
		return true
	}
	return false
}

// CanRetry reports whether Retry applies.
func (s TaskStatus) CanRetry() bool {
	switch s {
	case TaskAttention, TaskInterrupted, TaskFailed, TaskCancelled:
		return true
	}
	return false
}

// CanCancel reports whether Cancel applies.
func (s TaskStatus) CanCancel() bool { return !s.Terminal() }

// ResourceKey is one queue lane. The original groups work by the endpoint
// that bounds throughput: the local device, one Host, or one Session.
type ResourceKey string

// LocalResourceKey is the lane shared by local filesystem work.
func LocalResourceKey() ResourceKey { return "local" }

// HostResourceKey is the lane for one saved Host.
func HostResourceKey(host string) ResourceKey { return ResourceKey("host:" + host) }

// SessionResourceKey is the lane for one live Session.
func SessionResourceKey(session string) ResourceKey { return ResourceKey("session:" + session) }

// Valid reports whether the key names a lane.
func (k ResourceKey) Valid() bool {
	switch {
	case k == LocalResourceKey():
		return true
	case strings.HasPrefix(string(k), "host:"):
		return len(k) > len("host:")
	case strings.HasPrefix(string(k), "session:"):
		return len(k) > len("session:")
	}
	return false
}

// Queue limits. They match the original scheduler: six files per resource
// (1-16), 32 KiB requests, 64 in-flight requests per file, one fast-download
// channel per Session, and four listing workers (1-8).
const (
	DefaultConcurrency             = 6
	MinConcurrency                 = 1
	MaxConcurrency                 = 16
	RequestSize                    = 32 * 1024
	InFlightRequestsPerFile        = 64
	FastDownloadChannelsPerSession = 1
	DefaultListingWorkers          = 4
	MinListingWorkers              = 1
	MaxListingWorkers              = 8
	// HistoryLimit is how many terminal rows the Transfer center retains.
	HistoryLimit = 200
)

// HistoryRetention is how long a terminal row stays visible.
const HistoryRetention = 30 * 24 * time.Hour

// ResolveConcurrency returns a stored concurrency inside the supported
// range, or the default when the value is absent or out of range.
func ResolveConcurrency(stored int) int {
	if stored >= MinConcurrency && stored <= MaxConcurrency {
		return stored
	}
	return DefaultConcurrency
}

// ResolveListingWorkers returns a stored listing-worker count inside the
// supported range, or the default.
func ResolveListingWorkers(stored int) int {
	if stored >= MinListingWorkers && stored <= MaxListingWorkers {
		return stored
	}
	return DefaultListingWorkers
}

// Task is one Transfer in the global queue. It carries the durable metadata
// the Transfer center shows and persists; the live filesystem handles and
// cancel function stay in the application queue.
type Task struct {
	ID     TaskID        `json:"id"`
	Kind   OperationKind `json:"kind"`
	Status TaskStatus    `json:"status"`
	// DisplayName is the row's label. The UI sets it only when a caller knows
	// a better name; otherwise Name derives one from a single source.
	DisplayName string `json:"name"`
	// Sources are the entries being copied; DestDir is their common target.
	Sources []string `json:"sources,omitempty"`
	DestDir string   `json:"destDir,omitempty"`
	Move    bool     `json:"move,omitempty"`
	// Conflict is the policy the Transfer runs with.
	Conflict string `json:"conflict,omitempty"`
	// ResourceKeys are the lanes this Transfer occupies.
	ResourceKeys []ResourceKey `json:"resourceKeys,omitempty"`
	SourceLabel  string        `json:"sourceLabel,omitempty"`
	DestLabel    string        `json:"destLabel,omitempty"`

	BytesDone  int64 `json:"bytesDone"`
	BytesTotal int64 `json:"bytesTotal"`
	FilesDone  int   `json:"filesDone"`
	FilesTotal int   `json:"filesTotal"`
	// CurrentFile is the entry being copied right now. It lets the center
	// show per-file progress for a multi-file Transfer; it is not a label.
	CurrentFile string `json:"currentFile,omitempty"`
	// Speed is the last measured bytes per second.
	Speed int64 `json:"speed"`

	// Error is the localized-by-code failure classification. Err carries the
	// live error for the process that owns the queue and is never persisted.
	Error string `json:"error,omitempty"`
	Err   error  `json:"-"`

	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`

	// Epoch advances on every intentional control action. A stale worker
	// settlement can then be recognized and ignored.
	Epoch uint64 `json:"epoch"`
	// PauseRequested and CancelRequested are the control latches.
	PauseRequested    bool `json:"pauseRequested,omitempty"`
	CancelRequested   bool `json:"cancelRequested,omitempty"`
	ReconnectRequired bool `json:"reconnectRequired,omitempty"`
	Attempts          int  `json:"attempts,omitempty"`
}

// Name returns the row's label, deriving it from the single source when the
// queue did not set one. A multi-file row has no name here: its label is a
// localized count the UI renders, so the domain holds no display strings.
func (t Task) Name() string {
	if t.DisplayName != "" {
		return t.DisplayName
	}
	if len(t.Sources) == 1 {
		trimmed := strings.TrimRight(t.Sources[0], `/\`)
		if index := strings.LastIndexAny(trimmed, `/\`); index >= 0 && index+1 < len(trimmed) {
			return trimmed[index+1:]
		}
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// Terminal reports whether the Transfer's status is final.
func (t Task) Terminal() bool { return t.Status.Terminal() }

// Percent returns byte progress in 0..100. It falls back to file counts for
// an operation whose size is unknown, and a completed Transfer is 100.
func (t Task) Percent() int {
	if t.Status == TaskCompleted {
		return 100
	}
	if t.BytesTotal > 0 {
		percent := int(t.BytesDone * 100 / t.BytesTotal)
		if percent < 0 {
			return 0
		}
		if percent > 100 {
			return 100
		}
		return percent
	}
	if t.FilesTotal > 0 {
		percent := t.FilesDone * 100 / t.FilesTotal
		if percent < 0 {
			return 0
		}
		if percent > 100 {
			return 100
		}
		return percent
	}
	return 0
}

// ETASeconds estimates the remaining time while transferring. It returns 0
// when there is no meaningful estimate: not transferring, no remaining
// bytes, a speed sample below 1 KiB/s (the original's WinSCP-style gate), or
// an estimate beyond 48 hours (a momentary stall must not show days).
func (t Task) ETASeconds() int {
	if t.Status != TaskTransferring {
		return 0
	}
	remaining := t.BytesTotal - t.BytesDone
	if remaining <= 0 {
		return 0
	}
	if t.Speed < 1024 {
		return 0
	}
	seconds := (remaining + t.Speed - 1) / t.Speed
	if seconds <= 0 || seconds > 48*3600 {
		return 0
	}
	return int(seconds)
}

// Clone returns a copy safe to hand to another goroutine. The error value is
// immutable, so the copy may share it.
func (t Task) Clone() Task {
	out := t
	if t.Sources != nil {
		out.Sources = append([]string(nil), t.Sources...)
	}
	if t.ResourceKeys != nil {
		out.ResourceKeys = append([]ResourceKey(nil), t.ResourceKeys...)
	}
	return out
}

// DowngradeInterrupted turns every unfinished row into an interrupted,
// reconnect-required Transfer. After a restart no live transport, stream, or
// pause latch survives, so resuming must establish a new connection.
func DowngradeInterrupted(tasks []Task) []Task {
	out := make([]Task, len(tasks))
	for index, task := range tasks {
		task = task.Clone()
		if !task.Terminal() {
			task.Status = TaskInterrupted
			task.ReconnectRequired = true
			task.Speed = 0
			task.PauseRequested = false
			task.CancelRequested = false
			// A previous process's epoch must not fence the first progress
			// after a dedicated reconnect.
			task.Epoch = 0
		}
		out[index] = task
	}
	return out
}

// PruneTasks keeps every unfinished row and at most HistoryLimit terminal
// rows from the last HistoryRetention, newest first. Unfinished rows sort
// before terminal rows, and each group keeps creation order.
func PruneTasks(tasks []Task, now time.Time) []Task {
	unfinished := make([]Task, 0, len(tasks))
	terminal := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Terminal() {
			terminal = append(terminal, task.Clone())
		} else {
			unfinished = append(unfinished, task.Clone())
		}
	}
	fresh := terminal[:0]
	for _, task := range terminal {
		if historyAge(task, now) <= HistoryRetention {
			fresh = append(fresh, task)
		}
	}
	terminal = fresh
	sort.SliceStable(terminal, func(i, j int) bool {
		return historyTime(terminal[i]).After(historyTime(terminal[j]))
	})
	if len(terminal) > HistoryLimit {
		terminal = terminal[:HistoryLimit]
	}
	out := make([]Task, 0, len(unfinished)+len(terminal))
	out = append(out, unfinished...)
	out = append(out, terminal...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// historyTime is the row's terminal timestamp, falling back to the last
// update or creation for a legacy row without one.
func historyTime(task Task) time.Time {
	switch {
	case !task.FinishedAt.IsZero():
		return task.FinishedAt
	case !task.UpdatedAt.IsZero():
		return task.UpdatedAt
	default:
		return task.CreatedAt
	}
}

func historyAge(task Task, now time.Time) time.Duration {
	at := historyTime(task)
	if at.IsZero() {
		return 0
	}
	return now.Sub(at)
}

// ErrTaskNotFound reports a control action on an unknown Transfer.
var ErrTaskNotFound = errors.New("transfer queue: task not found")

// ErrTaskUnavailable reports a Transfer whose source or destination is no
// longer available, as after a restart without a reconnect.
var ErrTaskUnavailable = errors.New("transfer queue: task needs its panes reconnected")

// ErrTaskNotRetryable reports Retry on a row that cannot be replayed.
var ErrTaskNotRetryable = errors.New("transfer queue: task cannot be retried")
