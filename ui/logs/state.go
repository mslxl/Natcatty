// Package logs renders the connection log and shell/command history surfaces.
// The state owns filters, paging, and the row actions; the view renders them.
// Records are read from the history service and never written here except
// through its delete and clear methods, so both surfaces share one store.
package logs

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

// Tab selects which record family the surface lists.
type Tab int

const (
	// TabConnections lists connection attempts.
	TabConnections Tab = iota
	// TabHistory lists recorded command executions.
	TabHistory
)

// ErrHostMissing reports a record whose Host has been deleted from the Vault.
// The composition root translates the shell's unavailable-Host error into it
// so this package need not know the shell.
var ErrHostMissing = errors.New("logs: the related Host no longer exists")

// Problem is a localized problem code. The view maps it to a catalog string;
// the state never carries user-visible text.
type Problem int

const (
	ProblemNone Problem = iota
	ProblemLoad
	ProblemDelete
	ProblemClear
	ProblemOpen
	ProblemHostMissing
	ProblemDate
)

// Source is the read/write seam over the history service.
type Source interface {
	ConnectionLogs(context.Context) ([]domainhistory.ConnectionLog, error)
	Entries(context.Context) ([]domainhistory.Entry, error)
	DeleteConnectionLog(context.Context, string) error
	DeleteEntry(context.Context, string) error
	ClearHistory(context.Context) (int, error)
	Revision() uint64
	Subscribe(func()) func()
}

// TerminalActions opens or focuses the terminal related to a row.
type TerminalActions interface {
	OpenTerminal(model.SessionID, model.HostID) error
}

// HostOption is one value of the Host filter.
type HostOption struct {
	// Key is "", "local", or a Host identity.
	Key   string
	Label string
}

// PageSize is how many matching rows render before Show more.
const PageSize = 100

// State is the Logs and History surface state. It is mutated on the UI thread;
// the only cross-goroutine entry point is the refresh callback installed by
// WithRefreshRequest, which just asks for a redraw.
type State struct {
	source  Source
	actions TerminalActions
	redraw  func()
	// afterMutation lets the composition root refresh a consumer that shares
	// the store, so deleting or clearing history stops autocomplete from
	// suggesting rows that no longer exist.
	afterMutation func()

	logs    []domainhistory.ConnectionLog
	entries []domainhistory.Entry
	loaded  bool
	// revision is the source revision the lists were read at.
	revision uint64

	tab          Tab
	search       string
	status       domainhistory.ConnectionStatus
	sourceFilter string
	hostKey      string
	fromText     string
	toText       string
	visible      int

	pendingClear bool
	cleared      int
	problem      Problem
	openProblem  Problem
}

// Option customizes a State.
type Option func(*State)

// WithRefreshRequest receives a redraw request when the source changes on
// another goroutine.
func WithRefreshRequest(fn func()) Option {
	return func(s *State) { s.redraw = fn }
}

// WithMutationHook receives a callback after a successful delete or clear, so
// another consumer of the same store (autocomplete) can reload.
func WithMutationHook(fn func()) Option {
	return func(s *State) { s.afterMutation = fn }
}

// NewState returns a State over source and actions and subscribes to source
// changes. source may be nil in tests that only exercise filters.
func NewState(source Source, actions TerminalActions, opts ...Option) *State {
	state := &State{source: source, actions: actions, visible: PageSize}
	for _, opt := range opts {
		opt(state)
	}
	if source != nil {
		source.Subscribe(func() {
			if state.redraw != nil {
				state.redraw()
			}
		})
	}
	return state
}

// Tab returns the selected tab.
func (s *State) Tab() Tab { return s.tab }

// SelectTab changes the selected tab, resets paging, and clears a pending
// clear confirmation.
func (s *State) SelectTab(tab Tab) {
	if s.tab == tab {
		return
	}
	s.tab = tab
	s.visible = PageSize
	s.pendingClear = false
	s.cleared = 0
}

// Refresh reloads the lists when the source revision changed. The view calls
// it at the start of a frame, which keeps database reads on the UI thread and
// out of the source's notification goroutine.
func (s *State) Refresh() {
	if s.source == nil {
		return
	}
	revision := s.source.Revision()
	if s.loaded && revision == s.revision {
		return
	}
	logs, err := s.source.ConnectionLogs(context.Background())
	if err != nil {
		s.problem = ProblemLoad
		return
	}
	entries, err := s.source.Entries(context.Background())
	if err != nil {
		s.problem = ProblemLoad
		return
	}
	s.logs = logs
	s.entries = entries
	s.loaded = true
	s.revision = revision
	s.problem = ProblemNone
}

// Problem returns the current problem code.
func (s *State) Problem() Problem { return s.problem }

// OpenProblem returns the problem from the last row action.
func (s *State) OpenProblem() Problem { return s.openProblem }

// Cleared returns how many history entries the last confirmed clear removed.
func (s *State) Cleared() int { return s.cleared }

// Search returns the editable search draft.
func (s *State) Search() *string { return &s.search }

// SetSearch stores the search text and resets paging.
func (s *State) SetSearch(text string) {
	s.search = text
	s.visible = PageSize
}

// Status returns the selected connection status filter.
func (s *State) Status() domainhistory.ConnectionStatus { return s.status }

// SetStatus stores the connection status filter and resets paging.
func (s *State) SetStatus(status domainhistory.ConnectionStatus) {
	s.status = status
	s.visible = PageSize
}

// Source returns the selected history source filter.
func (s *State) SourceFilter() string { return s.sourceFilter }

// SetSourceFilter stores the history source filter and resets paging.
func (s *State) SetSourceFilter(source string) {
	s.sourceFilter = source
	s.visible = PageSize
}

// HostKey returns the selected Host filter key.
func (s *State) HostKey() string { return s.hostKey }

// SetHostKey stores the Host filter key and resets paging.
func (s *State) SetHostKey(key string) {
	s.hostKey = key
	s.visible = PageSize
}

// From returns the editable start-date draft, YYYY-MM-DD.
func (s *State) From() *string { return &s.fromText }

// To returns the editable end-date draft, YYYY-MM-DD.
func (s *State) To() *string { return &s.toText }

// SetDateRange stores the date drafts and resets paging. The view calls it
// when either field changes.
func (s *State) SetDateRange(from, to string) {
	s.fromText = from
	s.toText = to
	s.visible = PageSize
}

// HostOptions lists the Host filter values present in the records plus the
// local shell. Labels come from the records, so a deleted Host remains
// identifiable by its recorded label.
func (s *State) HostOptions() []HostOption {
	options := []HostOption{{Key: "", Label: ""}, {Key: domainhistory.LocalHostKey, Label: ""}}
	labels := make(map[string]string)
	add := func(hostID model.HostID, label, hostname string) {
		key := string(hostID)
		if hostID == "" {
			key = domainhistory.LocalHostKey
		}
		display := label
		if display == "" {
			display = hostname
		}
		if display == "" {
			display = string(hostID)
		}
		if key != domainhistory.LocalHostKey {
			if existing, ok := labels[key]; !ok || existing == string(hostID) {
				labels[key] = display
			}
		}
	}
	for _, log := range s.logs {
		add(log.HostID, log.HostLabel, log.Hostname)
	}
	for _, entry := range s.entries {
		add(entry.HostID, entry.HostLabel, "")
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return strings.ToLower(labels[keys[i]]) < strings.ToLower(labels[keys[j]])
	})
	for _, key := range keys {
		options = append(options, HostOption{Key: key, Label: labels[key]})
	}
	return options
}

// dateRange parses the drafts into a filter range. A blank field is open; a
// malformed or inverted range reports ProblemDate and matches nothing, so a
// typo never silently shows an unrelated list.
func (s *State) dateRange() (time.Time, time.Time, bool) {
	from, ok := parseDay(s.fromText)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	to, ok := parseDay(s.toText)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	if !to.IsZero() {
		to = to.Add(24 * time.Hour)
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func parseDay(text string) (time.Time, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, true
	}
	parsed, err := time.ParseInLocation("2006-01-02", text, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// DateProblem reports a malformed date draft.
func (s *State) DateProblem() bool {
	_, _, ok := s.dateRange()
	return !ok
}

func (s *State) connectionFilter() domainhistory.ConnectionFilter {
	from, to, ok := s.dateRange()
	if !ok {
		return domainhistory.ConnectionFilter{From: farFuture, To: farFuture.Add(time.Hour)}
	}
	return domainhistory.ConnectionFilter{
		Host:   s.hostKey,
		Status: s.status,
		Query:  s.search,
		From:   from,
		To:     to,
	}
}

func (s *State) entryFilter() domainhistory.EntryFilter {
	from, to, ok := s.dateRange()
	if !ok {
		return domainhistory.EntryFilter{From: farFuture, To: farFuture.Add(time.Hour)}
	}
	return domainhistory.EntryFilter{
		Host:   s.hostKey,
		Source: s.sourceFilter,
		Query:  s.search,
		From:   from,
		To:     to,
	}
}

// farFuture is a bound no record reaches; it makes an invalid date range match
// nothing instead of everything.
var farFuture = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// FilteredLogs returns every connection log matching the filters.
func (s *State) FilteredLogs() []domainhistory.ConnectionLog {
	filter := s.connectionFilter()
	matched := make([]domainhistory.ConnectionLog, 0, len(s.logs))
	for _, log := range s.logs {
		if filter.Match(log) {
			matched = append(matched, log)
		}
	}
	return matched
}

// FilteredEntries returns every history entry matching the filters.
func (s *State) FilteredEntries() []domainhistory.Entry {
	filter := s.entryFilter()
	matched := make([]domainhistory.Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if filter.Match(entry) {
			matched = append(matched, entry)
		}
	}
	return matched
}

// VisibleLogs returns the paged connection logs.
func (s *State) VisibleLogs() []domainhistory.ConnectionLog {
	return page(s.FilteredLogs(), s.visible)
}

// VisibleEntries returns the paged history entries.
func (s *State) VisibleEntries() []domainhistory.Entry {
	return page(s.FilteredEntries(), s.visible)
}

func page[T any](items []T, visible int) []T {
	if visible <= 0 || visible > len(items) {
		visible = len(items)
	}
	return items[:visible]
}

// HasMore reports whether another page is available on the selected tab.
func (s *State) HasMore() bool {
	if s.tab == TabConnections {
		return len(s.FilteredLogs()) > s.visible
	}
	return len(s.FilteredEntries()) > s.visible
}

// ShowMore reveals the next page.
func (s *State) ShowMore() {
	s.visible += PageSize
}

// TotalLogs reports how many connection logs exist before filtering.
func (s *State) TotalLogs() int { return len(s.logs) }

// TotalEntries reports how many history entries exist before filtering.
func (s *State) TotalEntries() int { return len(s.entries) }

// DeleteLog removes one connection log.
func (s *State) DeleteLog(id string) {
	if s.source == nil {
		return
	}
	if err := s.source.DeleteConnectionLog(context.Background(), id); err != nil {
		s.problem = ProblemDelete
		return
	}
	s.problem = ProblemNone
	s.reload()
	s.mutated()
}

// DeleteEntry removes one recorded command execution.
func (s *State) DeleteEntry(id string) {
	if s.source == nil {
		return
	}
	if err := s.source.DeleteEntry(context.Background(), id); err != nil {
		s.problem = ProblemDelete
		return
	}
	s.problem = ProblemNone
	s.reload()
	s.mutated()
}

// RequestClear opens the clear-history confirmation.
func (s *State) RequestClear() { s.pendingClear = true }

// CancelClear closes the clear-history confirmation.
func (s *State) CancelClear() { s.pendingClear = false }

// ConfirmClear removes every history entry.
func (s *State) ConfirmClear() {
	s.pendingClear = false
	if s.source == nil {
		return
	}
	removed, err := s.source.ClearHistory(context.Background())
	if err != nil {
		s.problem = ProblemClear
		return
	}
	s.problem = ProblemNone
	s.cleared = removed
	s.reload()
	s.mutated()
}

func (s *State) mutated() {
	if s.afterMutation != nil {
		s.afterMutation()
	}
}

// PendingClear reports whether the clear-history confirmation is open.
func (s *State) PendingClear() bool { return s.pendingClear }

// OpenTerminal opens or focuses the terminal related to a row and records a
// localized problem code when that fails.
func (s *State) OpenTerminal(sessionID model.SessionID, hostID model.HostID) {
	s.openProblem = ProblemNone
	if s.actions == nil {
		return
	}
	err := s.actions.OpenTerminal(sessionID, hostID)
	switch {
	case err == nil:
	case errors.Is(err, ErrHostMissing):
		s.openProblem = ProblemHostMissing
	default:
		s.openProblem = ProblemOpen
	}
}

func (s *State) reload() {
	s.loaded = false
	s.Refresh()
}
