package history

import (
	"fmt"
	"testing"
	"time"
)

func at(hour int) time.Time {
	return time.Date(2026, 3, 1, hour, 0, 0, 0, time.UTC)
}

func TestConnectionFilterMatchesHostStatusTextAndRange(t *testing.T) {
	logs := []ConnectionLog{
		{ID: "a", HostID: "host-1", HostLabel: "web-1", Hostname: "10.0.0.1",
			Username: "deploy", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(9)},
		{ID: "b", HostID: "host-1", HostLabel: "web-1", Hostname: "10.0.0.1",
			Username: "deploy", Kind: "ssh", Status: ConnectionFailed,
			Error: "permission denied", StartedAt: at(10)},
		{ID: "c", HostLabel: "Local Terminal", Kind: "local",
			Status: ConnectionConnected, StartedAt: at(11)},
	}
	tests := []struct {
		name   string
		filter ConnectionFilter
		want   []string
	}{
		{"all", ConnectionFilter{}, []string{"a", "b", "c"}},
		{"host", ConnectionFilter{Host: "host-1"}, []string{"a", "b"}},
		{"local", ConnectionFilter{Host: LocalHostKey}, []string{"c"}},
		{"status", ConnectionFilter{Status: ConnectionFailed}, []string{"b"}},
		{"query host label", ConnectionFilter{Query: "WEB"}, []string{"a", "b"}},
		{"query error", ConnectionFilter{Query: "denied"}, []string{"b"}},
		{"from", ConnectionFilter{From: at(10)}, []string{"b", "c"}},
		{"to exclusive", ConnectionFilter{To: at(11)}, []string{"a", "b"}},
		{"combined", ConnectionFilter{Host: "host-1", Status: ConnectionConnected,
			From: at(8), To: at(10)}, []string{"a"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for _, log := range logs {
				if test.filter.Match(log) {
					got = append(got, log.ID)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("matches = %v, want %v", got, test.want)
			}
		})
	}
}

func TestEntryFilterMatchesCommandHostSourceAndRange(t *testing.T) {
	entries := []Entry{
		{ID: "a", HostID: "host-1", HostLabel: "web-1", Scope: "host:host-1",
			Command: "git status", Source: SourceSession, RecordedAt: at(9)},
		{ID: "b", HostID: "host-1", HostLabel: "web-1", Scope: "host:host-1",
			Command: "kubectl get pods", Source: SourceSession, RecordedAt: at(10)},
		{ID: "c", HostLabel: "Local Terminal", Scope: "local",
			Command: "make test", Source: SourceShellHistory, RecordedAt: at(11)},
	}
	tests := []struct {
		name   string
		filter EntryFilter
		want   []string
	}{
		{"all", EntryFilter{}, []string{"a", "b", "c"}},
		{"host", EntryFilter{Host: "host-1"}, []string{"a", "b"}},
		{"local", EntryFilter{Host: LocalHostKey}, []string{"c"}},
		{"source", EntryFilter{Source: SourceSession}, []string{"a", "b"}},
		{"query", EntryFilter{Query: "KUBECTL"}, []string{"b"}},
		{"host label", EntryFilter{Query: "web-1"}, []string{"a", "b"}},
		{"to exclusive", EntryFilter{To: at(11)}, []string{"a", "b"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for _, entry := range entries {
				if test.filter.Match(entry) {
					got = append(got, entry.ID)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("matches = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPruneKeepsTheNewestCountAndIsDeterministic(t *testing.T) {
	logs := make([]ConnectionLog, 0, 5)
	for index := 0; index < 5; index++ {
		logs = append(logs, ConnectionLog{
			ID: fmt.Sprintf("log-%d", index), Kind: "ssh", Status: ConnectionConnected,
			StartedAt: at(index),
		})
	}
	// Feed them oldest-first to prove pruning does not depend on input order.
	got := PruneConnectionLogs(logs, Retention{Count: 3}, at(12))
	want := []string{"log-4", "log-3", "log-2"}
	if len(got) != len(want) {
		t.Fatalf("kept %d logs, want %d: %+v", len(got), len(want), got)
	}
	for index, id := range want {
		if got[index].ID != id {
			t.Fatalf("kept[%d] = %q, want %q", index, got[index].ID, id)
		}
	}

	sameTime := []ConnectionLog{
		{ID: "a", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(9)},
		{ID: "c", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(9)},
		{ID: "b", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(9)},
	}
	got = PruneConnectionLogs(sameTime, Retention{Count: 2}, at(12))
	if got[0].ID != "c" || got[1].ID != "b" {
		t.Fatalf("tie-break = %q, %q; want c, b", got[0].ID, got[1].ID)
	}
}

func TestZeroRetentionKeepsEveryEntry(t *testing.T) {
	logs := []ConnectionLog{
		{ID: "a", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(1)},
		{ID: "b", Kind: "ssh", Status: ConnectionConnected, StartedAt: at(2)},
	}
	if got := PruneConnectionLogs(logs, Retention{}, at(100)); len(got) != 2 {
		t.Fatalf("count-zero retention kept %d, want 2", len(got))
	}
	entries := []Entry{
		{ID: "a", Scope: "local", Command: "one", RecordedAt: at(1)},
		{ID: "b", Scope: "local", Command: "two", RecordedAt: at(2)},
	}
	if got := PruneEntries(entries, Retention{}, at(100)); len(got) != 2 {
		t.Fatalf("count-zero retention kept %d, want 2", len(got))
	}
}

func TestAgeRetentionRemovesOnlyEntriesOlderThanTheBoundary(t *testing.T) {
	now := at(12)
	entries := []Entry{
		{ID: "exactly", Scope: "local", Command: "kept", RecordedAt: now.Add(-24 * time.Hour)},
		{ID: "older", Scope: "local", Command: "gone", RecordedAt: now.Add(-24*time.Hour - time.Second)},
		{ID: "newer", Scope: "local", Command: "kept", RecordedAt: now},
	}
	got := PruneEntries(entries, Retention{MaxAge: 24 * time.Hour}, now)
	if len(got) != 2 || got[0].ID != "newer" || got[1].ID != "exactly" {
		t.Fatalf("kept = %+v, want newer and the exact boundary", got)
	}
}

func TestAgeAndCountApplyTogether(t *testing.T) {
	now := at(12)
	entries := []Entry{
		{ID: "old", Scope: "local", Command: "old", RecordedAt: now.Add(-72 * time.Hour)},
		{ID: "a", Scope: "local", Command: "a", RecordedAt: now.Add(-3 * time.Hour)},
		{ID: "b", Scope: "local", Command: "b", RecordedAt: now.Add(-2 * time.Hour)},
		{ID: "c", Scope: "local", Command: "c", RecordedAt: now.Add(-time.Hour)},
	}
	got := PruneEntries(entries, Retention{Count: 2, MaxAge: 24 * time.Hour}, now)
	if len(got) != 2 || got[0].ID != "c" || got[1].ID != "b" {
		t.Fatalf("kept = %+v, want the newest two within the age limit", got)
	}
}

func TestNormalizeEntryTrimsAndBoundsTheCommand(t *testing.T) {
	long := make([]byte, MaxCommandLength+10)
	for index := range long {
		long[index] = 'x'
	}
	entry := NormalizeEntry(Entry{Command: "  " + string(long) + "  ", Scope: " local "})
	if len(entry.Command) != MaxCommandLength {
		t.Fatalf("command length = %d, want %d", len(entry.Command), MaxCommandLength)
	}
	if entry.Scope != "local" {
		t.Fatalf("scope = %q", entry.Scope)
	}
	if entry.Source != SourceSession {
		t.Fatalf("source = %q, want %q", entry.Source, SourceSession)
	}
}

func TestNormalizeConnectionLogDerivesStatus(t *testing.T) {
	if got := NormalizeConnectionLog(ConnectionLog{}); got.Status != ConnectionConnected {
		t.Fatalf("empty error status = %q, want connected", got.Status)
	}
	if got := NormalizeConnectionLog(ConnectionLog{Error: "dial failed"}); got.Status != ConnectionFailed {
		t.Fatalf("error status = %q, want failed", got.Status)
	}
}
