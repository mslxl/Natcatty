//go:build selftest

package app

// Ticket 32 self-test. Press F11, then 3, then 2. The check proves pause,
// resume, and directory checkpoints through the real local adapter, queue,
// persisted history, and surface state:
//
//   - Pausing a directory Transfer records a bounded checkpoint whose
//     completed prefix matches the files already copied, and honest paused
//     progress excludes the file that was interrupted.
//   - Resuming the paused Transfer skips every completed file (it is not
//     opened again) and completes the remaining files.
//   - The pause and cancel latches stay consistent: a second Pause is a
//     no-op, Resume while pausing is refused, Cancel wins over Pause, and
//     cancelling a paused row clears the checkpoint and pause latch.
//   - The checkpoint limits and the symlink-depth limit are the production
//     values and the traversal budget rejects a directory or entry overrun.
//   - After a queue restart the row is interrupted with reconnect required,
//     keeps its checkpoint, and reconnecting the Host in the SFTP pane
//     reattaches it through the production State.OpenRemote path; Resume then
//     continues it without re-copying the completed prefix.
//
// The check drives the same Queue and State controls the app wires. Every
// wait polls a real task state or the persisted repository with a hard
// deadline; there is no fixed sleep.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-32.json.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/selftest"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

const selftest32Ticket = 32

const selftest32Wait = 30 * time.Second

type selftest32 struct {
	workdir  string
	service  *apptransfer.Service
	files    apptransfer.FileSystem
	repo     *selftest32Repo
	queue    *apptransfer.Queue
	state    *transferstate.State
	cleanup  func()
	deadline time.Time

	// pause and resume share one task and one gate.
	pausePhase  int
	pauseID     domainsftp.TaskID
	pauseGate   *selftest32Gate
	resumePhase int

	latchPhase   int
	latchID      domainsftp.TaskID
	latchGate    *selftest32Gate
	latch2ID     domainsftp.TaskID
	latch2Gate   *selftest32Gate
	limitsPhase  int
	restartPhase int
	restartID    domainsftp.TaskID
	restartGate  *selftest32Gate
	restartDest  *selftest32DeadStaging
	restartSpec  apptransfer.TaskSpec
	foreignStage string
	finished     bool
}

func (t *selftest32) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest32Ticket,
		Name:   "Pause, resume, and directory checkpoints",
		Steps: []selftest.Step{
			{Name: "pause-records-a-directory-checkpoint", Run: t.pauseStep},
			{Name: "resume-skips-completed-files", Run: t.resumeStep},
			{Name: "pause-and-cancel-latches", Run: t.latchStep},
			{Name: "enforce-checkpoint-limits-and-symlink-depth", Run: t.limitsStep},
			{Name: "host-reconnect-resumes-after-restart", Run: t.restartStep},
		},
	}
}

// selftest32Repo is the in-memory form of the app's JSON queue repository, so
// the restart step exercises the production load-and-downgrade path.
type selftest32Repo struct {
	mu    sync.Mutex
	tasks []domainsftp.Task
}

func (r *selftest32Repo) LoadTasks(context.Context) ([]domainsftp.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domainsftp.Task, len(r.tasks))
	copy(out, r.tasks)
	return out, nil
}

func (r *selftest32Repo) SaveTasks(_ context.Context, tasks []domainsftp.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append([]domainsftp.Task(nil), tasks...)
	return nil
}

func (r *selftest32Repo) task(id domainsftp.TaskID) (domainsftp.Task, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, task := range r.tasks {
		if task.ID == id {
			return task, true
		}
	}
	return domainsftp.Task{}, false
}

// selftest32Remote names the restart fixture's gated filesystem as a saved
// Host's connection, so the surface's reconnect path treats it as live.
type selftest32Remote struct {
	apptransfer.FileSystem
}

func (selftest32Remote) Backing() string { return "remote:loopback" }

// selftest32Opener opens local panes and reconnects the "loopback" Host over
// the restart fixture's gated filesystem. Every handle wraps the same gate,
// so the Open counts include the resumed attempt and prove the checkpointed
// prefix was skipped.
type selftest32Opener struct {
	local  *adaptersftp.Local
	remote apptransfer.FileSystem
}

func (o selftest32Opener) OpenLocal(ctx context.Context) (apptransfer.FileSystem, error) {
	return o.local.OpenLocal(ctx)
}

func (o selftest32Opener) OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error) {
	if o.remote == nil {
		return nil, fmt.Errorf("the ticket 32 self-test has no remote host")
	}
	return selftest32Remote{FileSystem: o.remote}, nil
}

type selftest32Bindings struct{}

func (selftest32Bindings) Load(context.Context) (appkeybindings.State, error) {
	return appkeybindings.State{}, nil
}

func (selftest32Bindings) Save(context.Context, appkeybindings.State) error { return nil }

// selftest32Gate wraps the local source. It counts every Open and holds the
// gate path until release. With hold set, the first Open ignores context
// cancellation until the step opens hold as well, so the row stays pausing
// long enough to exercise the latch rules deterministically.
type selftest32Gate struct {
	apptransfer.FileSystem
	path    string
	entered chan struct{}
	release chan struct{}
	hold    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	opens   map[string]int
	// burst, when positive, yields this many bytes before the release gate,
	// so the destination holds a partially written staging file while the row
	// is paused. partial closes once that burst was read.
	burst       int
	partial     chan struct{}
	partialOnce sync.Once
}

func newSelftest32Gate(files apptransfer.FileSystem, path string, hold bool) *selftest32Gate {
	gate := &selftest32Gate{
		FileSystem: files,
		path:       path,
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
		opens:      map[string]int{},
	}
	if hold {
		gate.hold = make(chan struct{})
	}
	return gate
}

// newSelftest32BurstGate leaves a partial staging file behind: the first read
// of the gate path yields burst bytes and then waits for the release gate.
func newSelftest32BurstGate(files apptransfer.FileSystem, path string, burst int) *selftest32Gate {
	gate := newSelftest32Gate(files, path, false)
	gate.burst = burst
	gate.partial = make(chan struct{})
	return gate
}

func (g *selftest32Gate) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	g.mu.Lock()
	g.opens[path]++
	g.mu.Unlock()
	if filepath.Clean(path) == filepath.Clean(g.path) {
		g.once.Do(func() { close(g.entered) })
		if g.burst > 0 {
			reader, err := g.FileSystem.Open(ctx, path)
			if err != nil {
				return nil, err
			}
			return &selftest32BurstReader{
				inner:       reader,
				budget:      g.burst,
				partial:     g.partial,
				partialOnce: &g.partialOnce,
				release:     g.release,
				ctx:         ctx,
			}, nil
		}
		if g.hold != nil {
			select {
			case <-g.release:
			case <-g.hold:
			case <-ctx.Done():
				// Stay in the read until the step ends the hold. This is the
				// observable condition for a row that is still pausing.
				select {
				case <-g.release:
				case <-g.hold:
				}
			}
		} else {
			select {
			case <-g.release:
			case <-ctx.Done():
			}
		}
	}
	return g.FileSystem.Open(ctx, path)
}

func (g *selftest32Gate) openCount(path string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.opens[path]
}

func (g *selftest32Gate) opened() bool {
	select {
	case <-g.entered:
		return true
	default:
		return false
	}
}

func (g *selftest32Gate) partialReached() bool {
	select {
	case <-g.partial:
		return true
	default:
		return false
	}
}

// selftest32BurstReader yields one bounded chunk, then waits for the release
// gate or the copy's cancellation.
type selftest32BurstReader struct {
	inner       io.ReadCloser
	budget      int
	partial     chan struct{}
	partialOnce *sync.Once
	release     chan struct{}
	ctx         context.Context
}

func (r *selftest32BurstReader) Read(p []byte) (int, error) {
	if r.budget > 0 {
		if len(p) > r.budget {
			p = p[:r.budget]
		}
		n, err := r.inner.Read(p)
		r.budget -= n
		if r.budget <= 0 {
			r.partialOnce.Do(func() { close(r.partial) })
		}
		return n, err
	}
	select {
	case <-r.release:
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	}
	return r.inner.Read(p)
}

func (r *selftest32BurstReader) Close() error { return r.inner.Close() }

// selftest32DeadStaging models the killed process's missing cleanup. The pause
// that simulates the kill asks the copy to remove its staging file; with dead
// set, that removal is a no-op, so the partial file the dead process would
// have left is still in the destination when the resumed process starts.
// Process two writes through the real filesystem, not this wrapper.
type selftest32DeadStaging struct {
	apptransfer.FileSystem
	dead atomic.Bool
}

func (d *selftest32DeadStaging) Remove(ctx context.Context, path string) error {
	if d.dead.Load() && apptransfer.IsStagingName(filepath.Base(path)) {
		return nil
	}
	return d.FileSystem.Remove(ctx, path)
}

func (t *selftest32) setup() error {
	workdir, err := os.MkdirTemp("", "natcatty-selftest-32-")
	if err != nil {
		return err
	}
	t.workdir = workdir
	for _, name := range []string{
		"pause-source", "out-pause",
		"latch-source", "out-latch",
		"latch2-source", "out-latch2",
		"restart-source", "out-restart",
	} {
		path := filepath.Join(workdir, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		switch name {
		case "restart-source":
			// The restart step needs a file large enough to be in flight with a
			// partially written staging file at the simulated kill.
			if err := writeSelftest32RestartTree(path); err != nil {
				return err
			}
		case "pause-source", "latch-source", "latch2-source":
			if err := writeSelftest32Tree(path); err != nil {
				return err
			}
		}
	}

	t.repo = &selftest32Repo{}
	local := adaptersftp.NewLocal()
	files, err := local.OpenLocal(context.Background())
	if err != nil {
		return err
	}
	t.files = files
	// The restart step's source bursts one 32 KiB chunk and then holds, so a
	// staging file is in flight when the process is killed; the destination
	// wrapper suppresses the dead process's staging cleanup.
	t.restartGate = newSelftest32BurstGate(files,
		filepath.Join(workdir, "restart-source", "c.txt"), 32*1024)
	t.restartDest = &selftest32DeadStaging{FileSystem: files}
	opener := selftest32Opener{local: local, remote: t.restartGate}
	t.service = apptransfer.NewService(opener, opener)
	t.queue = apptransfer.NewQueue(t.service,
		apptransfer.WithQueueConcurrency(1),
		apptransfer.WithQueueRepository(t.repo),
		apptransfer.WithQueueRetryDelay(0),
	)
	if err := t.queue.Start(context.Background()); err != nil {
		_ = files.Close()
		return err
	}
	keybindings := appkeybindings.NewService(selftest32Bindings{}, appkeybindings.WithGOOS("windows"))
	if err := keybindings.Load(context.Background()); err != nil {
		_ = files.Close()
		return err
	}
	t.state = transferstate.NewState(t.service,
		transferstate.WithQueue(t.queue),
		transferstate.WithKeybindings(keybindings),
	)
	t.cleanup = func() {
		if t.queue != nil {
			t.queue.Shutdown()
		}
		if t.state != nil {
			t.state.Shutdown()
		}
		if t.files != nil {
			_ = t.files.Close()
		}
		_ = os.RemoveAll(t.workdir)
	}
	return nil
}

// writeSelftest32Tree creates four files whose names sort in copy order. Each
// is small: the gate, not a size-based race, makes the pause deterministic.
func writeSelftest32Tree(dir string) error {
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name),
			[]byte("payload-"+name), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeSelftest32RestartTree gives the restart step a third file large enough
// to still be streaming when the simulated kill happens; its first 32 KiB
// reach the staging file before the gate holds the read.
func writeSelftest32RestartTree(dir string) error {
	files := map[string]string{
		"a.txt": "payload-a.txt",
		"b.txt": "payload-b.txt",
		"c.txt": strings.Repeat("c", 128*1024),
		"d.txt": "payload-d.txt",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (t *selftest32) directorySpec(source apptransfer.FileSystem, sourcePath, destDir string) apptransfer.TaskSpec {
	return apptransfer.TaskSpec{
		Kind:        domainsftp.OpCopy,
		Source:      source,
		Dest:        t.files,
		Sources:     []string{sourcePath},
		DestDir:     destDir,
		DisplayName: filepath.Base(sourcePath),
		Conflict:    apptransfer.ConflictReplace,
		SourceLabel: "local",
		DestLabel:   "local",
	}
}

func (t *selftest32) task(id domainsftp.TaskID) (domainsftp.Task, bool) {
	return t.queue.Task(id)
}

func (t *selftest32) expired(what string) selftest.Outcome {
	if time.Now().After(t.deadline) {
		return selftest.Failed(fmt.Errorf("%s did not settle within %s", what, selftest32Wait))
	}
	return selftest.Pending
}

func (t *selftest32) pauseStep(*ui.Context) selftest.Outcome {
	if t.finished {
		*t = selftest32{}
	}
	switch t.pausePhase {
	case 0:
		if err := t.setup(); err != nil {
			return selftest.Failed(err)
		}
		t.deadline = time.Now().Add(selftest32Wait)
		source := filepath.Join(t.workdir, "pause-source")
		t.pauseGate = newSelftest32Gate(t.files, filepath.Join(source, "c.txt"), false)
		id, err := t.queue.Enqueue(t.directorySpec(t.pauseGate, source, filepath.Join(t.workdir, "out-pause")))
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the directory transfer: %v", err))
		}
		t.pauseID = id
		t.pausePhase = 1
		return selftest.Pending
	case 1:
		if !t.pauseGate.opened() {
			if task, ok := t.task(t.pauseID); ok && (task.Status == domainsftp.TaskFailed ||
				task.Status == domainsftp.TaskCancelled) {
				return selftest.Failed(fmt.Errorf("the directory transfer ended %s: %v", task.Status, task.Err))
			}
			return t.expired("the copy to reach its third file")
		}
		t.state.PauseTransfer(t.pauseID)
		t.pausePhase = 2
		return selftest.Pending
	case 2:
		task, ok := t.task(t.pauseID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the pausing directory transfer disappeared"))
		}
		if task.Status == domainsftp.TaskFailed || task.Status == domainsftp.TaskCancelled {
			return selftest.Failed(fmt.Errorf("the paused transfer ended %s: %v", task.Status, task.Err))
		}
		if task.Status != domainsftp.TaskPaused {
			return t.expired("the directory transfer to pause")
		}
		close(t.pauseGate.release)
		checkpoint := task.DirectoryResumeCheckpoint
		if checkpoint == nil || !checkpoint.Valid() {
			return selftest.Failed(fmt.Errorf("the paused row has no valid directory checkpoint: %+v", checkpoint))
		}
		if checkpoint.CompletedEntries != 3 {
			return selftest.Failed(fmt.Errorf("checkpoint covers %d entries, want the root and two files",
				checkpoint.CompletedEntries))
		}
		if task.FilesDone != 3 || task.FilesTotal != 5 {
			return selftest.Failed(fmt.Errorf("paused progress = %d/%d files, want 3/5", task.FilesDone, task.FilesTotal))
		}
		if task.BytesDone != 26 || task.CheckpointBytesDone != 26 {
			return selftest.Failed(fmt.Errorf("paused bytes = %d (checkpoint %d), want 26 honest bytes",
				task.BytesDone, task.CheckpointBytesDone))
		}
		saved, ok := t.repo.task(t.pauseID)
		if !ok || saved.DirectoryResumeCheckpoint == nil ||
			saved.DirectoryResumeCheckpoint.CompletedEntries != 3 {
			return selftest.Failed(fmt.Errorf("the repository did not save the checkpoint: %+v", saved))
		}
		wantTarget := filepath.Join(t.workdir, "out-pause", "pause-source")
		if saved.Targets[filepath.Join(t.workdir, "pause-source")] != wantTarget {
			return selftest.Failed(fmt.Errorf("the repository pinned targets = %v, want %q", saved.Targets, wantTarget))
		}
		return selftest.Passed(
			"pausing after two files saved a valid checkpoint covering the root and two files, with honest 26-byte progress, both in memory and in the repository",
			"apptransfer.Queue + domainsftp.DirectoryResumeCheckpoint")
	}
	return selftest.Failed(fmt.Errorf("unexpected pause phase %d", t.pausePhase))
}

func (t *selftest32) resumeStep(*ui.Context) selftest.Outcome {
	switch t.resumePhase {
	case 0:
		if t.pausePhase != 2 {
			return selftest.Failed(fmt.Errorf("the resume step started before a checkpoint was paused"))
		}
		t.deadline = time.Now().Add(selftest32Wait)
		t.state.ResumeTransfer(t.pauseID)
		t.resumePhase = 1
		return selftest.Pending
	case 1:
		task, ok := t.task(t.pauseID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the resumed directory transfer disappeared"))
		}
		if task.Status == domainsftp.TaskFailed || task.Status == domainsftp.TaskCancelled {
			return selftest.Failed(fmt.Errorf("the resumed transfer ended %s: %v", task.Status, task.Err))
		}
		if task.Status != domainsftp.TaskCompleted {
			return t.expired("the resumed directory transfer")
		}
		source := filepath.Join(t.workdir, "pause-source")
		for name, want := range map[string]int{
			filepath.Join(source, "a.txt"): 1,
			filepath.Join(source, "b.txt"): 1,
			filepath.Join(source, "c.txt"): 2,
			filepath.Join(source, "d.txt"): 1,
		} {
			if got := t.pauseGate.openCount(name); got != want {
				return selftest.Failed(fmt.Errorf("%s opens = %d, want %d",
					filepath.Base(name), got, want))
			}
		}
		dest := filepath.Join(t.workdir, "out-pause", "pause-source")
		for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
			data, err := os.ReadFile(filepath.Join(dest, name))
			if err != nil {
				return selftest.Failed(fmt.Errorf("read %s after resume: %v", name, err))
			}
			if string(data) != "payload-"+name {
				return selftest.Failed(fmt.Errorf("%s content = %q", name, data))
			}
		}
		if task.DirectoryResumeCheckpoint != nil {
			return selftest.Failed(fmt.Errorf("the completed row kept its checkpoint: %+v", task.DirectoryResumeCheckpoint))
		}
		return selftest.Passed(
			"Resume reopened only c.txt (plus d.txt) and never reopened the two checkpointed files; all four destinations match and the completed row dropped its checkpoint",
			"transferstate.State + apptransfer.Queue")
	}
	return selftest.Failed(fmt.Errorf("unexpected resume phase %d", t.resumePhase))
}

func (t *selftest32) latchStep(*ui.Context) selftest.Outcome {
	switch t.latchPhase {
	case 0:
		t.deadline = time.Now().Add(selftest32Wait)
		source := filepath.Join(t.workdir, "latch-source")
		t.latchGate = newSelftest32Gate(t.files, filepath.Join(source, "c.txt"), true)
		id, err := t.queue.Enqueue(t.directorySpec(t.latchGate, source, filepath.Join(t.workdir, "out-latch")))
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the latch transfer: %v", err))
		}
		t.latchID = id
		t.latchPhase = 1
		return selftest.Pending
	case 1:
		if !t.latchGate.opened() {
			return t.expired("the latch copy to reach its third file")
		}
		t.state.PauseTransfer(t.latchID)
		t.latchPhase = 2
		return selftest.Pending
	case 2:
		task, ok := t.task(t.latchID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the pausing transfer disappeared"))
		}
		if task.Status != domainsftp.TaskPausing {
			if task.Status == domainsftp.TaskFailed || task.Status == domainsftp.TaskCancelled {
				return selftest.Failed(fmt.Errorf("the latch transfer ended %s: %v", task.Status, task.Err))
			}
			return t.expired("the transfer to report pausing")
		}
		t.state.PauseTransfer(t.latchID)
		if task, _ := t.task(t.latchID); task.Status != domainsftp.TaskPausing {
			return selftest.Failed(fmt.Errorf("the second Pause changed the status to %q", task.Status))
		}
		t.state.ResumeTransfer(t.latchID)
		if t.state.CenterError() != "transfer-not-retryable" {
			return selftest.Failed(fmt.Errorf("Resume while pausing reported %q, want transfer-not-retryable",
				t.state.CenterError()))
		}
		if task, _ := t.task(t.latchID); task.Status != domainsftp.TaskPausing {
			return selftest.Failed(fmt.Errorf("the refused Resume changed the status to %q", task.Status))
		}
		t.state.CancelTransfer(t.latchID)
		close(t.latchGate.release)
		close(t.latchGate.hold)
		t.latchPhase = 3
		return selftest.Pending
	case 3:
		after, ok := t.task(t.latchID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the cancelled transfer disappeared"))
		}
		if after.Status != domainsftp.TaskCancelled {
			return t.expired("Cancel to win while the row was pausing")
		}
		if after.PauseRequested || !after.CancelRequested {
			return selftest.Failed(fmt.Errorf("cancel latches = pause=%v cancel=%v",
				after.PauseRequested, after.CancelRequested))
		}
		if after.DirectoryResumeCheckpoint != nil || after.Targets != nil {
			return selftest.Failed(fmt.Errorf("Cancel kept resume state: %+v", after))
		}

		source := filepath.Join(t.workdir, "latch2-source")
		t.latch2Gate = newSelftest32Gate(t.files, filepath.Join(source, "c.txt"), false)
		id, err := t.queue.Enqueue(t.directorySpec(t.latch2Gate, source, filepath.Join(t.workdir, "out-latch2")))
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the paused-cancel transfer: %v", err))
		}
		t.latch2ID = id
		t.latchPhase = 4
		return selftest.Pending
	case 4:
		if !t.latch2Gate.opened() {
			return t.expired("the paused-cancel copy to reach its third file")
		}
		t.state.PauseTransfer(t.latch2ID)
		t.latchPhase = 5
		return selftest.Pending
	case 5:
		paused, ok := t.task(t.latch2ID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the second pausing transfer disappeared"))
		}
		if paused.Status != domainsftp.TaskPaused {
			if paused.Status == domainsftp.TaskFailed || paused.Status == domainsftp.TaskCancelled {
				return selftest.Failed(fmt.Errorf("the second transfer ended %s: %v", paused.Status, paused.Err))
			}
			return t.expired("the second transfer to pause")
		}
		if paused.DirectoryResumeCheckpoint == nil {
			return selftest.Failed(fmt.Errorf("the paused row has no checkpoint to clear"))
		}
		close(t.latch2Gate.release)
		t.state.CancelTransfer(t.latch2ID)
		t.latchPhase = 6
		return selftest.Pending
	case 6:
		after, ok := t.task(t.latch2ID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the cancelled paused transfer disappeared"))
		}
		if after.Status != domainsftp.TaskCancelled {
			return t.expired("Cancel on the paused row")
		}
		if after.PauseRequested {
			return selftest.Failed(fmt.Errorf("Cancel did not clear the pause latch on a paused row"))
		}
		if after.DirectoryResumeCheckpoint != nil {
			return selftest.Failed(fmt.Errorf("Cancel on a paused row kept the checkpoint"))
		}
		return selftest.Passed(
			"a held pause refused a second Pause and Resume, Cancel won and cleared the pause latch and resume state, and cancelling a paused row also cleared its checkpoint",
			"transferstate.State + domainsftp.TaskPauseRequested")
	}
	return selftest.Failed(fmt.Errorf("unexpected latch phase %d", t.latchPhase))
}

func (t *selftest32) limitsStep(*ui.Context) selftest.Outcome {
	if t.limitsPhase == 0 {
		t.deadline = time.Now().Add(selftest32Wait)
		t.limitsPhase = 1
	}
	if domainsftp.MaxFollowedSymlinkDepth != 32 {
		return selftest.Failed(fmt.Errorf("MaxFollowedSymlinkDepth = %d, want 32",
			domainsftp.MaxFollowedSymlinkDepth))
	}
	if domainsftp.MaxTraversalDirectories != 50_000 || domainsftp.MaxTraversalEntries != 200_000 {
		return selftest.Failed(fmt.Errorf("traversal limits = %d directories, %d entries",
			domainsftp.MaxTraversalDirectories, domainsftp.MaxTraversalEntries))
	}
	if apptransfer.MaxWalkDepth != domainsftp.MaxFollowedSymlinkDepth {
		return selftest.Failed(fmt.Errorf("copy depth %d does not match the follow limit %d",
			apptransfer.MaxWalkDepth, domainsftp.MaxFollowedSymlinkDepth))
	}

	ancestors := map[string]struct{}{}
	budget := domainsftp.NewDirectoryTraversalBudget(2, 10)
	for _, path := range []string{"/one", "/two"} {
		if _, err := budget.Visit(path, ancestors); err != nil {
			return selftest.Failed(fmt.Errorf("visit %s: %v", path, err))
		}
	}
	if _, err := budget.Visit("/three", ancestors); !errors.Is(err, domainsftp.ErrDirectoryTraversalDirectoryLimit) {
		return selftest.Failed(fmt.Errorf("the directory limit returned %v", err))
	}
	cycle := map[string]struct{}{}
	cycleBudget := domainsftp.NewDirectoryTraversalBudget(10, 10)
	if claimed, err := cycleBudget.Visit("/loop", cycle); err != nil || claimed != "/loop" {
		return selftest.Failed(fmt.Errorf("first loop visit = %q, %v", claimed, err))
	}
	if claimed, err := cycleBudget.Visit("/loop", cycle); err != nil || claimed != "" {
		return selftest.Failed(fmt.Errorf("a canonical cycle = %q, %v, want a silent skip", claimed, err))
	}
	if cycleBudget.VisitedDirectories != 1 {
		return selftest.Failed(fmt.Errorf("a cycle counted %d directories", cycleBudget.VisitedDirectories))
	}
	entries := domainsftp.NewDirectoryTraversalBudget(10, 3)
	if err := entries.Account(2); err != nil {
		return selftest.Failed(fmt.Errorf("account two entries: %v", err))
	}
	if err := entries.Account(2); !errors.Is(err, domainsftp.ErrDirectoryTraversalEntryLimit) {
		return selftest.Failed(fmt.Errorf("the entry limit returned %v", err))
	}
	for depth := 0; depth < 32; depth++ {
		if !domainsftp.ShouldFollowSymlinkDirectory(depth) {
			return selftest.Failed(fmt.Errorf("symlink depth %d was refused, want followed up to 31", depth))
		}
	}
	if domainsftp.ShouldFollowSymlinkDirectory(32) {
		return selftest.Failed(fmt.Errorf("symlink depth 32 was followed, want refused"))
	}
	return selftest.Passed(
		"the production checkpoint limits are 50,000 directories, 200,000 entries, and 32 symlink levels; the budget rejected one more directory or entry and skipped a canonical cycle",
		"domainsftp.DirectoryTraversalBudget + domainsftp.MaxFollowedSymlinkDepth")
}

func (t *selftest32) restartStep(*ui.Context) selftest.Outcome {
	switch t.restartPhase {
	case 0:
		t.deadline = time.Now().Add(selftest32Wait)
		source := filepath.Join(t.workdir, "restart-source")
		t.restartSpec = t.directorySpec(selftest32Remote{FileSystem: t.restartGate},
			source, filepath.Join(t.workdir, "out-restart"))
		t.restartSpec.SourceLabel = "Loopback"
		t.restartSpec.DestLabel = "local"
		t.restartSpec.Dest = t.restartDest
		id, err := t.queue.Enqueue(t.restartSpec)
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the restart transfer: %v", err))
		}
		t.restartID = id
		t.restartPhase = 1
		return selftest.Pending
	case 1:
		if !t.restartGate.partialReached() {
			return t.expired("the restart copy to leave a partial staging file")
		}
		// From here the process cannot clean up its staging file: the wrapper
		// ignores the removal the pause will request.
		t.restartDest.dead.Store(true)
		t.state.PauseTransfer(t.restartID)
		t.restartPhase = 2
		return selftest.Pending
	case 2:
		paused, ok := t.task(t.restartID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the pausing restart transfer disappeared"))
		}
		if paused.Status != domainsftp.TaskPaused {
			if paused.Status == domainsftp.TaskFailed || paused.Status == domainsftp.TaskCancelled {
				return selftest.Failed(fmt.Errorf("the restart transfer ended %s: %v", paused.Status, paused.Err))
			}
			return t.expired("the restart transfer to pause")
		}
		if paused.DirectoryResumeCheckpoint == nil ||
			paused.DirectoryResumeCheckpoint.CompletedEntries != 3 {
			return selftest.Failed(fmt.Errorf("the paused restart row has checkpoint %+v",
				paused.DirectoryResumeCheckpoint))
		}
		// The killed process left its partial staging file: the destination
		// wrapper ignored the cleanup request the pause issued.
		destRoot := filepath.Join(t.workdir, "out-restart")
		orphan := ""
		err := filepath.WalkDir(destRoot, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && apptransfer.IsStagingName(entry.Name()) &&
				strings.HasSuffix(entry.Name(), "-c.txt.part") {
				orphan = path
			}
			return nil
		})
		if err != nil {
			return selftest.Failed(err)
		}
		if orphan == "" {
			return selftest.Failed(fmt.Errorf(
				"the killed-process simulation left no partial staging file"))
		}
		// Another task's live staging file in the same destination root must
		// survive the resume.
		t.foreignStage = filepath.Join(destRoot, ".netcatty-upload-cafebabe-foreign.txt.part")
		if err := os.WriteFile(t.foreignStage, []byte("live-other"), 0o644); err != nil {
			return selftest.Failed(fmt.Errorf("seed another task's staging file: %v", err))
		}
		// Simulate the process restart: stop the queue without resuming.
		t.queue.Shutdown()
		restarted := apptransfer.NewQueue(t.service,
			apptransfer.WithQueueConcurrency(1),
			apptransfer.WithQueueRepository(t.repo),
			apptransfer.WithQueueRetryDelay(0),
		)
		if err := restarted.Start(context.Background()); err != nil {
			return selftest.Failed(fmt.Errorf("restart the queue: %v", err))
		}
		t.queue = restarted
		restored, ok := restarted.Task(t.restartID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the directory row vanished across the restart"))
		}
		if restored.Status != domainsftp.TaskInterrupted || !restored.ReconnectRequired {
			return selftest.Failed(fmt.Errorf("the restored row = %+v, want interrupted and reconnect-required", restored))
		}
		if restored.SourceHost != "loopback" {
			return selftest.Failed(fmt.Errorf("the restored row lost its Host identity: %+v", restored))
		}
		if restored.DirectoryResumeCheckpoint == nil ||
			restored.DirectoryResumeCheckpoint.CompletedEntries != 3 {
			return selftest.Failed(fmt.Errorf("the restart lost the checkpoint: %+v", restored.DirectoryResumeCheckpoint))
		}
		// Relaunch the surface around the restored queue, as the app does at
		// startup. The reconnect, not a direct Queue.Reattach, must bind the
		// new filesystems.
		t.state.Shutdown()
		t.state = transferstate.NewState(t.service,
			transferstate.WithQueue(restarted))
		// The prompt must stay truthful while the Host is not connected.
		t.state.ResumeTransfer(t.restartID)
		if code := t.state.CenterError(); code != "transfer-reconnect-required" {
			return selftest.Failed(fmt.Errorf("Resume before the Host reconnect reports %q, want transfer-reconnect-required", code))
		}
		if task, _ := restarted.Task(t.restartID); task.Status != domainsftp.TaskInterrupted {
			return selftest.Failed(fmt.Errorf("Resume before the Host reconnect moved the row to %q", task.Status))
		}
		t.restartPhase = 3
		return selftest.Pending
	case 3:
		// Reconnecting the Host in the SFTP pane is the product's reconnect
		// path; the surface binds the live filesystem to the restored row.
		t.state.Flush()
		t.state.OpenRemote(transferstate.Right, "loopback", "Loopback")
		t.restartPhase = 4
		return selftest.Pending
	case 4:
		t.state.Flush()
		reattached, ok := t.task(t.restartID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the restored restart row disappeared"))
		}
		if reattached.ReconnectRequired {
			if right := t.state.Tab(transferstate.Right); right != nil && right.Problem != nil {
				return selftest.Failed(fmt.Errorf("the Host reconnect failed: %v", right.Problem))
			}
			return t.expired("the Host reconnect to reattach the restored row")
		}
		if reattached.Status != domainsftp.TaskInterrupted {
			return selftest.Failed(fmt.Errorf("the Host reconnect auto-started the row: %+v", reattached))
		}
		close(t.restartGate.release)
		t.state.ResumeTransfer(t.restartID)
		if code := t.state.CenterError(); code != "" {
			return selftest.Failed(fmt.Errorf("Resume after the Host reconnect reports %q", code))
		}
		t.restartPhase = 5
		return selftest.Pending
	case 5:
		t.state.Flush()
		task, ok := t.task(t.restartID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the resumed restart row disappeared"))
		}
		if task.Status == domainsftp.TaskFailed || task.Status == domainsftp.TaskCancelled {
			return selftest.Failed(fmt.Errorf("the restart resume ended %s: %v", task.Status, task.Err))
		}
		if task.Status != domainsftp.TaskCompleted {
			return t.expired("the restart resume")
		}
		source := filepath.Join(t.workdir, "restart-source")
		if got := t.restartGate.openCount(filepath.Join(source, "a.txt")); got != 1 {
			return selftest.Failed(fmt.Errorf("a.txt opens = %d; the restart re-copied a checkpointed file", got))
		}
		if got := t.restartGate.openCount(filepath.Join(source, "b.txt")); got != 1 {
			return selftest.Failed(fmt.Errorf("b.txt opens = %d; the restart re-copied a checkpointed file", got))
		}
		dest := filepath.Join(t.workdir, "out-restart", "restart-source")
		for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
			if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
				return selftest.Failed(fmt.Errorf("%s missing after the restart resume: %v", name, err))
			}
		}
		// A resumed Transfer reuses the staging path its durable task identity
		// names and the promotion removes it. The killed process's partial
		// staging file must be gone, while another task's staging file in the
		// same root must survive untouched.
		foreignIntact := false
		err := filepath.WalkDir(filepath.Join(t.workdir, "out-restart"),
			func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() || !apptransfer.IsStagingName(entry.Name()) {
					return nil
				}
				if filepath.Clean(path) == filepath.Clean(t.foreignStage) {
					foreignIntact = true
					return nil
				}
				return fmt.Errorf("an interrupted staging file survived the restart resume: %s", path)
			})
		if err != nil {
			return selftest.Failed(err)
		}
		if !foreignIntact {
			return selftest.Failed(fmt.Errorf(
				"the resumed Transfer removed another task's staging file: %s", t.foreignStage))
		}
		if data, err := os.ReadFile(t.foreignStage); err != nil || string(data) != "live-other" {
			return selftest.Failed(fmt.Errorf(
				"another task's staging file changed to %q: %v", data, err))
		}
		if task.DirectoryResumeCheckpoint != nil {
			return selftest.Failed(fmt.Errorf("the completed restart row kept its checkpoint"))
		}
		if t.cleanup != nil {
			t.cleanup()
			t.cleanup = nil
		}
		t.finished = true
		return selftest.Passed(
			"a restarted queue kept the row interrupted with reconnect required and its checkpoint; reconnecting the Host in the SFTP pane reattached the row, and Resume finished it without reopening the two completed files",
			"transferstate.State.OpenRemote -> apptransfer.Queue.ReattachHost")
	}
	return selftest.Failed(fmt.Errorf("unexpected restart phase %d", t.restartPhase))
}
