package pluginaudit_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/pluginaudit"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

func TestWriterAppendsJSONLWithoutPayloadContent(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	writer, err := pluginaudit.Open(pluginaudit.Options{
		Directory: dir,
		Clock:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	writer.Record(domainplugin.AuditEvent{
		Event: domainplugin.AuditInstall, PluginID: "acme.notes",
		Source: domainplugin.SourceLocalFile, Version: "1.0.0",
	})
	writer.Record(domainplugin.AuditEvent{
		Event: domainplugin.AuditGrant, PluginID: "acme.notes",
		Permission: domainplugin.PermissionFilesRead,
		Scope:      &domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/notes"},
	})
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	path := filepath.Join(dir, "audit-2026-11-06.jsonl")
	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	var event domainplugin.AuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("line 0 is not JSON: %v", err)
	}
	if event.Event != domainplugin.AuditInstall || event.PluginID != "acme.notes" {
		t.Fatalf("event = %+v", event)
	}
	if strings.Contains(lines[1], "content") || strings.Contains(lines[1], "payload") {
		t.Fatalf("audit row carries content fields: %s", lines[1])
	}
}

func TestWriterRotatesDailyByEventTime(t *testing.T) {
	dir := t.TempDir()
	var clockMu sync.Mutex
	clock := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	writer, err := pluginaudit.Open(pluginaudit.Options{
		Directory: dir,
		Clock:     now,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	writer.Record(domainplugin.AuditEvent{
		Time:  time.Date(2026, 11, 6, 23, 59, 0, 0, time.UTC),
		Event: domainplugin.AuditEnable, PluginID: "acme.notes",
	})
	clockMu.Lock()
	clock = time.Date(2026, 11, 7, 0, 1, 0, 0, time.UTC)
	clockMu.Unlock()
	writer.Record(domainplugin.AuditEvent{
		Time: clock, Event: domainplugin.AuditDisable, PluginID: "acme.notes",
	})
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, day := range []string{"2026-11-06", "2026-11-07"} {
		path := filepath.Join(dir, "audit-"+day+".jsonl")
		if lines := readLines(t, path); len(lines) != 1 {
			t.Fatalf("%s has %d lines", path, len(lines))
		}
	}
}

func TestWriterRetainsOnlyTheConfiguredAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	for _, day := range []string{"2026-09-01", "2026-10-20", "2026-11-05"} {
		if err := os.WriteFile(filepath.Join(dir, "audit-"+day+".jsonl"),
			[]byte(`{"event":"install"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writer, err := pluginaudit.Open(pluginaudit.Options{
		Directory: dir, Clock: func() time.Time { return now }, RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	writer.Record(domainplugin.AuditEvent{Event: domainplugin.AuditInstall, PluginID: "acme.notes"})
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit-2026-09-01.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("an expired audit file survived: %v", err)
	}
	for _, day := range []string{"2026-10-20", "2026-11-05", "2026-11-06"} {
		if _, err := os.Stat(filepath.Join(dir, "audit-"+day+".jsonl")); err != nil {
			t.Fatalf("audit-%s.jsonl was removed: %v", day, err)
		}
	}
}

func TestWriterTrimsOldestFilesToTheSizeCap(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	for _, day := range []string{"2026-11-04", "2026-11-05"} {
		if err := os.WriteFile(filepath.Join(dir, "audit-"+day+".jsonl"),
			[]byte(strings.Repeat("x", 600)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writer, err := pluginaudit.Open(pluginaudit.Options{
		Directory: dir, Clock: func() time.Time { return now }, MaxBytes: 700,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	writer.Record(domainplugin.AuditEvent{Event: domainplugin.AuditInstall, PluginID: "acme.notes"})
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit-2026-11-04.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("the oldest file survived the size cap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit-2026-11-05.jsonl")); err != nil {
		t.Fatalf("the newer file was removed: %v", err)
	}
}

func TestWriterCountsDroppedEventsAfterClose(t *testing.T) {
	writer, err := pluginaudit.Open(pluginaudit.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	writer.Record(domainplugin.AuditEvent{Event: domainplugin.AuditInstall})
	if writer.Dropped() == 0 {
		t.Fatal("a record after Close was not counted as dropped")
	}
}

// TestWriterRecordAndCloseAreRaceFree exercises concurrent Record and Close
// under -race. Record must never send on a closed queue, and every call after
// Close must count a drop instead of panicking.
func TestWriterRecordAndCloseAreRaceFree(t *testing.T) {
	writer, err := pluginaudit.Open(pluginaudit.Options{
		Directory: t.TempDir(),
		Queue:     16,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var senders sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		senders.Add(1)
		go func() {
			defer senders.Done()
			<-start
			for j := 0; j < 500; j++ {
				writer.Record(domainplugin.AuditEvent{
					Event: domainplugin.AuditActivation, PluginID: "acme.race",
				})
			}
		}()
	}

	var closers sync.WaitGroup
	for i := 0; i < 4; i++ {
		closers.Add(1)
		go func() {
			defer closers.Done()
			<-start
			if err := writer.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
	}
	close(start)
	senders.Wait()
	closers.Wait()

	if err := writer.Close(); err != nil {
		t.Fatalf("Close after race: %v", err)
	}
	before := writer.Dropped()
	writer.Record(domainplugin.AuditEvent{Event: domainplugin.AuditInstall})
	if writer.Dropped() <= before {
		t.Fatal("a Record after Close was not counted as dropped")
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return lines
}
