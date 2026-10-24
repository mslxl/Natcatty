package transfer

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// waitQueueCondition polls an observable queue condition with a hard deadline.
// It never assumes a fixed delay is enough.
func waitQueueCondition(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(queueWait)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", queueWait, what)
		}
		time.Sleep(time.Millisecond)
	}
}

// blockedSource returns a source whose first read waits until the task is
// cancelled, so the queue keeps the row active while a test reserves paths.
func blockedSource(name, file, content string) *fakeFS {
	source := newFakeFS(name)
	source.addFile("/", file, content, 0o644)
	source.block = make(chan struct{})
	return source
}

func TestQueueApplyToAllReplaceCoversTheRestOfTheBatch(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new-a", 0o644)
	source.addFile("/", "b.txt", "new-b", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old-a", 0o644)
	dest.addFile("/", "b.txt", "old-b", 0o644)

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1))
	startTestQueue(t, queue)
	var attentionMu sync.Mutex
	var attention []domainsftp.TaskID
	queue.Observe(func(task domainsftp.Task) {
		if task.Status == domainsftp.TaskAttention {
			attentionMu.Lock()
			attention = append(attention, task.ID)
			attentionMu.Unlock()
		}
	})
	first, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "paste-1",
	})
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	second, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/b.txt"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "paste-1",
	})
	if err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	waitQueueTask(t, queue, first, domainsftp.TaskAttention)
	if err := queue.ResolveConflict(first, ConflictReplace, true); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	waitQueueTask(t, queue, first, domainsftp.TaskCompleted)
	waitQueueTask(t, queue, second, domainsftp.TaskCompleted)
	if got, _ := dest.file("/a.txt"); string(got) != "new-a" {
		t.Fatalf("a.txt = %q", got)
	}
	if got, _ := dest.file("/b.txt"); string(got) != "new-b" {
		t.Fatalf("b.txt = %q", got)
	}
	attentionMu.Lock()
	prompts := append([]domainsftp.TaskID(nil), attention...)
	attentionMu.Unlock()
	if len(prompts) != 1 || prompts[0] != first {
		t.Fatalf("attention prompts = %v, want only %q", prompts, first)
	}
}

func TestQueueApplyToAllSkipLeavesEveryDestination(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new-a", 0o644)
	source.addFile("/", "b.txt", "new-b", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old-a", 0o644)
	dest.addFile("/", "b.txt", "old-b", 0o644)

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1))
	startTestQueue(t, queue)
	first, _ := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "copy-1",
	})
	second, _ := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/b.txt"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "copy-1",
	})
	waitQueueTask(t, queue, first, domainsftp.TaskAttention)
	if err := queue.ResolveConflict(first, ConflictSkip, true); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	firstTask := waitQueueTask(t, queue, first, domainsftp.TaskCompleted)
	secondTask := waitQueueTask(t, queue, second, domainsftp.TaskCompleted)
	if firstTask.Skipped != 1 || secondTask.Skipped != 1 {
		t.Fatalf("skipped counts = %d/%d", firstTask.Skipped, secondTask.Skipped)
	}
	if got, _ := dest.file("/a.txt"); string(got) != "old-a" {
		t.Fatalf("a.txt = %q", got)
	}
	if got, _ := dest.file("/b.txt"); string(got) != "old-b" {
		t.Fatalf("b.txt = %q", got)
	}
}

func TestQueueStopCancelsTheWholeBatch(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new-a", 0o644)
	source.addFile("/", "b.txt", "new-b", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old-a", 0o644)
	dest.addFile("/", "b.txt", "old-b", 0o644)

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1))
	startTestQueue(t, queue)
	first, _ := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "stop-1",
	})
	second, _ := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/b.txt"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "stop-1",
	})
	waitQueueTask(t, queue, first, domainsftp.TaskAttention)
	if err := queue.ResolveConflict(first, ConflictStop, false); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	waitQueueTask(t, queue, first, domainsftp.TaskCancelled)
	waitQueueTask(t, queue, second, domainsftp.TaskCancelled)
	if got, _ := dest.file("/a.txt"); string(got) != "old-a" {
		t.Fatalf("a.txt = %q", got)
	}
	if got, _ := dest.file("/b.txt"); string(got) != "old-b" {
		t.Fatalf("b.txt = %q", got)
	}
}

func TestQueueApplyToAllIsKeyedByConflictType(t *testing.T) {
	source := newFakeFS("source")
	source.addDir("/folder")
	source.addFile("/folder", "x", "x", 0o644)
	source.addFile("/", "file.txt", "new", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/folder")
	dest.addFile("/", "file.txt", "old", 0o644)

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1))
	startTestQueue(t, queue)
	folder, _ := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/folder"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "mixed",
	})
	file, _ := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/file.txt"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "mixed",
	})
	folderTask := waitQueueTask(t, queue, folder, domainsftp.TaskAttention)
	if folderTask.PendingConflict == nil || !folderTask.PendingConflict.IsDirectory {
		t.Fatalf("folder conflict = %+v", folderTask.PendingConflict)
	}
	if err := queue.ResolveConflict(folder, ConflictMerge, true); err != nil {
		t.Fatalf("resolve folder: %v", err)
	}
	waitQueueTask(t, queue, folder, domainsftp.TaskCompleted)
	// A different destination type is not covered by the folder merge answer.
	fileTask := waitQueueTask(t, queue, file, domainsftp.TaskAttention)
	if fileTask.PendingConflict == nil || fileTask.PendingConflict.IsDirectory {
		t.Fatalf("file conflict = %+v", fileTask.PendingConflict)
	}
	if err := queue.ResolveConflict(file, ConflictReplace, false); err != nil {
		t.Fatalf("resolve file: %v", err)
	}
	waitQueueTask(t, queue, file, domainsftp.TaskCompleted)
}

func TestQueueRejectsATransferWhosePathAnotherOneReserves(t *testing.T) {
	firstSource := blockedSource("first", "a.txt", strings.Repeat("x", 64))
	secondSource := newFakeFS("second")
	secondSource.addFile("/", "a.txt", "second", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/out")

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(2))
	startTestQueue(t, queue)
	first, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: firstSource, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	waitQueueActive(t, queue, 1)

	_, err = queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: secondSource, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/out",
	})
	if KindOf(err) != KindReserved {
		t.Fatalf("second enqueue error = %v (kind %q), want reserved", err, KindOf(err))
	}

	if err := queue.Cancel(first); err != nil {
		t.Fatalf("cancel first: %v", err)
	}
	waitQueueTask(t, queue, first, domainsftp.TaskCancelled)
	waitQueueActive(t, queue, 0)
	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: secondSource, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue after release: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
}

func TestQueueReservesDirectoryPathsAndSubtrees(t *testing.T) {
	dirSource := newFakeFS("dir")
	dirSource.addDir("/tree")
	dirSource.addFile("/tree", "inside.txt", "inside", 0o644)
	dirSource.block = make(chan struct{})
	fileSource := newFakeFS("file")
	fileSource.addFile("/", "x.txt", "x", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/out")

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(2))
	startTestQueue(t, queue)
	// A directory Transfer reserves its root and every descendant.
	first, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: dirSource, Dest: dest,
		Sources: []string{"/tree"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue directory: %v", err)
	}
	waitQueueActive(t, queue, 1)
	// The plan claims the destination root as a directory before it reports
	// the target, so the target is the observable proof of the subtree claim.
	waitQueueCondition(t, "the directory's destination claim", func() bool {
		task, ok := queue.Task(first)
		return ok && task.Targets["/tree"] == "/out/tree"
	})

	for _, spec := range []TaskSpec{
		{Kind: domainsftp.OpCopy, Source: fileSource, Dest: dest, Sources: []string{"/x.txt"}, DestDir: "/out/tree"},
		{Kind: domainsftp.OpCopy, Source: dirSource, Dest: dest, Sources: []string{"/tree"}, DestDir: "/out/tree"},
	} {
		if _, err := queue.Enqueue(spec); KindOf(err) != KindReserved {
			t.Fatalf("enqueue under a reserved directory = %v (kind %q)", err, KindOf(err))
		}
	}
	_ = queue.Cancel(first)
	waitQueueTask(t, queue, first, domainsftp.TaskCancelled)
	waitQueueActive(t, queue, 0)
}

func TestQueueDuplicateTransfersReserveOnlyTheirChosenNames(t *testing.T) {
	source := blockedSource("source", "a.txt", "new")
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old", 0o644)
	other := newFakeFS("other")
	other.addFile("/", "a.txt", "other", 0o644)
	other.block = make(chan struct{})

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(2))
	startTestQueue(t, queue)
	first, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/", Conflict: ConflictDuplicate,
	})
	if err != nil {
		t.Fatalf("enqueue duplicate: %v", err)
	}
	waitQueueActive(t, queue, 1)
	waitQueueCondition(t, "the first duplicate target", func() bool {
		task, ok := queue.Task(first)
		return ok && task.Targets["/a.txt"] == "/a (copy).txt"
	})

	// A plain Transfer to the original name is not blocked: the duplicate wrote
	// beside it, not over it.
	if _, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: other, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/",
	}); err != nil {
		t.Fatalf("a Transfer to the unmodified original name was refused: %v", err)
	}

	// A second duplicate plans while the first is still live. The reservation
	// makes it choose the next suffix instead of the same one.
	second, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: other, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/", Conflict: ConflictDuplicate,
	})
	if err != nil {
		t.Fatalf("enqueue second duplicate: %v", err)
	}
	waitQueueCondition(t, "the second duplicate target", func() bool {
		task, ok := queue.Task(second)
		return ok && task.Targets["/a.txt"] == "/a (copy 2).txt"
	})
	_ = queue.Cancel(first)
	_ = queue.Cancel(second)
	waitQueueActive(t, queue, 0)
	if _, ok := dest.file("/a (copy 2).txt"); ok {
		t.Fatal("a cancelled duplicate promoted its staging file")
	}
}

func TestQueueRejectsUnknownAndInvalidConflictAnswers(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old", 0o644)
	queue := NewQueue(NewService(nil, nil))
	startTestQueue(t, queue)
	id, _ := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/", Conflict: ConflictAsk,
	})
	waitQueueTask(t, queue, id, domainsftp.TaskAttention)
	if err := queue.ResolveConflict(id, ConflictPolicy("unknown"), false); !errors.Is(err, ErrConflictActionInvalid) {
		t.Fatalf("unknown answer = %v", err)
	}
	if err := queue.ResolveConflict(id, ConflictMerge, false); !errors.Is(err, ErrConflictActionInvalid) {
		t.Fatalf("merge for a file = %v", err)
	}
	if err := queue.ResolveConflict(id, ConflictSkip, false); err != nil {
		t.Fatalf("skip: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
}

func TestQueueAnswersOnlyTheRowThatAskedWithoutApplyToAll(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new-a", 0o644)
	source.addFile("/", "b.txt", "new-b", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old-a", 0o644)
	dest.addFile("/", "b.txt", "old-b", 0o644)

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(2))
	startTestQueue(t, queue)
	first, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "paste-1",
	})
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	second, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/b.txt"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "paste-1",
	})
	if err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	waitQueueTask(t, queue, first, domainsftp.TaskAttention)
	waitQueueTask(t, queue, second, domainsftp.TaskAttention)

	if err := queue.ResolveConflict(first, ConflictReplace, false); err != nil {
		t.Fatalf("resolve first: %v", err)
	}
	waitQueueTask(t, queue, first, domainsftp.TaskCompleted)
	// Without apply-to-all the sibling keeps waiting for its own answer.
	if task, ok := queue.Task(second); !ok || task.Status != domainsftp.TaskAttention ||
		task.PendingConflict == nil {
		t.Fatalf("the sibling left attention without its own answer: %+v", task)
	}
	if got, _ := dest.file("/b.txt"); string(got) != "old-b" {
		t.Fatalf("the unasked sibling rewrote b.txt to %q", got)
	}
	if err := queue.ResolveConflict(second, ConflictSkip, false); err != nil {
		t.Fatalf("resolve second: %v", err)
	}
	waitQueueTask(t, queue, second, domainsftp.TaskCompleted)
}

func TestQueueApplyToAllCountCoversOnlyTheSameConflictType(t *testing.T) {
	dirSource := newFakeFS("dir-source")
	dirSource.addDir("/folder")
	dirSource.addFile("/folder", "x", "x", 0o644)
	dirDest := newFakeFS("dir-dest")
	dirDest.addDir("/folder")
	fileSource := newFakeFS("file-source")
	fileSource.addFile("/", "file.txt", "new", 0o644)
	fileDest := newFakeFS("file-dest")
	fileDest.addFile("/", "file.txt", "old", 0o644)

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1))
	startTestQueue(t, queue)
	folder, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: dirSource, Dest: dirDest,
		Sources: []string{"/folder"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "mixed",
	})
	if err != nil {
		t.Fatalf("enqueue folder: %v", err)
	}
	folderTask := waitQueueTask(t, queue, folder, domainsftp.TaskAttention)
	if folderTask.PendingConflict == nil || folderTask.PendingConflict.ApplyToAllCount != 1 {
		t.Fatalf("folder count = %+v, want 1", folderTask.PendingConflict)
	}

	file, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: fileSource, Dest: fileDest,
		Sources: []string{"/file.txt"}, DestDir: "/", Conflict: ConflictAsk, BatchID: "mixed",
	})
	if err != nil {
		t.Fatalf("enqueue file: %v", err)
	}
	fileTask := waitQueueTask(t, queue, file, domainsftp.TaskAttention)
	// The folder is waiting on a directory:directory conflict, which a
	// file:file answer cannot cover, so it is not part of the count.
	if fileTask.PendingConflict == nil || fileTask.PendingConflict.ApplyToAllCount != 1 {
		t.Fatalf("file count = %+v, want 1: a different conflict type is not covered",
			fileTask.PendingConflict)
	}
	if err := queue.ResolveConflict(folder, ConflictMerge, false); err != nil {
		t.Fatalf("resolve folder: %v", err)
	}
	if err := queue.ResolveConflict(file, ConflictSkip, false); err != nil {
		t.Fatalf("resolve file: %v", err)
	}
	waitQueueTask(t, queue, folder, domainsftp.TaskCompleted)
	waitQueueTask(t, queue, file, domainsftp.TaskCompleted)
}

func TestQueuePausedDuplicateKeepsItsChosenNameReserved(t *testing.T) {
	firstSource := newFakeFS("first")
	firstSource.addFile("/", "a.txt", strings.Repeat("x", 64), 0o644)
	firstSource.block = make(chan struct{})
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old", 0o644)
	secondSource := newFakeFS("second")
	secondSource.addFile("/", "a.txt", "new", 0o644)
	secondSource.block = make(chan struct{})

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(2))
	startTestQueue(t, queue)
	first, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: firstSource, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/", Conflict: ConflictDuplicate,
	})
	if err != nil {
		t.Fatalf("enqueue first duplicate: %v", err)
	}
	waitQueueCondition(t, "the first duplicate target", func() bool {
		task, ok := queue.Task(first)
		return ok && task.Targets["/a.txt"] == "/a (copy).txt"
	})
	if err := queue.Pause(first); err != nil {
		t.Fatalf("pause first duplicate: %v", err)
	}
	waitQueueTask(t, queue, first, domainsftp.TaskPaused)

	second, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: secondSource, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/", Conflict: ConflictDuplicate,
	})
	if err != nil {
		t.Fatalf("enqueue second duplicate: %v", err)
	}
	// The paused row still owns "/a (copy).txt"; the new duplicate must skip
	// that name instead of racing the row that will resume into it.
	waitQueueCondition(t, "the second duplicate target", func() bool {
		task, ok := queue.Task(second)
		return ok && task.Targets["/a.txt"] == "/a (copy 2).txt"
	})
	if err := queue.Resume(first); err != nil {
		t.Fatalf("resume first duplicate: %v", err)
	}
	_ = queue.Cancel(first)
	_ = queue.Cancel(second)
	waitQueueActive(t, queue, 0)
}

func TestQueueRetryReclaimsItsDestinationOrReportsTheNewOwner(t *testing.T) {
	dest := newFakeFS("dest")
	dest.addDir("/out")
	dest.failCreate = true // refuses every staging name that contains "denied"
	failing := newFakeFS("failing")
	failing.addFile("/", "denied.txt", "x", 0o644)

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(2))
	startTestQueue(t, queue)
	first, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: failing, Dest: dest,
		Sources: []string{"/denied.txt"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue failing transfer: %v", err)
	}
	waitQueueTask(t, queue, first, domainsftp.TaskFailed)

	holding := newFakeFS("holding")
	holding.addFile("/", "denied.txt", "y", 0o644)
	holding.block = make(chan struct{})
	second, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: holding, Dest: dest,
		Sources: []string{"/denied.txt"}, DestDir: "/out",
	})
	if err != nil {
		t.Fatalf("enqueue the new owner: %v", err)
	}
	waitQueueActive(t, queue, 1)

	// The failed row's retry must not send a second writer to a destination
	// another live Transfer now owns.
	if err := queue.Retry(first); KindOf(err) != KindReserved {
		t.Fatalf("retrying a taken destination = %v (kind %q), want reserved", err, KindOf(err))
	}
	_ = queue.Cancel(second)
	waitQueueActive(t, queue, 0)
}
