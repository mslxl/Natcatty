// Package pluginaudit writes the append-only plugin audit log. Files are
// daily-rotated JSONL under the host's user-data directory, outside every
// plugin package and data directory; the plugin runtime has no filesystem
// import, so plugins cannot read, write, or delete them. Writes are
// asynchronous and bounded; dropped events are counted, never silently
// discarded.
package pluginaudit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Defaults from the permission decision.
const (
	DefaultRetentionDays = 30
	DefaultMaxBytes      = 50 << 20
	DefaultQueue         = 1024
	filePrefix           = "audit-"
	fileSuffix           = ".jsonl"
	dayLayout            = "2006-01-02"
)

// Options configures a Writer.
type Options struct {
	// Directory is the host-owned audit directory.
	Directory string
	// Clock supplies the rotation day and retention reference time.
	Clock func() time.Time
	// RetentionDays keeps files whose day is at most this many days old.
	RetentionDays int
	// MaxBytes bounds the total directory size; oldest files go first.
	MaxBytes int64
	// Queue bounds pending events. A full queue counts a drop.
	Queue int
}

// Writer is the append-only JSONL audit sink.
type Writer struct {
	directory     string
	clock         func() time.Time
	retentionDays int
	maxBytes      int64
	queue         chan domainplugin.AuditEvent
	done          chan struct{}

	// queueMu guards closed, queue sends, and closing the queue. Record never
	// holds it while doing disk I/O; mu separately guards the open file.
	queueMu sync.Mutex
	closed  bool

	mu   sync.Mutex
	file *os.File
	day  string

	dropped   atomic.Uint64
	closeOnce sync.Once
	closeErr  error
}

var _ interface {
	Record(domainplugin.AuditEvent)
	Close() error
} = (*Writer)(nil)

// Open creates the audit directory, applies retention, and starts the writer
// goroutine.
func Open(opts Options) (*Writer, error) {
	if strings.TrimSpace(opts.Directory) == "" {
		return nil, errors.New("plugin audit: directory is required")
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.RetentionDays <= 0 {
		opts.RetentionDays = DefaultRetentionDays
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.Queue <= 0 {
		opts.Queue = DefaultQueue
	}
	if err := os.MkdirAll(opts.Directory, 0o700); err != nil {
		return nil, fmt.Errorf("plugin audit: create directory: %w", err)
	}
	w := &Writer{
		directory:     opts.Directory,
		clock:         opts.Clock,
		retentionDays: opts.RetentionDays,
		maxBytes:      opts.MaxBytes,
		queue:         make(chan domainplugin.AuditEvent, opts.Queue),
		done:          make(chan struct{}),
	}
	if err := w.trim(); err != nil {
		return nil, err
	}
	go w.run()
	return w, nil
}

// Record enqueues one event without blocking. A full queue increments the
// dropped counter.
func (w *Writer) Record(event domainplugin.AuditEvent) {
	if w == nil {
		return
	}
	// Holding queueMu across the non-blocking send makes Close race-free: the
	// channel cannot be closed between the closed check and the send.
	w.queueMu.Lock()
	defer w.queueMu.Unlock()
	if w.closed {
		w.dropped.Add(1)
		return
	}
	select {
	case w.queue <- event:
	default:
		w.dropped.Add(1)
	}
}

// Dropped returns the number of events the bounded queue or a write error
// discarded.
func (w *Writer) Dropped() uint64 {
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

// Close drains pending events and closes the current file.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.queueMu.Lock()
		if !w.closed {
			w.closed = true
			close(w.queue)
		}
		w.queueMu.Unlock()
		<-w.done
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.file != nil {
			w.closeErr = w.file.Close()
			w.file = nil
		}
	})
	return w.closeErr
}

func (w *Writer) run() {
	defer close(w.done)
	for event := range w.queue {
		if err := w.write(event); err != nil {
			w.dropped.Add(1)
		}
	}
}

func (w *Writer) write(event domainplugin.AuditEvent) error {
	if event.Time.IsZero() {
		event.Time = w.clock().UTC()
	} else {
		event.Time = event.Time.UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	day := event.Time.Format(dayLayout)
	if err := w.rotateLocked(day); err != nil {
		return err
	}
	if w.file == nil {
		return errors.New("plugin audit: no open file")
	}
	_, err = w.file.Write(data)
	return err
}

func (w *Writer) rotateLocked(day string) error {
	if w.file != nil && w.day == day {
		return nil
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			w.file = nil
			return err
		}
		w.file = nil
	}
	file, err := os.OpenFile(filepath.Join(w.directory, filePrefix+day+fileSuffix),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w.file = file
	w.day = day
	return w.trimLocked()
}

// trim applies retention by age, then by total size. The current day's file is
// never deleted; it may temporarily exceed MaxBytes because live events are
// never discarded to satisfy the directory cap.
func (w *Writer) trim() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.trimLocked()
}

func (w *Writer) trimLocked() error {
	entries, err := os.ReadDir(w.directory)
	if err != nil {
		return err
	}
	type auditFile struct {
		name string
		day  time.Time
		size int64
	}
	var files []auditFile
	today := w.clock().UTC().Format(dayLayout)
	var total int64
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileSuffix) {
			continue
		}
		dayText := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
		day, err := time.ParseInLocation(dayLayout, dayText, time.UTC)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, auditFile{name: name, day: day, size: info.Size()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].day.Before(files[j].day) })

	cutoff := w.clock().UTC().AddDate(0, 0, -w.retentionDays)
	kept := files[:0]
	for _, file := range files {
		if file.day.Before(cutoff) && file.name != filePrefix+today+fileSuffix {
			if err := os.Remove(filepath.Join(w.directory, file.name)); err != nil {
				return err
			}
			total -= file.size
			continue
		}
		kept = append(kept, file)
	}
	for _, file := range kept {
		if total <= w.maxBytes {
			break
		}
		if file.name == filePrefix+today+fileSuffix {
			continue
		}
		if err := os.Remove(filepath.Join(w.directory, file.name)); err != nil {
			return err
		}
		total -= file.size
	}
	return nil
}
