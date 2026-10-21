// Package history holds the connection log and shell/command history records
// and their deterministic retention and filter rules. It has no storage, UI,
// or transport knowledge: adapters persist these records and application
// services decide when to record and prune them.
package history

import (
	"sort"
	"strings"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// MaxCommandLength bounds one recorded command so a malformed line cannot
// grow the history without limit. It matches autocomplete's accepted length.
const MaxCommandLength = 4096

// ConnectionStatus is the outcome of one connection attempt.
type ConnectionStatus string

const (
	// ConnectionConnected reports an attempt that opened a Session.
	ConnectionConnected ConnectionStatus = "connected"
	// ConnectionFailed reports an attempt that did not open a Session.
	ConnectionFailed ConnectionStatus = "failed"
)

// Valid reports whether the status is one the application knows.
func (status ConnectionStatus) Valid() bool {
	return status == ConnectionConnected || status == ConnectionFailed
}

// ConnectionLog is one recorded connection attempt. A local Session has an
// empty HostID; a Host Session keeps the Host identity so a log row can open
// the related terminal again.
type ConnectionLog struct {
	ID        string           `json:"id"`
	SessionID model.SessionID  `json:"sessionId,omitempty"`
	HostID    model.HostID     `json:"hostId,omitempty"`
	HostLabel string           `json:"hostLabel,omitempty"`
	Hostname  string           `json:"hostname,omitempty"`
	Username  string           `json:"username,omitempty"`
	Kind      string           `json:"kind"`
	Status    ConnectionStatus `json:"status"`
	Error     string           `json:"error,omitempty"`
	StartedAt time.Time        `json:"startedAt"`
	EndedAt   time.Time        `json:"endedAt,omitempty"`
}

// Valid reports whether the log carries the minimum identity and timing a
// persisted row needs.
func (log ConnectionLog) Valid() bool {
	return log.ID != "" && log.Kind != "" && log.Status.Valid() && !log.StartedAt.IsZero()
}

// LocalHostKey is the Host filter value that selects local records, which
// have no Host identity.
const LocalHostKey = "local"

// ConnectionFilter narrows a connection log list. Zero fields match
// everything. Host is LocalHostKey for local records, a Host identity
// otherwise. From is inclusive and To is exclusive.
type ConnectionFilter struct {
	Host   string
	Status ConnectionStatus
	Query  string
	From   time.Time
	To     time.Time
}

// Match reports whether log satisfies the filter.
func (filter ConnectionFilter) Match(log ConnectionLog) bool {
	switch filter.Host {
	case "":
	case LocalHostKey:
		if log.HostID != "" {
			return false
		}
	default:
		if string(log.HostID) != filter.Host {
			return false
		}
	}
	if filter.Status != "" && log.Status != filter.Status {
		return false
	}
	if !filter.From.IsZero() && log.StartedAt.Before(filter.From) {
		return false
	}
	if !filter.To.IsZero() && !log.StartedAt.Before(filter.To) {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	if query == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		log.HostLabel, log.Hostname, log.Username, log.Kind, log.Error,
	}, "\n"))
	return strings.Contains(haystack, query)
}

// Source names recorded shell history. Session means a command submitted
// inside a Natcatty Session; shell-history means a row imported from the
// local shell's history file.
const (
	SourceSession      = "session"
	SourceShellHistory = "shell-history"
)

// Entry is one recorded command execution. It is a per-execution journal, so
// autocomplete aggregates it while the history surface lists each execution.
type Entry struct {
	ID         string          `json:"id"`
	SessionID  model.SessionID `json:"sessionId,omitempty"`
	HostID     model.HostID    `json:"hostId,omitempty"`
	HostLabel  string          `json:"hostLabel,omitempty"`
	Scope      string          `json:"scope"`
	Command    string          `json:"command"`
	Source     string          `json:"source"`
	RecordedAt time.Time       `json:"recordedAt"`
}

// Valid reports whether the entry carries the minimum persisted identity.
func (entry Entry) Valid() bool {
	return entry.ID != "" && entry.Scope != "" && entry.Command != "" &&
		!entry.RecordedAt.IsZero()
}

// EntryFilter narrows a command history list. Zero fields match everything.
// From is inclusive and To is exclusive.
type EntryFilter struct {
	Host   string
	Source string
	Query  string
	From   time.Time
	To     time.Time
}

// Match reports whether entry satisfies the filter.
func (filter EntryFilter) Match(entry Entry) bool {
	switch filter.Host {
	case "":
	case LocalHostKey:
		if entry.HostID != "" {
			return false
		}
	default:
		if string(entry.HostID) != filter.Host {
			return false
		}
	}
	if filter.Source != "" && entry.Source != filter.Source {
		return false
	}
	if !filter.From.IsZero() && entry.RecordedAt.Before(filter.From) {
		return false
	}
	if !filter.To.IsZero() && !entry.RecordedAt.Before(filter.To) {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	if query == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		entry.Command, entry.HostLabel, string(entry.HostID), entry.Source, entry.Scope,
	}, "\n"))
	return strings.Contains(haystack, query)
}

// Retention bounds one record family. Count zero keeps every entry; MaxAge
// zero keeps entries of every age.
type Retention struct {
	Count  int
	MaxAge time.Duration
}

// Bounded reports whether the retention removes anything.
func (retention Retention) Bounded() bool {
	return retention.Count > 0 || retention.MaxAge > 0
}

// Keep reports whether a record recorded at at survives retention now. Age is
// the only criterion here; the newest Count are kept by Prune.
func (retention Retention) Keep(at, now time.Time) bool {
	if retention.MaxAge <= 0 {
		return true
	}
	return !at.Before(now.Add(-retention.MaxAge))
}

// NormalizeScope returns the history scope of a Host identity. An empty Host
// identity is the local shell, matching autocomplete's scope vocabulary.
func NormalizeScope(hostID model.HostID) string {
	if hostID == "" {
		return "local"
	}
	return "host:" + string(hostID)
}

// NormalizeEntry trims and bounds an entry's free text. It does not invent an
// ID, timestamp, or scope; the application service owns those defaults.
func NormalizeEntry(entry Entry) Entry {
	entry.Command = strings.TrimSpace(entry.Command)
	if len(entry.Command) > MaxCommandLength {
		entry.Command = entry.Command[:MaxCommandLength]
	}
	entry.Scope = strings.TrimSpace(entry.Scope)
	entry.HostLabel = strings.TrimSpace(entry.HostLabel)
	entry.Source = strings.TrimSpace(entry.Source)
	if entry.Source == "" {
		entry.Source = SourceSession
	}
	return entry
}

// NormalizeConnectionLog trims the display fields and derives the default
// status from the recorded error.
func NormalizeConnectionLog(log ConnectionLog) ConnectionLog {
	log.Kind = strings.TrimSpace(log.Kind)
	log.HostLabel = strings.TrimSpace(log.HostLabel)
	log.Hostname = strings.TrimSpace(log.Hostname)
	log.Username = strings.TrimSpace(log.Username)
	log.Error = strings.TrimSpace(log.Error)
	if !log.Status.Valid() {
		if log.Error == "" {
			log.Status = ConnectionConnected
		} else {
			log.Status = ConnectionFailed
		}
	}
	return log
}

// PruneConnectionLogs returns the newest logs that survive retention, ordered
// newest first. Equal timestamps tie-break by descending ID, so pruning is
// deterministic regardless of storage order.
func PruneConnectionLogs(logs []ConnectionLog, retention Retention, now time.Time) []ConnectionLog {
	kept := make([]ConnectionLog, 0, len(logs))
	for _, log := range logs {
		if retention.Keep(log.StartedAt, now) {
			kept = append(kept, log)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return newer(kept[i].StartedAt, kept[i].ID, kept[j].StartedAt, kept[j].ID)
	})
	return truncate(kept, retention.Count)
}

// PruneEntries returns the newest command entries that survive retention,
// ordered newest first. Equal timestamps tie-break by descending ID.
func PruneEntries(entries []Entry, retention Retention, now time.Time) []Entry {
	kept := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if retention.Keep(entry.RecordedAt, now) {
			kept = append(kept, entry)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return newer(kept[i].RecordedAt, kept[i].ID, kept[j].RecordedAt, kept[j].ID)
	})
	return truncate(kept, retention.Count)
}

func newer(at time.Time, id string, otherAt time.Time, otherID string) bool {
	if at.Equal(otherAt) {
		return id > otherID
	}
	return at.After(otherAt)
}

func truncate[T any](items []T, count int) []T {
	if count > 0 && len(items) > count {
		return items[:count]
	}
	return items
}
