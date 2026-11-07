package history

import (
	"testing"
	"time"
)

// statisticsEntries is the worked example for the summary tests: three
// commands named "git status", one failure, two rows whose finish time the
// shell never reported, and rows from a saved Host, a typed Host, the local
// shell, and a remote origin.
func statisticsEntries() []Entry {
	return []Entry{
		{ID: "a", HostID: "host-1", HostLabel: "web-1", Scope: "host:host-1",
			Command: "git status", CWD: "/srv/app", RecordedAt: at(9),
			FinishedAt: at(9).Add(time.Second), Duration: time.Second,
			ExitCode: 0, HasExitCode: true, Source: SourceSession},
		{ID: "b", HostID: "host-1", HostLabel: "web-1", Scope: "host:host-1",
			Command: "git status", CWD: "/srv/app", RecordedAt: at(9).Add(time.Minute),
			FinishedAt: at(9).Add(time.Minute + 3*time.Second), Duration: 3 * time.Second,
			ExitCode: 2, HasExitCode: true, Source: SourceSession},
		{ID: "c", HostID: "host-2", HostLabel: "db-1", Scope: "host:host-2",
			Command: "make test", CWD: "/var/log", RecordedAt: at(10),
			Source: SourceSession},
		{ID: "d", Scope: "local", Command: "ls", RecordedAt: at(9).Add(30 * time.Minute),
			FinishedAt: at(9).Add(30 * time.Minute), ExitCode: 0, HasExitCode: true,
			Source: SourceSession},
		{ID: "e", Scope: "remote:remote:deploy", Command: "git status", CWD: "/root",
			Origin: "remote:deploy", RecordedAt: at(10),
			ExitCode: 0, HasExitCode: true, Source: SourceAtuinSync},
	}
}

func TestStatisticsSummarizeTheResultSet(t *testing.T) {
	stats := ComputeStatistics(statisticsEntries())
	if stats.Total != 5 || stats.Unique != 3 {
		t.Fatalf("total = %d, unique = %d; want 5 and 3", stats.Total, stats.Unique)
	}
	if stats.Failures != 1 || stats.KnownExits != 4 {
		t.Fatalf("failures = %d, known exits = %d; want 1 and 4",
			stats.Failures, stats.KnownExits)
	}
	if stats.FailureRate != 0.25 {
		t.Fatalf("failure rate = %v, want 0.25", stats.FailureRate)
	}
	if stats.DurationsKnown != 3 || stats.DurationsUnknown != 2 {
		t.Fatalf("known durations = %d, unknown = %d; want 3 and 2",
			stats.DurationsKnown, stats.DurationsUnknown)
	}
	if want := 4 * time.Second / 3; stats.AverageDuration != want {
		t.Fatalf("average duration = %v, want %v", stats.AverageDuration, want)
	}

	wantCommands := []CommandStat{
		{Command: "git status", Count: 3},
		{Command: "ls", Count: 1},
		{Command: "make test", Count: 1},
	}
	assertCommandStats(t, stats.TopCommands, wantCommands)
	wantCwds := []CwdStat{
		{CWD: "/srv/app", Count: 2},
		{CWD: "/root", Count: 1},
		{CWD: "/var/log", Count: 1},
	}
	if len(stats.TopCwds) != len(wantCwds) {
		t.Fatalf("top cwds = %+v", stats.TopCwds)
	}
	for index, want := range wantCwds {
		if stats.TopCwds[index] != want {
			t.Fatalf("top cwds = %+v, want %+v", stats.TopCwds, wantCwds)
		}
	}

	if stats.BusiestHour == nil || stats.BusiestHour.Hour != 9 ||
		stats.BusiestHour.Count != 3 {
		t.Fatalf("busiest hour = %+v, want 9 with 3", stats.BusiestHour)
	}

	wantHosts := []HostStat{
		{Key: "host-1", Label: "web-1", Count: 2},
		{Key: "host-2", Label: "db-1", Count: 1},
		{Key: LocalHostKey, Count: 1},
		{Key: OriginHostKey("remote:deploy"), Label: "remote:deploy", Count: 1},
	}
	if len(stats.Hosts) != len(wantHosts) {
		t.Fatalf("hosts = %+v, want %+v", stats.Hosts, wantHosts)
	}
	for index, want := range wantHosts {
		if stats.Hosts[index] != want {
			t.Fatalf("hosts = %+v, want %+v", stats.Hosts, wantHosts)
		}
	}
}

func TestStatisticsOfAnEmptySetAreZero(t *testing.T) {
	stats := ComputeStatistics(nil)
	if stats.Total != 0 || stats.Unique != 0 || stats.Failures != 0 ||
		stats.KnownExits != 0 || stats.FailureRate != 0 ||
		stats.AverageDuration != 0 || stats.DurationsKnown != 0 ||
		stats.DurationsUnknown != 0 || stats.BusiestHour != nil ||
		len(stats.TopCommands) != 0 || len(stats.TopCwds) != 0 ||
		len(stats.Hosts) != 0 {
		t.Fatalf("empty statistics = %+v", stats)
	}
}

// TestStatisticsKeepTypedHostsDistinctByOrigin proves two unsaved Hosts are
// not merged into one "unsaved" bucket: their captured origins are the stable
// identity, as they are for imported and synced rows.
func TestStatisticsKeepTypedHostsDistinctByOrigin(t *testing.T) {
	stats := ComputeStatistics([]Entry{
		{ID: "one", UnsavedHost: true, Scope: "host:unsaved:alpha.example:deploy",
			Command: "uptime", Origin: "alpha.example:deploy",
			RecordedAt: at(9), Source: SourceSession},
		{ID: "two", UnsavedHost: true, Scope: "host:unsaved:beta.example:admin",
			Command: "df -h", Origin: "beta.example:admin",
			RecordedAt: at(9), Source: SourceSession},
		{ID: "legacy", UnsavedHost: true, HostLabel: "typed-without-origin",
			Scope: "local", Command: "ls", RecordedAt: at(9),
			Source: SourceSession},
	})
	want := []HostStat{
		{Key: OriginHostKey("alpha.example:deploy"), Label: "alpha.example:deploy", Count: 1},
		{Key: OriginHostKey("beta.example:admin"), Label: "beta.example:admin", Count: 1},
		{Key: UnsavedHostKey, Label: "typed-without-origin", Count: 1},
	}
	if len(stats.Hosts) != len(want) {
		t.Fatalf("hosts = %+v, want %+v", stats.Hosts, want)
	}
	for index := range want {
		if stats.Hosts[index] != want[index] {
			t.Fatalf("hosts = %+v, want %+v", stats.Hosts, want)
		}
	}
}

// TestStatisticsBoundTheRankedListsAndClampDurations proves each ranked list
// is capped at StatisticsTopLimit and that a negative stored duration cannot
// pull the average below zero.
func TestStatisticsBoundTheRankedListsAndClampDurations(t *testing.T) {
	entries := make([]Entry, 0, 7)
	for index, command := range []string{
		"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf",
	} {
		runs := 7 - index
		for run := 0; run < runs; run++ {
			entries = append(entries, Entry{
				ID: command + string(rune('a'+run)), Scope: "local",
				Command: command, RecordedAt: at(12),
				FinishedAt: at(12), Duration: time.Second, Source: SourceSession,
			})
		}
	}
	// One negative stored duration is clamped instead of lowering the average.
	entries = append(entries, Entry{
		ID: "negative", Scope: "local", Command: "negative",
		RecordedAt: at(12), FinishedAt: at(12), Duration: -time.Hour,
		Source: SourceSession,
	})

	stats := ComputeStatistics(entries)
	if len(stats.TopCommands) != StatisticsTopLimit {
		t.Fatalf("top commands = %d, want %d", len(stats.TopCommands), StatisticsTopLimit)
	}
	for index := 0; index < StatisticsTopLimit; index++ {
		wantCount := 7 - index
		if stats.TopCommands[index].Count != wantCount {
			t.Fatalf("top commands = %+v", stats.TopCommands)
		}
	}
	wantAverage := time.Duration(len(entries)-1) * time.Second / time.Duration(len(entries))
	if stats.AverageDuration != wantAverage {
		t.Fatalf("average duration = %v, want %v", stats.AverageDuration, wantAverage)
	}
	if stats.FailureRate != 0 || stats.KnownExits != 0 {
		t.Fatalf("failure rate = %v over %d exits, want no known exits",
			stats.FailureRate, stats.KnownExits)
	}
}

func assertCommandStats(t *testing.T, got, want []CommandStat) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("command stats = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("command stats = %+v, want %+v", got, want)
		}
	}
}
