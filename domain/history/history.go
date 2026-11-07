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

// KindLocal is the recorded transport kind of a local shell Session. It
// distinguishes a local attempt from a typed Host with no saved identity.
const KindLocal = "local"

// ConnectionLog is one recorded connection attempt. A local Session has an
// empty HostID; a Host Session keeps the Host identity so a log row can open
// the related terminal again. A typed (unsaved) Host has no identity either,
// but it is a Host attempt: UnsavedHost says so, and its recorded label and
// address keep the target the user typed.
type ConnectionLog struct {
	ID          string           `json:"id"`
	SessionID   model.SessionID  `json:"sessionId,omitempty"`
	HostID      model.HostID     `json:"hostId,omitempty"`
	HostLabel   string           `json:"hostLabel,omitempty"`
	Hostname    string           `json:"hostname,omitempty"`
	Username    string           `json:"username,omitempty"`
	UnsavedHost bool             `json:"unsavedHost,omitempty"`
	Kind        string           `json:"kind"`
	Status      ConnectionStatus `json:"status"`
	Error       string           `json:"error,omitempty"`
	StartedAt   time.Time        `json:"startedAt"`
	EndedAt     time.Time        `json:"endedAt,omitempty"`
	// Network Provider status rows describe a Network Profile's tunnel rather
	// than a Host Session. NetworkProviderPhase is the marker that tells them
	// apart; the remaining fields are display metadata and never include
	// credential material or an Auth URL. The WireGuard fields report the live
	// tunnel: the most recent handshake and the summed rx/tx counters. A zero
	// handshake time means the Provider has never completed one.
	NetworkProfileID     string    `json:"networkProfileId,omitempty"`
	NetworkProfileLabel  string    `json:"networkProfileLabel,omitempty"`
	NetworkProviderKind  string    `json:"networkProviderKind,omitempty"`
	NetworkProviderPhase string    `json:"networkProviderPhase,omitempty"`
	NetworkAddress       string    `json:"networkAddress,omitempty"`
	NetworkLastHandshake time.Time `json:"networkLastHandshake,omitempty"`
	NetworkRxBytes       uint64    `json:"networkRxBytes,omitempty"`
	NetworkTxBytes       uint64    `json:"networkTxBytes,omitempty"`
}

// IsProvider reports a Network Provider status row. Such a row is neither a
// local shell nor a typed Host: it has its own Vault Profile identity.
func (log ConnectionLog) IsProvider() bool {
	return log.NetworkProviderPhase != ""
}

// IsUnsaved reports a Host attempt the user typed without a saved Vault
// record. Rows recorded before UnsavedHost existed are classified by their
// non-local kind, so an old draft row is not mistaken for a local shell.
func (log ConnectionLog) IsUnsaved() bool {
	if log.IsProvider() {
		return false
	}
	if log.HostID != "" {
		return false
	}
	if log.UnsavedHost {
		return true
	}
	return !isLocalKind(log.Kind)
}

// IsLocal reports a local shell attempt, which has neither a saved nor a
// typed Host identity. A Network Provider status row belongs to its Profile,
// not to the local shell.
func (log ConnectionLog) IsLocal() bool {
	if log.IsProvider() {
		return false
	}
	return log.HostID == "" && !log.IsUnsaved()
}

// isLocalKind reports whether kind names the local shell. An empty kind is
// the default local kind, matching the Session service's default.
func isLocalKind(kind string) bool {
	return kind == "" || strings.EqualFold(kind, KindLocal)
}

// Valid reports whether the log carries the minimum identity and timing a
// persisted row needs.
func (log ConnectionLog) Valid() bool {
	return log.ID != "" && log.Kind != "" && log.Status.Valid() && !log.StartedAt.IsZero()
}

// LocalHostKey is the Host filter value that selects local records, which
// have no Host identity.
const LocalHostKey = "local"

// UnsavedHostKey is the Host filter value that selects attempts to a typed
// Host with no saved Vault record. It is separate from LocalHostKey so a
// local shell never matches a draft Host.
const UnsavedHostKey = "unsaved"

// ConnectionFilter narrows a connection log list. Zero fields match
// everything. Host is LocalHostKey for local records, UnsavedHostKey for
// typed Hosts with no saved identity, a Host identity otherwise. From is
// inclusive and To is exclusive.
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
		if !log.IsLocal() {
			return false
		}
	case UnsavedHostKey:
		if !log.IsUnsaved() {
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
		log.NetworkProfileLabel, log.NetworkProviderKind,
		log.NetworkProviderPhase, log.NetworkAddress,
	}, "\n"))
	return strings.Contains(haystack, query)
}

// Source names where a recorded command came from. Session means a command
// captured inside an app Session; shell-history means a row imported from the
// local shell's history file. Atuin import and sync are remote-origin rows and
// are never reported as the local shell.
const (
	SourceSession      = "session"
	SourceShellHistory = "shell-history"
	SourceAtuinImport  = "atuin-import"
	SourceAtuinSync    = "atuin-sync"
)

// Capture tiers describe how much of an execution Netcatty could observe. A
// Tier 1 row comes from an authoritative shell hook, Tier 2 from native OSC
// 133/7 integration, and Tier 3 from typed input plus prompt detection.
// Imported and synced rows have no tier.
const (
	CaptureTierHook      = 1
	CaptureTierNativeOSC = 2
	CaptureTierFallback  = 3
)

// AuthorKindUser is the Atuin author kind for a command run by a user.
const AuthorKindUser = "user"

// Entry is one recorded command execution. It is a per-execution journal, so
// autocomplete aggregates it while the history surface lists each execution.
// A captured or shell-history row carries a UUIDv7 ID; an imported or synced
// row keeps its Atuin UUID. The ID is the Atuin history id and the target of a
// tombstone. Rows recorded before these fields existed decode with zero
// values, so their interpretation is "unknown".
type Entry struct {
	ID          string          `json:"id"`
	SessionID   model.SessionID `json:"sessionId,omitempty"`
	SessionUUID string          `json:"sessionUuid,omitempty"`
	HostID      model.HostID    `json:"hostId,omitempty"`
	HostLabel   string          `json:"hostLabel,omitempty"`
	UnsavedHost bool            `json:"unsavedHost,omitempty"`
	Scope       string          `json:"scope"`
	Command     string          `json:"command"`
	CWD         string          `json:"cwd,omitempty"`
	Shell       string          `json:"shell,omitempty"`
	RecordedAt  time.Time       `json:"recordedAt"`
	FinishedAt  time.Time       `json:"finishedAt,omitempty"`
	Duration    time.Duration   `json:"duration,omitempty"`
	ExitCode    int             `json:"exitCode,omitempty"`
	HasExitCode bool            `json:"hasExitCode,omitempty"`
	Origin      string          `json:"origin,omitempty"`
	Author      string          `json:"author,omitempty"`
	AuthorKind  string          `json:"authorKind,omitempty"`
	Intent      string          `json:"intent,omitempty"`
	DeletedAt   time.Time       `json:"deletedAt,omitempty"`
	Source      string          `json:"source"`
	CaptureTier int             `json:"captureTier,omitempty"`
	// ImportLabels names the fields a Host Atuin import could not fill, or the
	// fields it derived instead of reading. It lets the History surface explain
	// a partial record rather than showing an empty value as authoritative.
	ImportLabels []string `json:"importLabels,omitempty"`
	// ImportUnknown preserves columns an Atuin database carried that this build
	// does not map. They are labelled in ImportLabels as well.
	ImportUnknown map[string]string `json:"importUnknown,omitempty"`
}

// Valid reports whether the entry carries the minimum persisted identity.
func (entry Entry) Valid() bool {
	return entry.ID != "" && entry.Scope != "" && entry.Command != "" &&
		!entry.RecordedAt.IsZero()
}

// HasUUIDIdentity reports whether the entry can be represented on the Atuin
// wire. Legacy local rows carry a non-UUID identity: they stay readable and
// deletable locally, but they are never uploaded and get no fabricated UUID.
func (entry Entry) HasUUIDIdentity() bool {
	return IsUUID(entry.ID)
}

// IsUnsaved reports a command that ran against a Host the user typed without a
// saved Vault record. Unlike a ConnectionLog, an Entry has no kind, so rows
// recorded before UnsavedHost existed cannot be told apart from local ones and
// remain local.
func (entry Entry) IsUnsaved() bool {
	return entry.HostID == "" && entry.UnsavedHost
}

// IsLocal reports a command that ran in the local shell, with neither a saved
// nor a typed Host identity. Only captured and shell-history rows can be the
// local shell; an imported or synced remote-origin row is never local.
func (entry Entry) IsLocal() bool {
	if entry.Scope != "local" || entry.HostID != "" || entry.UnsavedHost {
		return false
	}
	return isCapturedSource(entry.Source)
}

// IsLocallyOwned reports a row whose capture source is this install's: a
// session capture or a shell-history import, including a legacy row whose
// source predates the source field. Imported and synced remote-origin rows
// belong to another writer and are not local-owned by this check.
//
// The source alone cannot decide ownership: import or sync can rewrite a row's
// source while a create record from this install's writer still links it. The
// History service therefore also consults the record log
// (Store.LocallyRecordedHistoryIDs) and treats a linked row as locally owned
// even when this predicate is false.
func (entry Entry) IsLocallyOwned() bool {
	return entry.Source == "" || isCapturedSource(entry.Source)
}

// isCapturedSource reports a row this install observed, as opposed to an
// imported or synced projection of another writer's history.
func isCapturedSource(source string) bool {
	return source == SourceSession || source == SourceShellHistory
}

// OriginHostKeyPrefix marks a Host filter value that names the captured
// origin of a command rather than a saved Host identity. It is the filter key
// imported and synced rows use when they have no local Host identity.
const OriginHostKeyPrefix = "origin:"

// OriginHostKey returns the Host filter value that selects one origin.
func OriginHostKey(origin string) string {
	return OriginHostKeyPrefix + origin
}

// ExitFilter narrows a command history list by how the command exited.
// ExitAny matches every row; the other values separate a known success, a
// known non-zero failure, and a row whose exit status the shell never
// reported.
type ExitFilter string

const (
	// ExitAny keeps every exit status, known or unknown.
	ExitAny ExitFilter = ""
	// ExitSuccess selects rows known to have exited with zero.
	ExitSuccess ExitFilter = "success"
	// ExitFailure selects rows known to have exited non-zero.
	ExitFailure ExitFilter = "failure"
	// ExitUnknown selects rows whose exit status was never reported.
	ExitUnknown ExitFilter = "unknown"
)

// Match reports whether entry satisfies the exit filter. An unrecognized
// value matches nothing rather than silently behaving like ExitAny.
func (filter ExitFilter) Match(entry Entry) bool {
	switch filter {
	case ExitAny:
		return true
	case ExitSuccess:
		return entry.HasExitCode && entry.ExitCode == 0
	case ExitFailure:
		return entry.HasExitCode && entry.ExitCode != 0
	case ExitUnknown:
		return !entry.HasExitCode
	default:
		return false
	}
}

// EntryFilter narrows a command history list. Zero fields match everything.
// Host accepts the local, unsaved, and saved Host keys, or an origin key built
// by OriginHostKey. Query is split on whitespace and every token must appear
// in the command, case-insensitively. From is inclusive and To is exclusive.
type EntryFilter struct {
	Host    string
	Session string
	CWD     string
	Exit    ExitFilter
	Shell   string
	Source  string
	Query   string
	From    time.Time
	To      time.Time
}

// Match reports whether entry satisfies the filter. A typed Host entry has no
// Host identity and is not local; the unsaved key selects it, and the local
// key never does. An origin key selects rows by their captured origin. The
// Session value matches either the app Session identity or the Atuin session
// UUID, so imported and synced rows are filterable too.
func (filter EntryFilter) Match(entry Entry) bool {
	switch host := filter.Host; {
	case host == "":
	case host == LocalHostKey:
		if !entry.IsLocal() {
			return false
		}
	case host == UnsavedHostKey:
		if !entry.IsUnsaved() {
			return false
		}
	case strings.HasPrefix(host, OriginHostKeyPrefix):
		if entry.Origin != strings.TrimPrefix(host, OriginHostKeyPrefix) {
			return false
		}
	default:
		if string(entry.HostID) != host {
			return false
		}
	}
	if filter.Session != "" && string(entry.SessionID) != filter.Session &&
		entry.SessionUUID != filter.Session {
		return false
	}
	if filter.CWD != "" && entry.CWD != filter.CWD {
		return false
	}
	if !filter.Exit.Match(entry) {
		return false
	}
	if filter.Shell != "" && !strings.EqualFold(entry.Shell, filter.Shell) {
		return false
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
	command := strings.ToLower(entry.Command)
	for _, token := range strings.Fields(strings.ToLower(filter.Query)) {
		if !strings.Contains(command, token) {
			return false
		}
	}
	return true
}

// SortEntries orders entries newest first. Equal timestamps tie-break by
// descending ID, so filtering and paging are deterministic across pages and
// independent of the order a Store returned rows in.
func SortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return newer(entries[i].RecordedAt, entries[i].ID,
			entries[j].RecordedAt, entries[j].ID)
	})
}

// SortEntriesForQuery orders entries by how well their command matches query,
// then newest first, then descending identity. Every candidate already
// contains the query tokens, so the score only separates an exact command
// match from a prefix, a word prefix, and a plain substring. The time and
// identity tie-break keeps paging deterministic even when several rows match
// equally well. An empty query keeps the default newest-first order.
func SortEntriesForQuery(entries []Entry, query string) {
	query = strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if query == "" {
		SortEntries(entries)
		return
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left := queryScore(entries[i].Command, query)
		right := queryScore(entries[j].Command, query)
		if left != right {
			return left > right
		}
		return newer(entries[i].RecordedAt, entries[i].ID,
			entries[j].RecordedAt, entries[j].ID)
	})
}

// queryScore rates one command against a lowercased, whitespace-normalized
// query. The values run from an exact command match down to a substring-only
// match, so "git status" outranks "run git status" and both outrank "legit".
func queryScore(command, query string) int {
	command = strings.ToLower(strings.TrimSpace(command))
	switch {
	case command == query:
		return 4
	case strings.HasPrefix(command, query):
		return 3
	case wordPrefix(command, strings.Fields(query)[0]):
		return 2
	default:
		return 1
	}
}

// wordPrefix reports whether any word of command starts with token.
func wordPrefix(command, token string) bool {
	for _, word := range strings.Fields(command) {
		if strings.HasPrefix(word, token) {
			return true
		}
	}
	return false
}

// Retention bounds Connection Logs. Count zero keeps every log; MaxAge zero
// keeps logs of every age. Command History has no automatic retention.
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

// CaptureOrigin renders the host:user origin stored with a captured command. A
// missing hostname falls back to the recorded Host label and then "unknown"; a
// missing username is "unknown". The rule comes from the capture spec, which
// requires a value rather than an empty origin for a Host Session.
func CaptureOrigin(hostname, username, label string) string {
	host := strings.TrimSpace(hostname)
	if host == "" {
		host = strings.TrimSpace(label)
	}
	if host == "" {
		host = "unknown"
	}
	name := strings.TrimSpace(username)
	if name == "" {
		name = "unknown"
	}
	return host + ":" + name
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
	entry.SessionUUID = strings.TrimSpace(entry.SessionUUID)
	entry.CWD = strings.TrimSpace(entry.CWD)
	entry.Shell = strings.TrimSpace(entry.Shell)
	entry.Origin = strings.TrimSpace(entry.Origin)
	entry.Author = strings.TrimSpace(entry.Author)
	entry.AuthorKind = strings.TrimSpace(entry.AuthorKind)
	entry.Intent = strings.TrimSpace(entry.Intent)
	entry.Source = strings.TrimSpace(entry.Source)
	if entry.Source == "" {
		entry.Source = SourceSession
	}
	if isCapturedSource(entry.Source) {
		// A captured command defaults to the origin user as its author; an
		// imported or synced row keeps whatever its Atuin record carried.
		if entry.AuthorKind == "" {
			entry.AuthorKind = AuthorKindUser
		}
		if entry.Author == "" {
			entry.Author = authorFromOrigin(entry.Origin)
		}
	}
	return entry
}

// authorFromOrigin returns the user part of a host:user origin. The last colon
// delimits the user so a bracketed IPv6 host stays intact.
func authorFromOrigin(origin string) string {
	index := strings.LastIndex(origin, ":")
	if index < 0 || index == len(origin)-1 {
		return ""
	}
	return strings.TrimSpace(origin[index+1:])
}

// NormalizeConnectionLog trims the display fields, derives the default status
// from the recorded error, and classifies an attempt with no Host identity as
// either the local shell or an unsaved Host. A local attempt keeps no label,
// so the Logs surface localizes its title; an unsaved Host keeps the typed
// label and address and is marked so filters can tell it from local.
func NormalizeConnectionLog(log ConnectionLog) ConnectionLog {
	log.Kind = strings.TrimSpace(log.Kind)
	log.HostLabel = strings.TrimSpace(log.HostLabel)
	log.Hostname = strings.TrimSpace(log.Hostname)
	log.Username = strings.TrimSpace(log.Username)
	log.Error = strings.TrimSpace(log.Error)
	log.NetworkProfileID = strings.TrimSpace(log.NetworkProfileID)
	log.NetworkProfileLabel = strings.TrimSpace(log.NetworkProfileLabel)
	log.NetworkProviderKind = strings.TrimSpace(log.NetworkProviderKind)
	log.NetworkProviderPhase = strings.TrimSpace(log.NetworkProviderPhase)
	log.NetworkAddress = strings.TrimSpace(log.NetworkAddress)
	if log.IsProvider() {
		// A provider row keeps the Profile label; it is not a draft Host and
		// the local-shell title must not replace it.
		log.UnsavedHost = false
		if log.NetworkProfileLabel == "" {
			log.NetworkProfileLabel = log.HostLabel
		}
		if log.HostLabel == "" {
			log.HostLabel = log.NetworkProfileLabel
		}
	} else if log.HostID == "" {
		if isLocalKind(log.Kind) {
			log.UnsavedHost = false
			log.HostLabel = ""
		} else {
			log.UnsavedHost = true
		}
	}
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
