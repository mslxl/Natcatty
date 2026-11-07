package history

import (
	"sort"
	"strings"
	"time"
)

// StatisticsTopLimit bounds each "top" list (commands and directories) in a
// Statistics summary, so a large journal cannot make those ranked lists
// unbounded. Host/origin counts list every group because they answer "where",
// not "what did I run most".
const StatisticsTopLimit = 5

// CommandStat is one command and how many times it ran in the summarized set.
type CommandStat struct {
	Command string
	Count   int
}

// CwdStat is one working directory and how many summarized commands ran there.
type CwdStat struct {
	CWD   string
	Count int
}

// HostStat is one Host or origin and its number of commands. Key uses the same
// vocabulary as the History Host filter (a Host identity, LocalHostKey,
// UnsavedHostKey, or an origin key), and Label is the recorded display value.
type HostStat struct {
	Key   string
	Label string
	Count int
}

// HourStat is the hour of day with the most summarized commands and that
// hour's count.
type HourStat struct {
	Hour  int
	Count int
}

// Statistics summarizes a live Command History result set. It is computed from
// the rows after the active History filters, so the summary and the list always
// describe the same commands.
//
// Failures count only rows whose exit status was observed and non-zero;
// FailureRate divides those by KnownExits rather than by Total, because an
// unknown exit is neither a success nor a failure. AverageDuration covers only
// rows with a known finish time; DurationsUnknown counts the excluded rows so
// the UI can explain the omission.
type Statistics struct {
	Total            int
	Unique           int
	Failures         int
	KnownExits       int
	FailureRate      float64
	AverageDuration  time.Duration
	DurationsKnown   int
	DurationsUnknown int
	TopCommands      []CommandStat
	TopCwds          []CwdStat
	BusiestHour      *HourStat
	Hosts            []HostStat
}

// ComputeStatistics summarizes entries. It reads only the public Entry fields
// and is deterministic: ranked lists sort by count, then by value, so the
// same result set always produces the same summary.
func ComputeStatistics(entries []Entry) Statistics {
	stats := Statistics{}
	commandCounts := make(map[string]int)
	cwdCounts := make(map[string]int)
	hourCounts := make(map[int]int)
	hostCounts := make(map[string]*HostStat)
	var durationSum time.Duration

	for _, entry := range entries {
		stats.Total++
		if command := strings.TrimSpace(entry.Command); command != "" {
			commandCounts[command]++
		}
		if entry.HasExitCode {
			stats.KnownExits++
			if entry.ExitCode != 0 {
				stats.Failures++
			}
		}
		if entry.FinishedAt.IsZero() {
			stats.DurationsUnknown++
		} else {
			stats.DurationsKnown++
			duration := entry.Duration
			if duration < 0 {
				// Duration is clamped to zero when a command is recorded; an old
				// or imported row must not pull the average below zero.
				duration = 0
			}
			durationSum += duration
		}
		if !entry.RecordedAt.IsZero() {
			hourCounts[entry.RecordedAt.Hour()]++
		}
		if cwd := strings.TrimSpace(entry.CWD); cwd != "" {
			cwdCounts[cwd]++
		}
		key, label := statisticsHostKey(entry)
		host := hostCounts[key]
		if host == nil {
			host = &HostStat{Key: key, Label: label}
			hostCounts[key] = host
		}
		host.Count++
		if host.Label == "" && label != "" {
			host.Label = label
		}
	}

	stats.Unique = len(commandCounts)
	if stats.KnownExits > 0 {
		stats.FailureRate = float64(stats.Failures) / float64(stats.KnownExits)
	}
	if stats.DurationsKnown > 0 {
		stats.AverageDuration = durationSum / time.Duration(stats.DurationsKnown)
	}
	stats.TopCommands = topCommands(commandCounts)
	stats.TopCwds = topCwds(cwdCounts)
	stats.Hosts = hostStats(hostCounts)
	for hour := 0; hour < 24; hour++ {
		count := hourCounts[hour]
		if count == 0 {
			continue
		}
		if stats.BusiestHour == nil || count > stats.BusiestHour.Count {
			stats.BusiestHour = &HourStat{Hour: hour, Count: count}
		}
	}
	return stats
}

// statisticsHostKey groups a row by the Host filter vocabulary it already
// uses. A saved Host groups by identity, an imported or synced row by its
// immutable origin, and an unsaved Host by its captured origin, which keeps
// two typed Hosts distinct. A typed Host with no origin falls back to the
// unsaved marker, a captured local row to the local marker, and a row with no
// Host identity or origin to an empty origin key, so the Host counts always
// add up to Total.
func statisticsHostKey(entry Entry) (string, string) {
	switch {
	case entry.HostID != "":
		return string(entry.HostID), entry.HostLabel
	case entry.IsLocal():
		return LocalHostKey, ""
	case entry.IsUnsaved() && entry.Origin != "":
		return OriginHostKey(entry.Origin), entry.Origin
	case entry.IsUnsaved():
		return UnsavedHostKey, entry.HostLabel
	case entry.Origin != "":
		return OriginHostKey(entry.Origin), entry.Origin
	default:
		return OriginHostKey(""), ""
	}
}

// rankedKeys returns the most frequent values, capped at StatisticsTopLimit.
// Equal counts sort case-insensitively by value, then by value, so the ranking
// is stable for any input order.
func rankedKeys(counts map[string]int) []string {
	values := make([]string, 0, len(counts))
	for value := range counts {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		if counts[values[i]] != counts[values[j]] {
			return counts[values[i]] > counts[values[j]]
		}
		left, right := strings.ToLower(values[i]), strings.ToLower(values[j])
		if left == right {
			return values[i] < values[j]
		}
		return left < right
	})
	if len(values) > StatisticsTopLimit {
		values = values[:StatisticsTopLimit]
	}
	return values
}

func topCommands(counts map[string]int) []CommandStat {
	values := rankedKeys(counts)
	out := make([]CommandStat, 0, len(values))
	for _, value := range values {
		out = append(out, CommandStat{Command: value, Count: counts[value]})
	}
	return out
}

func topCwds(counts map[string]int) []CwdStat {
	values := rankedKeys(counts)
	out := make([]CwdStat, 0, len(values))
	for _, value := range values {
		out = append(out, CwdStat{CWD: value, Count: counts[value]})
	}
	return out
}

// statisticsHostSortName is the label a Host statistic sorts under. A local or
// otherwise unlabelled group falls back to its filter key, so the ranked list
// stays deterministic without depending on a locale.
func statisticsHostSortName(host HostStat) string {
	name := host.Label
	if name == "" {
		name = host.Key
	}
	return strings.ToLower(name)
}

func hostStats(counts map[string]*HostStat) []HostStat {
	out := make([]HostStat, 0, len(counts))
	for _, host := range counts {
		out = append(out, *host)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		left, right := statisticsHostSortName(out[i]), statisticsHostSortName(out[j])
		if left != right {
			return left < right
		}
		return out[i].Key < out[j].Key
	})
	return out
}
