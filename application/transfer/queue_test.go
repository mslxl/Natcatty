package transfer

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// fakeQueueRepository records the saved snapshots and serves a fixed load.
type fakeQueueRepository struct {
	mu      sync.Mutex
	stored  []domainsftp.Task
	saved   []domainsftp.Task
	saves   int
	loadErr error
}

func (r *fakeQueueRepository) LoadTasks(context.Context) ([]domainsftp.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	out := make([]domainsftp.Task, len(r.stored))
	for index, task := range r.stored {
		out[index] = task.Clone()
	}
	return out, nil
}

func (r *fakeQueueRepository) SaveTasks(_ context.Context, tasks []domainsftp.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saves++
	r.saved = make([]domainsftp.Task, len(tasks))
	for index, task := range tasks {
		r.saved[index] = task.Clone()
	}
	return nil
}

func (r *fakeQueueRepository) saveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saves
}

func (r *fakeQueueRepository) savedTasks() []domainsftp.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domainsftp.Task, len(r.saved))
	copy(out, r.saved)
	return out
}

const queueWait = 5 * time.Second

func startTestQueue(t *testing.T, q *Queue) {
	t.Helper()
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("start queue: %v", err)
	}
	t.Cleanup(q.Shutdown)
}

// TestQueueStartReportsADamagedHistoryWithoutOverwritingIt proves a failed
// load leaves the saved file alone instead of replacing it with the empty
// in-memory result.
func TestQueueStartReportsADamagedHistoryWithoutOverwritingIt(t *testing.T) {
	repo := &fakeQueueRepository{loadErr: errors.New("damaged history")}
	q := NewQueue(NewService(nil, nil), WithQueueRepository(repo))
	t.Cleanup(q.Shutdown)
	if err := q.Start(context.Background()); err == nil {
		t.Fatal("a damaged history was accepted")
	}
	if saves := repo.saveCount(); saves != 0 {
		t.Fatalf("the damaged history was overwritten with %d save(s)", saves)
	}
}

func waitQueueTask(t *testing.T, q *Queue, id domainsftp.TaskID, want domainsftp.TaskStatus) domainsftp.Task {
	t.Helper()
	deadline := time.Now().Add(queueWait)
	for {
		task, ok := q.Task(id)
		if ok && task.Status == want {
			return task
		}
		if time.Now().After(deadline) {
			if ok {
				t.Fatalf("timed out after %s waiting for %q; task is %q (%+v)", queueWait, want, task.Status, task)
			}
			t.Fatalf("timed out after %s waiting for %q; task vanished", queueWait, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitQueueActive(t *testing.T, q *Queue, want int) {
	t.Helper()
	deadline := time.Now().Add(queueWait)
	for {
		if q.Stats().Active == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %d active workers; stats = %+v", queueWait, want, q.Stats())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestQueueRunsATransferAndReportsProgress(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "report.txt", strings.Repeat("x", 4096), 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/out")

	queue := NewQueue(NewService(nil, nil))
	startTestQueue(t, queue)
	var sawProgress bool
	queue.Observe(func(task domainsftp.Task) {
		if task.BytesDone > 0 && task.BytesTotal > 0 {
			sawProgress = true
		}
	})

	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/report.txt"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	task := waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if task.BytesTotal != 4096 || task.BytesDone != 4096 || task.FilesDone != 1 {
		t.Errorf("completed task = %+v", task)
	}
	if task.Percent() != 100 {
		t.Errorf("Percent = %d, want 100", task.Percent())
	}
	data, ok := dest.file("/out/report.txt")
	if !ok || len(data) != 4096 {
		t.Errorf("destination file = %d bytes, ok=%v", len(data), ok)
	}
	if !sawProgress {
		t.Error("no progress snapshot carried byte totals")
	}
}

func TestQueueEnforcesThePerResourceLimit(t *testing.T) {
	source := newFakeFS("source")
	dest := newFakeFS("dest")
	dest.addDir("/out")
	release := make(chan struct{})
	source.block = release
	for index, name := range []string{"a.bin", "b.bin", "c.bin", "d.bin"} {
		source.addFile("/", name, strings.Repeat(string(rune('a'+index)), 2048), 0o644)
	}

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(2))
	startTestQueue(t, queue)
	ids := make([]domainsftp.TaskID, 0, 4)
	for _, name := range []string{"a.bin", "b.bin", "c.bin", "d.bin"} {
		id, err := queue.Enqueue(TaskSpec{
			Kind: domainsftp.OpCopy, Source: source, Dest: dest,
			Sources: []string{"/" + name}, DestDir: "/out",
		})
		if err != nil {
			t.Fatalf("enqueue %s: %v", name, err)
		}
		ids = append(ids, id)
	}
	waitQueueActive(t, queue, 2)
	if stats := queue.Stats(); stats.Queued != 2 || stats.PeakActive != 2 {
		t.Errorf("stats = %+v, want 2 queued and peak 2", stats)
	}
	for _, key := range []domainsftp.ResourceKey{
		domainsftp.LocalResourceKey(), "fake:source", "fake:dest",
	} {
		if got := queue.Active(key); got > 2 {
			t.Errorf("resource %q has %d active workers, limit is 2", key, got)
		}
	}
	close(release)
	for _, id := range ids {
		waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	}
	if peak := queue.Stats().PeakActive; peak != 2 {
		t.Errorf("PeakActive = %d, want 2", peak)
	}
}

func TestQueueGivesIndependentResourcesTheirOwnSlots(t *testing.T) {
	firstSource := newFakeFS("first")
	secondSource := newFakeFS("second")
	firstSource.addFile("/", "a.bin", strings.Repeat("a", 2048), 0o644)
	secondSource.addFile("/", "b.bin", strings.Repeat("b", 2048), 0o644)
	release := make(chan struct{})
	firstSource.block = release
	secondSource.block = release
	dest := newFakeFS("dest")
	dest.addDir("/out")

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1))
	startTestQueue(t, queue)
	for index, source := range []FileSystem{firstSource, secondSource} {
		name := []string{"a.bin", "b.bin"}[index]
		_, err := queue.Enqueue(TaskSpec{
			Kind: domainsftp.OpCopy, Source: source, Dest: dest,
			Sources: []string{"/" + name}, DestDir: "/out",
			SourceKey: domainsftp.HostResourceKey("host" + name),
			DestKey:   domainsftp.HostResourceKey("host" + name),
		})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	waitQueueActive(t, queue, 2)
	close(release)
	deadline := time.Now().Add(queueWait)
	for queue.Stats().Active != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("workers did not settle: %+v", queue.Stats())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestQueuePauseAndResume(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "big.bin", strings.Repeat("z", 8192), 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/out")
	release := make(chan struct{})
	source.block = release

	queue := NewQueue(NewService(nil, nil))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/big.bin"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueActive(t, queue, 1)
	if err := queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused := waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	if paused.PauseRequested {
		// The latch stays set while paused; Resume clears it.
		if !paused.Status.CanResume() {
			t.Errorf("paused row cannot resume: %+v", paused)
		}
	}
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitQueueActive(t, queue, 1)
	close(release)
	task := waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if task.BytesDone != 8192 {
		t.Errorf("resumed task copied %d bytes, want 8192", task.BytesDone)
	}
}

func TestQueueCancelStopsTheTransfer(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "big.bin", strings.Repeat("z", 8192), 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/out")
	release := make(chan struct{})
	source.block = release

	queue := NewQueue(NewService(nil, nil))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/big.bin"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueActive(t, queue, 1)
	if err := queue.Cancel(id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	task := waitQueueTask(t, queue, id, domainsftp.TaskCancelled)
	if task.Err != nil || task.Error != "" {
		t.Errorf("cancelled task kept an error: %+v", task)
	}
	if dest.has("/out/big.bin") {
		t.Error("cancel left the destination file")
	}
}

func TestQueueCancelOfAQueuedTransferIsImmediate(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.bin", "a", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/out")
	release := make(chan struct{})
	source.block = release

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1))
	startTestQueue(t, queue)
	first, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/a.bin"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	second, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/a.bin"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueActive(t, queue, 1)
	if err := queue.Cancel(second); err != nil {
		t.Fatalf("cancel queued: %v", err)
	}
	waitQueueTask(t, queue, second, domainsftp.TaskCancelled)
	close(release)
	waitQueueTask(t, queue, first, domainsftp.TaskCompleted)
}

func TestQueueRetriesAFailedTransfer(t *testing.T) {
	source := newFakeFS("source")
	dest := newFakeFS("dest")
	dest.addDir("/out")

	queue := NewQueue(NewService(nil, nil))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/missing.txt"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	failed := waitQueueTask(t, queue, id, domainsftp.TaskFailed)
	if failed.Error != string(KindNotFound) {
		t.Errorf("failure code = %q, want %q", failed.Error, KindNotFound)
	}
	// The retry needs a source that exists now; the failed attempt proved
	// the queue retains the request.
	source.addFile("/", "missing.txt", "recovered", 0o644)
	if err := queue.Retry(id); err != nil {
		t.Fatalf("retry: %v", err)
	}
	waitQueueActive(t, queue, 1)
	task := waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if task.BytesDone != int64(len("recovered")) {
		t.Errorf("retried task copied %d bytes", task.BytesDone)
	}
	if data, ok := dest.file("/out/missing.txt"); !ok || string(data) != "recovered" {
		t.Errorf("retry destination = %q, ok=%v", data, ok)
	}
}

// flakyFS fails the first Open with a transient network message.
type flakyFS struct {
	*fakeFS
	mu       sync.Mutex
	failNext bool
}

func (f *flakyFS) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	f.mu.Lock()
	fail := f.failNext
	f.failNext = false
	f.mu.Unlock()
	if fail {
		return nil, errors.New("read: econnreset by peer")
	}
	return f.fakeFS.Open(ctx, path)
}

func TestQueueRetriesATransientFailureOnce(t *testing.T) {
	base := newFakeFS("flaky")
	base.addFile("/", "a.bin", "payload", 0o644)
	source := &flakyFS{fakeFS: base, failNext: true}
	dest := newFakeFS("dest")
	dest.addDir("/out")

	queue := NewQueue(NewService(nil, nil), WithQueueRetryDelay(time.Millisecond))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/a.bin"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	task := waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if task.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 automatic retry", task.Attempts)
	}
	if data, ok := dest.file("/out/a.bin"); !ok || string(data) != "payload" {
		t.Errorf("destination = %q, ok=%v", data, ok)
	}
}

func TestQueueRestartDowngradesUnfinishedTransfers(t *testing.T) {
	now := time.Now()
	repo := &fakeQueueRepository{stored: []domainsftp.Task{
		{ID: "queued", Status: domainsftp.TaskQueued, DisplayName: "one", CreatedAt: now.Add(-3 * time.Second)},
		{ID: "paused", Status: domainsftp.TaskPaused, DisplayName: "two", CreatedAt: now.Add(-2 * time.Second)},
		{ID: "done", Status: domainsftp.TaskCompleted, DisplayName: "three", CreatedAt: now.Add(-time.Second), FinishedAt: now},
	}}
	queue := NewQueue(NewService(nil, nil), WithQueueRepository(repo))
	startTestQueue(t, queue)

	for _, id := range []domainsftp.TaskID{"queued", "paused"} {
		task, ok := queue.Task(id)
		if !ok {
			t.Fatalf("%s disappeared", id)
		}
		if task.Status != domainsftp.TaskInterrupted || !task.ReconnectRequired {
			t.Errorf("%s = %+v, want interrupted and reconnect-required", id, task)
		}
	}
	if done, _ := queue.Task("done"); done.Status != domainsftp.TaskCompleted {
		t.Errorf("terminal row changed: %+v", done)
	}
	if err := queue.Resume("queued"); !errors.Is(err, domainsftp.ErrTaskUnavailable) {
		t.Errorf("Resume of a restored row = %v, want ErrTaskUnavailable", err)
	}
	if err := queue.Retry("paused"); !errors.Is(err, domainsftp.ErrTaskUnavailable) {
		t.Errorf("Retry of a restored row = %v, want ErrTaskUnavailable", err)
	}
	saved := repo.savedTasks()
	if len(saved) != 3 {
		t.Fatalf("saved %d rows, want 3", len(saved))
	}
	for _, task := range saved {
		if task.ID == "done" {
			continue
		}
		if task.Status != domainsftp.TaskInterrupted || !task.ReconnectRequired {
			t.Errorf("persisted %s = %+v", task.ID, task)
		}
	}
}

func TestQueuePrunesTerminalHistoryOnStart(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	stored := []domainsftp.Task{
		{ID: "unfinished", Status: domainsftp.TaskQueued, CreatedAt: now},
	}
	for index := 0; index < 230; index++ {
		stored = append(stored, domainsftp.Task{
			ID:         domainsftp.TaskID("t" + string(rune('a'+index%26)) + string(rune('a'+index/26))),
			Status:     domainsftp.TaskCompleted,
			CreatedAt:  now.Add(time.Duration(index) * time.Second),
			FinishedAt: now.Add(time.Duration(index) * time.Second),
		})
	}
	repo := &fakeQueueRepository{stored: stored}
	queue := NewQueue(NewService(nil, nil),
		WithQueueRepository(repo),
		WithQueueClock(func() time.Time { return now.Add(time.Hour) }))
	startTestQueue(t, queue)
	tasks := queue.Tasks()
	if len(tasks) != domainsftp.HistoryLimit+1 {
		t.Fatalf("rows = %d, want %d", len(tasks), domainsftp.HistoryLimit+1)
	}
}

func TestQueueControlErrorsAreTyped(t *testing.T) {
	queue := NewQueue(NewService(nil, nil))
	startTestQueue(t, queue)
	if err := queue.Pause("missing"); !errors.Is(err, domainsftp.ErrTaskNotFound) {
		t.Errorf("Pause(missing) = %v", err)
	}
	source := newFakeFS("source")
	source.addFile("/", "a", "a", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/out")
	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/a"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if err := queue.Retry(id); !errors.Is(err, domainsftp.ErrTaskNotRetryable) {
		t.Errorf("Retry(completed) = %v", err)
	}
	if _, err := queue.Enqueue(TaskSpec{}); err == nil {
		t.Error("Enqueue accepted an empty request")
	}
}

func TestQueueDerivesResourceKeysFromBackings(t *testing.T) {
	spec := TaskSpec{Source: fakeBacking("local"), Dest: fakeBacking("remote:h1")}
	keys := queueResourceKeys(spec)
	if len(keys) != 2 || keys[0] != domainsftp.LocalResourceKey() ||
		keys[1] != domainsftp.HostResourceKey("h1") {
		t.Errorf("keys = %v", keys)
	}
	spec = TaskSpec{Source: fakeBacking("remote:h1"), Dest: fakeBacking("remote:h1")}
	keys = queueResourceKeys(spec)
	if len(keys) != 1 || keys[0] != domainsftp.HostResourceKey("h1") {
		t.Errorf("deduped keys = %v", keys)
	}
	spec = TaskSpec{
		Source: fakeBacking("a"), Dest: fakeBacking("b"),
		SourceKey: domainsftp.SessionResourceKey("s1"),
		DestKey:   domainsftp.SessionResourceKey("s2"),
	}
	keys = queueResourceKeys(spec)
	if len(keys) != 2 || keys[0] != domainsftp.SessionResourceKey("s1") ||
		keys[1] != domainsftp.SessionResourceKey("s2") {
		t.Errorf("explicit keys = %v", keys)
	}
}

// fakeBacking is a FileSystem whose only meaningful method is Backing.
type fakeBacking string

func (f fakeBacking) Root(context.Context) (string, error) { return "/", nil }
func (f fakeBacking) List(context.Context, string) ([]domainsftp.Entry, error) {
	return nil, nil
}
func (f fakeBacking) Stat(context.Context, string) (domainsftp.Entry, error) {
	return domainsftp.Entry{}, nil
}
func (f fakeBacking) Mkdir(context.Context, string) error                 { return nil }
func (f fakeBacking) MkdirAll(context.Context, string) error              { return nil }
func (f fakeBacking) Open(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (f fakeBacking) Create(context.Context, string) (io.WriteCloser, error) {
	return nil, nil
}
func (f fakeBacking) Remove(context.Context, string) error    { return nil }
func (f fakeBacking) RemoveAll(context.Context, string) error { return nil }
func (f fakeBacking) Rename(context.Context, string, string) error {
	return nil
}
func (f fakeBacking) Chmod(context.Context, string, uint32) error { return nil }
func (f fakeBacking) Join(parts ...string) string                 { return strings.Join(parts, "/") }
func (f fakeBacking) Parent(path string) string                   { return path }
func (f fakeBacking) Base(path string) string                     { return path }
func (f fakeBacking) Style() PathStyle                            { return StylePOSIX }
func (f fakeBacking) Backing() string                             { return string(f) }
func (f fakeBacking) Canonical(path string) string                { return path }
func (f fakeBacking) Close() error                                { return nil }
