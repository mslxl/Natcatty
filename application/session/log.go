package session

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// LogOptions describes one Session log. Directory is the root; the logger
// creates Directory/<safe label>/<local timestamp>.<ext>, matching the
// original. An empty Directory produces no log.
type LogOptions struct {
	Directory  string
	Format     domainterminal.LogFormat
	Timestamps bool
	Label      string
	// Clock supplies timestamps. Nil uses time.Now; tests set it.
	Clock func() time.Time
}

// LogStatus is the immutable logging state the UI renders.
type LogStatus struct {
	Active  bool
	Format  domainterminal.LogFormat
	Path    string
	Started time.Time
	Problem string
}

// logFlushInterval is the original's 500 ms snapshot cadence.
const logFlushInterval = 500 * time.Millisecond

// sessionLogger writes one Session's log. Raw logs stream bytes; text and
// HTML logs keep a terminal renderer and atomically rewrite the readable file
// on each flush, so a reader never observes a half-written snapshot.
type sessionLogger struct {
	options LogOptions
	path    string
	started time.Time

	mu       sync.Mutex
	file     *os.File
	writer   *bufio.Writer
	renderer *logRenderer
	dirty    bool
	stopped  bool
	problem  string
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func newSessionLogger(options LogOptions) (*sessionLogger, error) {
	if options.Directory == "" {
		return nil, fmt.Errorf("session log: no directory")
	}
	options.Format = normalizeLogFormat(options.Format)
	if options.Clock == nil {
		options.Clock = time.Now
	}
	label := options.Label
	if label == "" {
		label = "session"
	}
	started := options.Clock()
	hostDir := filepath.Join(options.Directory, safePathSegment(label))
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		return nil, fmt.Errorf("session log: create %s: %w", hostDir, err)
	}
	extension := "txt"
	switch options.Format {
	case domainterminal.LogFormatRaw:
		extension = "log"
	case domainterminal.LogFormatHTML:
		extension = "html"
	}
	path := filepath.Join(hostDir, localLogTimestamp(started)+"."+extension)

	logger := &sessionLogger{
		options: options,
		path:    path,
		started: started,
		dirty:   true,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	if options.Format == domainterminal.LogFormatRaw {
		file, err := os.Create(path)
		if err != nil {
			return nil, fmt.Errorf("session log: create %s: %w", path, err)
		}
		logger.file = file
		logger.writer = bufio.NewWriterSize(file, 64<<10)
	}
	if options.Format != domainterminal.LogFormatRaw {
		logger.renderer = newLogRenderer()
	}
	go logger.flushLoop()
	return logger, nil
}

func normalizeLogFormat(format domainterminal.LogFormat) domainterminal.LogFormat {
	switch format {
	case domainterminal.LogFormatRaw, domainterminal.LogFormatHTML, domainterminal.LogFormatText:
		return format
	default:
		return domainterminal.LogFormatText
	}
}

func localLogTimestamp(at time.Time) string {
	return at.Format("2006-01-02T15-04-05")
}

// safePathSegment replaces path separators, Windows-reserved characters, and
// control characters so a Host label can name a directory safely. It mirrors
// the original's fallback and reserved-device handling.
func safePathSegment(value string) string {
	const reserved = `<>:"/\|?*`
	safe := strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f || strings.ContainsRune(reserved, character) {
			return '_'
		}
		return character
	}, value)
	safe = strings.TrimSpace(safe)
	if safe == "" || safe == "." || safe == ".." {
		return "unknown"
	}
	trimmed := strings.TrimRight(safe, ".")
	if trimmed != safe {
		safe = trimmed + strings.Repeat("_", len(safe)-len(trimmed))
	}
	base := strings.ToUpper(strings.TrimSuffix(safe, filepath.Ext(safe)))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5",
		"COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5",
		"LPT6", "LPT7", "LPT8", "LPT9":
		safe += "_"
	}
	return safe
}

// write records terminal output. Gated protocol bytes never reach this seam.
func (l *sessionLogger) write(p []byte) {
	if len(p) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	if l.renderer != nil {
		l.renderer.feed(p, l.options.Clock())
		l.dirty = true
		return
	}
	if l.writer == nil {
		return
	}
	if _, err := l.writer.Write(p); err != nil {
		l.problem = err.Error()
	}
}

func (l *sessionLogger) flushLoop() {
	ticker := time.NewTicker(logFlushInterval)
	defer ticker.Stop()
	defer close(l.doneCh)
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			if err := l.flush(); err != nil {
				l.mu.Lock()
				l.problem = err.Error()
				l.mu.Unlock()
			}
		}
	}
}

// flush writes the current snapshot. Raw logs flush their buffered stream;
// text and HTML logs atomically replace the readable file.
func (l *sessionLogger) flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flushLocked(false)
}

func (l *sessionLogger) flushLocked(final bool) error {
	if l.stopped && !final {
		return nil
	}
	if l.options.Format == domainterminal.LogFormatRaw {
		if l.writer == nil {
			return nil
		}
		if err := l.writer.Flush(); err != nil {
			l.problem = err.Error()
			return err
		}
		if err := l.file.Sync(); err != nil {
			l.problem = err.Error()
			return err
		}
		return nil
	}
	if !l.dirty && !final {
		return nil
	}
	if final && l.renderer != nil {
		l.renderer.finish()
	}
	content := ""
	if l.options.Format == domainterminal.LogFormatHTML {
		content = wrapLogHTML(l.renderer.html(l.linePrefix), l.options.Label, l.started)
	} else {
		content = l.renderer.text(l.linePrefix)
	}
	if err := writeFileAtomic(l.path, []byte(content), 0o644); err != nil {
		l.problem = err.Error()
		return err
	}
	l.dirty = false
	return nil
}

// linePrefix formats one non-empty line's optional timestamp.
func (l *sessionLogger) linePrefix(at time.Time) string {
	if !l.options.Timestamps || at.IsZero() {
		return ""
	}
	return "[" + at.Format("2006-01-02 15:04:05.000") + "] "
}

// stop flushes and closes the log, returning its final status. It is safe to
// call more than once.
func (l *sessionLogger) stop() LogStatus {
	l.stopOnce.Do(func() {
		close(l.stopCh)
		<-l.doneCh
		l.mu.Lock()
		defer l.mu.Unlock()
		if err := l.flushLocked(true); err != nil && l.problem == "" {
			l.problem = err.Error()
		}
		if l.writer != nil {
			_ = l.writer.Flush()
		}
		if l.file != nil {
			_ = l.file.Close()
			l.file = nil
		}
		l.stopped = true
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	return LogStatus{
		Active:  false,
		Format:  l.options.Format,
		Path:    l.path,
		Started: l.started,
		Problem: l.problem,
	}
}

func (l *sessionLogger) status() LogStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return LogStatus{
		Active:  !l.stopped,
		Format:  l.options.Format,
		Path:    l.path,
		Started: l.started,
		Problem: l.problem,
	}
}

// writeFileAtomic writes through a temporary file in the same directory and
// renames it into place, so a partial snapshot is never visible.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

// wrapLogHTML wraps rendered content in the original's readable, light/dark
// aware HTML page.
func wrapLogHTML(content, label string, started time.Time) string {
	if label == "" {
		label = "Unknown"
	}
	return `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="color-scheme" content="light dark">
  <title>Session Log - ` + escapeHTML(label) + `</title>
  <style>
    :root {
      color-scheme: light dark;
      --term-default-bg: #ffffff;
      --term-default-fg: #24292f;
      --term-header-fg: #6a737d;
      --term-border: #d0d7de;
    }
    @media (prefers-color-scheme: dark) {
      :root {
        --term-default-bg: #1e1e1e;
        --term-default-fg: #d4d4d4;
        --term-header-fg: #9da5b4;
        --term-border: #3c3c3c;
      }
    }
    html, body { background: var(--term-default-bg); color: var(--term-default-fg); }
    body {
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 13px;
      line-height: 1.5;
      padding: 20px;
      white-space: pre-wrap;
      word-wrap: break-word;
    }
    .header {
      border-bottom: 1px solid var(--term-border);
      padding-bottom: 10px;
      margin-bottom: 20px;
      color: var(--term-header-fg);
    }
  </style>
</head>
<body>
  <div class="header">
    Host: ` + escapeHTML(label) + `<br>
    Date: ` + escapeHTML(started.Format("2006-01-02 15:04:05")) + `
  </div>
  <div class="content">` + content + `</div>
</body>
</html>
`
}

func escapeHTML(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#039;",
	)
	return replacer.Replace(value)
}
