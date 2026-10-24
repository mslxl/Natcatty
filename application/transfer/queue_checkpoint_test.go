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

// queueDirectorySpec builds a directory copy over the local-style fakes.
func queueDirectorySpec(source, dest FileSystem) TaskSpec {
	return TaskSpec{
		Kind:     domainsftp.OpCopy,
		Source:   source,
		Dest:     dest,
		Sources:  []string{"/src"},
		DestDir:  "/dest",
		Conflict: ConflictReplace,
	}
}

// queueDirectorySource builds a source tree whose third sorted file blocks.
func queueDirectorySource(t *testing.T) *gatedFS {
	t.Helper()
	source := newGatedFS("source", "/src/c.txt")
	source.addDir("/src")
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		source.addFile("/src", name, "payload-"+name, 0o644)
	}
	return source
}

// waitQueueGate waits for the copy to reach the gated source file.
func waitQueueGate(t *testing.T, source *gatedFS) {
	t.Helper()
	select {
	case <-source.entered:
	case <-time.After(queueWait):
		t.Fatalf("the copy never reached the gated file after %s", queueWait)
	}
}

// TestQueueDirectoryCheckpointResumeSkipsCompletedFiles proves the queue saves
// the completed prefix, exposes honest paused progress, and resumes without
// opening a completed file again.
func TestQueueDirectoryCheckpointResumeSkipsCompletedFiles(t *testing.T) {
	source := queueDirectorySource(t)
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	repo := &fakeQueueRepository{}

	queue := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(1),
		WithQueueRepository(repo),
		WithQueueRetryDelay(0),
	)
	startTestQueue(t, queue)
	id, err := queue.Enqueue(queueDirectorySpec(source, dest))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueGate(t, source)
	if err := queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused := waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	if paused.DirectoryResumeCheckpoint == nil || !paused.DirectoryResumeCheckpoint.Valid() {
		t.Fatalf("paused row has no checkpoint: %+v", paused)
	}
	if paused.DirectoryResumeCheckpoint.CompletedEntries != 3 {
		t.Fatalf("completed entries = %d, want the directory and two files",
			paused.DirectoryResumeCheckpoint.CompletedEntries)
	}
	if paused.FilesDone != 3 || paused.BytesDone != 26 {
		t.Errorf("paused progress = %d files, %d bytes", paused.FilesDone, paused.BytesDone)
	}
	if paused.FilesTotal != 5 {
		t.Errorf("FilesTotal = %d, want the directory and four files", paused.FilesTotal)
	}
	saved := repo.savedTasks()
	if len(saved) != 1 || saved[0].DirectoryResumeCheckpoint == nil ||
		saved[0].DirectoryResumeCheckpoint.CompletedEntries != 3 {
		t.Fatalf("persisted checkpoint = %+v", saved)
	}
	if saved[0].Targets["/src"] != "/dest/src" {
		t.Errorf("persisted targets = %v", saved[0].Targets)
	}

	close(source.gate)
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	task := waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if got, ok := dest.file("/dest/src/" + name); !ok || string(got) != "payload-"+name {
			t.Errorf("%s = %q, exists=%v", name, got, ok)
		}
	}
	for name, want := range map[string]int{
		"/src/a.txt": 1, "/src/b.txt": 1, "/src/c.txt": 2, "/src/d.txt": 1,
	} {
		if got := source.openCount(name); got != want {
			t.Errorf("%s opens = %d, want %d", name, got, want)
		}
	}
	if task.FilesDone != task.FilesTotal || task.BytesDone != task.BytesTotal {
		t.Errorf("completed progress = %d/%d files, %d/%d bytes",
			task.FilesDone, task.FilesTotal, task.BytesDone, task.BytesTotal)
	}
	if task.DirectoryResumeCheckpoint != nil {
		t.Errorf("a completed row kept its checkpoint: %+v", task.DirectoryResumeCheckpoint)
	}
}

// TestQueueRestartResumesADirectoryCheckpointAfterReattach restarts a queue
// over its saved history. The row becomes interrupted with its checkpoint
// intact; Reattach gives it live filesystems and Resume skips completed files.
func TestQueueRestartResumesADirectoryCheckpointAfterReattach(t *testing.T) {
	source := queueDirectorySource(t)
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	repo := &fakeQueueRepository{}

	first := NewQueue(NewService(nil, nil),
		WithQueueRepository(repo), WithQueueRetryDelay(0))
	if err := first.Start(context.Background()); err != nil {
		t.Fatalf("start first queue: %v", err)
	}
	id, err := first.Enqueue(queueDirectorySpec(source, dest))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueGate(t, source)
	if err := first.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused := waitQueueTask(t, first, id, domainsftp.TaskPaused)
	if paused.DirectoryResumeCheckpoint == nil {
		t.Fatal("the paused row has no checkpoint to survive the restart")
	}
	first.Shutdown()

	second := NewQueue(NewService(nil, nil),
		WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, second)
	restored, ok := second.Task(id)
	if !ok {
		t.Fatal("the directory row vanished across the restart")
	}
	if restored.Status != domainsftp.TaskInterrupted || !restored.ReconnectRequired {
		t.Fatalf("restored row = %+v", restored)
	}
	if restored.DirectoryResumeCheckpoint == nil ||
		restored.DirectoryResumeCheckpoint.CompletedEntries != 3 {
		t.Fatalf("restored checkpoint = %+v", restored.DirectoryResumeCheckpoint)
	}
	if err := second.Resume(id); !errors.Is(err, domainsftp.ErrTaskUnavailable) {
		t.Fatalf("Resume before Reattach = %v, want ErrTaskUnavailable", err)
	}
	if err := second.Reattach(id, queueDirectorySpec(source, dest)); err != nil {
		t.Fatalf("reattach: %v", err)
	}
	reattached, _ := second.Task(id)
	if reattached.ReconnectRequired || reattached.Status != domainsftp.TaskInterrupted {
		t.Fatalf("reattached row = %+v", reattached)
	}
	close(source.gate)
	if err := second.Resume(id); err != nil {
		t.Fatalf("resume after reattach: %v", err)
	}
	waitQueueTask(t, second, id, domainsftp.TaskCompleted)
	if got := source.openCount("/src/a.txt"); got != 1 {
		t.Errorf("a.txt opens = %d; the restart re-copied a completed file", got)
	}
	if got := source.openCount("/src/b.txt"); got != 1 {
		t.Errorf("b.txt opens = %d; the restart re-copied a completed file", got)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if !dest.has("/dest/src/" + name) {
			t.Errorf("%s missing after the restart resume", name)
		}
	}
}

// TestQueueCancelClearsTheCheckpointAndRetryStartsOver keeps Cancel a hard
// stop: Retry replays the request from the beginning.
func TestQueueCancelClearsTheCheckpointAndRetryStartsOver(t *testing.T) {
	source := queueDirectorySource(t)
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(queueDirectorySpec(source, dest))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueGate(t, source)
	if err := queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	if err := queue.Cancel(id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cancelled := waitQueueTask(t, queue, id, domainsftp.TaskCancelled)
	if cancelled.DirectoryResumeCheckpoint != nil || cancelled.Targets != nil {
		t.Errorf("cancel kept resume state: %+v", cancelled)
	}
	close(source.gate)
	if err := queue.Retry(id); err != nil {
		t.Fatalf("retry: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if got := source.openCount("/src/a.txt"); got != 2 {
		t.Errorf("a.txt opens after Retry = %d, want a full restart", got)
	}
}

// waitQueueStatus polls one real task status with a hard deadline.
func waitQueueStatus(t *testing.T, q *Queue, id domainsftp.TaskID, want domainsftp.TaskStatus) domainsftp.Task {
	t.Helper()
	deadline := time.Now().Add(queueWait)
	for {
		task, ok := q.Task(id)
		if ok && task.Status == want {
			return task
		}
		if time.Now().After(deadline) {
			if ok {
				t.Fatalf("timed out after %s waiting for %q; task is %q", queueWait, want, task.Status)
			}
			t.Fatalf("timed out after %s waiting for %q; task vanished", queueWait, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestQueuePauseAndCancelLatchesStayConsistent covers the latch matrix: a
// second Pause is a no-op, Resume while pausing is refused, Pause after Cancel
// cannot undo the cancel, and a paused row can still be cancelled.
func TestQueuePauseAndCancelLatchesStayConsistent(t *testing.T) {
	source := newGatedFS("source", "/src/c.txt")
	source.addDir("/src")
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		source.addFile("/src", name, strings.Repeat("x", 2048), 0o644)
	}
	source.holdCancel = make(chan struct{})
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(queueDirectorySpec(source, dest))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueGate(t, source)
	if err := queue.Pause(id); err != nil {
		t.Fatalf("first pause: %v", err)
	}
	// The held Open keeps the row pausing instead of settling.
	waitQueueStatus(t, queue, id, domainsftp.TaskPausing)
	if err := queue.Pause(id); err != nil {
		t.Fatalf("second pause: %v", err)
	}
	if err := queue.Resume(id); !errors.Is(err, domainsftp.ErrTaskNotRetryable) {
		t.Fatalf("Resume while pausing = %v, want ErrTaskNotRetryable", err)
	}
	if task, _ := queue.Task(id); task.Status != domainsftp.TaskPausing {
		t.Fatalf("the second pause changed the status to %q", task.Status)
	}
	if err := queue.Cancel(id); err != nil {
		t.Fatalf("cancel after pause: %v", err)
	}
	close(source.holdCancel)
	cancelled := waitQueueTask(t, queue, id, domainsftp.TaskCancelled)
	if cancelled.PauseRequested || !cancelled.CancelRequested {
		t.Errorf("cancel latches = pause=%v cancel=%v", cancelled.PauseRequested, cancelled.CancelRequested)
	}
	if err := queue.Cancel(id); err != nil {
		t.Fatalf("second cancel: %v", err)
	}

	// A paused row accepts Cancel immediately and clears the pause latch.
	secondSource := newGatedFS("second", "/src/c.txt")
	secondSource.addDir("/src")
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		secondSource.addFile("/src", name, strings.Repeat("y", 2048), 0o644)
	}
	secondSource.holdCancel = make(chan struct{})
	secondDest := newFakeFS("second-dest")
	secondDest.addDir("/dest2")
	second, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: secondSource, Dest: secondDest,
		Sources: []string{"/src"}, DestDir: "/dest2",
	})
	if err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	waitQueueStatus(t, queue, second, domainsftp.TaskTransferring)
	waitQueueGate(t, secondSource)
	if err := queue.Pause(second); err != nil {
		t.Fatalf("pause second: %v", err)
	}
	waitQueueStatus(t, queue, second, domainsftp.TaskPausing)
	close(secondSource.holdCancel)
	paused := waitQueueTask(t, queue, second, domainsftp.TaskPaused)
	if !paused.PauseRequested {
		t.Fatal("the paused row lost its pause latch")
	}
	if err := queue.Cancel(second); err != nil {
		t.Fatalf("cancel paused: %v", err)
	}
	after := waitQueueTask(t, queue, second, domainsftp.TaskCancelled)
	if after.PauseRequested {
		t.Fatal("Cancel did not clear the pause latch on a paused row")
	}
}

// queueFailingFS fails one source path with a chosen error kind until the
// test clears the failure, and records opens so a resume can prove it skipped
// the checkpointed prefix.
type queueFailingFS struct {
	*fakeFS
	path string
	kind ErrorKind

	mu    sync.Mutex
	fail  bool
	opens map[string]int
}

func newQueueFailingFS(name, path string, kind ErrorKind) *queueFailingFS {
	return &queueFailingFS{
		fakeFS: newFakeFS(name),
		path:   path,
		kind:   kind,
		fail:   true,
		opens:  map[string]int{},
	}
}

func (f *queueFailingFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opens[p]++
	fail := f.fail && p == f.path
	f.mu.Unlock()
	if fail {
		return nil, &Error{Kind: f.kind, Op: domainsftp.OpUpload, Path: p,
			Cause: errors.New("stream lost")}
	}
	return f.fakeFS.Open(ctx, p)
}

func (f *queueFailingFS) setFail(fail bool) {
	f.mu.Lock()
	f.fail = fail
	f.mu.Unlock()
}

func (f *queueFailingFS) openCount(p string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens[p]
}

// queueReplaceTree writes an existing destination tree a replacement must
// preserve until the new tree is complete.
func queueReplaceTree(t *testing.T) *fakeFS {
	t.Helper()
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	dest.addDir("/dest/src")
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		dest.addFile("/dest/src", name, "old-"+name, 0o644)
	}
	return dest
}

// TestQueueTransportLossRetainsTheCheckpointForReattach proves a connection
// failure keeps the partial replacement and checkpoint, and Reattach plus
// Resume finishes the same tree without reopening completed files.
func TestQueueTransportLossRetainsTheCheckpointForReattach(t *testing.T) {
	source := newQueueFailingFS("source", "/src/c.txt", KindConnection)
	source.addDir("/src")
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		source.addFile("/src", name, "payload-"+name, 0o644)
	}
	dest := queueReplaceTree(t)
	repo := &fakeQueueRepository{}
	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1),
		WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	spec := queueDirectorySpec(source, dest)
	id, err := queue.Enqueue(spec)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	failed := waitQueueTask(t, queue, id, domainsftp.TaskFailed)
	if failed.Error != string(KindConnection) || !failed.ReconnectRequired {
		t.Fatalf("failed row = %+v", failed)
	}
	checkpoint := failed.DirectoryResumeCheckpoint
	if checkpoint == nil || checkpoint.CompletedEntries != 3 {
		t.Fatalf("a lost transport lost the checkpoint: %+v", checkpoint)
	}
	if got, _ := dest.file("/dest/src/a.txt"); string(got) != "payload-a.txt" {
		t.Fatalf("connection failure rolled the new prefix back: a.txt = %q", got)
	}
	source.setFail(false)
	if err := queue.Reattach(id, spec); err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if got, ok := dest.file("/dest/src/" + name); !ok || string(got) != "payload-"+name {
			t.Errorf("%s = %q, exists=%v", name, got, ok)
		}
	}
	if got := source.openCount("/src/a.txt"); got != 1 {
		t.Errorf("a.txt opens = %d; the reconnect re-copied the prefix", got)
	}
	if got := source.openCount("/src/b.txt"); got != 1 {
		t.Errorf("b.txt opens = %d; the reconnect re-copied the prefix", got)
	}
	for _, full := range dest.entriesAll() {
		if strings.Contains(full, ".netcatty-") {
			t.Errorf("the completed replacement left %s", full)
		}
	}
}

// TestQueueOrdinaryFailureRestoresTheOriginalAndDropsTheCheckpoint proves a
// non-transport failure keeps the ticket-30 replacement contract: the
// original is back and the checkpoint cannot skip its prefix.
func TestQueueOrdinaryFailureRestoresTheOriginalAndDropsTheCheckpoint(t *testing.T) {
	source := newQueueFailingFS("source", "/src/c.txt", KindPermission)
	source.addDir("/src")
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		source.addFile("/src", name, "payload-"+name, 0o644)
	}
	dest := queueReplaceTree(t)
	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(queueDirectorySpec(source, dest))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	failed := waitQueueTask(t, queue, id, domainsftp.TaskFailed)
	if failed.DirectoryResumeCheckpoint != nil {
		t.Fatalf("a rolled-back replacement kept its checkpoint: %+v", failed.DirectoryResumeCheckpoint)
	}
	if got, _ := dest.file("/dest/src/a.txt"); string(got) != "old-a.txt" {
		t.Fatalf("failure left destination a.txt = %q, want the original", got)
	}
	for _, full := range dest.entriesAll() {
		if strings.Contains(full, ".netcatty-") {
			t.Errorf("a staging or backup file survived the failure: %s", full)
		}
	}
}
