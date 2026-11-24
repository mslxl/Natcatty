package phoneshell

// Behavior tests for the phone's global Transfer queue. They drive the real
// Shell, the real shared queue, and the real copy engine over temporary trees,
// so resuming a checkpoint, replaying a row, restoring it after a relaunch,
// and re-selecting an evicted source all run through the code the phone root
// wires.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// gateTree blocks one source file's read and counts every open. It stands in
// for a large or slow transfer, so a test can pause at a checkpoint and later
// prove which files a resume opened again.
type gateTree struct {
	*localTree
	gatePath string
	gate     chan struct{}
	entered  chan struct{}
	once     sync.Once

	mu    sync.Mutex
	opens map[string]int
}

func newGateTree(t *testing.T, tree *localTree, gatePath string) *gateTree {
	t.Helper()
	return &gateTree{
		localTree: tree,
		gatePath:  gatePath,
		gate:      make(chan struct{}),
		entered:   make(chan struct{}),
		opens:     map[string]int{},
	}
}

func (g *gateTree) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	g.mu.Lock()
	g.opens[path]++
	blocked := path == g.gatePath
	g.mu.Unlock()
	if blocked {
		g.once.Do(func() { close(g.entered) })
		select {
		case <-g.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.FileSystem.Open(ctx, path)
}

func (g *gateTree) openCount(path string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.opens[path]
}

// recordingQueueRepository keeps every saved snapshot, so a test can inspect
// the durable state between two control actions.
type recordingQueueRepository struct {
	mu      sync.Mutex
	stored  []domainsftp.Task
	saves   [][]domainsftp.Task
	loadErr error
}

func (r *recordingQueueRepository) LoadTasks(context.Context) ([]domainsftp.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	return cloneTasks(r.stored), nil
}

func (r *recordingQueueRepository) SaveTasks(_ context.Context, tasks []domainsftp.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	saved := cloneTasks(tasks)
	r.stored = saved
	r.saves = append(r.saves, saved)
	return nil
}

func (r *recordingQueueRepository) latest() []domainsftp.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneTasks(r.stored)
}

func (r *recordingQueueRepository) savedWhere(condition func(domainsftp.Task) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, save := range r.saves {
		for _, task := range save {
			if condition(task) {
				return true
			}
		}
	}
	return false
}

func cloneTasks(tasks []domainsftp.Task) []domainsftp.Task {
	out := make([]domainsftp.Task, len(tasks))
	for index, task := range tasks {
		out[index] = task.Clone()
	}
	return out
}

// queueTask waits for one queue row to reach want, with a hard deadline.
func (f *filesFixture) queueTask(t *testing.T, id domainsftp.TaskID, want domainsftp.TaskStatus) domainsftp.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		task, ok := f.queue.Task(id)
		if ok && task.Status == want {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q; task = %+v", want, task)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitGate(t *testing.T, gate *gateTree) {
	t.Helper()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the copy never reached the gated file")
	}
}

// startBatchCopy enqueues a directory copy whose third file blocks, so the row
// can be paused with a checkpoint.
func (f *filesFixture) startBatchCopy(t *testing.T, gate *gateTree, answer map[string]apptransfer.ConflictPolicy) domainsftp.TaskID {
	t.Helper()
	id, err := f.queue.Enqueue(apptransfer.TaskSpec{
		Kind:        domainsftp.OpCopy,
		Source:      gate,
		Dest:        f.target,
		Sources:     []string{filepath.Join(f.source.root, "batch")},
		DestDir:     f.target.root,
		Conflict:    apptransfer.ConflictReplace,
		Answers:     answer,
		SourceLabel: "prod box",
		DestLabel:   "target box",
		SourceHost:  "prod",
		DestHost:    "target",
	})
	if err != nil {
		t.Fatalf("enqueue batch copy: %v", err)
	}
	return id
}

func writeBatch(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "batch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir batch: %v", err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		writeTreeFile(t, dir, name, "payload-"+name)
	}
}

// TestTransferQueueAppearsOnFilesAndSessionsHome is the shared-queue bullet:
// one row is drawn by the Files page and by the Sessions home from the same
// application queue.
func TestTransferQueueAppearsOnFilesAndSessionsHome(t *testing.T) {
	fixture := newFilesFixture(t)
	fixture.openHostFiles(t)
	fixture.startCopy(t, "report.txt")
	fixture.await(t, "the copy result", func() bool {
		return fixture.tester.HasText("Copied 1 file to target box.")
	})
	if !fixture.tester.HasText("Transfers") || !fixture.tester.HasText("Completed") {
		t.Fatalf("the Files page did not show the queue row; texts: %q", fixture.tester.Texts())
	}
	fixture.shell.Select(Sessions)
	fixture.tester.Frame()
	fixture.await(t, "the queue row on the Sessions home", func() bool {
		return fixture.tester.HasText("Transfers") && fixture.tester.HasText("Completed")
	})
}

// TestFilesUploadsADeviceFileThroughTheQueue proves the Files page's device
// upload enters the same global queue and reports through its row.
func TestFilesUploadsADeviceFileThroughTheQueue(t *testing.T) {
	fixture := newFilesFixture(t)
	device := filepath.Join(t.TempDir(), "photo.txt")
	if err := os.WriteFile(device, []byte("device-bytes"), 0o644); err != nil {
		t.Fatalf("write device file: %v", err)
	}
	fixture.picker.set(device)
	fixture.openHostFiles(t)
	if err := fixture.tester.Click("Upload from device"); err != nil {
		t.Fatalf("click Upload from device: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.await(t, "the upload result", func() bool {
		return fixture.tester.HasText("Copied 1 file to prod box.")
	})
	if got := readTreeFile(t, fixture.source.root, "photo.txt"); got != "device-bytes" {
		t.Fatalf("uploaded photo.txt = %q", got)
	}
}

// TestTransferQueueProgressContinuesAfterLeavingFiles proves the queue, not
// the page, owns the work: the copy keeps running after the user moves to the
// Sessions home, and its completed state appears there.
func TestTransferQueueProgressContinuesAfterLeavingFiles(t *testing.T) {
	fixture := newFilesFixture(t)
	gate := newGateTree(t, fixture.source, filepath.Join(fixture.source.root, "report.txt"))
	fixture.opener.setTree("prod", gate)
	fixture.openHostFiles(t)
	fixture.startCopy(t, "report.txt")
	waitGate(t, gate)
	fixture.await(t, "the running row on the Files page", func() bool {
		return fixture.tester.HasText("Transferring")
	})

	fixture.shell.Select(Sessions)
	fixture.tester.Frame()
	fixture.await(t, "the running row on the Sessions home", func() bool {
		return fixture.tester.HasText("Transferring")
	})
	close(gate.gate)
	fixture.await(t, "the completed row on the Sessions home", func() bool {
		return fixture.tester.HasText("Completed")
	})
	if got := readTreeFile(t, fixture.target.root, "report.txt"); got != "report" {
		t.Fatalf("target report.txt = %q", got)
	}
}

// TestTransferQueueBackgroundingKeepsWorkRunning proves a background/foreground
// cycle does not stop a queued Transfer; the row is live when the app returns.
func TestTransferQueueBackgroundingKeepsWorkRunning(t *testing.T) {
	fixture := newFilesFixture(t)
	gate := newGateTree(t, fixture.source, filepath.Join(fixture.source.root, "report.txt"))
	fixture.opener.setTree("prod", gate)
	fixture.openHostFiles(t)
	fixture.startCopy(t, "report.txt")
	waitGate(t, gate)
	fixture.shell.Background()
	if fixture.queue.Stats().Active != 1 {
		t.Fatalf("backgrounding stopped the queue: %+v", fixture.queue.Stats())
	}
	fixture.shell.Foreground()
	close(gate.gate)
	fixture.await(t, "the copy to finish after foreground", func() bool {
		return fixture.tester.HasText("Copied 1 file to target box.")
	})
}

// TestTransferQueueResumesFromCheckpoint pauses a directory copy, restarts the
// queue over its saved history, and proves Resume continues from the
// checkpoint instead of opening the completed files again.
func TestTransferQueueResumesFromCheckpoint(t *testing.T) {
	repo := &recordingQueueRepository{}
	fixture := newFilesFixtureWithQueue(t, repo)
	writeBatch(t, fixture.source.root)
	gatePath := filepath.Join(fixture.source.root, "batch", "c.txt")
	gate := newGateTree(t, fixture.source, gatePath)
	fixture.opener.setTree("prod", gate)
	id := fixture.startBatchCopy(t, gate, nil)
	waitGate(t, gate)
	if err := fixture.queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused := fixture.queueTask(t, id, domainsftp.TaskPaused)
	if paused.DirectoryResumeCheckpoint == nil {
		t.Fatal("paused row has no checkpoint")
	}
	close(gate.gate)

	relaunched := relaunchedFixture(t, fixture, repo.latest())
	relaunched.shell.Select(Sessions)
	relaunched.tester.Frame()
	relaunched.await(t, "the interrupted row", func() bool {
		return relaunched.tester.HasText("Interrupted")
	})
	if err := relaunched.tester.Click("Resume"); err != nil {
		t.Fatalf("click Resume: %v; texts: %q", err, relaunched.tester.Texts())
	}
	relaunched.await(t, "the resumed copy", func() bool {
		return relaunched.tester.HasText("Completed")
	})
	for _, name := range []string{"a.txt", "b.txt"} {
		if got := gate.openCount(filepath.Join(fixture.source.root, "batch", name)); got != 1 {
			t.Errorf("%s opened %d times; Resume re-copied a checkpointed file", name, got)
		}
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if got := readTreeFile(t, fixture.target.root, "batch/"+name); got != "payload-"+name {
			t.Errorf("%s = %q", name, got)
		}
	}
}

// TestTransferQueueRestartsFromTheBeginning pauses the same copy and chooses
// Restart, which replays every file and drops the checkpoint.
func TestTransferQueueRestartsFromTheBeginning(t *testing.T) {
	repo := &recordingQueueRepository{}
	fixture := newFilesFixtureWithQueue(t, repo)
	writeBatch(t, fixture.source.root)
	gatePath := filepath.Join(fixture.source.root, "batch", "c.txt")
	gate := newGateTree(t, fixture.source, gatePath)
	fixture.opener.setTree("prod", gate)
	id := fixture.startBatchCopy(t, gate, nil)
	waitGate(t, gate)
	if err := fixture.queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	fixture.queueTask(t, id, domainsftp.TaskPaused)
	close(gate.gate)

	relaunched := relaunchedFixture(t, fixture, repo.latest())
	relaunched.shell.Select(Sessions)
	relaunched.tester.Frame()
	relaunched.await(t, "the interrupted row", func() bool {
		return relaunched.tester.HasText("Interrupted")
	})
	if err := relaunched.tester.Click("Restart"); err != nil {
		t.Fatalf("click Restart: %v; texts: %q", err, relaunched.tester.Texts())
	}
	relaunched.await(t, "the replayed copy", func() bool {
		return relaunched.tester.HasText("Completed")
	})
	for _, name := range []string{"a.txt", "b.txt"} {
		if got := gate.openCount(filepath.Join(fixture.source.root, "batch", name)); got != 2 {
			t.Errorf("%s opened %d times; Restart must replay every file", name, got)
		}
	}
}

// TestTransferQueueReselectsAnEvictedSource is the Android invalidation
// bullet: an upload paused at a checkpoint loses its cached source copy. The
// row keeps its ID, destination, checkpoint, and conflict answers; choosing a
// replacement continues the same task.
func TestTransferQueueReselectsAnEvictedSource(t *testing.T) {
	repo := &recordingQueueRepository{}
	fixture := newFilesFixtureWithQueue(t, repo)
	picked := t.TempDir()
	writeBatch(t, picked)
	gatePath := filepath.Join(picked, "batch", "c.txt")
	pickTree := newGateTree(t, newLocalTreeAt(t, picked), gatePath)
	original := filepath.Join(picked, "batch")
	replacement := t.TempDir()
	// A same-named directory already in the destination makes the replacement
	// ask again, so the test can inspect the row before it settles.
	if err := os.MkdirAll(filepath.Join(fixture.target.root, "batch"), 0o755); err != nil {
		t.Fatalf("mkdir destination batch: %v", err)
	}
	writeTreeFile(t, filepath.Join(fixture.target.root, "batch"), "a.txt", "old-a")

	id, err := fixture.queue.Enqueue(apptransfer.TaskSpec{
		Kind:        domainsftp.OpUpload,
		Source:      pickTree,
		Dest:        fixture.target,
		Sources:     []string{original},
		DestDir:     fixture.target.root,
		Conflict:    apptransfer.ConflictAsk,
		Answers:     map[string]apptransfer.ConflictPolicy{original: apptransfer.ConflictReplace},
		SourceLabel: "This device",
		DestLabel:   "target box",
		DestHost:    "target",
	})
	if err != nil {
		t.Fatalf("enqueue upload: %v", err)
	}
	waitGate(t, pickTree)
	if err := fixture.queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused := fixture.queueTask(t, id, domainsftp.TaskPaused)
	if paused.DirectoryResumeCheckpoint == nil {
		t.Fatal("paused upload has no checkpoint")
	}
	// Android evicted the cached copy while the app was away.
	if err := os.RemoveAll(picked); err != nil {
		t.Fatalf("remove cached source: %v", err)
	}
	replacementBatch := filepath.Join(replacement, "batch")
	writeBatch(t, replacement)
	fixture.picker.set(replacementBatch)

	fixture.shell.Select(Sessions)
	fixture.tester.Frame()
	fixture.await(t, "the missing-source prompt", func() bool {
		return fixture.tester.HasText("Re-select source")
	})
	if err := fixture.tester.Click("Re-select source"); err != nil {
		t.Fatalf("click Re-select source: %v; texts: %q", err, fixture.tester.Texts())
	}
	// ReplaceSource persists the durable state before Resume: the same row ID
	// with the new source, the checkpoint, and the conflict answer moved onto
	// the replacement path. The original Overwrite decision belongs to the
	// task, so the replacement must not ask again.
	fixture.await(t, "the re-bound row", func() bool {
		return repo.savedWhere(func(task domainsftp.Task) bool {
			return task.ID == id && len(task.Sources) == 1 &&
				task.Sources[0] == replacementBatch &&
				task.DirectoryResumeCheckpoint != nil &&
				task.ReplaceTargets[replacementBatch] &&
				!task.ReplaceTargets[original] &&
				task.DestDir == fixture.target.root
		})
	})
	fixture.await(t, "the continued upload", func() bool {
		return fixture.tester.HasText("Completed")
	})
	if repo.savedWhere(func(task domainsftp.Task) bool {
		return task.ID == id && task.Status == domainsftp.TaskAttention
	}) {
		t.Error("the replacement prompted for a conflict the task had already decided")
	}
	if fixture.tester.HasText("Needs attention") {
		t.Errorf("the replacement asked again after re-selection; texts: %q", fixture.tester.Texts())
	}
	if tasks := fixture.queue.Tasks(); len(tasks) != 1 || tasks[0].ID != id {
		t.Fatalf("re-selection started a new task: %+v", tasks)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if got := readTreeFile(t, fixture.target.root, "batch/"+name); got != "payload-"+name {
			t.Errorf("%s = %q", name, got)
		}
	}
}

// TestTransferQueueRestoredReselectionPersistsReplacementSource restores an
// interrupted upload from the real queue-history file, re-selects the evicted
// cached copy, and proves the same durable row now names the replacement
// source and reuses the task's earlier Overwrite decision. Without that, the
// row would keep prompting after every relaunch because its saved Sources
// still named the vanished path.
func TestTransferQueueRestoredReselectionPersistsReplacementSource(t *testing.T) {
	target := newLocalTree(t, "target")
	writeTreeFile(t, target.root, "note.txt", "old-bytes")
	evictedDir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(evictedDir, 0o755); err != nil {
		t.Fatalf("mkdir cached source: %v", err)
	}
	evicted := filepath.Join(evictedDir, "note.txt")
	writeTreeFile(t, evictedDir, "note.txt", "stale-bytes")
	if err := os.RemoveAll(evictedDir); err != nil {
		t.Fatalf("evict cached source: %v", err)
	}
	replacementDir := t.TempDir()
	replacement := filepath.Join(replacementDir, "note.txt")
	if err := os.WriteFile(replacement, []byte("fresh-bytes"), 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}

	path := filepath.Join(t.TempDir(), "sftp-transfer-queue.json")
	repository := adaptersftp.NewTransferQueue(path)
	saved := domainsftp.Task{
		ID:            "evicted-upload",
		Kind:          domainsftp.OpUpload,
		Status:        domainsftp.TaskTransferring,
		Sources:       []string{evicted},
		DestDir:       target.root,
		Conflict:      string(apptransfer.ConflictAsk),
		SkipUnchanged: false,
		SourceLabel:   "This device",
		DestLabel:     "target box",
		DestHost:      "target",
		FilesTotal:    1,
		BytesTotal:    11,
		ReplaceTargets: map[string]bool{
			evicted: true,
		},
		CreatedAt: time.Now().Add(-time.Minute),
		UpdatedAt: time.Now().Add(-time.Minute),
	}
	if err := repository.SaveTasks(context.Background(), []domainsftp.Task{saved}); err != nil {
		t.Fatalf("save the durable row: %v", err)
	}
	// Gate the replacement's read so the row stays live while the test reads
	// the durable file. Completion clears the answer maps by design; the
	// remapped Overwrite must be visible in the running row.
	device := newGateTree(t, newLocalTreeAt(t, replacementDir), replacement)
	opener := &fakeFileOpener{
		trees: map[model.HostID]apptransfer.FileSystem{"target": target},
		errs:  map[model.HostID]error{},
	}
	service := apptransfer.NewService(fixedLocalOpener{device}, opener)
	queue := apptransfer.NewQueue(service,
		apptransfer.WithQueueRepository(adaptersftp.NewTransferQueue(path)))
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start the relaunched queue: %v", err)
	}
	t.Cleanup(queue.Shutdown)
	picker := &fakePicker{}
	picker.set(replacement)
	hosts := append(phoneHosts(), domainvaultHost("target", "target box"))
	fixture := newPhoneFixtureWith(t, hosts, &fakeOpener{output: "remote prompt $ "},
		WithFiles(service), WithTransferQueue(queue),
		WithTransferSourcePicker(picker.pick), WithScreenFactory(fakeScreens))
	fixture.shell.Select(Sessions)
	fixture.tester.Frame()
	fixture.await(t, "the missing-source prompt", func() bool {
		return fixture.tester.HasText("Re-select source")
	})
	if err := fixture.tester.Click("Re-select source"); err != nil {
		t.Fatalf("click Re-select source: %v; texts: %q", err, fixture.tester.Texts())
	}
	waitGate(t, device)
	waitTaskState(t, queue, saved.ID, domainsftp.TaskTransferring)
	fixture.tester.Frame()
	if fixture.tester.HasText("Re-select source") || fixture.tester.HasText("Needs attention") {
		t.Errorf("the row still prompts after re-selection; texts: %q", fixture.tester.Texts())
	}
	load := func() []domainsftp.Task {
		t.Helper()
		rows, err := adaptersftp.NewTransferQueue(path).LoadTasks(context.Background())
		if err != nil {
			t.Fatalf("reload the saved history: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("saved rows = %+v", rows)
		}
		return rows
	}
	row := load()[0]
	if row.ID != saved.ID {
		t.Errorf("row ID = %q", row.ID)
	}
	if len(row.Sources) != 1 || row.Sources[0] != replacement {
		t.Errorf("persisted sources = %v, want %q", row.Sources, replacement)
	}
	if !row.ReplaceTargets[replacement] || row.ReplaceTargets[evicted] {
		t.Errorf("persisted conflict answers = %v", row.ReplaceTargets)
	}
	if row.DestDir != target.root {
		t.Errorf("persisted destination = %q", row.DestDir)
	}
	close(device.gate)
	fixture.await(t, "the re-selected upload", func() bool {
		return fixture.tester.HasText("Completed")
	})
	// The replacement path is durable after another save, so a second
	// relaunch will not ask again.
	final := load()[0]
	if len(final.Sources) != 1 || final.Sources[0] != replacement {
		t.Errorf("final persisted sources = %v, want %q", final.Sources, replacement)
	}
	if got := readTreeFile(t, target.root, "note.txt"); got != "fresh-bytes" {
		t.Fatalf("replacement upload = %q", got)
	}
}

// TestTransferQueueReselectsALiveFailedUpload proves a not-found upload that
// already settled as failed still offers Re-select next to Restart. Restart
// alone would replay the evicted path; Re-select continues with the chosen
// copy.
func TestTransferQueueReselectsALiveFailedUpload(t *testing.T) {
	fixture := newFilesFixture(t)
	missing := filepath.Join(t.TempDir(), "evicted.txt")
	replacementDir := t.TempDir()
	replacement := filepath.Join(replacementDir, "evicted.txt")
	if err := os.WriteFile(replacement, []byte("fresh"), 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	fixture.picker.set(replacement)
	local, err := adaptersftp.NewLocal().OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("open local filesystem: %v", err)
	}
	id, err := fixture.queue.Enqueue(apptransfer.TaskSpec{
		Kind: domainsftp.OpUpload, Source: local, Dest: fixture.target,
		Sources: []string{missing}, DestDir: fixture.target.root,
		Conflict: apptransfer.ConflictReplace, SourceLabel: "This device",
		DestLabel: "target box", DestHost: "target",
	})
	if err != nil {
		t.Fatalf("enqueue upload: %v", err)
	}
	failed := fixture.queueTask(t, id, domainsftp.TaskFailed)
	if failed.Error != string(apptransfer.KindNotFound) {
		t.Fatalf("failure = %q, want not-found", failed.Error)
	}
	fixture.shell.Select(Sessions)
	fixture.tester.Frame()
	fixture.await(t, "the missing-source prompt on a failed row", func() bool {
		return fixture.tester.HasText("Re-select source")
	})
	if !fixture.tester.HasText("Restart") {
		t.Errorf("Restart disappeared from the failed row; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Re-select source"); err != nil {
		t.Fatalf("click Re-select source: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.await(t, "the re-selected upload", func() bool {
		return fixture.tester.HasText("Completed")
	})
	if tasks := fixture.queue.Tasks(); len(tasks) != 1 || tasks[0].ID != id {
		t.Fatalf("re-selection started a new task: %+v", tasks)
	}
	if got := readTreeFile(t, fixture.target.root, "evicted.txt"); got != "fresh" {
		t.Fatalf("replacement upload = %q", got)
	}
}

// TestTransferQueueReselectsARestoredFailedUpload covers the same not-found
// row after a process restart: it stayed terminal, has no live request, and
// Re-select must rebuild the request rather than return "unavailable".
func TestTransferQueueReselectsARestoredFailedUpload(t *testing.T) {
	target := newLocalTree(t, "target")
	missingDir := filepath.Join(t.TempDir(), "cache")
	missing := filepath.Join(missingDir, "evicted.txt")
	replacementDir := t.TempDir()
	replacement := filepath.Join(replacementDir, "evicted.txt")
	if err := os.WriteFile(replacement, []byte("restored-fresh"), 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	path := filepath.Join(t.TempDir(), "sftp-transfer-queue.json")
	repository := adaptersftp.NewTransferQueue(path)
	saved := domainsftp.Task{
		ID:          "failed-upload",
		Kind:        domainsftp.OpUpload,
		Status:      domainsftp.TaskFailed,
		Sources:     []string{missing},
		DestDir:     target.root,
		Conflict:    string(apptransfer.ConflictReplace),
		SourceLabel: "This device",
		DestLabel:   "target box",
		DestHost:    "target",
		Error:       string(apptransfer.KindNotFound),
		CreatedAt:   time.Now().Add(-time.Minute),
		UpdatedAt:   time.Now().Add(-time.Minute),
	}
	if err := repository.SaveTasks(context.Background(), []domainsftp.Task{saved}); err != nil {
		t.Fatalf("save the failed row: %v", err)
	}
	opener := &fakeFileOpener{
		trees: map[model.HostID]apptransfer.FileSystem{"target": target},
		errs:  map[model.HostID]error{},
	}
	service := apptransfer.NewService(adaptersftp.NewLocal(), opener)
	queue := apptransfer.NewQueue(service,
		apptransfer.WithQueueRepository(adaptersftp.NewTransferQueue(path)))
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start the relaunched queue: %v", err)
	}
	t.Cleanup(queue.Shutdown)
	picker := &fakePicker{}
	picker.set(replacement)
	hosts := append(phoneHosts(), domainvaultHost("target", "target box"))
	fixture := newPhoneFixtureWith(t, hosts, &fakeOpener{output: "remote prompt $ "},
		WithFiles(service), WithTransferQueue(queue),
		WithTransferSourcePicker(picker.pick), WithScreenFactory(fakeScreens))
	fixture.shell.Select(Sessions)
	fixture.tester.Frame()
	fixture.await(t, "the restored failed row's prompt", func() bool {
		return fixture.tester.HasText("Re-select source")
	})
	if err := fixture.tester.Click("Re-select source"); err != nil {
		t.Fatalf("click Re-select source: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.await(t, "the restored upload", func() bool {
		return fixture.tester.HasText("Completed")
	})
	if got := readTreeFile(t, target.root, "evicted.txt"); got != "restored-fresh" {
		t.Fatalf("restored replacement upload = %q", got)
	}
}

// TestTransferQueueRestoresInterruptedAfterRelaunch writes a durable row
// through the real queue-history adapter, starts a new process's queue over
// it, and proves the row comes back interrupted and resumable.
func TestTransferQueueRestoresInterruptedAfterRelaunch(t *testing.T) {
	source := newLocalTree(t, "prod")
	target := newLocalTree(t, "target")
	writeTreeFile(t, source.root, "resume.txt", "resumed")
	path := filepath.Join(t.TempDir(), "sftp-transfer-queue.json")
	repository := adaptersftp.NewTransferQueue(path)
	saved := domainsftp.Task{
		ID:            "relaunched",
		Kind:          domainsftp.OpCopy,
		Status:        domainsftp.TaskTransferring,
		Sources:       []string{filepath.Join(source.root, "resume.txt")},
		DestDir:       target.root,
		Conflict:      string(apptransfer.ConflictReplace),
		SkipUnchanged: true,
		SourceLabel:   "prod box",
		DestLabel:     "target box",
		SourceHost:    "prod",
		DestHost:      "target",
		BytesTotal:    6,
		FilesTotal:    1,
		CreatedAt:     time.Now().Add(-time.Minute),
		UpdatedAt:     time.Now().Add(-time.Minute),
	}
	if err := repository.SaveTasks(context.Background(), []domainsftp.Task{saved}); err != nil {
		t.Fatalf("save the durable row: %v", err)
	}

	opener := &fakeFileOpener{
		trees: map[model.HostID]apptransfer.FileSystem{
			"prod":   source,
			"target": target,
		},
		errs: map[model.HostID]error{},
	}
	service := apptransfer.NewService(adaptersftp.NewLocal(), opener)
	queue := apptransfer.NewQueue(service,
		apptransfer.WithQueueRepository(adaptersftp.NewTransferQueue(path)))
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start the relaunched queue: %v", err)
	}
	t.Cleanup(queue.Shutdown)
	hosts := append(phoneHosts(), domainvaultHost("target", "target box"))
	fixture := newPhoneFixtureWith(t, hosts, &fakeOpener{output: "remote prompt $ "},
		WithFiles(service), WithTransferQueue(queue), WithScreenFactory(fakeScreens))
	fixture.shell.Select(Sessions)
	fixture.tester.Frame()
	fixture.await(t, "the restored row", func() bool {
		return fixture.tester.HasText("Interrupted")
	})
	if err := fixture.tester.Click("Resume"); err != nil {
		t.Fatalf("click Resume: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.await(t, "the resumed row", func() bool {
		return fixture.tester.HasText("Completed")
	})
	if got := readTreeFile(t, target.root, "resume.txt"); got != "resumed" {
		t.Fatalf("resumed file = %q", got)
	}
}

// relaunchedFixture builds a second Shell over the same Host transports and a
// queue started from the saved history, as a process restart would.
func relaunchedFixture(t *testing.T, previous *filesFixture, tasks []domainsftp.Task) *filesFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sftp-transfer-queue.json")
	repository := adaptersftp.NewTransferQueue(path)
	if err := repository.SaveTasks(context.Background(), tasks); err != nil {
		t.Fatalf("write the saved history: %v", err)
	}
	service := apptransfer.NewService(adaptersftp.NewLocal(), previous.opener)
	queue := apptransfer.NewQueue(service,
		apptransfer.WithQueueRepository(adaptersftp.NewTransferQueue(path)))
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start the relaunched queue: %v", err)
	}
	t.Cleanup(queue.Shutdown)
	picker := &fakePicker{}
	fixture := newPhoneFixtureWith(t, previous.hosts, previous.phoneFixture.opener,
		WithFiles(service), WithTransferQueue(queue),
		WithTransferSourcePicker(picker.pick), WithScreenFactory(fakeScreens))
	return &filesFixture{
		phoneFixture: fixture,
		source:       previous.source,
		target:       previous.target,
		opener:       previous.opener,
		queue:        queue,
		picker:       picker,
	}
}

// newLocalTreeAt wraps an existing directory, so a device upload can gate a
// real path.
func newLocalTreeAt(t *testing.T, root string) *localTree {
	t.Helper()
	files, err := adaptersftp.NewLocal().OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("open local filesystem: %v", err)
	}
	return &localTree{FileSystem: files, root: root, backing: "test:device", failDir: map[string]int{}}
}

// fixedLocalOpener serves one prepared local filesystem, so a test can gate
// the device side the queue reads.
type fixedLocalOpener struct{ fs apptransfer.FileSystem }

func (o fixedLocalOpener) OpenLocal(context.Context) (apptransfer.FileSystem, error) {
	return o.fs, nil
}

// closeCountingFS records how often a transfer-owned transport is closed. A
// leak keeps the Host connection alive for the rest of the process.
type closeCountingFS struct {
	apptransfer.FileSystem
	mu     sync.Mutex
	closes int
}

func (f *closeCountingFS) Close() error {
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()
	return f.FileSystem.Close()
}

func (f *closeCountingFS) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

func waitTaskState(t *testing.T, queue *apptransfer.Queue, id domainsftp.TaskID, want domainsftp.TaskStatus) domainsftp.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		task, ok := queue.Task(id)
		if ok && task.Status == want {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q; task = %+v", want, task)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestTransferQueueOwnsHandlesThatArriveAfterSettlement pins the ordering fix
// for a fast row: Enqueue and Resume may finish the worker before the page has
// recorded the transports it opened. Recording first and then closing a
// settled row must leave no handle behind, whichever path closed it.
func TestTransferQueueOwnsHandlesThatArriveAfterSettlement(t *testing.T) {
	target := newLocalTree(t, "target")
	counting := &closeCountingFS{FileSystem: target}
	opener := &fakeFileOpener{
		trees: map[model.HostID]apptransfer.FileSystem{"target": counting},
		errs:  map[model.HostID]error{},
	}
	service := apptransfer.NewService(adaptersftp.NewLocal(), opener)
	queue := apptransfer.NewQueue(service, apptransfer.WithQueueConcurrency(1))
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start queue: %v", err)
	}
	t.Cleanup(queue.Shutdown)
	pages := newTransferQueuePages(queue, service, nil, nil)
	path := filepath.Join(t.TempDir(), "note.txt")
	writeTreeFile(t, filepath.Dir(path), "note.txt", "bytes")
	device := newLocalTree(t, "device")
	id, err := queue.Enqueue(apptransfer.TaskSpec{
		Kind: domainsftp.OpUpload, Source: device, Dest: counting,
		Sources: []string{path}, DestDir: target.root, Conflict: apptransfer.ConflictReplace,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// The row may already be terminal by the time the page receives its
	// handles, which is exactly the race the ownership helper closes.
	waitTaskState(t, queue, id, domainsftp.TaskCompleted)
	late := &closeCountingFS{FileSystem: target}
	pages.own(id, late)
	if got := late.closeCount(); got != 1 {
		t.Fatalf("late handle closed %d times, want 1", got)
	}
	pages.mu.Lock()
	retained := len(pages.handles[id])
	pages.mu.Unlock()
	if retained != 0 {
		t.Fatalf("a settled row retained %d handles", retained)
	}
}

// TestTransferQueueClosesFastUploadTransports runs a tiny upload through the
// page and proves the destination transport the page opened is closed once the
// row settles, whether the observer or the post-record check wins the race.
func TestTransferQueueClosesFastUploadTransports(t *testing.T) {
	target := newLocalTree(t, "target")
	counting := &closeCountingFS{FileSystem: target}
	opener := &fakeFileOpener{
		trees: map[model.HostID]apptransfer.FileSystem{"target": counting},
		errs:  map[model.HostID]error{},
	}
	service := apptransfer.NewService(adaptersftp.NewLocal(), opener)
	queue := apptransfer.NewQueue(service, apptransfer.WithQueueConcurrency(1))
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start queue: %v", err)
	}
	t.Cleanup(queue.Shutdown)
	picker := &fakePicker{}
	path := filepath.Join(t.TempDir(), "photo.txt")
	writeTreeFile(t, filepath.Dir(path), "photo.txt", "tiny")
	picker.set(path)
	pages := newTransferQueuePages(queue, service, picker.pick, nil)
	id, err := pages.enqueueUpload(context.Background(), "target", "target box",
		target.root, []string{path})
	if err != nil {
		t.Fatalf("enqueue upload: %v", err)
	}
	waitTaskState(t, queue, id, domainsftp.TaskCompleted)
	deadline := time.Now().Add(5 * time.Second)
	for counting.closeCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := counting.closeCount(); got != 1 {
		t.Fatalf("destination transport closed %d times, want 1", got)
	}
	pages.mu.Lock()
	retained := len(pages.handles[id])
	pages.mu.Unlock()
	if retained != 0 {
		t.Fatalf("the settled upload retained %d handles", retained)
	}
}

func domainvaultHost(id, label string) domainvault.Host {
	return domainvault.Host{
		ID: model.HostID(id), Label: label,
		Hostname: label + ".example.com", Port: 22,
		Username: "deploy", Protocol: domainvault.ProtocolSSH,
	}
}
