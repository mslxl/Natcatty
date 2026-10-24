package sftp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// transferQueueVersion is the persisted Transfer history schema. A file with
// another version is treated as empty rather than guessed at, so a future
// build cannot resurrect rows with a different meaning.
const transferQueueVersion = 1

// TransferQueue persists the global Transfer history as one JSON document.
// Each save replaces the file atomically, so a crash mid-write cannot leave a
// truncated history; a missing file is an empty history, while a malformed
// one is reported so the app can start with an empty center instead of
// silently losing rows.
type TransferQueue struct {
	path string
}

// NewTransferQueue returns a repository over path. An empty path keeps the
// queue memory-only.
func NewTransferQueue(path string) *TransferQueue {
	return &TransferQueue{path: path}
}

var _ apptransfer.QueueRepository = (*TransferQueue)(nil)

// transferQueueFile is the on-disk document.
type transferQueueFile struct {
	Version int               `json:"version"`
	Tasks   []domainsftp.Task `json:"tasks"`
}

// LoadTasks reads the saved history. A missing or empty file is an empty
// list; an unreadable or malformed one is an error.
func (q *TransferQueue) LoadTasks(ctx context.Context) ([]domainsftp.Task, error) {
	if q == nil || q.path == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(q.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sftp transfer queue: read: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var file transferQueueFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("sftp transfer queue: parse: %w", err)
	}
	if file.Version != transferQueueVersion {
		return nil, nil
	}
	return file.Tasks, nil
}

// SaveTasks replaces the saved history.
func (q *TransferQueue) SaveTasks(ctx context.Context, tasks []domainsftp.Task) error {
	if q == nil || q.path == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(transferQueueFile{
		Version: transferQueueVersion,
		Tasks:   tasks,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("sftp transfer queue: encode: %w", err)
	}
	dir := filepath.Dir(q.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("sftp transfer queue: directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".sftp-transfer-queue-*.tmp")
	if err != nil {
		return fmt.Errorf("sftp transfer queue: temporary file: %w", err)
	}
	temp := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(temp)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("sftp transfer queue: permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("sftp transfer queue: write: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("sftp transfer queue: close: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, q.path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("sftp transfer queue: replace: %w", err)
	}
	return nil
}
