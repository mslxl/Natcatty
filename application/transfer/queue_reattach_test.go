package transfer

import (
	"context"
	"errors"
	"testing"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// TestQueueReattachHostRebindsOnlyTheHostsInterruptedRows proves the reconnect
// seam matches the Host the user just reconnected: the durable endpoint
// identity picks the row, the fresh filesystems are bound, and another Host's
// row keeps telling the user to reconnect.
func TestQueueReattachHostRebindsOnlyTheHostsInterruptedRows(t *testing.T) {
	now := time.Now()
	repo := &fakeQueueRepository{stored: []domainsftp.Task{
		{
			ID: "h1-row", Kind: domainsftp.OpUpload, Status: domainsftp.TaskInterrupted,
			DestHost: "h1", Sources: []string{"/src/a.txt"}, DestDir: "/dest",
			ReconnectRequired: true, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "lost-row", Kind: domainsftp.OpDownload, Status: domainsftp.TaskFailed,
			SourceHost: "h1", Error: string(KindConnection), Sources: []string{"/src/lost.txt"},
			DestDir: "/dest", ReconnectRequired: true,
			CreatedAt: now.Add(time.Second), UpdatedAt: now,
		},
		{
			ID: "stale-failure", Kind: domainsftp.OpUpload, Status: domainsftp.TaskFailed,
			DestHost: "h1", Error: string(KindExists), Err: errors.New("destination exists"),
			Sources: []string{"/src/stale.txt"}, DestDir: "/dest",
			CreatedAt: now.Add(2 * time.Second), UpdatedAt: now,
		},
		{
			ID: "h2-row", Kind: domainsftp.OpUpload, Status: domainsftp.TaskInterrupted,
			DestHost: "h2", Sources: []string{"/src/b.txt"}, DestDir: "/dest",
			ReconnectRequired: true, CreatedAt: now.Add(3 * time.Second), UpdatedAt: now,
		},
		{
			ID: "done", Kind: domainsftp.OpUpload, Status: domainsftp.TaskCompleted,
			DestHost: "h1", Sources: []string{"/src/c.txt"}, DestDir: "/dest",
			CreatedAt: now.Add(4 * time.Second), UpdatedAt: now,
		},
	}}
	queue := NewQueue(NewService(nil, nil), WithQueueRepository(repo))
	startTestQueue(t, queue)

	remote := newFakeFS("h1")
	local := newFakeFS("local")
	count, err := queue.ReattachHost(context.Background(), HostReattach{
		Host:   "h1",
		Remote: remote,
		OpenLocal: func(context.Context) (FileSystem, error) {
			return local, nil
		},
	})
	if err != nil {
		t.Fatalf("ReattachHost: %v", err)
	}
	if count != 2 {
		t.Errorf("ReattachHost bound %d rows, want the h1 restart and transport-loss rows", count)
	}
	rebound, _ := queue.Task("h1-row")
	if rebound.ReconnectRequired || rebound.Status != domainsftp.TaskInterrupted {
		t.Errorf("the reconnected Host's row = %+v, want interrupted without reconnect required", rebound)
	}
	if err := queue.Resume("h1-row"); err != nil {
		t.Errorf("Resume after ReattachHost = %v, want the checkpoint to continue", err)
	}
	lost, _ := queue.Task("lost-row")
	if lost.ReconnectRequired || lost.Status != domainsftp.TaskInterrupted {
		t.Errorf("the transport-loss row = %+v, want interrupted without reconnect required", lost)
	}
	if err := queue.Resume("lost-row"); err != nil {
		t.Errorf("Resume on the transport-loss row = %v, want a live retry", err)
	}
	other, _ := queue.Task("h2-row")
	if !other.ReconnectRequired || other.Status != domainsftp.TaskInterrupted {
		t.Errorf("another Host's row = %+v, want it still reconnect-required", other)
	}
	if err := queue.Resume("h2-row"); err != domainsftp.ErrTaskUnavailable {
		t.Errorf("Resume on another Host's row = %v, want ErrTaskUnavailable", err)
	}
	done, _ := queue.Task("done")
	if done.Status != domainsftp.TaskCompleted {
		t.Errorf("a completed row was touched: %+v", done)
	}
	stale, _ := queue.Task("stale-failure")
	if stale.Status != domainsftp.TaskFailed || stale.Error != string(KindExists) {
		t.Errorf("an ordinary failure was revived by a reconnect: %+v", stale)
	}
	if err := queue.Resume("stale-failure"); err == nil {
		t.Error("Resume on an ordinary failure was accepted; it has no live request")
	}
}

// TestQueueReattachHostInfersLegacyEndpoints proves a row persisted before
// SourceHost/DestHost existed still reattaches in the right direction. The
// queue lanes recorded the request order, so an upload's Host is its
// destination and a download's Host is its source; getting this backwards
// would resolve each path on the wrong Backing.
func TestQueueReattachHostInfersLegacyEndpoints(t *testing.T) {
	now := time.Now()
	repo := &fakeQueueRepository{stored: []domainsftp.Task{
		{
			ID: "legacy-upload", Kind: domainsftp.OpUpload, Status: domainsftp.TaskInterrupted,
			ResourceKeys: []domainsftp.ResourceKey{
				domainsftp.LocalResourceKey(), domainsftp.HostResourceKey("h1"),
			},
			Sources: []string{"/src/up.txt"}, DestDir: "/dest",
			ReconnectRequired: true, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "legacy-download", Kind: domainsftp.OpDownload, Status: domainsftp.TaskInterrupted,
			ResourceKeys: []domainsftp.ResourceKey{
				domainsftp.HostResourceKey("h1"), domainsftp.LocalResourceKey(),
			},
			Sources: []string{"/src/down.txt"}, DestDir: "/dest",
			ReconnectRequired: true, CreatedAt: now.Add(time.Second), UpdatedAt: now,
		},
		{
			ID: "legacy-copy", Kind: domainsftp.OpCopy, Status: domainsftp.TaskInterrupted,
			ResourceKeys: []domainsftp.ResourceKey{
				domainsftp.HostResourceKey("h1"), domainsftp.HostResourceKey("h2"),
			},
			Sources: []string{"/src/copy.txt"}, DestDir: "/dest",
			ReconnectRequired: true, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now,
		},
		{
			// Enqueue collapses both endpoints of a same-Backing copy onto one lane.
			ID: "legacy-same-host", Kind: domainsftp.OpCopy, Status: domainsftp.TaskInterrupted,
			ResourceKeys: []domainsftp.ResourceKey{domainsftp.HostResourceKey("h1")},
			Sources:      []string{"/src/inner.txt"}, DestDir: "/dest",
			ReconnectRequired: true, CreatedAt: now.Add(3 * time.Second), UpdatedAt: now,
		},
	}}
	queue := NewQueue(NewService(nil, nil), WithQueueRepository(repo))
	startTestQueue(t, queue)

	remote := newFakeFS("h1")
	remote.addDir("/src")
	remote.addFile("/src", "down.txt", "down-data", 0o644)
	remote.addFile("/src", "copy.txt", "copy-data", 0o644)
	remote.addFile("/src", "inner.txt", "inner-data", 0o644)
	remote.addDir("/dest")
	peer := newFakeFS("h2")
	peer.addDir("/dest")
	local := newFakeFS("local")
	local.addDir("/src")
	local.addFile("/src", "up.txt", "up-data", 0o644)
	local.addDir("/dest")

	count, err := queue.ReattachHost(context.Background(), HostReattach{
		Host:   "h1",
		Remote: remote,
		Peers:  map[string]FileSystem{"h2": peer},
		OpenLocal: func(context.Context) (FileSystem, error) {
			return local, nil
		},
	})
	if err != nil {
		t.Fatalf("ReattachHost: %v", err)
	}
	if count != 4 {
		t.Fatalf("ReattachHost bound %d legacy rows, want all four", count)
	}
	for _, id := range []domainsftp.TaskID{
		"legacy-upload", "legacy-download", "legacy-copy", "legacy-same-host",
	} {
		if err := queue.Resume(id); err != nil {
			t.Fatalf("Resume %s: %v", id, err)
		}
		waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	}

	if data, ok := remote.file("/dest/up.txt"); !ok || string(data) != "up-data" {
		t.Errorf("the legacy upload did not write its local source to the Host: %q ok=%v", data, ok)
	}
	if local.has("/dest/up.txt") {
		t.Error("the legacy upload wrote its destination locally; the direction is inverted")
	}
	if data, ok := local.file("/dest/down.txt"); !ok || string(data) != "down-data" {
		t.Errorf("the legacy download did not write the Host source locally: %q ok=%v", data, ok)
	}
	if remote.has("/dest/down.txt") {
		t.Error("the legacy download wrote its destination on the Host; the direction is inverted")
	}
	if data, ok := peer.file("/dest/copy.txt"); !ok || string(data) != "copy-data" {
		t.Errorf("the legacy Host-to-Host copy did not reach the peer Host: %q ok=%v", data, ok)
	}
	if data, ok := remote.file("/dest/inner.txt"); !ok || string(data) != "inner-data" {
		t.Errorf("the legacy same-Host copy did not stay on the reconnected Host: %q ok=%v", data, ok)
	}
	upload, _ := queue.Task("legacy-upload")
	if upload.DestHost != "h1" || upload.SourceHost != "" {
		t.Errorf("the legacy upload endpoints = %q -> %q, want local -> h1",
			upload.SourceHost, upload.DestHost)
	}
	download, _ := queue.Task("legacy-download")
	if download.SourceHost != "h1" || download.DestHost != "" {
		t.Errorf("the legacy download endpoints = %q -> %q, want h1 -> local",
			download.SourceHost, download.DestHost)
	}
	copied, _ := queue.Task("legacy-copy")
	if copied.SourceHost != "h1" || copied.DestHost != "h2" {
		t.Errorf("the legacy copy endpoints = %q -> %q, want h1 -> h2",
			copied.SourceHost, copied.DestHost)
	}
	same, _ := queue.Task("legacy-same-host")
	if same.SourceHost != "h1" || same.DestHost != "h1" {
		t.Errorf("the legacy same-Host copy endpoints = %q -> %q, want h1 -> h1",
			same.SourceHost, same.DestHost)
	}
}

// TestQueueReattachHostResumeSkipsTheCheckpointAndRetryReplays proves the two
// production continuations stay distinct: Resume continues from the
// checkpoint without opening a completed file, while Retry replays every file.
func TestQueueReattachHostResumeSkipsTheCheckpointAndRetryReplays(t *testing.T) {
	t.Run("resume continues from the checkpoint", func(t *testing.T) {
		queue, id, source, dest, attach := pausedHostUpload(t)
		close(source.gate)
		if _, err := queue.ReattachHost(context.Background(), attach); err != nil {
			t.Fatalf("ReattachHost: %v", err)
		}
		rebound, _ := queue.Task(id)
		if rebound.ReconnectRequired || rebound.Status != domainsftp.TaskInterrupted {
			t.Fatalf("the rebound row = %+v", rebound)
		}
		if err := queue.Resume(id); err != nil {
			t.Fatalf("Resume after ReattachHost: %v", err)
		}
		waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
		if got := source.openCount("/src/a.txt"); got != 1 {
			t.Errorf("a.txt opens = %d; the reconnect re-copied the checkpointed prefix", got)
		}
		if got := source.openCount("/src/b.txt"); got != 1 {
			t.Errorf("b.txt opens = %d; the reconnect re-copied the checkpointed prefix", got)
		}
		for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
			if !dest.has("/dest/src/" + name) {
				t.Errorf("%s missing after the reconnected resume", name)
			}
		}
	})

	t.Run("retry replays every file", func(t *testing.T) {
		queue, id, source, dest, attach := pausedHostUpload(t)
		close(source.gate)
		if _, err := queue.ReattachHost(context.Background(), attach); err != nil {
			t.Fatalf("ReattachHost: %v", err)
		}
		if err := queue.Retry(id); err != nil {
			t.Fatalf("Retry after ReattachHost: %v", err)
		}
		waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
		// The paused attempt opened a.txt and b.txt once; the replay opens them
		// again. A resume would leave both counts at one.
		if got := source.openCount("/src/a.txt"); got != 2 {
			t.Errorf("a.txt opens = %d; Retry did not replay the checkpointed prefix", got)
		}
		if got := source.openCount("/src/b.txt"); got != 2 {
			t.Errorf("b.txt opens = %d; Retry did not replay the checkpointed prefix", got)
		}
		for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
			if !dest.has("/dest/src/" + name) {
				t.Errorf("%s missing after the replayed retry", name)
			}
		}
	})
}

// pausedHostUpload pauses an upload from a Host at its third file, restarts
// the queue over the durable history, and returns the restored queue, the row,
// the gated Host filesystem, its local destination, and the reconnect seam.
func pausedHostUpload(t *testing.T) (*Queue, domainsftp.TaskID, *gatedFS, *fakeFS, HostReattach) {
	t.Helper()
	source := queueDirectorySource(t)
	dest := newFakeFS("local")
	dest.addDir("/dest")
	repo := &fakeQueueRepository{}

	first := NewQueue(NewService(nil, nil),
		WithQueueRepository(repo), WithQueueRetryDelay(0))
	if err := first.Start(context.Background()); err != nil {
		t.Fatalf("start first queue: %v", err)
	}
	spec := queueDirectorySpec(source, dest)
	spec.SourceHost = "h1"
	id, err := first.Enqueue(spec)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueGate(t, source)
	if err := first.Pause(id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused := waitQueueTask(t, first, id, domainsftp.TaskPaused)
	if paused.DirectoryResumeCheckpoint == nil ||
		paused.DirectoryResumeCheckpoint.CompletedEntries != 3 {
		t.Fatalf("the paused row has checkpoint %+v", paused.DirectoryResumeCheckpoint)
	}
	first.Shutdown()

	second := NewQueue(NewService(nil, nil),
		WithQueueRepository(repo), WithQueueRetryDelay(0))
	startTestQueue(t, second)
	restored, ok := second.Task(id)
	if !ok {
		t.Fatal("the row vanished across the restart")
	}
	if restored.Status != domainsftp.TaskInterrupted || !restored.ReconnectRequired {
		t.Fatalf("the restored row = %+v", restored)
	}
	if restored.SourceHost != "h1" {
		t.Fatalf("the restored row lost its endpoint identity: %+v", restored)
	}
	if err := second.Resume(id); err != domainsftp.ErrTaskUnavailable {
		t.Fatalf("Resume before the Host is reconnected = %v, want ErrTaskUnavailable", err)
	}
	attach := HostReattach{
		Host:   "h1",
		Remote: source,
		OpenLocal: func(context.Context) (FileSystem, error) {
			return dest, nil
		},
	}
	return second, id, source, dest, attach
}
