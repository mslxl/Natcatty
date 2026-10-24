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
//     keeps its checkpoint, and Reattach plus Resume continues it without
//     re-copying the completed prefix.
//
// The check drives the same Queue and State controls the app wires. Every
// wait polls a real task state or the persisted repository with a hard
// deadline; there is no fixed sleep.
//
// Result: %TEMP%\natcatty-selftest-32.json.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
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
	restartSpec  apptransfer.TaskSpec
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
			{Name: "restart-resumes-after-reattach", Run: t.restartStep},
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

// selftest32Local is a local-only opener; any remote request says so.
type selftest32Local struct {
	*adaptersftp.Local
}

func (selftest32Local) OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error) {
	return nil, fmt.Errorf("the ticket 32 self-test has no remote host")
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

func (g *selftest32Gate) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	g.mu.Lock()
	g.opens[path]++
	g.mu.Unlock()
	if filepath.Clean(path) == filepath.Clean(g.path) {
		g.once.Do(func() { close(g.entered) })
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
		if name == "pause-source" || name == "latch-source" ||
			name == "latch2-source" || name == "restart-source" {
			if err := writeSelftest32Tree(path); err != nil {
				return err
			}
		}
	}

	t.repo = &selftest32Repo{}
	opener := selftest32Local{Local: adaptersftp.NewLocal()}
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
		t.restartGate = newSelftest32Gate(t.files, filepath.Join(source, "c.txt"), false)
		t.restartSpec = t.directorySpec(t.restartGate, source, filepath.Join(t.workdir, "out-restart"))
		id, err := t.queue.Enqueue(t.restartSpec)
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the restart transfer: %v", err))
		}
		t.restartID = id
		t.restartPhase = 1
		return selftest.Pending
	case 1:
		if !t.restartGate.opened() {
			return t.expired("the restart copy to reach its third file")
		}
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
		if restored.DirectoryResumeCheckpoint == nil ||
			restored.DirectoryResumeCheckpoint.CompletedEntries != 3 {
			return selftest.Failed(fmt.Errorf("the restart lost the checkpoint: %+v", restored.DirectoryResumeCheckpoint))
		}
		if err := restarted.Resume(t.restartID); !errors.Is(err, domainsftp.ErrTaskUnavailable) {
			return selftest.Failed(fmt.Errorf("Resume before Reattach = %v, want ErrTaskUnavailable", err))
		}
		if err := restarted.Reattach(t.restartID, t.restartSpec); err != nil {
			return selftest.Failed(fmt.Errorf("reattach the restored row: %v", err))
		}
		reattached, _ := restarted.Task(t.restartID)
		if reattached.ReconnectRequired || reattached.Status != domainsftp.TaskInterrupted {
			return selftest.Failed(fmt.Errorf("the reattached row = %+v", reattached))
		}
		close(t.restartGate.release)
		if err := restarted.Resume(t.restartID); err != nil {
			return selftest.Failed(fmt.Errorf("resume after reattach: %v", err))
		}
		t.restartPhase = 3
		return selftest.Pending
	case 3:
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
		if task.DirectoryResumeCheckpoint != nil {
			return selftest.Failed(fmt.Errorf("the completed restart row kept its checkpoint"))
		}
		if t.cleanup != nil {
			t.cleanup()
			t.cleanup = nil
		}
		t.finished = true
		return selftest.Passed(
			"a restarted queue kept the row interrupted with reconnect required and its checkpoint; Reattach plus Resume finished it without reopening the two completed files",
			"apptransfer.Queue.Reattach + domainsftp.DirectoryResumeCheckpoint")
	}
	return selftest.Failed(fmt.Errorf("unexpected restart phase %d", t.restartPhase))
}
