package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

var logTestClock = time.Date(2026, 10, 20, 9, 8, 7, 0, time.Local)

func fixedLogClock() time.Time { return logTestClock }

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRawLogStreamsExactBytes(t *testing.T) {
	directory := t.TempDir()
	logger, err := newSessionLogger(LogOptions{
		Directory: directory,
		Format:    domainterminal.LogFormatRaw,
		Label:     "example",
		Clock:     fixedLogClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("hello\r\n\x1b[31mred\x1b[0m\r\n")
	logger.write(payload)
	status := logger.stop()
	if status.Active {
		t.Fatal("stopped log still reports active")
	}
	if got := readLog(t, status.Path); got != string(payload) {
		t.Fatalf("raw log = %q, want %q", got, payload)
	}
	if filepath.Ext(status.Path) != ".log" {
		t.Fatalf("extension = %q", filepath.Ext(status.Path))
	}
	if filepath.Base(filepath.Dir(status.Path)) != "example" {
		t.Fatalf("host directory = %q", filepath.Dir(status.Path))
	}
}

func TestTextLogIsReadable(t *testing.T) {
	logger, err := newSessionLogger(LogOptions{
		Directory: t.TempDir(),
		Format:    domainterminal.LogFormatText,
		Label:     "example",
		Clock:     fixedLogClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger.write([]byte("hello\r\nworld\x1b[K\r\n\x1b[32mgreen\x1b[0m\r\n"))
	status := logger.stop()
	got := readLog(t, status.Path)
	want := "hello\nworld\ngreen\n"
	if got != want {
		t.Fatalf("text log = %q, want %q", got, want)
	}
	if filepath.Ext(status.Path) != ".txt" {
		t.Fatalf("extension = %q", filepath.Ext(status.Path))
	}
}

func TestHTMLLogEscapesTerminalOutput(t *testing.T) {
	logger, err := newSessionLogger(LogOptions{
		Directory: t.TempDir(),
		Format:    domainterminal.LogFormatHTML,
		Label:     "host <one>",
		Clock:     fixedLogClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger.write([]byte("plain <script>alert(1)</script> & done\r\n"))
	status := logger.stop()
	got := readLog(t, status.Path)
	for _, want := range []string{
		"<!DOCTYPE html>",
		"Host: host &lt;one&gt;",
		"plain &lt;script&gt;alert(1)&lt;/script&gt; &amp; done",
		"Date: 2026-10-20 09:08:07",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("HTML log missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>alert(1)</script>") {
		t.Fatal("HTML log contains unescaped terminal output")
	}
	if filepath.Ext(status.Path) != ".html" {
		t.Fatalf("extension = %q", filepath.Ext(status.Path))
	}
}

func TestTextLogTimestampsNonEmptyLines(t *testing.T) {
	logger, err := newSessionLogger(LogOptions{
		Directory:  t.TempDir(),
		Format:     domainterminal.LogFormatText,
		Timestamps: true,
		Label:      "example",
		Clock:      fixedLogClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger.write([]byte("first\n\nsecond\n"))
	status := logger.stop()
	got := readLog(t, status.Path)
	want := "[2026-10-20 09:08:07.000] first\n\n[2026-10-20 09:08:07.000] second\n"
	if got != want {
		t.Fatalf("timestamped log = %q, want %q", got, want)
	}
}

func TestLogFilenameUsesLocalTimestamp(t *testing.T) {
	logger, err := newSessionLogger(LogOptions{
		Directory: t.TempDir(),
		Format:    domainterminal.LogFormatText,
		Label:     "example",
		Clock:     fixedLogClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := logger.stop()
	if base := filepath.Base(status.Path); base != "2026-10-20T09-08-07.txt" {
		t.Fatalf("filename = %q", base)
	}
}

func TestSafePathSegmentRejectsTraversalAndReservedNames(t *testing.T) {
	cases := map[string]string{
		`..\..\evil`:  ".._.._evil",
		"/etc/passwd": "_etc_passwd",
		"CON":         "CON_",
		"com1":        "com1_",
		"  ":          "unknown",
		"host.name":   "host.name",
	}
	for input, want := range cases {
		if got := safePathSegment(input); got != want {
			t.Errorf("safePathSegment(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLogRejectsEmptyDirectory(t *testing.T) {
	if _, err := newSessionLogger(LogOptions{Format: domainterminal.LogFormatText}); err == nil {
		t.Fatal("empty log directory was accepted")
	}
}

func TestLogStopIsIdempotent(t *testing.T) {
	logger, err := newSessionLogger(LogOptions{
		Directory: t.TempDir(),
		Format:    domainterminal.LogFormatText,
		Clock:     fixedLogClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger.write([]byte("once\n"))
	first := logger.stop()
	second := logger.stop()
	if first.Path != second.Path {
		t.Fatalf("paths differ: %q and %q", first.Path, second.Path)
	}
	if got := readLog(t, first.Path); got != "once\n" {
		t.Fatalf("log = %q", got)
	}
}

func TestRendererHandlesSplitEscapeSequences(t *testing.T) {
	renderer := newLogRenderer()
	renderer.feed([]byte("a\x1b[3"), logTestClock)
	renderer.feed([]byte("1mb"), logTestClock)
	renderer.feed([]byte("c\x1b"), logTestClock)
	renderer.feed([]byte("[0m\n"), logTestClock)
	got := renderer.text(func(time.Time) string { return "" })
	if got != "abc\n" {
		t.Fatalf("rendered = %q", got)
	}
	html := renderer.html(func(time.Time) string { return "" })
	if !strings.Contains(html, "<span style=\"color:#cd0000") {
		t.Fatalf("rendered HTML lost SGR color: %q", html)
	}
}

func TestRendererOmitsAlternateScreenPainting(t *testing.T) {
	renderer := newLogRenderer()
	renderer.feed([]byte("before\r\n\x1b[?1049hTUI\r\n\x1b[?1049lafter\r\n"), logTestClock)
	got := renderer.text(func(time.Time) string { return "" })
	if strings.Contains(got, "TUI") {
		t.Fatalf("alternate-screen paint leaked into the log: %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Fatalf("normal screen text lost: %q", got)
	}
}

func TestRendererHandlesWideReplacementAndTabs(t *testing.T) {
	renderer := newLogRenderer()
	renderer.feed([]byte("a\tb\r\n"), logTestClock)
	if got := renderer.text(func(time.Time) string { return "" }); got != "a       b\n" {
		t.Fatalf("tab rendering = %q", got)
	}
}
