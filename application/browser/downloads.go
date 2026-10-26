package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DownloadStatus is the lifecycle state shown in the panel. The application
// layer deliberately carries no localized text.
type DownloadStatus string

const (
	DownloadWaiting     DownloadStatus = "waiting"
	DownloadProgressing DownloadStatus = "progressing"
	DownloadCompleted   DownloadStatus = "completed"
	DownloadFailed      DownloadStatus = "failed"
	DownloadCancelled   DownloadStatus = "cancelled"
)

// DownloadFailure classifies a failed download for the UI.
type DownloadFailure string

const (
	DownloadFailureNone     DownloadFailure = ""
	DownloadFailureTooLarge DownloadFailure = "too_large"
	DownloadFailureNetwork  DownloadFailure = "network"
	DownloadFailureStatus   DownloadFailure = "status"
	DownloadFailureWrite    DownloadFailure = "write"
)

// Download errors.
var (
	ErrDownloadURL   = errors.New("browser: download URL is outside the gateway")
	ErrDownloadDir   = errors.New("browser: download directory is unavailable")
	ErrDownloadLimit = errors.New("browser: download limit reached")
	ErrDownloadGone  = errors.New("browser: unknown download")
)

// DefaultMaxDownloadBytes bounds one file. It is deliberately generous for a
// firmware image or log bundle but small enough that a hostile Host cannot
// fill the local disk before the user cancels.
const DefaultMaxDownloadBytes int64 = 256 << 20

// MaxDownloads is the original download history limit.
const MaxDownloads = 500

// Download is one download record. URL is the real remote address; SavePath is
// inside the configured directory.
type Download struct {
	ID        string          `json:"id"`
	Filename  string          `json:"filename"`
	URL       string          `json:"url"`
	SavePath  string          `json:"savePath"`
	Status    DownloadStatus  `json:"status"`
	Failure   DownloadFailure `json:"failure,omitempty"`
	Received  int64           `json:"received"`
	Total     int64           `json:"total"`
	StartedAt int64           `json:"startedAt"`
	EndedAt   int64           `json:"endedAt"`
}

// DownloadOptions are the app's file and cookie policy for page downloads.
type DownloadOptions struct {
	// Dir returns the destination directory. The app uses the user's
	// Downloads directory; tests use a private temporary directory.
	Dir func() (string, error)
	// MaxBytes bounds one file. Zero uses DefaultMaxDownloadBytes.
	MaxBytes int64
	// Cookies returns the Cookie header for a gateway URL. The app reads the
	// WebView's cookie store; the download itself never uses the page.
	Cookies func(gatewayURL string) string
	// Validate rejects a URL before a destination file is reserved. The panel
	// supplies the gateway's origin check, so no direct remote URL can start a
	// download.
	Validate func(gatewayURL string) error
	// Redraw requests a frame after progress or a status change.
	Redraw func()
}

type downloadOpener func(ctx context.Context, gatewayURL, cookies string) (*http.Response, error)

type downloadManager struct {
	opts DownloadOptions
	open downloadOpener

	mu       sync.Mutex
	items    []Download
	active   map[string]*activeDownload
	next     int
	closed   bool
	life     context.Context
	cancel   context.CancelFunc
	wait     sync.WaitGroup
	redrawAt time.Time
}

type activeDownload struct {
	cancel   context.CancelFunc
	finished chan struct{}
}

func newDownloadManager(opts DownloadOptions, open downloadOpener) *downloadManager {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxDownloadBytes
	}
	life, cancel := context.WithCancel(context.Background())
	return &downloadManager{
		opts:   opts,
		open:   open,
		active: make(map[string]*activeDownload),
		life:   life,
		cancel: cancel,
	}
}

// Start reserves a clean .part file and begins fetching through the gateway.
// The destination directory and filename are fixed before the first byte is
// written, so a redirect or Content-Disposition cannot escape it.
func (m *downloadManager) Start(gatewayURL, suggested string) (Download, error) {
	if m == nil {
		return Download{}, ErrDownloadGone
	}
	if m.opts.Dir == nil {
		return Download{}, ErrDownloadDir
	}
	if m.opts.Validate != nil {
		if err := m.opts.Validate(gatewayURL); err != nil {
			return Download{}, err
		}
	}
	dir, err := m.opts.Dir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return Download{}, ErrDownloadDir
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return Download{}, ErrDownloadDir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Download{}, fmt.Errorf("%w: %v", ErrDownloadDir, err)
	}
	name := safeDownloadName(suggested)
	final, part, err := reserveDownloadPath(dir, name)
	if err != nil {
		return Download{}, err
	}
	file, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Download{}, fmt.Errorf("%w: %v", ErrDownloadDir, err)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = file.Close()
		_ = os.Remove(part)
		return Download{}, ErrDownloadGone
	}
	if len(m.items) >= MaxDownloads {
		trim := m.trimFinishedLocked()
		if !trim {
			m.mu.Unlock()
			_ = file.Close()
			_ = os.Remove(part)
			return Download{}, ErrDownloadLimit
		}
	}
	m.next++
	item := Download{
		ID: fmt.Sprintf("download-%d", m.next), Filename: filepath.Base(final),
		URL: gatewayURL, SavePath: final, Status: DownloadWaiting,
		Total: -1, StartedAt: time.Now().UnixMilli(),
	}
	ctx, cancel := context.WithCancel(m.life)
	active := &activeDownload{cancel: cancel, finished: make(chan struct{})}
	m.items = append(m.items, item)
	m.active[item.ID] = active
	m.mu.Unlock()

	m.wait.Add(1)
	go m.run(ctx, item.ID, gatewayURL, file, part, final, active)
	return item, nil
}

func (m *downloadManager) run(ctx context.Context, id, gatewayURL string, file *os.File, part, final string, active *activeDownload) {
	defer m.wait.Done()
	defer close(active.finished)
	cleanup := false
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(part)
		}
		m.mu.Lock()
		delete(m.active, id)
		m.mu.Unlock()
	}()

	cookies := ""
	if m.opts.Cookies != nil {
		cookies = m.opts.Cookies(gatewayURL)
	}
	response, err := m.open(ctx, gatewayURL, cookies)
	if err != nil {
		if ctx.Err() != nil {
			m.finish(id, DownloadCancelled, DownloadFailureNone, 0, -1)
		} else {
			m.finish(id, DownloadFailed, DownloadFailureNetwork, 0, -1)
		}
		cleanup = true
		return
	}
	defer response.Body.Close()

	total := response.ContentLength
	m.update(id, func(item *Download) {
		item.Status = DownloadProgressing
		item.Total = total
	})
	if response.Request != nil && response.Request.URL != nil {
		remote := response.Request.URL.String()
		m.update(id, func(item *Download) { item.URL = remote })
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		m.finish(id, DownloadFailed, DownloadFailureStatus, 0, total)
		cleanup = true
		return
	}
	if total > m.opts.MaxBytes {
		m.finish(id, DownloadFailed, DownloadFailureTooLarge, 0, total)
		cleanup = true
		return
	}
	buffer := make([]byte, 32*1024)
	var received int64
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			received += int64(n)
			if received > m.opts.MaxBytes {
				m.finish(id, DownloadFailed, DownloadFailureTooLarge, received, total)
				cleanup = true
				return
			}
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				m.finish(id, DownloadFailed, DownloadFailureWrite, received, total)
				cleanup = true
				return
			}
			m.progress(id, received, total)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if ctx.Err() != nil {
				m.finish(id, DownloadCancelled, DownloadFailureNone, received, total)
			} else {
				m.finish(id, DownloadFailed, DownloadFailureNetwork, received, total)
			}
			cleanup = true
			return
		}
	}
	if err := file.Sync(); err != nil {
		m.finish(id, DownloadFailed, DownloadFailureWrite, received, total)
		cleanup = true
		return
	}
	if err := file.Close(); err != nil {
		m.finish(id, DownloadFailed, DownloadFailureWrite, received, total)
		cleanup = true
		return
	}
	if err := os.Rename(part, final); err != nil {
		m.finish(id, DownloadFailed, DownloadFailureWrite, received, total)
		cleanup = true
		return
	}
	m.finish(id, DownloadCompleted, DownloadFailureNone, received, total)
}

// update replaces one record's mutable view while preserving the slice order.
func (m *downloadManager) update(id string, fn func(*Download)) {
	m.mu.Lock()
	index := m.indexLocked(id)
	if index >= 0 {
		fn(&m.items[index])
	}
	m.mu.Unlock()
}

func (m *downloadManager) progress(id string, received, total int64) {
	m.mu.Lock()
	index := m.indexLocked(id)
	if index < 0 {
		m.mu.Unlock()
		return
	}
	m.items[index].Status = DownloadProgressing
	m.items[index].Received = received
	m.items[index].Total = total
	now := time.Now()
	redraw := m.opts.Redraw != nil && (now.Sub(m.redrawAt) >= 100*time.Millisecond || received == total)
	if redraw {
		m.redrawAt = now
	}
	m.mu.Unlock()
	if redraw {
		m.opts.Redraw()
	}
}

func (m *downloadManager) finish(id string, status DownloadStatus, failure DownloadFailure, received, total int64) {
	m.mu.Lock()
	index := m.indexLocked(id)
	if index >= 0 {
		m.items[index].Status = status
		m.items[index].Failure = failure
		m.items[index].Received = received
		if total >= 0 {
			m.items[index].Total = total
		}
		m.items[index].EndedAt = time.Now().UnixMilli()
	}
	m.mu.Unlock()
	if m.opts.Redraw != nil {
		m.opts.Redraw()
	}
}

// Downloads returns a copy of the history, newest first.
func (m *downloadManager) Downloads() []Download {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Download, 0, len(m.items))
	for i := len(m.items) - 1; i >= 0; i-- {
		out = append(out, m.items[i])
	}
	return out
}

// Active returns the number of downloads still waiting or progressing.
func (m *downloadManager) Active() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}

// Get returns one record, or the zero value.
func (m *downloadManager) Get(id string) Download {
	if m == nil {
		return Download{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if index := m.indexLocked(id); index >= 0 {
		return m.items[index]
	}
	return Download{}
}

// Cancel aborts an active download; its .part file is removed by the worker.
func (m *downloadManager) Cancel(id string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	active := m.active[id]
	index := m.indexLocked(id)
	cancellable := active != nil && index >= 0 &&
		(m.items[index].Status == DownloadWaiting || m.items[index].Status == DownloadProgressing)
	m.mu.Unlock()
	if !cancellable {
		return false
	}
	active.cancel()
	return true
}

// Remove drops a finished record from the history. The downloaded file stays
// on disk, exactly as the original download panel left it.
func (m *downloadManager) Remove(id string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	index := m.indexLocked(id)
	if index < 0 {
		m.mu.Unlock()
		return false
	}
	if m.active[id] != nil {
		m.mu.Unlock()
		return false
	}
	items := append([]Download(nil), m.items[:index]...)
	items = append(items, m.items[index+1:]...)
	m.items = items
	m.mu.Unlock()
	if m.opts.Redraw != nil {
		m.opts.Redraw()
	}
	return true
}

// ClearFinished removes completed, failed, and cancelled records.
func (m *downloadManager) ClearFinished() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	kept := make([]Download, 0, len(m.items))
	changed := false
	for _, item := range m.items {
		if item.Status == DownloadWaiting || item.Status == DownloadProgressing {
			kept = append(kept, item)
		} else {
			changed = true
		}
	}
	m.items = kept
	m.mu.Unlock()
	if changed && m.opts.Redraw != nil {
		m.opts.Redraw()
	}
	return changed
}

func (m *downloadManager) indexLocked(id string) int {
	for i := range m.items {
		if m.items[i].ID == id {
			return i
		}
	}
	return -1
}

// trimFinishedLocked drops the oldest finished record to admit a new one.
func (m *downloadManager) trimFinishedLocked() bool {
	for i := range m.items {
		if m.active[m.items[i].ID] == nil {
			m.items = append(m.items[:i], m.items[i+1:]...)
			return true
		}
	}
	return false
}

// Wait waits for every active worker to settle. Tests and Close use it.
func (m *downloadManager) Wait() {
	if m == nil {
		return
	}
	m.wait.Wait()
}

// cancelAll aborts active downloads without waiting. A route disconnect uses
// it so no worker keeps writing after the tunnel is released.
func (m *downloadManager) cancelAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	active := make([]*activeDownload, 0, len(m.active))
	for _, item := range m.active {
		active = append(active, item)
	}
	m.mu.Unlock()
	for _, item := range active {
		item.cancel()
	}
}

// Close cancels active downloads and waits for their cleanup before the
// gateway and tunnel are released.
func (m *downloadManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, active := range m.active {
		active.cancel()
	}
	m.mu.Unlock()
	m.cancel()
	m.wait.Wait()
}

// safeDownloadName reduces an untrusted suggestion to one base filename in
// the destination directory.
func safeDownloadName(suggested string) string {
	name := strings.ReplaceAll(suggested, "\\", "/")
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"|?*`, r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimRight(name, " .")
	if name == "" || name == "." || name == ".." {
		name = "download"
	}
	if len([]rune(name)) > 180 {
		runeName := []rune(name)
		extension := filepath.Ext(name)
		stem := strings.TrimSuffix(string(runeName), extension)
		if len([]rune(extension)) > 32 {
			extension = ""
		}
		name = truncateRunes(stem, 180-len([]rune(extension))) + extension
	}
	return name
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// reserveDownloadPath chooses a free final name and its .part marker. Both
// paths are direct children of dir.
func reserveDownloadPath(dir, name string) (final, part string, err error) {
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for index := 0; index < 10000; index++ {
		candidate := name
		if index > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, index, extension)
		}
		final = filepath.Join(dir, candidate)
		part = final + ".part"
		if filepath.Dir(final) != filepath.Clean(dir) || filepath.Dir(part) != filepath.Clean(dir) {
			return "", "", ErrDownloadDir
		}
		if _, err := os.Stat(final); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("%w: %v", ErrDownloadDir, err)
		}
		if _, err := os.Lstat(part); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("%w: %v", ErrDownloadDir, err)
		}
		return final, part, nil
	}
	return "", "", errors.New("browser: no free download name")
}
