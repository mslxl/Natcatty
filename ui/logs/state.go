// Package logs renders the connection log and shell/command history surfaces.
// The state owns filters, paging, and the row actions; the view renders them.
// Records are read from the history service and never written here except
// through its delete and clear methods, so both surfaces share one store.
package logs

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

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

// HistoryMode selects the History tab's view. Commands and Statistics share
// the same active filters; the summary is a separate view, not a filter.
type HistoryMode int

const (
	// HistoryModeCommands lists the matching command rows.
	HistoryModeCommands HistoryMode = iota
	// HistoryModeStatistics summarizes the matching rows.
	HistoryModeStatistics
)

// ErrHostMissing reports a record whose Host has been deleted from the Vault.
// The composition root translates the shell's unavailable-Host error into it
// so this package need not know the shell.
var ErrHostMissing = errors.New("logs: the related Host no longer exists")

// ErrNoActiveSession reports that no focused Session can receive a history
// command. The composition root supplies it; the surface never guesses.
var ErrNoActiveSession = errors.New("logs: no active Session")

// ErrInsertUnsafe reports a multi-line command the focused shell cannot
// insert without executing it. The composition root translates the session
// surface's refusal into it; the surface never types the command instead.
var ErrInsertUnsafe = errors.New("logs: the shell cannot insert multi-line text safely")

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
	ProblemPrune
	ProblemInsert
	ProblemInsertUnsafe
)

// Source is the read/write seam over the history service.
type Source interface {
	ConnectionLogs(context.Context) ([]domainhistory.ConnectionLog, error)
	Entries(context.Context) ([]domainhistory.Entry, error)
	DeleteConnectionLog(context.Context, string) error
	DeleteEntry(context.Context, string) error
	ClearConnectionLogs(context.Context) (int, error)
	// ClearThisDevice soft-deletes locally owned rows and leaves imported and
	// synced remote-origin rows in place.
	ClearThisDevice(context.Context) (int, error)
	// PruneHistoryEntries soft-deletes exactly the listed live rows. The UI
	// passes the identities its preview showed, so a row that arrives between
	// preview and confirm is never removed unseen.
	PruneHistoryEntries(context.Context, []string) (int, error)
	Revision() uint64
	Subscribe(func()) func()
}

// TerminalActions opens or focuses the terminal related to a row.
type TerminalActions interface {
	OpenTerminal(model.SessionID, model.HostID) error
}

// CommandActions inserts a history command into the focused Session without
// running it. Insertion is always an explicit user action.
type CommandActions interface {
	InsertCommand(command string) error
}

// HostOption is one value of the Host filter.
type HostOption struct {
	// Key is "", "local", "unsaved", a Host identity, or an origin key.
	Key   string
	Label string
	// Count is how many rows in the facet's result set carry this value.
	Count int
}

// FacetValue is one distinct value present in the current result set, with
// the number of matching rows.
type FacetValue struct {
	Value string
	Count int
}

// Facets are the filter values present in the current result set. Each
// dimension is computed with its own filter cleared, so selecting one value
// does not hide the other values the user can switch to.
type Facets struct {
	Hosts    []HostOption
	Sessions []FacetValue
	Cwds     []FacetValue
	Shells   []FacetValue
	Sources  []FacetValue
	Exits    []FacetValue
}

// PruneFilter is the editable filter set of the Prune matching dialog. It is
// seeded from the History filters when the dialog opens, then applies to the
// prune that the dialog confirms.
type PruneFilter struct {
	Query   string
	Host    string
	Session string
	CWD     string
	Exit    domainhistory.ExitFilter
	Shell   string
	Source  string
	From    string
	To      string
}

// entryFilter converts the prune draft into the shared history filter. ok is
// false for a malformed or inverted date range, which must not match rows.
func (draft PruneFilter) entryFilter() (domainhistory.EntryFilter, bool) {
	from, ok := parseDay(draft.From)
	if !ok {
		return domainhistory.EntryFilter{}, false
	}
	to, ok := parseDay(draft.To)
	if !ok {
		return domainhistory.EntryFilter{}, false
	}
	if !to.IsZero() {
		to = to.Add(24 * time.Hour)
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return domainhistory.EntryFilter{}, false
	}
	return domainhistory.EntryFilter{
		Query:   draft.Query,
		Host:    draft.Host,
		Session: draft.Session,
		CWD:     draft.CWD,
		Exit:    draft.Exit,
		Shell:   draft.Shell,
		Source:  draft.Source,
		From:    from,
		To:      to,
	}, true
}

// focusTarget names the field OpenGlobalSearch asks the next frame to focus.
// It is a FocusBind value and must stay comparable.
type focusTarget int

const (
	focusNone focusTarget = iota
	focusHistorySearch
)

// PageSize is how many matching rows render before Show more.
const PageSize = 100

// State is the Logs and History surface state. It is mutated on the UI thread;
// the only cross-goroutine entry point is the refresh callback installed by
// WithRefreshRequest, which just asks for a redraw.
type State struct {
	// pickers are the calendars of the From and To filters while unset.
	pickers  [2]datePicker
	source   Source
	actions  TerminalActions
	commands CommandActions
	redraw   func()
	// afterMutation lets the composition root refresh a consumer that shares
	// the store, so deleting or clearing history stops autocomplete from
	// suggesting rows that no longer exist.
	afterMutation func()

	logs    []domainhistory.ConnectionLog
	entries []domainhistory.Entry
	loaded  bool
	// revision is the source revision the lists were read at.
	revision uint64

	// accountPanel opens the Settings Logs area's Atuin account panel. It is
	// nil when the composition root has no account service.
	accountPanel func()

	tab          Tab
	historyMode  HistoryMode
	search       string
	status       domainhistory.ConnectionStatus
	sourceFilter string
	hostKey      string
	sessionKey   string
	cwdKey       string
	shellKey     string
	exitKey      domainhistory.ExitFilter
	fromText     string
	toText       string
	visible      int
	// focusRequest is the FocusBind field the search input reads. Assigning
	// focusHistorySearch asks the next frame to focus the search field.
	focusRequest focusTarget

	pendingClear    bool
	pendingDeleteID string
	cleared         int
	clearedTab      Tab

	// Prune matching uses its own draft filters, seeded from the History
	// filters when the dialog opens, so a failed or cancelled prune never
	// changes what the surface is showing.
	pendingPrune bool
	pruneFilter  PruneFilter
	pruneLimit   string
	pruned       int
	// selectedEntryID names the history row whose detail panel is open. The
	// panel is inline under that row, so a reload that removes the row also
	// closes the panel without leaving it detached.
	selectedEntryID string
	problem         Problem
	// problemTab names the record family a clear failed for, so the banner
	// says "connection logs" or "history" based on the action, not on the tab
	// the user happens to be looking at now.
	problemTab  Tab
	openProblem Problem

	// scroll is the surface's vertical offset. The whole surface scrolls, so
	// the records stay reachable even when the window is too short for the
	// title, tabs, and filters. The view binds it, and tests read it to prove
	// real wheel and key input moved the list.
	scroll ui.ScrollState
}

// Option customizes a State.
type Option func(*State)

// WithRefreshRequest receives a redraw request when the source changes on
// another goroutine.
func WithRefreshRequest(fn func()) Option {
	return func(s *State) { s.redraw = fn }
}

// WithMutationHook receives a callback after a successful delete, prune, or
// clear, so another consumer of the same store (autocomplete) can reload.
func WithMutationHook(fn func()) Option {
	return func(s *State) { s.afterMutation = fn }
}

// WithAccountPanel adds the History surface's entry point to the single Atuin
// account panel. The composition root opens Settings at the Logs area; this
// package stays independent of the Settings window.
func WithAccountPanel(fn func()) Option {
	return func(s *State) { s.accountPanel = fn }
}

// AccountPanelAvailable reports whether the surface can show the account
// entry point.
func (s *State) AccountPanelAvailable() bool { return s.accountPanel != nil }

// OpenAccountPanel asks the composition root to show the account panel.
func (s *State) OpenAccountPanel() {
	if s.accountPanel != nil {
		s.accountPanel()
	}
}

// WithCommandActions supplies the explicit Insert into the focused Session
// action. Nil leaves Insert reporting that no Session is active.
func WithCommandActions(actions CommandActions) Option {
	return func(s *State) { s.commands = actions }
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

// SelectTab changes the selected tab, resets paging, and clears the pending
// confirmations and result banners that belong to the previous tab.
func (s *State) SelectTab(tab Tab) {
	if s.tab == tab {
		return
	}
	s.tab = tab
	s.visible = PageSize
	s.pendingClear = false
	s.pendingPrune = false
	s.pendingDeleteID = ""
	s.cleared = 0
	s.pruned = 0
	s.selectedEntryID = ""
	// Each tab starts at its top rather than at the other list's offset. The
	// unsaved Host key belongs to the record family that offered it; carrying
	// it across would hide every row of a family with no unsaved records.
	// Search, source/status, and date filters stay shared: they apply to both
	// families and losing them on every tab switch would surprise more than it
	// helps.
	s.scroll.Y = 0
	if s.hostKey == domainhistory.UnsavedHostKey {
		s.hostKey = ""
	}
	// An origin key only selects command history rows; leaving it in place on
	// the Connections tab would hide every connection log.
	if tab == TabConnections &&
		strings.HasPrefix(s.hostKey, domainhistory.OriginHostKeyPrefix) {
		s.hostKey = ""
	}
}

// HistoryMode returns the selected History view.
func (s *State) HistoryMode() HistoryMode { return s.historyMode }

// SetHistoryMode switches between the command list and the summary of the same
// filtered result set. Switching views starts at the top so the summary is not
// opened at the command list's old offset.
func (s *State) SetHistoryMode(mode HistoryMode) {
	if mode != HistoryModeStatistics {
		mode = HistoryModeCommands
	}
	if s.historyMode == mode {
		return
	}
	s.historyMode = mode
	s.visible = PageSize
	s.scroll.Y = 0
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
	s.clearMissingSelection()
}

// Problem returns the current problem code.
func (s *State) Problem() Problem { return s.problem }

// ProblemTab returns the record family the current problem belongs to. It
// matters only for ProblemClear, whose message names the family.
func (s *State) ProblemTab() Tab { return s.problemTab }

// OpenProblem returns the problem from the last row action.
func (s *State) OpenProblem() Problem { return s.openProblem }

// Cleared returns how many records the last confirmed clear removed.
func (s *State) Cleared() int { return s.cleared }

// ClearedTab returns which record family the last confirmed clear removed.
func (s *State) ClearedTab() Tab { return s.clearedTab }

// Scroll returns the surface's vertical offset. The view binds it; a test can
// drive real input and assert that the offset moved.
func (s *State) Scroll() *ui.ScrollState { return &s.scroll }

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

// SessionFilter returns the selected app Session or Atuin session UUID.
func (s *State) SessionFilter() string { return s.sessionKey }

// SetSessionFilter stores the Session filter and resets paging.
func (s *State) SetSessionFilter(session string) {
	s.sessionKey = session
	s.visible = PageSize
}

// CwdFilter returns the selected working directory.
func (s *State) CwdFilter() string { return s.cwdKey }

// SetCwdFilter stores the working-directory filter and resets paging.
func (s *State) SetCwdFilter(cwd string) {
	s.cwdKey = cwd
	s.visible = PageSize
}

// ShellFilter returns the selected shell.
func (s *State) ShellFilter() string { return s.shellKey }

// SetShellFilter stores the shell filter and resets paging.
func (s *State) SetShellFilter(shell string) {
	s.shellKey = shell
	s.visible = PageSize
}

// ExitFilter returns the selected exit-status filter.
func (s *State) ExitFilter() domainhistory.ExitFilter { return s.exitKey }

// SetExitFilter stores the exit-status filter and resets paging.
func (s *State) SetExitFilter(exit domainhistory.ExitFilter) {
	s.exitKey = exit
	s.visible = PageSize
}

// OpenGlobalSearch selects the History tab, clears the Host scope so every
// Host and origin is searched, and asks the next frame to focus the search
// field. It is the app-level shortcut's action; it never touches the
// terminal's Ctrl-R or Up keys. It returns to the command list so the shortcut
// always opens search results, even if the Statistics view was left open.
func (s *State) OpenGlobalSearch() {
	s.SelectTab(TabHistory)
	s.SetHistoryMode(HistoryModeCommands)
	s.hostKey = ""
	s.visible = PageSize
	s.focusRequest = focusHistorySearch
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

// HostOptions lists the Host/origin filter values present in the loaded
// records plus the local shell and, when the selected tab has any, unsaved
// Hosts. The Host filter is shared by both tabs, so saved Host values from
// either family stay selectable. Labels come from the records, so a deleted
// Host remains identifiable by its recorded label. Saved Hosts sharing a
// label are qualified by their address, or by their identity when even the
// address is shared, so the selector cannot show two indistinguishable
// entries. Imported and synced rows with no local Host identity offer an
// origin key on the History tab, where those rows are listed.
func (s *State) HostOptions() []HostOption {
	logFilter := s.connectionFilter()
	logFilter.Host = ""
	entryFilter := s.entryFilter()
	entryFilter.Host = ""

	labels := make(map[string]string)
	hostnames := make(map[string]string)
	counts := make(map[string]int)
	origins := make(map[string]int)
	unsaved, local, total := 0, 0, 0
	add := func(hostID model.HostID, label, hostname string) {
		if hostID == "" {
			return
		}
		key := string(hostID)
		counts[key]++
		display := label
		if display == "" {
			display = hostname
		}
		if display == "" {
			display = key
		}
		if existing, ok := labels[key]; !ok || existing == key {
			labels[key] = display
			hostnames[key] = hostname
		}
	}
	for _, log := range s.logs {
		if !logFilter.Match(log) {
			continue
		}
		total++
		if log.IsUnsaved() {
			if s.tab == TabConnections {
				unsaved++
			}
			continue
		}
		if log.IsLocal() {
			local++
		}
		add(log.HostID, log.HostLabel, log.Hostname)
	}
	for _, entry := range s.entries {
		if !entryFilter.Match(entry) {
			continue
		}
		total++
		if entry.IsUnsaved() {
			if s.tab == TabHistory {
				unsaved++
			}
			continue
		}
		if entry.IsLocal() {
			local++
		}
		add(entry.HostID, entry.HostLabel, "")
		// An origin key only makes sense on the History tab. Offering it on
		// Connections would let the user pick a value that matches no
		// connection log at all.
		if s.tab == TabHistory && entry.HostID == "" && !entry.IsLocal() &&
			entry.Origin != "" {
			origins[entry.Origin]++
		}
	}
	options := []HostOption{
		{Key: "", Label: "", Count: total},
		{Key: domainhistory.LocalHostKey, Label: "", Count: local},
	}
	if unsaved > 0 || s.hostKey == domainhistory.UnsavedHostKey {
		options = append(options, HostOption{
			Key: domainhistory.UnsavedHostKey, Count: unsaved,
		})
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left := strings.ToLower(labels[keys[i]])
		right := strings.ToLower(labels[keys[j]])
		if left == right {
			return keys[i] < keys[j]
		}
		return left < right
	})
	sharedLabels := make(map[string]int, len(labels))
	sharedHostnames := make(map[string]int, len(labels))
	for key, label := range labels {
		sharedLabels[strings.ToLower(label)]++
		sharedHostnames[strings.ToLower(hostnames[key])]++
	}
	for _, key := range keys {
		display := labels[key]
		if sharedLabels[strings.ToLower(display)] > 1 {
			display = qualifyHostLabel(display, hostnames[key], key,
				sharedHostnames[strings.ToLower(hostnames[key])] > 1)
		}
		options = append(options, HostOption{Key: key, Label: display, Count: counts[key]})
	}
	originValues := make([]string, 0, len(origins))
	for origin := range origins {
		originValues = append(originValues, origin)
	}
	sort.Slice(originValues, func(i, j int) bool {
		left := strings.ToLower(originValues[i])
		right := strings.ToLower(originValues[j])
		if left == right {
			return originValues[i] < originValues[j]
		}
		return left < right
	})
	for _, origin := range originValues {
		options = append(options, HostOption{
			Key: domainhistory.OriginHostKey(origin), Label: origin, Count: origins[origin],
		})
	}
	if key := s.hostKey; key != "" {
		if !hostOptionPresent(options, key) {
			option := HostOption{Key: key}
			if strings.HasPrefix(key, domainhistory.OriginHostKeyPrefix) {
				option.Label = strings.TrimPrefix(key, domainhistory.OriginHostKeyPrefix)
			}
			options = append(options, option)
		}
	}
	return options
}

func hostOptionPresent(options []HostOption, key string) bool {
	for _, option := range options {
		if option.Key == key {
			return true
		}
	}
	return false
}

// Facets returns every filter dimension for the current result set.
func (s *State) Facets() Facets {
	return Facets{
		Hosts:    s.HostOptions(),
		Sessions: s.SessionOptions(),
		Cwds:     s.CwdOptions(),
		Shells:   s.ShellOptions(),
		Sources:  s.SourceOptions(),
		Exits:    s.ExitOptions(),
	}
}

// SessionOptions lists the Session values present in the history result set
// with the Session filter cleared. A captured row contributes its app Session
// identity; a synced row contributes its Atuin session UUID.
func (s *State) SessionOptions() []FacetValue {
	counts := make(map[string]int)
	for _, entry := range s.facetEntries(func(filter *domainhistory.EntryFilter) {
		filter.Session = ""
	}) {
		key := string(entry.SessionID)
		if key == "" {
			key = entry.SessionUUID
		}
		if key != "" {
			counts[key]++
		}
	}
	return sortedFacetValues(counts, s.sessionKey)
}

// CwdOptions lists the working directories present in the history result set
// with the cwd filter cleared.
func (s *State) CwdOptions() []FacetValue {
	counts := make(map[string]int)
	for _, entry := range s.facetEntries(func(filter *domainhistory.EntryFilter) {
		filter.CWD = ""
	}) {
		if entry.CWD != "" {
			counts[entry.CWD]++
		}
	}
	return sortedFacetValues(counts, s.cwdKey)
}

// ShellOptions lists the shells present in the history result set with the
// shell filter cleared.
func (s *State) ShellOptions() []FacetValue {
	counts := make(map[string]int)
	for _, entry := range s.facetEntries(func(filter *domainhistory.EntryFilter) {
		filter.Shell = ""
	}) {
		shell := strings.ToLower(entry.Shell)
		if shell != "" {
			counts[shell]++
		}
	}
	return sortedFacetValues(counts, strings.ToLower(s.shellKey))
}

// SourceOptions lists every known source with its count in the history result
// set, with the source filter cleared. Keeping absent sources in the selector
// lets a user switch directly between sources.
func (s *State) SourceOptions() []FacetValue {
	counts := make(map[string]int)
	for _, entry := range s.facetEntries(func(filter *domainhistory.EntryFilter) {
		filter.Source = ""
	}) {
		counts[entry.Source]++
	}
	order := []string{
		domainhistory.SourceSession, domainhistory.SourceShellHistory,
		domainhistory.SourceAtuinImport, domainhistory.SourceAtuinSync,
	}
	return fixedFacetValues(counts, order)
}

// ExitOptions lists success, failure, and unknown exit status with their
// counts in the history result set, with the exit filter cleared.
func (s *State) ExitOptions() []FacetValue {
	counts := make(map[string]int)
	for _, entry := range s.facetEntries(func(filter *domainhistory.EntryFilter) {
		filter.Exit = domainhistory.ExitAny
	}) {
		switch {
		case !entry.HasExitCode:
			counts[string(domainhistory.ExitUnknown)]++
		case entry.ExitCode == 0:
			counts[string(domainhistory.ExitSuccess)]++
		default:
			counts[string(domainhistory.ExitFailure)]++
		}
	}
	order := []string{
		string(domainhistory.ExitSuccess), string(domainhistory.ExitFailure),
		string(domainhistory.ExitUnknown),
	}
	return fixedFacetValues(counts, order)
}

// facetEntries applies the current entry filters after reset clears one
// dimension. Facets are computed this way so a selected value does not hide
// the alternatives in its own selector.
func (s *State) facetEntries(reset func(*domainhistory.EntryFilter)) []domainhistory.Entry {
	filter := s.entryFilter()
	if reset != nil {
		reset(&filter)
	}
	matched := make([]domainhistory.Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if filter.Match(entry) {
			matched = append(matched, entry)
		}
	}
	return matched
}

// sortedFacetValues returns counts sorted by value, with the selected value
// kept even when the rest of the filters exclude it, so the selector never
// silently falls back to a different value.
func sortedFacetValues(counts map[string]int, selected string) []FacetValue {
	values := make([]string, 0, len(counts)+1)
	for value := range counts {
		values = append(values, value)
	}
	if selected != "" {
		if _, ok := counts[selected]; !ok {
			values = append(values, selected)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		left := strings.ToLower(values[i])
		right := strings.ToLower(values[j])
		if left == right {
			return values[i] < values[j]
		}
		return left < right
	})
	out := make([]FacetValue, 0, len(values))
	for _, value := range values {
		out = append(out, FacetValue{Value: value, Count: counts[value]})
	}
	return out
}

// fixedFacetValues returns counts in a fixed vocabulary order, including
// values with a zero count.
func fixedFacetValues(counts map[string]int, order []string) []FacetValue {
	out := make([]FacetValue, 0, len(order))
	for _, value := range order {
		out = append(out, FacetValue{Value: value, Count: counts[value]})
	}
	return out
}

// qualifyHostLabel disambiguates a label shared by saved Hosts. The address
// tells them apart unless the address is shared too, in which case only the
// identity is unique.
func qualifyHostLabel(label, hostname, key string, addressShared bool) string {
	qualifier := hostname
	if qualifier == "" || addressShared {
		qualifier = key
	}
	return label + " (" + qualifier + ")"
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

// dayLayout is how a date filter end is kept: the day alone, in local time.
const dayLayout = "2006-01-02"

func parseDay(text string) (time.Time, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, true
	}
	parsed, err := time.ParseInLocation(dayLayout, text, time.Local)
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
		Host:    s.hostKey,
		Session: s.sessionKey,
		CWD:     s.cwdKey,
		Exit:    s.exitKey,
		Shell:   s.shellKey,
		Source:  s.sourceFilter,
		Query:   s.search,
		From:    from,
		To:      to,
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

// FilteredEntries returns every history entry matching the filters, ranked by
// search relevance when a query is active and newest first otherwise. The
// time-and-identity tie-break keeps paging stable even when the source
// returned rows in another order.
func (s *State) FilteredEntries() []domainhistory.Entry {
	filter := s.entryFilter()
	matched := make([]domainhistory.Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if filter.Match(entry) {
			matched = append(matched, entry)
		}
	}
	domainhistory.SortEntriesForQuery(matched, s.search)
	return matched
}

// Statistics summarizes the live history rows matching the active filters. It
// consumes the same FilteredEntries result set the list renders, so every
// filter change updates the summary without a second source query.
func (s *State) Statistics() domainhistory.Statistics {
	return domainhistory.ComputeStatistics(s.FilteredEntries())
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

// SelectEntry opens the inline detail panel for one history row. Selecting the
// open row again closes it, so the row's Details action is a toggle.
func (s *State) SelectEntry(id string) {
	if s.selectedEntryID == id {
		s.selectedEntryID = ""
		return
	}
	s.selectedEntryID = id
}

// CloseEntryDetail closes the inline history detail panel.
func (s *State) CloseEntryDetail() { s.selectedEntryID = "" }

// EntrySelected reports whether the detail panel is open for this row.
func (s *State) EntrySelected(id string) bool { return s.selectedEntryID == id }

// clearMissingSelection drops a selection whose row no longer exists, as after
// a delete, a clear, or a filter reload.
func (s *State) clearMissingSelection() {
	if s.selectedEntryID == "" {
		return
	}
	for _, entry := range s.entries {
		if entry.ID == s.selectedEntryID {
			return
		}
	}
	s.selectedEntryID = ""
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

// RequestDeleteEntry asks for confirmation before deleting one recorded
// command execution. The row's Delete action opens the confirmation; nothing
// is removed until ConfirmDeleteEntry runs.
func (s *State) RequestDeleteEntry(id string) {
	s.pendingDeleteID = id
}

// CancelDeleteEntry closes the row delete confirmation without removing
// anything.
func (s *State) CancelDeleteEntry() { s.pendingDeleteID = "" }

// PendingDeleteEntry returns the row awaiting delete confirmation, or an empty
// identity when no confirmation is open.
func (s *State) PendingDeleteEntry() string { return s.pendingDeleteID }

// ConfirmDeleteEntry deletes the row the user confirmed. It is the only path
// from the History surface's row action to the store.
func (s *State) ConfirmDeleteEntry() {
	id := s.pendingDeleteID
	s.pendingDeleteID = ""
	if id == "" {
		return
	}
	s.DeleteEntry(id)
}

// DeleteEntry removes one recorded command execution. Callers that offer a
// row action first ask through RequestDeleteEntry and then confirm; the side
// panel's paste/run history uses it directly.
func (s *State) DeleteEntry(id string) {
	if s.source == nil {
		return
	}
	if err := s.source.DeleteEntry(context.Background(), id); err != nil {
		s.problem = ProblemDelete
		return
	}
	if s.selectedEntryID == id {
		s.selectedEntryID = ""
	}
	s.problem = ProblemNone
	s.reload()
	s.mutated()
}

// RequestClear opens the clear-all confirmation for the selected tab.
func (s *State) RequestClear() { s.pendingClear = true }

// CancelClear closes the clear-all confirmation.
func (s *State) CancelClear() { s.pendingClear = false }

// ConfirmClear removes every record on the selected tab. Connection logs and
// command history are separate families; clearing one leaves the other in
// place. Clearing History removes only rows this device created: imported and
// synced remote-origin rows stay.
func (s *State) ConfirmClear() {
	s.pendingClear = false
	if s.source == nil {
		return
	}
	tab := s.tab
	var removed int
	var err error
	if tab == TabConnections {
		removed, err = s.source.ClearConnectionLogs(context.Background())
	} else {
		removed, err = s.source.ClearThisDevice(context.Background())
	}
	s.problemTab = tab
	if err != nil {
		// A batched clear can remove rows before a later batch fails. Reload
		// first so the list and the mutation hook reflect what actually
		// changed, then report the partial count and the error together:
		// reloading clears the problem field on a successful refresh.
		if tab == TabHistory {
			s.selectedEntryID = ""
		}
		s.reload()
		s.mutated()
		s.cleared = removed
		s.clearedTab = tab
		s.problem = ProblemClear
		return
	}
	s.problem = ProblemNone
	s.cleared = removed
	s.clearedTab = tab
	if tab == TabHistory {
		s.selectedEntryID = ""
	}
	s.reload()
	s.mutated()
}

// RequestPrune opens the prune dialog seeded with the History filters the
// user is looking at.
func (s *State) RequestPrune() {
	s.pruneFilter = PruneFilter{
		Query: s.search, Host: s.hostKey, Session: s.sessionKey, CWD: s.cwdKey,
		Exit: s.exitKey, Shell: s.shellKey, Source: s.sourceFilter,
		From: s.fromText, To: s.toText,
	}
	s.pruneLimit = ""
	s.pendingPrune = true
}

// CancelPrune closes the prune dialog without changing any record.
func (s *State) CancelPrune() { s.pendingPrune = false }

// PendingPrune reports whether the prune dialog is open.
func (s *State) PendingPrune() bool { return s.pendingPrune }

// PruneFilter returns the editable prune draft. The view binds its fields.
func (s *State) PruneFilter() *PruneFilter { return &s.pruneFilter }

// PruneLimit returns the editable optional limit draft.
func (s *State) PruneLimit() *string { return &s.pruneLimit }

// PrunePreview reports how many live rows the current draft would remove. A
// malformed date range or limit reports zero, and the view explains why.
func (s *State) PrunePreview() int {
	entries, ok := s.prunePreviewEntries()
	if !ok {
		return 0
	}
	return len(entries)
}

// prunePreviewEntries is the exact set the prune preview shows: the live rows
// in the loaded journal that match the draft, newest first, with a positive
// limit keeping the oldest matches. ConfirmPrune applies this set by identity,
// so a row that arrives between preview and confirm cannot be removed without
// having been counted.
func (s *State) prunePreviewEntries() ([]domainhistory.Entry, bool) {
	filter, ok := s.pruneFilter.entryFilter()
	if !ok || s.PruneLimitProblem() {
		return nil, false
	}
	matched := make([]domainhistory.Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry.DeletedAt.IsZero() && filter.Match(entry) {
			matched = append(matched, entry)
		}
	}
	domainhistory.SortEntries(matched)
	if limit := s.pruneLimitValue(); limit > 0 && len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	return matched, true
}

// PruneDateProblem reports a malformed or inverted date range in the draft.
func (s *State) PruneDateProblem() bool {
	_, ok := s.pruneFilter.entryFilter()
	return !ok
}

// PruneLimitProblem reports a limit that is not a whole non-negative number.
func (s *State) PruneLimitProblem() bool {
	text := strings.TrimSpace(s.pruneLimit)
	if text == "" {
		return false
	}
	limit, err := strconv.Atoi(text)
	return err != nil || limit < 0
}

// pruneLimitValue returns the parsed limit; zero means every match.
func (s *State) pruneLimitValue() int {
	limit, err := strconv.Atoi(strings.TrimSpace(s.pruneLimit))
	if err != nil || limit < 0 {
		return 0
	}
	return limit
}

// ConfirmPrune soft-deletes exactly the rows the preview counted. A malformed
// draft keeps the dialog open instead of guessing what the user meant.
func (s *State) ConfirmPrune() {
	if s.source == nil {
		return
	}
	previewed, ok := s.prunePreviewEntries()
	if !ok {
		return
	}
	ids := make([]string, 0, len(previewed))
	for _, entry := range previewed {
		ids = append(ids, entry.ID)
	}
	removed, err := s.source.PruneHistoryEntries(context.Background(), ids)
	s.problemTab = TabHistory
	if err != nil {
		s.pruned = 0
		s.problem = ProblemPrune
		return
	}
	s.pendingPrune = false
	s.problem = ProblemNone
	s.pruned = removed
	s.reload()
	s.mutated()
}

// Pruned reports how many entries the last confirmed prune removed.
func (s *State) Pruned() int { return s.pruned }

// InsertEntry inserts a history command into the focused Session without
// running it. It is only reachable through the row's explicit Insert action.
func (s *State) InsertEntry(command string) {
	s.openProblem = ProblemNone
	if s.commands == nil {
		s.openProblem = ProblemInsert
		return
	}
	if err := s.commands.InsertCommand(command); err != nil {
		if errors.Is(err, ErrInsertUnsafe) {
			s.openProblem = ProblemInsertUnsafe
			return
		}
		s.openProblem = ProblemInsert
	}
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
