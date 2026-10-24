package transfer

import (
	"context"
	"io"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// burstFS yields one bounded read of its gate file, then blocks until the copy
// is cancelled. The bounded read is what leaves a partially written staging
// file in the destination when a process is killed mid-file; blocking forever
// afterwards models the dead process that never runs its cleanup.
type burstFS struct {
	*fakeFS
	gatePath string
	burst    int
	entered  chan struct{}
	once     sync.Once
}

func newBurstFS(name, gatePath string, burst int) *burstFS {
	return &burstFS{
		fakeFS:   newFakeFS(name),
		gatePath: gatePath,
		burst:    burst,
		entered:  make(chan struct{}),
	}
}

func (f *burstFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	reader, err := f.fakeFS.Open(ctx, p)
	if err != nil || p != f.gatePath {
		return reader, err
	}
	return &burstReader{
		inner:   reader,
		budget:  f.burst,
		entered: f.entered,
		once:    &f.once,
		ctx:     ctx,
	}, nil
}

type burstReader struct {
	inner   io.ReadCloser
	budget  int
	entered chan struct{}
	once    *sync.Once
	ctx     context.Context
}

func (r *burstReader) Read(p []byte) (int, error) {
	if r.budget <= 0 {
		r.once.Do(func() { close(r.entered) })
		<-r.ctx.Done()
		return 0, r.ctx.Err()
	}
	if len(p) > r.budget {
		p = p[:r.budget]
	}
	n, err := r.inner.Read(p)
	r.budget -= n
	if r.budget <= 0 {
		r.once.Do(func() { close(r.entered) })
	}
	return n, err
}

func (r *burstReader) Close() error { return r.inner.Close() }

// stageFiles lists the staging entries in one destination tree.
func stageFiles(files *fakeFS) []string {
	var out []string
	for _, full := range files.entriesAll() {
		if IsStagingName(path.Base(full)) {
			out = append(out, full)
		}
	}
	return out
}

// TestResumeRemovesTheKilledProcessesStagingFile reproduces the restart-resume
// leak: a directory upload dies with a partially written staging file for the
// in-flight file, and a resume of the durable checkpoint must not leave that
// file behind once the Transfer completes. Another task's live staging file in
// the same destination root must survive, because a resume may only touch the
// staging file its own durable task identity names.
func TestResumeRemovesTheKilledProcessesStagingFile(t *testing.T) {
	const (
		burst   = 32 * 1024
		payload = "c.bin"
	)
	addTree := func(files *fakeFS) {
		files.addDir("/src")
		files.addFile("/src", "a.bin", "payload-a", 0o644)
		files.addFile("/src", "b.bin", "payload-b", 0o644)
		files.addFile("/src", payload, strings.Repeat("c", 4*burst), 0o644)
		files.addFile("/src", "d.bin", "payload-d", 0o644)
	}

	source := newBurstFS("remote", "/src/"+payload, burst)
	addTree(source.fakeFS)
	dest := newFakeFS("local")
	dest.addDir("/dest")
	// A live Transfer of another task keeps its own staging file beside the
	// destination. Its name belongs to that task, so this resume must not
	// remove it.
	otherStage := dest.Join("/dest", ".netcatty-upload-cafebabe-other.bin.part")
	dest.addFile("/dest", path.Base(otherStage), "live-other", 0o644)

	// Process one dies while c.bin is in flight. Its durable history is
	// whatever reached the repository before the kill: the pinned targets and
	// the checkpoint for the completed directory and files.
	repo := &fakeQueueRepository{}
	first := NewQueue(NewService(nil, nil),
		WithQueueRepository(repo),
		WithQueueConcurrency(1),
		WithQueueRetryDelay(0),
	)
	if err := first.Start(context.Background()); err != nil {
		t.Fatalf("start the killed queue: %v", err)
	}
	// Keep the dead worker blocked so it never runs copyFile's cleanup, which
	// is exactly what a killed process cannot do. Its only remaining write is
	// to its own repository, which process two does not read.
	t.Cleanup(first.Shutdown)
	id, err := first.Enqueue(TaskSpec{
		Kind: domainsftp.OpUpload, Source: source, Dest: dest,
		Sources: []string{"/src"}, DestDir: "/dest", Conflict: ConflictReplace,
	})
	if err != nil {
		t.Fatalf("enqueue the directory upload: %v", err)
	}
	select {
	case <-source.entered:
	case <-time.After(queueWait):
		t.Fatalf("the upload never reached %s within %s", payload, queueWait)
	}
	orphan := ""
	for _, full := range stageFiles(dest) {
		if strings.HasSuffix(full, "-"+payload+".part") {
			orphan = full
		}
	}
	if orphan == "" {
		t.Fatal("the in-flight file left no staging file to reproduce the leak")
	}
	first.persist()
	killed := repo.savedTasks()
	if len(killed) != 1 {
		t.Fatalf("the killed process persisted %d rows, want one", len(killed))
	}
	if checkpoint := killed[0].DirectoryResumeCheckpoint; checkpoint == nil ||
		checkpoint.CompletedEntries != 3 {
		t.Fatalf("the killed process persisted checkpoint %+v, want the root and two files", checkpoint)
	}

	// Process two restarts over that durable history and reconnects, exactly
	// as the product does after the app relaunches.
	restarted := &fakeQueueRepository{stored: killed}
	second := NewQueue(NewService(nil, nil),
		WithQueueRepository(restarted),
		WithQueueRetryDelay(0),
	)
	startTestQueue(t, second)
	restored, ok := second.Task(id)
	if !ok {
		t.Fatal("the Transfer vanished across the restart")
	}
	if restored.Status != domainsftp.TaskInterrupted || !restored.ReconnectRequired {
		t.Fatalf("the restored row = %+v, want interrupted and reconnect-required", restored)
	}
	reconnected := newFakeFS("remote")
	addTree(reconnected)
	if err := second.Reattach(id, TaskSpec{
		Kind: domainsftp.OpUpload, Source: reconnected, Dest: dest,
		Sources: []string{"/src"}, DestDir: "/dest", Conflict: ConflictReplace,
		SourceHost: "h1", DestHost: "",
	}); err != nil {
		t.Fatalf("reattach the restored row: %v", err)
	}
	if err := second.Resume(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitQueueTask(t, second, id, domainsftp.TaskCompleted)

	for _, name := range []string{"a.bin", "b.bin", "c.bin", "d.bin"} {
		if _, ok := dest.file("/dest/src/" + name); !ok {
			t.Errorf("%s missing after the resumed upload", name)
		}
	}
	if dest.has(orphan) {
		t.Errorf("the killed process's staging file survived the resume: %s", orphan)
	}
	for _, full := range stageFiles(dest) {
		if full != otherStage {
			t.Errorf("an unrelated staging file survived the resume: %s", full)
		}
	}
	if data, ok := dest.file(otherStage); !ok || string(data) != "live-other" {
		t.Errorf("the other task's live staging file = %q, exists=%v; the resume removed it",
			data, ok)
	}
}
