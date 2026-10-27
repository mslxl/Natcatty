//go:build selftest

package app

// Ticket 31 self-test. Press F11, then 3, then 1. The check proves the global
// Transfer queue and the Transfer center through the real queue, local
// adapter, persisted history, surface state, and surface view:
//
//   - A real local source is enqueued and copied. Progress events show bytes
//     moving and an honest ETA while the row is transferring.
//   - Pause stops a queued transfer and Resume replays it to completion.
//   - A failed transfer is retried, and the retried row runs again with a new
//     lifecycle epoch.
//   - Cancel removes a queued transfer without touching the source.
//   - The per-resource limit is one, so three enqueued transfers have at least
//     one queued waiter and the scheduler never runs more than one at a time.
//   - Restarting a queue over its saved history downgrades a transferring row
//     to interrupted, clears its speed, and keeps completed rows.
//   - A second Transfer pressed while the first is running is enqueued instead
//     of refused, and the center lists both.
//   - A lost transport settles as connection + reconnect-required, and Retry
//     refuses to replay the dead stream.
//   - The real Transfer center renders at 1100x720 in English and 720x480 in
//     Simplified Chinese, opens from the header button, closes on a backdrop
//     click and on the effective toggle chord, and explains a reconnect row.
//
// The queue and the surfaces run the same code the app wires. Every wait polls
// a real task state, the persisted repository, or the rendered view with a
// hard deadline; there is no fixed sleep.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-31.json.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	transferui "github.com/mslxl/natcatty/ui/transfer"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

const selftest31Ticket = 31

const selftest31Wait = 30 * time.Second

// selftest31BigSize holds a resource slot for the pause, cancel, and limit
// steps. selftest31EtaSize is much larger: the queue only computes a speed
// after its one-millisecond window, so the ETA step needs a transfer that
// spans many of those windows even on a very fast disk.
const (
	selftest31BigSize = 8 << 20
	selftest31EtaSize = 64 << 20
)

type selftest31 struct {
	workdir  string
	source   string
	outs     []string
	service  *apptransfer.Service
	files    apptransfer.FileSystem
	repo     *selftest31Repo
	queue    *apptransfer.Queue
	state    *transferstate.State
	cleanup  func()
	deadline time.Time

	mu     sync.Mutex
	events []domainsftp.Task

	enqueuePhase  int
	progressID    domainsftp.TaskID
	etaSeconds    int
	pausePhase    int
	pauseID       domainsftp.TaskID
	blockID       domainsftp.TaskID
	retryPhase    int
	retryID       domainsftp.TaskID
	retryEpoch    uint64
	cancelPhase   int
	cancelID      domainsftp.TaskID
	limitPhase    int
	limitIDs      []domainsftp.TaskID
	maxActive     int
	busyPhase     int
	busyBlockID   domainsftp.TaskID
	busySecondID  domainsftp.TaskID
	busyBlock     *selftest31BlockedFS
	busyRelease   chan struct{}
	busyNavigated bool
	failPhase     int
	failID        domainsftp.TaskID
	failEpoch     uint64
	finished      bool
	observedEpoch uint64
}

func (t *selftest31) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest31Ticket,
		Name:   "Transfer queue and Transfer center",
		Steps: []selftest.Step{
			{Name: "enqueue-reports-progress-and-eta", Run: t.enqueueStep},
			{Name: "pause-and-resume", Run: t.pauseStep},
			{Name: "retry-a-failed-transfer", Run: t.retryStep},
			{Name: "cancel-a-transfer", Run: t.cancelStep},
			{Name: "enforce-the-per-resource-limit", Run: t.limitStep},
			{Name: "restart-downgrades-unfinished-rows", Run: t.restartStep},
			{Name: "transfer-while-busy-enqueues", Run: t.busyStep},
			{Name: "transport-failure-requires-reconnect", Run: t.failStep},
			{Name: "transfer-center-renders-and-opens", Run: t.viewStep},
		},
	}
}

// selftest31Repo is an in-memory queue repository. It is the same port the
// app's JSON file implements, so the restart step exercises the production
// load-and-downgrade path rather than a test-only shortcut.
type selftest31Repo struct {
	mu    sync.Mutex
	tasks []domainsftp.Task
}

func (r *selftest31Repo) LoadTasks(context.Context) ([]domainsftp.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domainsftp.Task, len(r.tasks))
	copy(out, r.tasks)
	return out, nil
}

func (r *selftest31Repo) SaveTasks(_ context.Context, tasks []domainsftp.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append([]domainsftp.Task(nil), tasks...)
	return nil
}

// selftest31Local is a local-only opener. The ticket is about the queue and
// the center, so the remote side is absent and any remote request says so.
type selftest31Local struct {
	*adaptersftp.Local
}

func (selftest31Local) OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error) {
	return nil, fmt.Errorf("the ticket 31 self-test has no remote host")
}

// selftest31Bindings is an empty persisted binding document, so the service
// exposes the platform defaults: the Transfer center chord is Ctrl + Shift + T.
type selftest31Bindings struct{}

func (selftest31Bindings) Load(context.Context) (appkeybindings.State, error) {
	return appkeybindings.State{}, nil
}

func (selftest31Bindings) Save(context.Context, appkeybindings.State) error { return nil }

// selftest31BlockedFS holds a resource lane by waiting in Open until the test
// releases it. started reports that a worker actually reached the file, which
// is the observable condition the busy step waits on.
type selftest31BlockedFS struct {
	apptransfer.FileSystem
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (f *selftest31BlockedFS) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	f.once.Do(func() { close(f.started) })
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return f.FileSystem.Open(ctx, path)
}

// selftest31DeadFS is a source whose transport has gone away: every read
// fails the way a dropped SSH stream does.
type selftest31DeadFS struct {
	apptransfer.FileSystem
}

func (selftest31DeadFS) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("connection reset by peer")
}

func (t *selftest31) setup() error {
	workdir, err := os.MkdirTemp("", "natcatty-selftest-31-")
	if err != nil {
		return err
	}
	t.workdir = workdir
	t.source = filepath.Join(workdir, "source")
	t.outs = nil
	for _, dir := range []string{t.source} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for index := 1; index <= 12; index++ {
		out := filepath.Join(workdir, fmt.Sprintf("out%d", index))
		if err := os.MkdirAll(out, 0o755); err != nil {
			return err
		}
		t.outs = append(t.outs, out)
	}
	if err := os.WriteFile(filepath.Join(t.source, "small.txt"), []byte("small"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(t.source, "big.bin"),
		bytes.Repeat([]byte("n"), selftest31BigSize), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(t.source, "eta.bin"),
		bytes.Repeat([]byte("e"), selftest31EtaSize), 0o644); err != nil {
		return err
	}

	t.repo = &selftest31Repo{}
	opener := selftest31Local{Local: adaptersftp.NewLocal()}
	t.service = apptransfer.NewService(opener, opener)
	files, err := t.service.OpenLocal(context.Background())
	if err != nil {
		return err
	}
	t.files = files
	t.queue = apptransfer.NewQueue(t.service,
		apptransfer.WithQueueConcurrency(1),
		apptransfer.WithQueueRepository(t.repo),
		apptransfer.WithQueueRetryDelay(0),
	)
	t.queue.Observe(t.observe)
	if err := t.queue.Start(context.Background()); err != nil {
		_ = files.Close()
		return err
	}
	keybindings := appkeybindings.NewService(selftest31Bindings{}, appkeybindings.WithGOOS(runtime.GOOS))
	if err := keybindings.Load(context.Background()); err != nil {
		_ = files.Close()
		return err
	}
	t.state = transferstate.NewState(t.service,
		transferstate.WithQueue(t.queue),
		transferstate.WithKeybindings(keybindings),
	)
	t.cleanup = func() {
		t.queue.Shutdown()
		t.state.Shutdown()
		_ = t.files.Close()
		_ = os.RemoveAll(t.workdir)
	}
	return nil
}

// observe records progress snapshots off the queue's worker goroutines. The
// steps read the copy later, so a fast transfer cannot outrun the check.
func (t *selftest31) observe(task domainsftp.Task) {
	t.mu.Lock()
	t.events = append(t.events, task)
	if len(t.events) > 20000 {
		t.events = t.events[len(t.events)-10000:]
	}
	t.mu.Unlock()
}

func (t *selftest31) recorded(id domainsftp.TaskID) []domainsftp.Task {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]domainsftp.Task, 0, len(t.events))
	for _, task := range t.events {
		if task.ID == id {
			out = append(out, task)
		}
	}
	return out
}

func (t *selftest31) enqueue(name, source, dest string) (domainsftp.TaskID, error) {
	return t.queue.Enqueue(apptransfer.TaskSpec{
		Kind:        domainsftp.OpCopy,
		Source:      t.files,
		Dest:        t.files,
		Sources:     []string{source},
		DestDir:     dest,
		DisplayName: name,
		SourceLabel: "local",
		DestLabel:   "local",
	})
}

func (t *selftest31) task(id domainsftp.TaskID) (domainsftp.Task, bool) {
	return t.queue.Task(id)
}

func (t *selftest31) expired(what string) selftest.Outcome {
	if time.Now().After(t.deadline) {
		return selftest.Failed(fmt.Errorf("%s did not settle within %s", what, selftest31Wait))
	}
	return selftest.Pending
}

func (t *selftest31) enqueueStep(*ui.Context) selftest.Outcome {
	if t.finished {
		*t = selftest31{}
	}
	switch t.enqueuePhase {
	case 0:
		if err := t.setup(); err != nil {
			return selftest.Failed(err)
		}
		t.deadline = time.Now().Add(selftest31Wait)
		id, err := t.enqueue("eta.bin", filepath.Join(t.source, "eta.bin"), t.outs[0])
		if err != nil {
			return selftest.Failed(err)
		}
		t.progressID = id
		t.enqueuePhase = 1
		return selftest.Pending
	case 1:
		task, ok := t.task(t.progressID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the enqueued transfer disappeared"))
		}
		if t.etaSeconds == 0 {
			for _, event := range t.recorded(t.progressID) {
				if event.Status != domainsftp.TaskTransferring ||
					event.BytesDone <= 0 || event.BytesDone >= event.BytesTotal {
					continue
				}
				if eta := event.ETASeconds(); eta > 0 {
					t.etaSeconds = eta
					break
				}
			}
		}
		if task.Status == domainsftp.TaskFailed || task.Status == domainsftp.TaskCancelled {
			return selftest.Failed(fmt.Errorf("the first transfer ended %s: %v", task.Status, task.Err))
		}
		if task.Status == domainsftp.TaskCompleted {
			if t.etaSeconds == 0 {
				return selftest.Failed(fmt.Errorf("the transfer reported no ETA while it moved bytes"))
			}
			return selftest.Passed(
				fmt.Sprintf("the queue copied eta.bin with %d%% progress and an ETA of %ds from a live progress event",
					task.Percent(), t.etaSeconds),
				"apptransfer.Queue + adaptersftp.Local")
		}
		return t.expired("the first queue transfer")
	}
	return selftest.Failed(fmt.Errorf("unexpected enqueue phase %d", t.enqueuePhase))
}

func (t *selftest31) pauseStep(*ui.Context) selftest.Outcome {
	switch t.pausePhase {
	case 0:
		t.deadline = time.Now().Add(selftest31Wait)
		block, err := t.enqueue("big.bin", filepath.Join(t.source, "big.bin"), t.outs[1])
		if err != nil {
			return selftest.Failed(err)
		}
		target, err := t.enqueue("small.txt", filepath.Join(t.source, "small.txt"), t.outs[2])
		if err != nil {
			return selftest.Failed(err)
		}
		t.blockID, t.pauseID = block, target
		if err := t.queue.Pause(target); err != nil {
			return selftest.Failed(fmt.Errorf("pause the queued transfer: %v", err))
		}
		t.pausePhase = 1
		return selftest.Pending
	case 1:
		task, ok := t.task(t.pauseID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the paused transfer disappeared"))
		}
		if task.Status != domainsftp.TaskPaused {
			return t.expired(fmt.Sprintf("the transfer to become paused (is %s)", task.Status))
		}
		t.observedEpoch = task.Epoch
		if err := t.queue.Resume(t.pauseID); err != nil {
			return selftest.Failed(fmt.Errorf("resume the paused transfer: %v", err))
		}
		t.pausePhase = 2
		return selftest.Pending
	case 2:
		target, ok := t.task(t.pauseID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the resumed transfer disappeared"))
		}
		// Pause and resume allocate a new lifecycle epoch, so a late progress
		// event from the paused attempt cannot overwrite the new one.
		if target.Epoch <= t.observedEpoch {
			return selftest.Failed(fmt.Errorf("Resume did not advance the lifecycle epoch past %d", t.observedEpoch))
		}
		if target.Status == domainsftp.TaskFailed || target.Status == domainsftp.TaskCancelled {
			return selftest.Failed(fmt.Errorf("the resumed transfer ended %s: %v", target.Status, target.Err))
		}
		block, _ := t.task(t.blockID)
		if target.Status == domainsftp.TaskCompleted && block.Status.Terminal() {
			return selftest.Passed(
				"a queued transfer paused, resumed after the blocker, and completed in a newer lifecycle epoch",
				"apptransfer.Queue + transferstate.State")
		}
		return t.expired("the paused and resumed transfer")
	}
	return selftest.Failed(fmt.Errorf("unexpected pause phase %d", t.pausePhase))
}

func (t *selftest31) retryStep(*ui.Context) selftest.Outcome {
	switch t.retryPhase {
	case 0:
		t.deadline = time.Now().Add(selftest31Wait)
		id, err := t.enqueue("missing.bin", filepath.Join(t.source, "missing.bin"), t.outs[3])
		if err != nil {
			return selftest.Failed(err)
		}
		t.retryID = id
		t.retryPhase = 1
		return selftest.Pending
	case 1:
		task, ok := t.task(t.retryID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the failing transfer disappeared"))
		}
		if task.Status != domainsftp.TaskFailed {
			return t.expired(fmt.Sprintf("the missing-source transfer to fail (is %s)", task.Status))
		}
		before := task.Epoch
		if err := t.queue.Retry(t.retryID); err != nil {
			return selftest.Failed(fmt.Errorf("retry the failed transfer: %v", err))
		}
		again, _ := t.task(t.retryID)
		if again.Epoch <= before {
			return selftest.Failed(fmt.Errorf("Retry did not allocate a new lifecycle epoch"))
		}
		if again.Status != domainsftp.TaskQueued && again.Status != domainsftp.TaskTransferring {
			return selftest.Failed(fmt.Errorf("Retry left the row %s", again.Status))
		}
		t.retryEpoch = again.Epoch
		t.retryPhase = 2
		return selftest.Pending
	case 2:
		task, ok := t.task(t.retryID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the retried transfer disappeared"))
		}
		if task.Status.Terminal() && task.Epoch >= t.retryEpoch {
			if task.Status != domainsftp.TaskFailed {
				return selftest.Failed(fmt.Errorf("the retried missing source ended %s, want failed", task.Status))
			}
			return selftest.Passed(
				"a failed transfer was replayed on demand; the new attempt kept the new lifecycle epoch and failed for the same missing source",
				"apptransfer.Queue + domainsftp.TaskStatus.CanRetry")
		}
		return t.expired("the retried transfer")
	}
	return selftest.Failed(fmt.Errorf("unexpected retry phase %d", t.retryPhase))
}

func (t *selftest31) cancelStep(*ui.Context) selftest.Outcome {
	switch t.cancelPhase {
	case 0:
		t.deadline = time.Now().Add(selftest31Wait)
		block, err := t.enqueue("big.bin", filepath.Join(t.source, "big.bin"), t.outs[4])
		if err != nil {
			return selftest.Failed(err)
		}
		target, err := t.enqueue("small.txt", filepath.Join(t.source, "small.txt"), t.outs[5])
		if err != nil {
			return selftest.Failed(err)
		}
		t.blockID, t.cancelID = block, target
		if err := t.queue.Cancel(target); err != nil {
			return selftest.Failed(fmt.Errorf("cancel the queued transfer: %v", err))
		}
		t.cancelPhase = 1
		return selftest.Pending
	case 1:
		target, ok := t.task(t.cancelID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the cancelled transfer disappeared"))
		}
		if target.Status != domainsftp.TaskCancelled {
			return t.expired(fmt.Sprintf("the queued transfer to cancel (is %s)", target.Status))
		}
		if _, err := os.Stat(filepath.Join(t.outs[5], "small.txt")); !os.IsNotExist(err) {
			return selftest.Failed(fmt.Errorf("a cancelled transfer still wrote its destination"))
		}
		block, _ := t.task(t.blockID)
		if !block.Status.Terminal() {
			return t.expired("the blocker after the cancel")
		}
		return selftest.Passed(
			"a queued transfer cancelled immediately and never wrote its destination file",
			"apptransfer.Queue + adaptersftp.Local")
	}
	return selftest.Failed(fmt.Errorf("unexpected cancel phase %d", t.cancelPhase))
}

func (t *selftest31) limitStep(*ui.Context) selftest.Outcome {
	switch t.limitPhase {
	case 0:
		t.deadline = time.Now().Add(selftest31Wait)
		t.limitIDs = nil
		for index, name := range []string{"big.bin", "big.bin", "small.txt"} {
			source := filepath.Join(t.source, name)
			dest := t.outs[6+index]
			id, err := t.enqueue(name, source, dest)
			if err != nil {
				return selftest.Failed(err)
			}
			t.limitIDs = append(t.limitIDs, id)
		}
		stats := t.queue.Stats()
		if stats.Queued < 1 {
			return selftest.Failed(fmt.Errorf("the per-resource limit admitted %d waiters, want at least 1", stats.Queued))
		}
		if stats.PeakActive > 1 {
			return selftest.Failed(fmt.Errorf("the per-resource limit ran %d transfers at once", stats.PeakActive))
		}
		t.maxActive = stats.Active
		t.limitPhase = 1
		return selftest.Pending
	case 1:
		stats := t.queue.Stats()
		if stats.Active > t.maxActive {
			t.maxActive = stats.Active
		}
		if stats.Active > 1 || t.maxActive > 1 {
			return selftest.Failed(fmt.Errorf("the scheduler ran %d transfers at once under a limit of 1", stats.Active))
		}
		done := 0
		for _, id := range t.limitIDs {
			task, ok := t.task(id)
			if !ok {
				return selftest.Failed(fmt.Errorf("a limited transfer disappeared"))
			}
			if task.Status == domainsftp.TaskFailed || task.Status == domainsftp.TaskCancelled {
				return selftest.Failed(fmt.Errorf("a limited transfer ended %s: %v", task.Status, task.Err))
			}
			if task.Status == domainsftp.TaskCompleted {
				done++
			}
		}
		if done == len(t.limitIDs) {
			if stats.PeakActive > 1 {
				return selftest.Failed(fmt.Errorf("peak active = %d under a limit of 1", stats.PeakActive))
			}
			return selftest.Passed(
				fmt.Sprintf("three transfers queued behind one resource slot, ran one at a time (peak %d), and all completed", stats.PeakActive),
				"apptransfer.Queue.Stats + domainsftp.LocalResourceKey")
		}
		return t.expired("the limited transfers")
	}
	return selftest.Failed(fmt.Errorf("unexpected limit phase %d", t.limitPhase))
}

func (t *selftest31) restartStep(*ui.Context) selftest.Outcome {
	now := time.Now()
	repository := &selftest31Repo{tasks: []domainsftp.Task{
		{
			ID: "restart-live", Kind: domainsftp.OpCopy, Status: domainsftp.TaskTransferring,
			DisplayName: "live.bin", BytesDone: 4096, BytesTotal: 8192,
			Speed: 2048, FilesDone: 1, FilesTotal: 2,
			CreatedAt: now, UpdatedAt: now, Epoch: 4,
		},
		{
			ID: "restart-done", Kind: domainsftp.OpCopy, Status: domainsftp.TaskCompleted,
			DisplayName: "done.bin", BytesDone: 10, BytesTotal: 10,
			FilesDone: 1, FilesTotal: 1, CreatedAt: now, UpdatedAt: now,
		},
	}}
	restarted := apptransfer.NewQueue(t.service,
		apptransfer.WithQueueConcurrency(1),
		apptransfer.WithQueueRepository(repository),
	)
	if err := restarted.Start(context.Background()); err != nil {
		return selftest.Failed(fmt.Errorf("restart the queue: %v", err))
	}
	defer restarted.Shutdown()

	live, ok := restarted.Task("restart-live")
	if !ok {
		return selftest.Failed(fmt.Errorf("the unfinished row was dropped on restart"))
	}
	if live.Status != domainsftp.TaskInterrupted || !live.ReconnectRequired || live.Speed != 0 {
		return selftest.Failed(fmt.Errorf("restart left the unfinished row status=%s reconnect=%v speed=%d",
			live.Status, live.ReconnectRequired, live.Speed))
	}
	done, ok := restarted.Task("restart-done")
	if !ok || done.Status != domainsftp.TaskCompleted {
		return selftest.Failed(fmt.Errorf("restart lost or changed the completed row: %+v", done))
	}
	return selftest.Passed(
		"a transferring row from the saved history restarted as interrupted with reconnect required and no speed, while a completed row survived",
		"apptransfer.Queue + domainsftp.DowngradeInterrupted")
}

func (t *selftest31) busyStep(*ui.Context) selftest.Outcome {
	switch t.busyPhase {
	case 0:
		t.deadline = time.Now().Add(selftest31Wait)
		t.state.EnsureStarted()
		t.state.OpenLocal(transferstate.Right)
		t.busyRelease = make(chan struct{})
		t.busyBlock = &selftest31BlockedFS{
			FileSystem: t.files,
			started:    make(chan struct{}),
			release:    t.busyRelease,
		}
		id, err := t.queue.Enqueue(apptransfer.TaskSpec{
			Kind:        domainsftp.OpCopy,
			Source:      t.busyBlock,
			Dest:        t.files,
			Sources:     []string{filepath.Join(t.source, "big.bin")},
			DestDir:     t.outs[9],
			DisplayName: "big.bin",
			SourceLabel: "local",
			DestLabel:   "local",
		})
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the blocking transfer: %v", err))
		}
		t.busyBlockID = id
		t.busyPhase = 1
		return selftest.Pending
	case 1:
		// No View is rendering this state during the step, so apply the
		// listings its goroutines posted before reading the Tabs.
		t.state.Flush()
		left, right := t.state.Tab(transferstate.Left), t.state.Tab(transferstate.Right)
		// A local Tab receives its filesystem on the UI thread after the
		// asynchronous open, so navigate only once both Tabs can list.
		if !t.busyNavigated && left != nil && right != nil && left.FS != nil && right.FS != nil {
			t.state.Navigate(transferstate.Left, t.source)
			t.state.Navigate(transferstate.Right, t.outs[10])
			t.busyNavigated = true
			return selftest.Pending
		}
		if left == nil || right == nil || left.FS == nil || right.FS == nil ||
			left.Loading || right.Loading || left.Path != t.source || right.Path != t.outs[10] {
			return t.expired("both panes to finish loading before a second Transfer")
		}
		var entry domainsftp.Entry
		found := false
		for _, candidate := range left.Entries {
			if candidate.Name == "small.txt" {
				entry, found = candidate, true
				break
			}
		}
		if !found {
			return t.expired("the local source listing to show small.txt")
		}
		select {
		case <-t.busyBlock.started:
		default:
			return t.expired("the first transfer to reach its source file")
		}
		t.state.Click(transferstate.Left, entry, 0, false)
		before := map[domainsftp.TaskID]bool{}
		for _, task := range t.queue.Tasks() {
			before[task.ID] = true
		}
		t.state.Transfer(transferstate.Left)
		var added []domainsftp.Task
		for _, task := range t.queue.Tasks() {
			if !before[task.ID] {
				added = append(added, task)
			}
		}
		if len(added) != 1 {
			return selftest.Failed(fmt.Errorf("a Transfer pressed while busy added %d queue rows, want 1: %+v",
				len(added), t.queue.Tasks()))
		}
		stats := t.queue.Stats()
		if stats.Active != 1 || stats.Queued < 1 {
			return selftest.Failed(fmt.Errorf("while busy the queue reported active=%d queued=%d, want one of each",
				stats.Active, stats.Queued))
		}
		t.busySecondID = added[0].ID
		t.state.OpenCenter()
		listed := false
		for _, task := range t.state.QueueTasks() {
			if task.ID == t.busySecondID {
				listed = true
			}
		}
		t.state.CloseCenter()
		if !listed {
			return selftest.Failed(fmt.Errorf("the queued second Transfer is missing from the center rows"))
		}
		close(t.busyRelease)
		t.busyPhase = 2
		return selftest.Pending
	case 2:
		for _, id := range []domainsftp.TaskID{t.busyBlockID, t.busySecondID} {
			task, ok := t.task(id)
			if !ok {
				return selftest.Failed(fmt.Errorf("a transfer enqueued while busy disappeared"))
			}
			if task.Status == domainsftp.TaskFailed || task.Status == domainsftp.TaskCancelled {
				return selftest.Failed(fmt.Errorf("a transfer enqueued while busy ended %s: %v", task.Status, task.Err))
			}
			if task.Status != domainsftp.TaskCompleted {
				return t.expired(fmt.Sprintf("both transfers enqueued while busy to complete (is %s)", task.Status))
			}
		}
		return selftest.Passed(
			"a second Transfer pressed while the first held the resource slot was enqueued, appeared in the center, and both copied",
			"transferstate.State + apptransfer.Queue")
	}
	return selftest.Failed(fmt.Errorf("unexpected busy phase %d", t.busyPhase))
}

func (t *selftest31) failStep(*ui.Context) selftest.Outcome {
	switch t.failPhase {
	case 0:
		t.deadline = time.Now().Add(selftest31Wait)
		id, err := t.queue.Enqueue(apptransfer.TaskSpec{
			Kind:        domainsftp.OpCopy,
			Source:      selftest31DeadFS{FileSystem: t.files},
			Dest:        t.files,
			Sources:     []string{filepath.Join(t.source, "small.txt")},
			DestDir:     t.outs[11],
			DisplayName: "small.txt",
			SourceLabel: "local",
			DestLabel:   "local",
		})
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the dead-transport transfer: %v", err))
		}
		t.failID = id
		t.failPhase = 1
		return selftest.Pending
	case 1:
		task, ok := t.task(t.failID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the dead-transport transfer disappeared"))
		}
		if task.Status != domainsftp.TaskFailed {
			return t.expired(fmt.Sprintf("the dead-transport transfer to fail (is %s)", task.Status))
		}
		if task.Error != string(apptransfer.KindConnection) {
			return selftest.Failed(fmt.Errorf("failure code = %q, want %q", task.Error, apptransfer.KindConnection))
		}
		if !task.ReconnectRequired {
			return selftest.Failed(fmt.Errorf("the lost transport did not mark the row reconnect-required"))
		}
		t.failEpoch = task.Epoch
		if err := t.queue.Retry(t.failID); !errors.Is(err, domainsftp.ErrTaskUnavailable) {
			return selftest.Failed(fmt.Errorf("Retry without a transport = %v, want ErrTaskUnavailable", err))
		}
		again, _ := t.task(t.failID)
		if again.Status != domainsftp.TaskFailed || again.Epoch != t.failEpoch {
			return selftest.Failed(fmt.Errorf("Retry replayed the dead transport: %+v", again))
		}
		return selftest.Passed(
			"a lost transport settled as connection with reconnect required, and Retry refused to replay the dead stream",
			"apptransfer.Queue + domainsftp.ErrTaskUnavailable")
	}
	return selftest.Failed(fmt.Errorf("unexpected fail phase %d", t.failPhase))
}

func (t *selftest31) viewStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	t.state.CloseCenter()
	tester := ui.NewTester(transferui.View(t.state), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	if err := tester.Click(m.SftpTransferCenterButton()); err != nil {
		return selftest.Failed(fmt.Errorf("the Transfers button has no accessible label: %v", err))
	}
	tester.Frame()
	if !t.state.CenterOpen() {
		return selftest.Failed(fmt.Errorf("the header button did not open the Transfer center"))
	}
	// The modal suppresses the window shortcut that opened the center, so the
	// overlay must honor the effective toggle chord itself.
	chordMods := ui.Ctrl
	if runtime.GOOS == "darwin" {
		chordMods = ui.Super
	}
	tester.Key(chordMods|ui.Shift, ui.KeyT)
	if t.state.CenterOpen() {
		return selftest.Failed(fmt.Errorf("the configured transfer-center chord did not close the center"))
	}
	t.state.OpenCenter()
	tester.Frame()
	// The panel is centered with a 24 DIP top margin, so (12, 12) is backdrop.
	tester.ClickAt(12, 12)
	if t.state.CenterOpen() {
		return selftest.Failed(fmt.Errorf("a click on the backdrop did not close the center"))
	}
	t.state.OpenCenter()
	tester.Frame()
	for _, label := range []string{
		m.SftpTransferCenter(), m.SftpTransferStatusCompleted(),
		m.SftpTransferStatusFailed(), m.SftpTransferStatusCancelled(),
		m.SftpTransferRetry(),
	} {
		rect, ok := tester.Find(label)
		if !ok || rect.W <= 0 || rect.H <= 0 {
			return selftest.Failed(fmt.Errorf("the 1100x720 center does not show %q", label))
		}
		if rect.X < 0 || rect.Y < 0 || rect.X+rect.W > 1100 || rect.Y+rect.H > 720 {
			return selftest.Failed(fmt.Errorf("the 1100x720 center clips %q: %+v", label, rect))
		}
	}
	if !tester.HasText(m.SftpErrorConnection()) || !tester.HasText(m.SftpTransferReconnectRequired()) {
		return selftest.Failed(fmt.Errorf("the failed-transport row does not explain the reconnect: %v", tester.Texts()))
	}
	if !tester.HasText("big.bin") || !tester.HasText("small.txt") {
		return selftest.Failed(fmt.Errorf("the center does not list the completed transfers: %v", tester.Texts()))
	}

	zh := locales.In("zh-CN")
	small := ui.NewTester(transferui.View(t.state), 720, 480)
	small.SetLocale("zh-CN")
	small.Frame()
	for _, label := range []string{zh.SftpTransferCenter(), zh.SftpTransferStatusCompleted(), zh.SftpTransferStatusFailed()} {
		rect, ok := small.Find(label)
		if !ok || rect.W <= 0 || rect.H <= 0 {
			return selftest.Failed(fmt.Errorf("the 720x480 zh-CN center does not show %q", label))
		}
		if rect.X < 0 || rect.Y < 0 || rect.X+rect.W > 720 || rect.Y+rect.H > 480 {
			return selftest.Failed(fmt.Errorf("the 720x480 zh-CN center clips %q: %+v", label, rect))
		}
	}

	if t.cleanup != nil {
		t.cleanup()
		t.cleanup = nil
	}
	t.finished = true
	return selftest.Passed(
		"the center opened, closed on the configured chord and a backdrop click, listed completed, failed, cancelled, and reconnect rows, and fit 1100x720 en and 720x480 zh-CN without clipping",
		"transferui.View + transferstate.State + apptransfer.Queue")
}
