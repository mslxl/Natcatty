package transfer

import (
	"context"
	"testing"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// TestQueueEnqueueSeedsConflictAnswers proves a caller's already-decided
// per-source answers become the durable row's conflict maps, so a resume or
// restart keeps the decisions instead of asking again.
func TestQueueEnqueueSeedsConflictAnswers(t *testing.T) {
	source := queueDirectorySource(t)
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	dest.addFile("/dest", "a.txt", "old-a", 0o644)
	dest.addFile("/dest", "b.txt", "old-b", 0o644)
	repo := &fakeQueueRepository{}

	queue := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(1), WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(TaskSpec{
		Kind:    domainsftp.OpCopy,
		Source:  source,
		Dest:    dest,
		Sources: []string{"/src/a.txt", "/src/b.txt", "/src/c.txt"},
		DestDir: "/dest",
		// A third source has no conflict and gates the copy, so the row can be
		// paused while the seeded answers are still on it.
		Conflict: ConflictAsk,
		Answers: map[string]ConflictPolicy{
			"/src/a.txt": ConflictSkip,
			"/src/b.txt": ConflictReplace,
		},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-source.entered:
	case <-time.After(queueWait):
		task, _ := queue.Task(id)
		t.Fatalf("the copy never reached the gated file; task = %+v", task)
	}
	if err := queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	task := waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	if !task.SkipTargets["/src/a.txt"] {
		t.Errorf("skip answer was not seeded: %v", task.SkipTargets)
	}
	if !task.ReplaceTargets["/src/b.txt"] {
		t.Errorf("replace answer was not seeded: %v", task.ReplaceTargets)
	}
	saved := repo.savedTasks()
	if len(saved) != 1 || !saved[0].SkipTargets["/src/a.txt"] ||
		!saved[0].ReplaceTargets["/src/b.txt"] {
		t.Fatalf("persisted answers = %+v", saved)
	}
	close(source.gate)
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if got, _ := dest.file("/dest/a.txt"); string(got) != "old-a" {
		t.Errorf("skipped destination became %q", got)
	}
	if got, _ := dest.file("/dest/b.txt"); string(got) != "payload-b.txt" {
		t.Errorf("replaced destination became %q", got)
	}
	if got, _ := dest.file("/dest/c.txt"); string(got) != "payload-c.txt" {
		t.Errorf("unconflicted source became %q", got)
	}
}

// TestQueueReplaceSourceKeepsResumeState proves the phone's re-selection path
// swaps only the source side of the original row. The row keeps its identity,
// checkpoint, pinned targets, and conflict answers, and Resume then runs
// against the new source instead of a new task.
func TestQueueReplaceSourceKeepsResumeState(t *testing.T) {
	oldSource := queueDirectorySource(t)
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	repo := &fakeQueueRepository{}
	queue := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(1), WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(TaskSpec{
		Kind:     domainsftp.OpUpload,
		Source:   oldSource,
		Dest:     dest,
		Sources:  []string{"/src"},
		DestDir:  "/dest",
		Conflict: ConflictAsk,
		Answers:  map[string]ConflictPolicy{"/src/a.txt": ConflictReplace},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueGate(t, oldSource)
	if err := queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused := waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	if paused.DirectoryResumeCheckpoint == nil {
		t.Fatal("paused upload has no checkpoint")
	}

	replacement := newFakeFS("replacement")
	replacement.addDir("/picked")
	replacement.addFile("/picked", "fresh.txt", "fresh", 0o644)
	if err := queue.ReplaceSource(id, replacement, []string{"/picked"}); err != nil {
		t.Fatalf("replace source: %v", err)
	}
	replaced, ok := queue.Task(id)
	if !ok {
		t.Fatal("the row vanished")
	}
	if replaced.ID != id {
		t.Errorf("row ID changed to %q", replaced.ID)
	}
	if replaced.Status != domainsftp.TaskInterrupted {
		t.Errorf("status = %q, want interrupted", replaced.Status)
	}
	if len(replaced.Sources) != 1 || replaced.Sources[0] != "/picked" {
		t.Errorf("sources = %v", replaced.Sources)
	}
	if replaced.DirectoryResumeCheckpoint == nil ||
		replaced.DirectoryResumeCheckpoint.CompletedEntries !=
			paused.DirectoryResumeCheckpoint.CompletedEntries {
		t.Errorf("checkpoint changed: %+v", replaced.DirectoryResumeCheckpoint)
	}
	if !replaced.ReplaceTargets["/src/a.txt"] {
		t.Errorf("conflict answer was lost: %v", replaced.ReplaceTargets)
	}
	if replaced.DestDir != "/dest" || replaced.DestHost != "" {
		t.Errorf("target changed: dir=%q host=%q", replaced.DestDir, replaced.DestHost)
	}

	close(oldSource.gate)
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if got, ok := dest.file("/dest/picked/fresh.txt"); !ok || string(got) != "fresh" {
		t.Errorf("replacement source was not copied: %q ok=%v", got, ok)
	}
	if _, ok := dest.file("/dest/src/d.txt"); ok {
		t.Error("the old source was still copied")
	}
}

// TestQueueReplaceSourceReleasesOldReservations covers the persisted
// re-selection hazard: a paused row owns the old source's destination name.
// Once the source is replaced with a differently named one, that reservation
// must be dropped and the row must claim only the new name. Otherwise another
// transfer can never take the old path, and a stale claim makes Resume fail
// with a reservation error after that path is legitimately taken.
func TestQueueReplaceSourceReleasesOldReservations(t *testing.T) {
	oldSource := newGatedFS("old", "/src/a.txt")
	oldSource.addDir("/src")
	oldSource.addFile("/src", "a.txt", "old-bytes", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	repo := &fakeQueueRepository{}
	queue := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(2), WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, queue)

	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: oldSource, Dest: dest,
		Sources: []string{"/src/a.txt"}, DestDir: "/dest", Conflict: ConflictReplace,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueGate(t, oldSource)
	if err := queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	close(oldSource.gate)

	replacement := newFakeFS("replacement")
	replacement.addDir("/picked")
	replacement.addFile("/picked", "b.txt", "new-bytes", 0o644)
	if err := queue.ReplaceSource(id, replacement, []string{"/picked/b.txt"}); err != nil {
		t.Fatalf("replace source: %v", err)
	}

	// Another transfer may now take the old destination name. Without the
	// release it is refused because the interrupted row still reserves it.
	otherSource := newGatedFS("other", "/other/a.txt")
	otherSource.addDir("/other")
	otherSource.addFile("/other", "a.txt", "other-bytes", 0o644)
	other, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: otherSource, Dest: dest,
		Sources: []string{"/other/a.txt"}, DestDir: "/dest", Conflict: ConflictReplace,
	})
	if err != nil {
		t.Fatalf("the old destination name was still reserved: %v", err)
	}
	waitQueueGate(t, otherSource)

	// Resume must claim only /dest/b.txt; the other transfer holds /dest/a.txt.
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume after another transfer took the old path: %v", err)
	}
	close(otherSource.gate)
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	waitQueueTask(t, queue, other, domainsftp.TaskCompleted)
	if got, _ := dest.file("/dest/b.txt"); string(got) != "new-bytes" {
		t.Errorf("replacement destination = %q", got)
	}
	if got, _ := dest.file("/dest/a.txt"); string(got) != "other-bytes" {
		t.Errorf("old destination = %q", got)
	}
}

// TestQueueResolveConflictRemembersRename proves a live Rename answer becomes
// durable state like Overwrite and Skip, so a resumed row reuses it instead of
// prompting again.
func TestQueueResolveConflictRemembersRename(t *testing.T) {
	source := newGatedFS("source", "/src/b.txt")
	source.addDir("/src")
	source.addFile("/src", "a.txt", "payload", 0o644)
	source.addFile("/src", "b.txt", "second", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	dest.addFile("/dest", "a.txt", "old", 0o644)
	repo := &fakeQueueRepository{}
	queue := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(1), WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/src/a.txt", "/src/b.txt"}, DestDir: "/dest", Conflict: ConflictAsk,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskAttention)
	if err := queue.ResolveConflict(id, ConflictDuplicate, false); err != nil {
		t.Fatalf("resolve rename: %v", err)
	}
	// The second source gates, so the row can be paused while the Rename
	// answer is still on it (completion clears the answer maps).
	waitQueueGate(t, source)
	if err := queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused := waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	if !paused.DuplicateTargets["/src/a.txt"] {
		t.Errorf("rename answer was not recorded: %v", paused.DuplicateTargets)
	}
	persisted := false
	for _, saved := range repo.savedTasks() {
		if saved.ID == id && saved.DuplicateTargets["/src/a.txt"] {
			persisted = true
			break
		}
	}
	if !persisted {
		t.Errorf("rename answer was not persisted: %+v", repo.savedTasks())
	}
	close(source.gate)
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if got, _ := dest.file("/dest/a (copy).txt"); string(got) != "payload" {
		t.Errorf("renamed destination = %q", got)
	}
}

// checkpointTree builds a gated source tree with the four-file layout the
// checkpoint tests use. Only the root path varies between the original and the
// re-selected cache copy.
func checkpointTree(t *testing.T, name, root, gatePath string) *gatedFS {
	t.Helper()
	tree := newGatedFS(name, gatePath)
	tree.addDir(root)
	for _, file := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		tree.addFile(root, file, "payload-"+file, 0o644)
	}
	return tree
}

func pauseAtCheckpoint(t *testing.T, queue *Queue, source *gatedFS, spec TaskSpec) domainsftp.TaskID {
	t.Helper()
	id, err := queue.Enqueue(spec)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueGate(t, source)
	if err := queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	return id
}

// TestQueueReplaceSourceRebasesCheckpointToNewPath is the Android repro: the
// cached tree moves from /cache/batch to /cache2/batch, so every saved
// checkpoint identity differs. An explicit re-selection must rebase the
// completed prefix onto the replacement and skip it, not open a.txt and b.txt
// again.
func TestQueueReplaceSourceRebasesCheckpointToNewPath(t *testing.T) {
	source := checkpointTree(t, "source", "/cache/batch", "/cache/batch/c.txt")
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	repo := &fakeQueueRepository{}
	queue := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(1), WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id := pauseAtCheckpoint(t, queue, source, TaskSpec{
		Kind: domainsftp.OpUpload, Source: source, Dest: dest,
		Sources: []string{"/cache/batch"}, DestDir: "/dest", Conflict: ConflictReplace,
	})
	paused := waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	if paused.DirectoryResumeCheckpoint == nil ||
		paused.DirectoryResumeCheckpoint.CompletedEntries != 3 {
		t.Fatalf("checkpoint = %+v", paused.DirectoryResumeCheckpoint)
	}
	close(source.gate)

	replacement := checkpointTree(t, "replacement", "/cache2/batch", "")
	if err := queue.ReplaceSource(id, replacement, []string{"/cache2/batch"}); err != nil {
		t.Fatalf("replace source: %v", err)
	}
	replaced, _ := queue.Task(id)
	if !replaced.CheckpointRebase {
		t.Errorf("re-selection did not mark the checkpoint for rebase")
	}
	if replaced.DirectoryResumeCheckpoint == nil ||
		replaced.DirectoryResumeCheckpoint.CompletedEntries != 3 {
		t.Errorf("re-selection dropped the checkpoint: %+v", replaced.DirectoryResumeCheckpoint)
	}
	if saved := repo.savedTasks(); len(saved) != 1 || !saved[0].CheckpointRebase {
		t.Errorf("persisted rebase marker = %+v", saved)
	}
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	done := waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if done.CheckpointRebase {
		t.Errorf("a completed row kept the rebase marker")
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if got := replacement.openCount("/cache2/batch/" + name); got != 0 {
			t.Errorf("%s opened %d times; the rebased prefix was copied again", name, got)
		}
	}
	for _, name := range []string{"c.txt", "d.txt"} {
		if got := replacement.openCount("/cache2/batch/" + name); got != 1 {
			t.Errorf("%s opened %d times, want 1", name, got)
		}
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if got, ok := dest.file("/dest/batch/" + name); !ok || string(got) != "payload-"+name {
			t.Errorf("%s = %q, exists=%v", name, got, ok)
		}
	}
}

// TestQueueReplaceSourceKeepsAPendingRebase proves a repeated re-selection to
// the same replacement does not cancel a rebase the checkpoint still needs:
// the checkpoint names the original source until the next attempt consumes the
// marker, so clearing it early would re-copy the completed prefix.
func TestQueueReplaceSourceKeepsAPendingRebase(t *testing.T) {
	source := checkpointTree(t, "source", "/cache/batch", "/cache/batch/c.txt")
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	repo := &fakeQueueRepository{}
	queue := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(1), WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id := pauseAtCheckpoint(t, queue, source, TaskSpec{
		Kind: domainsftp.OpUpload, Source: source, Dest: dest,
		Sources: []string{"/cache/batch"}, DestDir: "/dest", Conflict: ConflictReplace,
	})
	waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	close(source.gate)

	replacement := checkpointTree(t, "replacement", "/cache2/batch", "")
	for attempt := 1; attempt <= 2; attempt++ {
		if err := queue.ReplaceSource(id, replacement, []string{"/cache2/batch"}); err != nil {
			t.Fatalf("replace source (attempt %d): %v", attempt, err)
		}
	}
	replaced, _ := queue.Task(id)
	if !replaced.CheckpointRebase {
		t.Errorf("a repeated re-selection cancelled the pending rebase marker")
	}
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	for _, name := range []string{"a.txt", "b.txt"} {
		if got := replacement.openCount("/cache2/batch/" + name); got != 0 {
			t.Errorf("%s opened %d times; the pending rebase was lost", name, got)
		}
	}
	for _, name := range []string{"c.txt", "d.txt"} {
		if got := replacement.openCount("/cache2/batch/" + name); got != 1 {
			t.Errorf("%s opened %d times, want 1", name, got)
		}
	}
}

// TestQueueRebaseMarkerSurvivesRestart proves the rebase decision is durable:
// a process restart between re-selection and Resume must still skip the
// completed prefix instead of verifying identities that name the old path.
func TestQueueRebaseMarkerSurvivesRestart(t *testing.T) {
	source := checkpointTree(t, "source", "/cache/batch", "/cache/batch/c.txt")
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	repo := &fakeQueueRepository{}
	queue := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(1), WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id := pauseAtCheckpoint(t, queue, source, TaskSpec{
		Kind: domainsftp.OpUpload, Source: source, Dest: dest,
		Sources: []string{"/cache/batch"}, DestDir: "/dest", Conflict: ConflictReplace,
	})
	waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	close(source.gate)
	firstReplacement := checkpointTree(t, "first", "/cache2/batch", "")
	if err := queue.ReplaceSource(id, firstReplacement, []string{"/cache2/batch"}); err != nil {
		t.Fatalf("replace source: %v", err)
	}
	queue.Shutdown()

	// A new process starts over the same history. The row comes back
	// interrupted; Reattach binds the same replacement request and the durable
	// marker must still rebase the prefix.
	next := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(1), WithQueueRepository(repo), WithQueueRetryDelay(0))
	if err := next.Start(context.Background()); err != nil {
		t.Fatalf("restart queue: %v", err)
	}
	t.Cleanup(next.Shutdown)
	restored, ok := next.Task(id)
	if !ok || !restored.CheckpointRebase || !restored.ReconnectRequired {
		t.Fatalf("restored row = %+v, exists=%v", restored, ok)
	}
	secondReplacement := checkpointTree(t, "second", "/cache2/batch", "")
	if err := next.Reattach(id, TaskSpec{
		Kind: domainsftp.OpUpload, Source: secondReplacement, Dest: dest,
		Sources: []string{"/cache2/batch"}, DestDir: "/dest", Conflict: ConflictReplace,
	}); err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if err := next.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitQueueTask(t, next, id, domainsftp.TaskCompleted)
	for _, name := range []string{"a.txt", "b.txt"} {
		if got := secondReplacement.openCount("/cache2/batch/" + name); got != 0 {
			t.Errorf("%s opened %d times after the restart; the rebase marker was lost", name, got)
		}
	}
	for _, name := range []string{"c.txt", "d.txt"} {
		if got := secondReplacement.openCount("/cache2/batch/" + name); got != 1 {
			t.Errorf("%s opened %d times, want 1", name, got)
		}
	}
}

// TestQueueReplaceSourceSamePathKeepsVerifiedCheckpoint is the control: an
// explicit re-selection at the same path uses the normal digest verification,
// not the rebase shortcut, and still skips the completed prefix.
func TestQueueReplaceSourceSamePathKeepsVerifiedCheckpoint(t *testing.T) {
	source := checkpointTree(t, "source", "/cache/batch", "/cache/batch/c.txt")
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	repo := &fakeQueueRepository{}
	queue := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(1), WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id := pauseAtCheckpoint(t, queue, source, TaskSpec{
		Kind: domainsftp.OpUpload, Source: source, Dest: dest,
		Sources: []string{"/cache/batch"}, DestDir: "/dest", Conflict: ConflictReplace,
	})
	waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	close(source.gate)

	replacement := checkpointTree(t, "replacement", "/cache/batch", "")
	if err := queue.ReplaceSource(id, replacement, []string{"/cache/batch"}); err != nil {
		t.Fatalf("replace source: %v", err)
	}
	replaced, _ := queue.Task(id)
	if replaced.CheckpointRebase {
		t.Errorf("same-path re-selection took the rebase shortcut")
	}
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	for _, name := range []string{"a.txt", "b.txt"} {
		if got := replacement.openCount("/cache/batch/" + name); got != 0 {
			t.Errorf("%s opened %d times; the verified checkpoint was not used", name, got)
		}
	}
	for _, name := range []string{"c.txt", "d.txt"} {
		if got := replacement.openCount("/cache/batch/" + name); got != 1 {
			t.Errorf("%s opened %d times, want 1", name, got)
		}
	}
}

// TestQueueReplaceSourceDropsShortenedSelectionState is the shrinking
// selection repro: the second source had a Rename-pinned destination, then the
// selection dropped it. Its answer and pinned target must go with it, so the
// row no longer claims the renamed name and another Transfer may take it.
func TestQueueReplaceSourceDropsShortenedSelectionState(t *testing.T) {
	first := newGatedFS("first", "/gone/tree/c.txt")
	first.addDir("/src")
	first.addFile("/src", "a.txt", "a-bytes", 0o644)
	first.addDir("/gone/tree")
	first.addFile("/gone/tree", "c.txt", "c-bytes", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	dest.addDir("/dest/tree")
	dest.addFile("/dest", "c.txt", "old-c", 0o644)
	repo := &fakeQueueRepository{}
	queue := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(2), WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: first, Dest: dest,
		Sources: []string{"/src/a.txt", "/gone/tree"}, DestDir: "/dest",
		Conflict: ConflictAsk,
		Answers:  map[string]ConflictPolicy{"/gone/tree": ConflictDuplicate},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueGate(t, first)
	if err := queue.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused := waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	if paused.Targets["/gone/tree"] != "/dest/tree (copy)" {
		t.Fatalf("rename target = %v", paused.Targets)
	}
	close(first.gate)

	picked := newFakeFS("picked")
	picked.addDir("/picked")
	picked.addFile("/picked", "a.txt", "new-a", 0o644)
	if err := queue.ReplaceSource(id, picked, []string{"/picked/a.txt"}); err != nil {
		t.Fatalf("replace source: %v", err)
	}
	replaced, _ := queue.Task(id)
	if _, ok := replaced.Targets["/gone/tree"]; ok {
		t.Errorf("a dropped source kept its pinned target: %v", replaced.Targets)
	}
	if replaced.DuplicateTargets["/gone/tree"] {
		t.Errorf("a dropped source kept its rename answer: %v", replaced.DuplicateTargets)
	}

	// Another Transfer takes the dropped rename name and stays live.
	other := newGatedFS("other", "/other/tree (copy)/c.txt")
	other.addDir("/other/tree (copy)")
	other.addFile("/other/tree (copy)", "c.txt", "other-c", 0o644)
	otherID, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: other, Dest: dest,
		Sources: []string{"/other/tree (copy)"}, DestDir: "/dest",
		Conflict: ConflictReplace,
	})
	if err != nil {
		t.Fatalf("another transfer could not take the dropped name: %v", err)
	}
	waitQueueGate(t, other)
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume after the dropped name was taken: %v", err)
	}
	close(other.gate)
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	waitQueueTask(t, queue, otherID, domainsftp.TaskCompleted)
	if got, ok := dest.file("/dest/a.txt"); !ok || string(got) != "new-a" {
		t.Errorf("replacement = %q, exists=%v; tasks = %+v", got, ok, queue.Tasks())
	}
}

// TestQueueReplaceSourceKeepsKnownTargetOnSameBasename covers the spurious
// re-prompt: a paused merge already pinned /dest/tree before the destination
// gained a same-named entry. Re-selection under a new parent with the same
// basename must carry that known target, not ask for a conflict the row had
// already resolved.
func TestQueueReplaceSourceKeepsKnownTargetOnSameBasename(t *testing.T) {
	source := checkpointTree(t, "source", "/cache/tree", "/cache/tree/c.txt")
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	repo := &fakeQueueRepository{}
	queue := NewQueue(NewService(nil, nil),
		WithQueueConcurrency(1), WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id := pauseAtCheckpoint(t, queue, source, TaskSpec{
		Kind: domainsftp.OpUpload, Source: source, Dest: dest,
		Sources: []string{"/cache/tree"}, DestDir: "/dest", Conflict: ConflictAsk,
	})
	paused := waitQueueTask(t, queue, id, domainsftp.TaskPaused)
	if paused.Targets["/cache/tree"] != "/dest/tree" {
		t.Fatalf("known target = %v", paused.Targets)
	}
	close(source.gate)
	// The destination now has the same name the row already owns.
	dest.addDir("/dest/tree")

	replacement := checkpointTree(t, "replacement", "/cache2/tree", "")
	if err := queue.ReplaceSource(id, replacement, []string{"/cache2/tree"}); err != nil {
		t.Fatalf("replace source: %v", err)
	}
	replaced, _ := queue.Task(id)
	if replaced.Targets["/cache2/tree"] != "/dest/tree" {
		t.Errorf("known target was not remapped: %v", replaced.Targets)
	}
	if err := queue.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if got, ok := dest.file("/dest/tree/" + name); !ok || string(got) != "payload-"+name {
			t.Errorf("%s = %q, exists=%v", name, got, ok)
		}
	}
}

// TestRemapSourceAnswersPairsOnlyChangedSources proves the re-selection remap
// is positional: only the source that moved gets its answer moved, while an
// unchanged sibling keeps its decision and pinned name.
func TestRemapSourceAnswersPairsOnlyChangedSources(t *testing.T) {
	task := domainsftp.Task{
		ReplaceTargets:   map[string]bool{"/old/a.txt": true},
		SkipTargets:      map[string]bool{"/same/b.txt": true},
		DuplicateTargets: map[string]bool{"/old/c.txt": true},
		Targets: map[string]string{
			"/old/a.txt": "/dest/a.txt", "/same/b.txt": "/dest/b.txt",
			"/old/c.txt": "/dest/c (copy).txt", "/gone/d.txt": "/dest/d.txt",
		},
	}
	remapSourceAnswers(&task,
		[]string{"/old/a.txt", "/same/b.txt", "/old/c.txt", "/gone/d.txt"},
		[]string{"/new/a.txt", "/same/b.txt", "/new/c.txt"})
	if !task.ReplaceTargets["/new/a.txt"] || task.ReplaceTargets["/old/a.txt"] {
		t.Errorf("replace answers = %v", task.ReplaceTargets)
	}
	if !task.SkipTargets["/same/b.txt"] {
		t.Errorf("an unchanged source lost its skip answer: %v", task.SkipTargets)
	}
	if !task.DuplicateTargets["/new/c.txt"] || task.DuplicateTargets["/old/c.txt"] {
		t.Errorf("duplicate answers = %v", task.DuplicateTargets)
	}
	if task.Targets["/new/a.txt"] != "/dest/a.txt" {
		t.Errorf("an ordinary known target was not carried to the new source: %v", task.Targets)
	}
	if _, ok := task.Targets["/old/c.txt"]; ok {
		t.Errorf("a rename-pinned target survived the move: %v", task.Targets)
	}
	if task.Targets["/same/b.txt"] != "/dest/b.txt" {
		t.Errorf("an unchanged source lost its pinned target: %v", task.Targets)
	}
	for _, dropped := range []string{"/gone/d.txt"} {
		if _, ok := task.Targets[dropped]; ok {
			t.Errorf("a dropped source kept its pinned target: %v", task.Targets)
		}
	}
}

// TestQueueReplaceSourceRefusesFinishedRows keeps the phone's Re-select action
// from reviving a row the user already finished or cancelled.
func TestQueueReplaceSourceRefusesFinishedRows(t *testing.T) {
	source := newFakeFS("source")
	source.addDir("/src")
	source.addFile("/src", "a.txt", "a", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/dest")
	queue := NewQueue(NewService(nil, nil))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: []string{"/src/a.txt"}, DestDir: "/dest", Conflict: ConflictReplace,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if err := queue.ReplaceSource(id, source, []string{"/src/a.txt"}); err == nil {
		t.Fatal("a completed row accepted a source replacement")
	}
	if err := queue.ReplaceSource(domainsftp.TaskID("absent"), source, []string{"/src/a.txt"}); err == nil {
		t.Fatal("an absent row accepted a source replacement")
	}
	if err := queue.ReplaceSource(id, nil, []string{"/src/a.txt"}); err == nil {
		t.Fatal("a nil source was accepted")
	}
}
