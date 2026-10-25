// Package monitoring renders and drives the Host monitoring surface. State
// owns panel demand, search, ordering, and column configuration; the view
// localizes every message through the catalogs.
package monitoring

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appmonitor "github.com/mslxl/natcatty/application/monitor"
	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Service is the monitoring use-case seam. application/monitor.Service
// implements it; tests provide a fake.
type Service interface {
	Start(panelID string, hosts []domainvault.Host, interval time.Duration) error
	Stop(panelID string)
	StopAll()
	Snapshot() domainmonitor.Snapshot
	Subscribe(func()) func()
}

// Hosts is the Vault Host list a panel draws from.
type Hosts interface {
	List(context.Context) ([]domainvault.Host, error)
}

// Preferences is the existing settings store. Monitor configuration lives in
// its own group so a panel and its columns survive a restart.
type Preferences interface {
	Settings() domainpreferences.Settings
	Update(func(*domainpreferences.Settings)) error
}

// OpenTerminal opens a Host's Session. The app wires the shell.
type OpenTerminal func(domainvault.Host) error

// Problem identifies a failure the view localizes.
type Problem string

const (
	ProblemNone          Problem = ""
	ProblemNameRequired  Problem = "name_required"
	ProblemDuplicateName Problem = "duplicate_name"
	ProblemSave          Problem = "save"
	ProblemLoad          Problem = "load"
	ProblemTooManyHosts  Problem = "too_many_hosts"
	ProblemNoPanel       Problem = "no_panel"
	ProblemOpenTerminal  Problem = "open_terminal"
	ProblemInternal      Problem = "internal"
)

// Form is the panel editor's text. Interval and Range stay strings so a
// hand-edited value produces a localized problem instead of a zero.
type Form struct {
	ID          string
	Name        string
	Description string
	HostIDs     []string
	View        domainpreferences.MonitorView
	Interval    string
	Range       string
	Sort        domainpreferences.MonitorSort
}

// State is the monitoring surface's window-scoped state. It is mutated on the
// UI thread; runtime snapshots are copied from the service on Flush.
type State struct {
	service      Service
	hosts        Hosts
	prefs        Preferences
	openTerminal OpenTerminal
	redraw       func()
	now          func() time.Time

	hostItems []domainvault.Host
	hostByID  map[string]domainvault.Host
	panels    []domainpreferences.MonitorPanel
	hidden    map[string]bool
	widths    map[string]int
	activeID  string
	search    string
	problem   Problem
	loadErr   error

	editing       bool
	form          Form
	formHosts     map[string]bool
	columnsOpen   bool
	pendingDelete bool

	runningPanel string
	snapshot     domainmonitor.Snapshot

	table ui.ListState
	grid  ui.GridState

	mu          sync.Mutex
	unsubscribe func()
}

// Option customizes a State.
type Option func(*State)

// WithRedrawRequest sets the function called when the surface changes.
func WithRedrawRequest(fn func()) Option {
	return func(s *State) { s.redraw = fn }
}

// WithClock replaces the source of time. Tests use it for stable ages.
func WithClock(now func() time.Time) Option {
	return func(s *State) { s.now = now }
}

// NewState returns a monitoring surface over its ports.
func NewState(service Service, hosts Hosts, prefs Preferences, open OpenTerminal, opts ...Option) *State {
	s := &State{
		service:      service,
		hosts:        hosts,
		prefs:        prefs,
		openTerminal: open,
		now:          time.Now,
		hostByID:     make(map[string]domainvault.Host),
		hidden:       make(map[string]bool),
		widths:       make(map[string]int),
		formHosts:    make(map[string]bool),
		snapshot: domainmonitor.Snapshot{
			Hosts:     make(map[string]domainmonitor.HostState),
			Running:   make(map[string]bool),
			StartedAt: make(map[string]time.Time),
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	s.reloadConfiguration()
	s.table.Key = func(row int) any {
		hosts := s.DisplayHosts()
		if row >= 0 && row < len(hosts) {
			return string(hosts[row].ID)
		}
		return row
	}
	s.table.Label = func(row int) string {
		hosts := s.DisplayHosts()
		if row >= 0 && row < len(hosts) {
			return s.HostLabel(hosts[row])
		}
		return ""
	}
	s.table.Columns.Widths = s.tableWidths()
	s.grid.Key = func(item int) any {
		hosts := s.DisplayHosts()
		if item >= 0 && item < len(hosts) {
			return string(hosts[item].ID)
		}
		return item
	}
	s.grid.Label = s.table.Label
	if service != nil {
		s.unsubscribe = service.Subscribe(func() { s.requestRedraw() })
	}
	return s
}

// Close releases the runtime subscription and stops sampling. The app closes
// the service separately.
func (s *State) Close() {
	s.mu.Lock()
	unsubscribe := s.unsubscribe
	s.unsubscribe = nil
	s.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
	if s.service != nil {
		s.service.StopAll()
	}
	s.runningPanel = ""
}

// Activate reloads Vault Hosts and the persisted configuration, then starts
// the active panel. The shell calls it when the surface becomes visible.
func (s *State) Activate() {
	s.reloadConfiguration()
	if s.hosts != nil {
		items, err := s.hosts.List(context.Background())
		s.hostItems = items
		s.loadErr = err
		s.hostByID = make(map[string]domainvault.Host, len(items))
		for _, host := range items {
			s.hostByID[string(host.ID)] = host
		}
		if err != nil {
			s.problem = ProblemLoad
		}
	}
	s.startActive()
	s.requestRedraw()
}

// Deactivate stops sampling. The shell calls it when another surface shows.
func (s *State) Deactivate() {
	s.stopActive()
	s.requestRedraw()
}

func (s *State) reloadConfiguration() {
	monitor := domainpreferences.DefaultMonitor()
	if s.prefs != nil {
		monitor = domainpreferences.NormalizeMonitor(s.prefs.Settings().Monitor)
	}
	s.panels = append([]domainpreferences.MonitorPanel(nil), monitor.Panels...)
	s.hidden = make(map[string]bool, len(monitor.HiddenColumns))
	for _, key := range monitor.HiddenColumns {
		s.hidden[key] = true
	}
	s.widths = make(map[string]int, len(monitor.ColumnWidths))
	for key, width := range monitor.ColumnWidths {
		s.widths[key] = width
	}
	if _, ok := s.findPanel(s.activeID); !ok {
		s.activeID = ""
		if len(s.panels) > 0 {
			s.activeID = s.panels[0].ID
		}
	}
}

// Flush copies the service's runtime snapshot. The view calls it every frame;
// an event only wakes the UI, so a coalesced event cannot leave stale metrics.
func (s *State) Flush() {
	if s.service == nil {
		return
	}
	s.snapshot = s.service.Snapshot()
}

// Snapshot returns the latest runtime snapshot.
func (s *State) Snapshot() domainmonitor.Snapshot { return s.snapshot }

// Hosts returns the Vault Hosts the surface knows.
func (s *State) Hosts() []domainvault.Host { return s.hostItems }

// Panels returns the configured panels.
func (s *State) Panels() []domainpreferences.MonitorPanel {
	return append([]domainpreferences.MonitorPanel(nil), s.panels...)
}

// ActivePanel returns the panel being displayed.
func (s *State) ActivePanel() (domainpreferences.MonitorPanel, bool) {
	return s.findPanel(s.activeID)
}

// SetPanel displays another panel and re-targets sampling.
func (s *State) SetPanel(id string) {
	if _, ok := s.findPanel(id); !ok || id == s.activeID {
		return
	}
	s.stopActive()
	s.activeID = id
	s.startActive()
	s.requestRedraw()
}

// DisplayHosts returns the active panel's Hosts after search and ordering.
func (s *State) DisplayHosts() []domainvault.Host {
	panel, ok := s.findPanel(s.activeID)
	if !ok {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(s.search))
	seen := make(map[string]bool, len(panel.HostIDs))
	out := make([]domainvault.Host, 0, len(panel.HostIDs))
	for _, id := range panel.HostIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		host, ok := s.hostByID[id]
		if !ok {
			continue
		}
		if query != "" && !hostMatches(host, query) {
			continue
		}
		out = append(out, host)
	}
	if panel.Sort == domainpreferences.MonitorSortName {
		sort.SliceStable(out, func(i, j int) bool {
			return strings.ToLower(s.HostLabel(out[i])) < strings.ToLower(s.HostLabel(out[j]))
		})
	}
	return out
}

func hostMatches(host domainvault.Host, query string) bool {
	fields := []string{host.Label, host.Hostname, host.Group, host.Notes, strings.Join(host.Tags, " ")}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

// HostLabel returns a Host's display label, falling back to its hostname and
// then its id.
func (s *State) HostLabel(host domainvault.Host) string {
	if label := strings.TrimSpace(host.Label); label != "" {
		return label
	}
	if host.Hostname != "" {
		return host.Hostname
	}
	return string(host.ID)
}

// HostState returns a Host's runtime state.
func (s *State) HostState(id string) domainmonitor.HostState {
	if state, ok := s.snapshot.Hosts[id]; ok {
		return state
	}
	return domainmonitor.HostState{HostID: id, Status: domainmonitor.StatusQueued}
}

// Search returns the current filter text.
func (s *State) Search() string { return s.search }

// SetSearch replaces the filter text.
func (s *State) SetSearch(value string) {
	if value == s.search {
		return
	}
	s.search = value
	s.requestRedraw()
}

// Problem returns the current failure, if any.
func (s *State) Problem() Problem { return s.problem }

// LoadingError returns the Host list failure, if any.
func (s *State) LoadingError() error { return s.loadErr }

// Running reports whether sampling is active for the displayed panel.
func (s *State) Running() bool {
	panel, ok := s.findPanel(s.activeID)
	return ok && s.runningPanel == panel.ID
}

// StartedAt returns when the running panel began sampling.
func (s *State) StartedAt() time.Time {
	panel, ok := s.findPanel(s.activeID)
	if !ok {
		return time.Time{}
	}
	return s.snapshot.StartedAt[panel.ID]
}

// Hidden reports whether an optional column is hidden.
func (s *State) Hidden(key string) bool { return s.hidden[key] }

// ToggleColumn hides or shows an optional column and persists the change.
func (s *State) ToggleColumn(key string) {
	if key == "host" || key == "terminal" {
		return
	}
	next := !s.hidden[key]
	if next {
		s.hidden[key] = true
	} else {
		delete(s.hidden, key)
	}
	if err := s.saveConfiguration(); err != nil {
		if next {
			delete(s.hidden, key)
		} else {
			s.hidden[key] = true
		}
		s.problem = ProblemSave
	}
	s.requestRedraw()
}

// Width returns a column's saved width.
func (s *State) Width(key string) int {
	if width, ok := s.widths[key]; ok {
		return width
	}
	return domainpreferences.MonitorColumnDefaults[key]
}

// TableWidths returns the saved widths as the table's layout.
func (s *State) TableWidths() map[string]float32 {
	out := make(map[string]float32, len(s.widths))
	for key, width := range s.widths {
		out[key] = float32(width)
	}
	return out
}

// SyncColumnWidths persists widths the user changed by dragging a table
// header. It is a no-op while nothing changed.
func (s *State) SyncColumnWidths() {
	changed := false
	for key, width := range s.table.Columns.Widths {
		rounded := int(width + 0.5)
		if rounded <= 0 || rounded == s.widths[key] {
			continue
		}
		s.widths[key] = rounded
		changed = true
	}
	if !changed {
		return
	}
	if err := s.saveConfiguration(); err != nil {
		s.problem = ProblemSave
	}
}

func (s *State) tableWidths() map[string]float32 {
	out := make(map[string]float32, len(domainpreferences.MonitorColumnKeys))
	for _, key := range domainpreferences.MonitorColumnKeys {
		out[key] = float32(s.Width(key))
	}
	return out
}

// ColumnsOpen reports whether the column editor is open.
func (s *State) ColumnsOpen() bool { return s.columnsOpen }

// ToggleColumns opens or closes the column editor.
func (s *State) ToggleColumns() {
	s.columnsOpen = !s.columnsOpen
	s.problem = ProblemNone
	s.requestRedraw()
}

// Editing reports whether the panel editor is open.
func (s *State) Editing() bool { return s.editing }

// Form returns the panel editor's form. It is valid while Editing.
func (s *State) Form() *Form { return &s.form }

// FormHostSelected reports whether a Host is selected in the editor.
func (s *State) FormHostSelected(id string) bool { return s.formHosts[id] }

// ToggleFormHost selects or deselects a Host in the editor.
func (s *State) ToggleFormHost(id string) {
	if s.formHosts[id] {
		delete(s.formHosts, id)
		for index, existing := range s.form.HostIDs {
			if existing == id {
				s.form.HostIDs = append(s.form.HostIDs[:index], s.form.HostIDs[index+1:]...)
				break
			}
		}
		return
	}
	s.formHosts[id] = true
	s.form.HostIDs = append(s.form.HostIDs, id)
}

// MoveFormHost moves a selected Host within the manual order.
func (s *State) MoveFormHost(id string, delta int) {
	for index, existing := range s.form.HostIDs {
		if existing != id {
			continue
		}
		next := index + delta
		if next < 0 || next >= len(s.form.HostIDs) {
			return
		}
		s.form.HostIDs[index], s.form.HostIDs[next] = s.form.HostIDs[next], s.form.HostIDs[index]
		return
	}
}

// BeginCreatePanel opens the editor with a new panel.
func (s *State) BeginCreatePanel() {
	s.editing = true
	s.pendingDelete = false
	s.problem = ProblemNone
	s.form = Form{
		ID:       s.newPanelID(),
		View:     domainpreferences.MonitorViewList,
		Interval: strconv.Itoa(domainpreferences.DefaultMonitorInterval),
		Range:    strconv.Itoa(domainpreferences.DefaultMonitorRange),
		Sort:     domainpreferences.MonitorSortManual,
	}
	s.formHosts = make(map[string]bool)
	s.requestRedraw()
}

// BeginEditPanel opens the editor with the active panel's values.
func (s *State) BeginEditPanel() {
	panel, ok := s.findPanel(s.activeID)
	if !ok {
		s.problem = ProblemNoPanel
		s.requestRedraw()
		return
	}
	s.editing = true
	s.pendingDelete = false
	s.problem = ProblemNone
	s.form = Form{
		ID:          panel.ID,
		Name:        panel.Name,
		Description: panel.Description,
		HostIDs:     append([]string(nil), panel.HostIDs...),
		View:        panel.View,
		Interval:    strconv.Itoa(panel.Interval),
		Range:       strconv.Itoa(panel.Range),
		Sort:        panel.Sort,
	}
	s.formHosts = make(map[string]bool, len(panel.HostIDs))
	for _, id := range panel.HostIDs {
		s.formHosts[id] = true
	}
	s.requestRedraw()
}

// CancelEdit closes the editor without saving.
func (s *State) CancelEdit() {
	s.editing = false
	s.pendingDelete = false
	s.problem = ProblemNone
	s.form = Form{}
	s.formHosts = make(map[string]bool)
	s.requestRedraw()
}

// SavePanel validates the form, persists it through the settings store, and
// re-targets sampling.
func (s *State) SavePanel() {
	name := strings.TrimSpace(s.form.Name)
	if name == "" {
		s.problem = ProblemNameRequired
		s.requestRedraw()
		return
	}
	for _, panel := range s.panels {
		if panel.ID != s.form.ID && strings.EqualFold(strings.TrimSpace(panel.Name), name) {
			s.problem = ProblemDuplicateName
			s.requestRedraw()
			return
		}
	}
	interval := parseChoice(s.form.Interval, domainpreferences.MonitorIntervalChoices,
		domainpreferences.DefaultMonitorInterval)
	window := parseChoice(s.form.Range, domainpreferences.MonitorRangeChoices,
		domainpreferences.DefaultMonitorRange)
	panel := domainpreferences.MonitorPanel{
		ID:          s.form.ID,
		Name:        name,
		Description: strings.TrimSpace(s.form.Description),
		HostIDs:     append([]string(nil), s.form.HostIDs...),
		View:        s.form.View,
		Interval:    interval,
		Range:       window,
		Sort:        s.form.Sort,
	}
	if panel.ID == "" {
		panel.ID = s.newPanelID()
	}
	next := make([]domainpreferences.MonitorPanel, 0, len(s.panels)+1)
	replaced := false
	for _, existing := range s.panels {
		if existing.ID == panel.ID {
			next = append(next, panel)
			replaced = true
			continue
		}
		next = append(next, existing)
	}
	if !replaced {
		next = append(next, panel)
	}
	previous := append([]domainpreferences.MonitorPanel(nil), s.panels...)
	previousActive := s.activeID
	s.panels = next
	s.activeID = panel.ID
	if err := s.saveConfiguration(); err != nil {
		s.panels = previous
		s.activeID = previousActive
		s.problem = ProblemSave
		s.requestRedraw()
		return
	}
	s.editing = false
	s.pendingDelete = false
	s.form = Form{}
	s.formHosts = make(map[string]bool)
	s.problem = ProblemNone
	s.stopActive()
	s.startActive()
	s.requestRedraw()
}

// RequestDeletePanel opens the delete confirmation.
func (s *State) RequestDeletePanel() {
	s.pendingDelete = true
	s.requestRedraw()
}

// CancelDeletePanel closes the delete confirmation.
func (s *State) CancelDeletePanel() {
	s.pendingDelete = false
	s.requestRedraw()
}

// ConfirmDeletePanel deletes the edited panel.
func (s *State) ConfirmDeletePanel() {
	panel, ok := s.findPanel(s.form.ID)
	if !ok {
		s.pendingDelete = false
		s.problem = ProblemNoPanel
		s.requestRedraw()
		return
	}
	next := make([]domainpreferences.MonitorPanel, 0, len(s.panels))
	for _, existing := range s.panels {
		if existing.ID != panel.ID {
			next = append(next, existing)
		}
	}
	previous := append([]domainpreferences.MonitorPanel(nil), s.panels...)
	previousActive := s.activeID
	s.panels = next
	if len(next) > 0 {
		s.activeID = next[0].ID
	} else {
		s.activeID = ""
	}
	if err := s.saveConfiguration(); err != nil {
		s.panels = previous
		s.activeID = previousActive
		s.problem = ProblemSave
		s.pendingDelete = false
		s.requestRedraw()
		return
	}
	s.editing = false
	s.pendingDelete = false
	s.form = Form{}
	s.formHosts = make(map[string]bool)
	s.problem = ProblemNone
	s.stopActive()
	s.startActive()
	s.requestRedraw()
}

// UpdatePanelInterval changes the active panel's refresh cadence and
// persists it.
func (s *State) UpdatePanelInterval(seconds int) {
	panel, ok := s.findPanel(s.activeID)
	if !ok || panel.Interval == seconds {
		return
	}
	panel.Interval = seconds
	s.applyPanelChange(panel)
	s.restartActive()
	s.requestRedraw()
}

// UpdatePanelView changes the active panel's presentation and persists it.
func (s *State) UpdatePanelView(view domainpreferences.MonitorView) {
	panel, ok := s.findPanel(s.activeID)
	if !ok || panel.View == view {
		return
	}
	panel.View = view
	s.applyPanelChange(panel)
	s.requestRedraw()
}

// UpdatePanelSort changes the active panel's ordering and persists it.
func (s *State) UpdatePanelSort(order domainpreferences.MonitorSort) {
	panel, ok := s.findPanel(s.activeID)
	if !ok || panel.Sort == order {
		return
	}
	panel.Sort = order
	s.applyPanelChange(panel)
	s.requestRedraw()
}

// AdjustColumnWidth widens or narrows a column by delta and persists it. The
// value is clamped by the same rules the settings store applies, so a width
// shown in the surface is always the width that survives a reload.
func (s *State) AdjustColumnWidth(key string, delta int) {
	if _, ok := domainpreferences.MonitorColumnDefaults[key]; !ok {
		return
	}
	width := s.Width(key) + delta
	normalized := domainpreferences.NormalizeMonitor(domainpreferences.Monitor{
		ColumnWidths: map[string]int{key: width},
	})
	width = normalized.ColumnWidths[key]
	if width == s.Width(key) {
		return
	}
	previous := s.widths[key]
	s.widths[key] = width
	if err := s.saveConfiguration(); err != nil {
		s.widths[key] = previous
		s.problem = ProblemSave
	}
	s.requestRedraw()
}

func (s *State) applyPanelChange(panel domainpreferences.MonitorPanel) {
	previous := append([]domainpreferences.MonitorPanel(nil), s.panels...)
	s.panels = s.replacePanel(panel)
	if err := s.saveConfiguration(); err != nil {
		s.panels = previous
		s.problem = ProblemSave
	}
}

// OpenHost opens a Host's Session.
func (s *State) OpenHost(id string) {
	host, ok := s.hostByID[id]
	if !ok {
		s.problem = ProblemOpenTerminal
		s.requestRedraw()
		return
	}
	if s.openTerminal == nil {
		s.problem = ProblemOpenTerminal
		s.requestRedraw()
		return
	}
	if err := s.openTerminal(host); err != nil {
		s.problem = ProblemOpenTerminal
	}
	s.requestRedraw()
}

func (s *State) replacePanel(panel domainpreferences.MonitorPanel) []domainpreferences.MonitorPanel {
	out := append([]domainpreferences.MonitorPanel(nil), s.panels...)
	for index := range out {
		if out[index].ID == panel.ID {
			out[index] = panel
			break
		}
	}
	return out
}

func (s *State) startActive() {
	panel, ok := s.findPanel(s.activeID)
	if !ok || s.service == nil {
		s.stopActive()
		return
	}
	hosts := make([]domainvault.Host, 0, len(panel.HostIDs))
	for _, id := range panel.HostIDs {
		if host, ok := s.hostByID[id]; ok {
			hosts = append(hosts, host)
		}
	}
	s.stopActive()
	if len(hosts) == 0 {
		return
	}
	interval := time.Duration(panel.Interval) * time.Second
	if err := s.service.Start(panel.ID, hosts, interval); err != nil {
		if errors.Is(err, appmonitor.ErrTooManyHosts) {
			s.problem = ProblemTooManyHosts
		} else {
			s.problem = ProblemInternal
		}
		s.runningPanel = ""
		return
	}
	if s.problem == ProblemTooManyHosts || s.problem == ProblemInternal {
		s.problem = ProblemNone
	}
	s.runningPanel = panel.ID
}

func (s *State) stopActive() {
	if s.service != nil && s.runningPanel != "" {
		s.service.Stop(s.runningPanel)
	}
	s.runningPanel = ""
}

func (s *State) restartActive() {
	s.stopActive()
	s.startActive()
}

func (s *State) findPanel(id string) (domainpreferences.MonitorPanel, bool) {
	if id == "" {
		return domainpreferences.MonitorPanel{}, false
	}
	for _, panel := range s.panels {
		if panel.ID == id {
			return panel, true
		}
	}
	return domainpreferences.MonitorPanel{}, false
}

func (s *State) saveConfiguration() error {
	next := domainpreferences.Monitor{
		Panels:        append([]domainpreferences.MonitorPanel(nil), s.panels...),
		HiddenColumns: hiddenList(s.hidden),
		ColumnWidths:  cloneWidths(s.widths),
	}
	if s.prefs == nil {
		return nil
	}
	if err := s.prefs.Update(func(settings *domainpreferences.Settings) {
		settings.Monitor = next
	}); err != nil {
		return err
	}
	// Adopt the store's normalized value: width clamps and name suffixing may
	// have changed it.
	stored := domainpreferences.NormalizeMonitor(s.prefs.Settings().Monitor)
	s.panels = append([]domainpreferences.MonitorPanel(nil), stored.Panels...)
	s.hidden = make(map[string]bool, len(stored.HiddenColumns))
	for _, key := range stored.HiddenColumns {
		s.hidden[key] = true
	}
	s.widths = cloneWidths(stored.ColumnWidths)
	s.table.Columns.Widths = s.tableWidths()
	return nil
}

func hiddenList(hidden map[string]bool) []string {
	var out []string
	for _, key := range domainpreferences.MonitorColumnKeys {
		if hidden[key] {
			out = append(out, key)
		}
	}
	return out
}

func cloneWidths(widths map[string]int) map[string]int {
	out := make(map[string]int, len(widths))
	for key, width := range widths {
		out[key] = width
	}
	return out
}

func parseChoice(value string, choices []int, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	for _, choice := range choices {
		if choice == parsed {
			return parsed
		}
	}
	return fallback
}

func (s *State) newPanelID() string {
	return "panel-" + strconv.FormatInt(s.now().UnixNano(), 36)
}

func (s *State) requestRedraw() {
	if s.redraw != nil {
		s.redraw()
	}
}
