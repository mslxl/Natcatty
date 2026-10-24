package sftp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

func TestTransferQueueRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	repository := NewTransferQueue(path)
	now := time.Unix(1_700_000_000, 0)
	tasks := []domainsftp.Task{
		{
			ID: "one", Kind: domainsftp.OpUpload, Status: domainsftp.TaskInterrupted,
			DisplayName: "report.txt", Sources: []string{"/tmp/report.txt"},
			DestDir: "/srv", ResourceKeys: []domainsftp.ResourceKey{domainsftp.HostResourceKey("h1")},
			BytesDone: 10, BytesTotal: 20, FilesDone: 1, FilesTotal: 2,
			CreatedAt: now, UpdatedAt: now, ReconnectRequired: true,
		},
		{
			ID: "two", Kind: domainsftp.OpDownload, Status: domainsftp.TaskCompleted,
			DisplayName: "b.bin", CreatedAt: now.Add(time.Second),
			FinishedAt: now.Add(2 * time.Second),
		},
	}
	if err := repository.SaveTasks(context.Background(), tasks); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := repository.LoadTasks(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d tasks, want 2", len(loaded))
	}
	if loaded[0].ID != "one" || loaded[0].Status != domainsftp.TaskInterrupted ||
		!loaded[0].ReconnectRequired || loaded[0].BytesTotal != 20 {
		t.Errorf("first task = %+v", loaded[0])
	}
	if loaded[1].Status != domainsftp.TaskCompleted || !loaded[1].FinishedAt.Equal(now.Add(2*time.Second)) {
		t.Errorf("second task = %+v", loaded[1])
	}
}

func TestTransferQueueMissingFileIsEmpty(t *testing.T) {
	repository := NewTransferQueue(filepath.Join(t.TempDir(), "absent.json"))
	tasks, err := repository.LoadTasks(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("loaded %d tasks from a missing file", len(tasks))
	}
}

func TestTransferQueueMalformedFileIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTransferQueue(path).LoadTasks(context.Background()); err == nil {
		t.Fatal("a malformed history was accepted")
	}
}

func TestTransferQueueUnknownVersionIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"tasks":[{"id":"x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tasks, err := NewTransferQueue(path).LoadTasks(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("a future schema leaked %d rows", len(tasks))
	}
}
