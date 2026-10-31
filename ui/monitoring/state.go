// Package monitoring renders and drives the Host monitoring surface. State
// owns panel demand, search, ordering, the editor and detail panels, and the
// list's column configuration; the view localizes every message through the
// catalogs.
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
	ProblemHostRequired  Problem = "host_required"
	ProblemDuplicateName Problem = "duplicate_name"
	ProblemSave          Problem = "save"
	ProblemLoad          Problem = "load"
	ProblemTooManyHosts  Problem = "too_many_hosts"
	ProblemNoPanel       Problem = "no_panel"
	ProblemOpenTerminal  Problem = "open_terminal"
	ProblemInternal      Problem = "internal"
)

// The surface's own Host states, beside the runtime's: a Host of a panel that
// is not sampling, and a live Host whose last sample is too old.
const (
	StatusStopped domainmonitor.Status = "stopped"
	StatusStale   domainmonitor.Status = "stale"
)

// ColumnKeys is the list's column order, as the original's table: the Host,
// its metadata, its metrics, then its state and the terminal action.
var ColumnKeys = []string{
	"host", "latency", "tags", "notes", "cpu", "memory", "disks",
	"network", "updated", "status", "terminal",
}

// RangeChoices are the trend windows the toolbar offers, in minutes.
var RangeChoices = []int{5, 15, 30}

// IntervalChoices are the refresh cadences the toolbar offers: the
// original hides the sub-three-second ones.
func IntervalChoices() []int {
	out := make([]int, 0, len(domainpreferences.MonitorIntervalChoices))
	for _, choice := range domainpreferences.MonitorIntervalChoices {
		if choice >= 3 {
			out = append(out, choice)
		}
	}
	return out
}

// Form is the panel editor's draft. Interval, Range, and Sort are carried
// from the edited panel: the toolbar changes them, the editor does not.
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
	dragging  map[string]float32
	activeID  string
	search    string
	problem   Problem
	loadErr   error

	editing      bool
	creating     bool
	form         Form
	formHosts    map[string]bool
	picking      bool
	pickerSearch string
	deleteID     string
	detailID     string

	// started is the panels the user started. Only the displayed one samples;
	// leaving the surface pauses it and coming back resumes it.
	started      map[string]bool
	runningPanel string
	snapshot     domainmonitor.Snapshot

	grid ui.GridState

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
		dragging:     make(map[string]float32),
		formHosts:    make(map[string]bool),
		started:      make(map[string]bool),
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
	s.grid.Key = func(item int) any {
		hosts := s.DisplayHosts()
		if item >= 0 && item < len(hosts) {
			return string(hosts[item].ID)
		}
		return item
	}
	s.grid.Label = func(item int) string {
		hosts := s.DisplayHosts()
		if item >= 0 && item < len(hosts) {
			return s.HostLabel(hosts[item])
		}
		return ""
	}
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

// Activate reloads Vault Hosts and the persisted configuration, then resumes
// the displayed panel if the user started it. The shell calls it when the
// surface becomes visible. Saving or opening a panel never connects by
// itself: sampling starts with Start, as in the original.
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
	if s.started[s.activeID] {
		s.startActive()
	}
	s.requestRedraw()
}

// Deactivate pauses sampling. The shell calls it when another surface shows;
// the started panel resumes on the next Activate.
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
	s.widths = cloneWidths(monitor.ColumnWidths)
	if _, ok := s.findPanel(s.activeID); !ok {
		s.activeID = ""
		if len(s.panels) > 0 {
			s.activeID = s.panels[0].ID
		}
	}
}

// Flush copies the service's runtime snapshot. The view calls it every frame;
// an event only wakes the UI, so a coalesced event cannot leave stale metrics.
// A detail panel closes once its Host is no longer live.
func (s *State) Flush() {
	if s.service != nil {
		s.snapshot = s.service.Snapshot()
	}
	if s.detailID != "" && s.DisplayStatus(s.detailID) != domainmonitor.StatusLive {
		s.detailID = ""
	}
}

// Snapshot returns the latest runtime snapshot.
func (s *State) Snapshot() domainmonitor.Snapshot { return s.snapshot }

// Hosts returns the Vault Hosts the surface knows.
func (s *State) Hosts() []domainvault.Host { return s.hostItems }

// Host returns a known Vault Host by id.
func (s *State) Host(id string) (domainvault.Host, bool) {
	host, ok := s.hostByID[id]
	return host, ok
}

// Panels returns the configured panels.
func (s *State) Panels() []domainpreferences.MonitorPanel {
	return append([]domainpreferences.MonitorPanel(nil), s.panels...)
}

// ActivePanel returns the panel being displayed.
func (s *State) ActivePanel() (domainpreferences.MonitorPanel, bool) {
	return s.findPanel(s.activeID)
}

// SetPanel displays another panel. The previous one pauses; the new one
// samples only if the user started it.
func (s *State) SetPanel(id string) {
	if _, ok := s.findPanel(id); !ok || id == s.activeID {
		return
	}
	s.stopActive()
	s.activeID = id
	s.detailID = ""
	s.closeEditor()
	if s.started[id] {
		s.startActive()
	}
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

// HostState returns a Host's runtime state for the displayed panel. While
// the panel is not sampling it is an empty, stopped state, so a previous
// run's readings never pass for current ones.
func (s *State) HostState(id string) domainmonitor.HostState {
	if !s.Running() {
		return domainmonitor.HostState{HostID: id, Status: StatusStopped}
	}
	if state, ok := s.snapshot.Hosts[id]; ok {
		return state
	}
	return domainmonitor.HostState{HostID: id, Status: domainmonitor.StatusQueued}
}

// DisplayStatus is the state a Host's badge shows: the runtime status, or
// stopped while the panel is not sampling, or stale for a live Host whose
// last sample is older than the freshness window.
func (s *State) DisplayStatus(id string) domainmonitor.Status {
	state := s.HostState(id)
	if state.Status == domainmonitor.StatusLive {
		interval := domainpreferences.DefaultMonitorInterval
		if panel, ok := s.findPanel(s.activeID); ok {
			interval = panel.Interval
		}
		if domainmonitor.IsStale(state, time.Duration(interval)*time.Second, s.now()) {
			return StatusStale
		}
	}
	return state.Status
}

// Now returns the surface's clock.
func (s *State) Now() time.Time { return s.now() }

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

// Started reports whether the user started the displayed panel.
func (s *State) Started() bool { return s.started[s.activeID] }

// AnyStarted reports whether any panel was started, for Stop all.
func (s *State) AnyStarted() bool { return len(s.started) > 0 || s.runningPanel != "" }

// Start begins sampling the displayed panel.
func (s *State) Start() {
	if _, ok := s.findPanel(s.activeID); !ok {
		s.problem = ProblemNoPanel
		s.requestRedraw()
		return
	}
	s.started[s.activeID] = true
	s.startActive()
	if !s.Running() {
		delete(s.started, s.activeID)
	}
	s.requestRedraw()
}

// Stop ends sampling of the displayed panel.
func (s *State) Stop() {
	delete(s.started, s.activeID)
	s.stopActive()
	s.detailID = ""
	s.requestRedraw()
}

// StopAll ends sampling of every panel.
func (s *State) StopAll() {
	s.started = make(map[string]bool)
	if s.service != nil {
		s.service.StopAll()
	}
	s.runningPanel = ""
	s.detailID = ""
	s.requestRedraw()
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

// SetColumnVisible shows or hides an optional column and persists it. Host
// and terminal are always shown.
func (s *State) SetColumnVisible(key string, visible bool) {
	if key == "host" || key == "terminal" || s.hidden[key] == !visible {
		return
	}
	if visible {
		delete(s.hidden, key)
	} else {
		s.hidden[key] = true
	}
	if err := s.saveConfiguration(); err != nil {
		if visible {
			s.hidden[key] = true
		} else {
			delete(s.hidden, key)
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

// ColumnWidth is the width a column draws at: the saved width, or the width
// a header drag in progress has reached.
func (s *State) ColumnWidth(key string) float32 {
	if width, ok := s.dragging[key]; ok {
		return width
	}
	return float32(s.Width(key))
}

// DragColumn widens or narrows a column by dx while its header edge drags.
// Nothing is saved until EndColumnDrag.
func (s *State) DragColumn(key string, dx float32) {
	if _, ok := domainpreferences.MonitorColumnDefaults[key]; !ok || dx == 0 {
		return
	}
	width := s.ColumnWidth(key) + dx
	if width < 40 {
		width = 40
	}
	s.dragging[key] = width
	s.requestRedraw()
}

// EndColumnDrag persists the width a header drag reached, clamped by the
// settings store's rules so the width shown is the width a reload restores.
func (s *State) EndColumnDrag(key string) {
	width, ok := s.dragging[key]
	if !ok {
		return
	}
	delete(s.dragging, key)
	s.SetColumnWidth(key, int(width+0.5))
}

// ResetColumnWidth restores a column's default width.
func (s *State) ResetColumnWidth(key string) {
	delete(s.dragging, key)
	s.SetColumnWidth(key, domainpreferences.MonitorColumnDefaults[key])
}

// SetColumnWidth saves a column's width, clamped as the settings store
// clamps it.
func (s *State) SetColumnWidth(key string, width int) {
	if _, ok := domainpreferences.MonitorColumnDefaults[key]; !ok {
		return
	}
	normalized := domainpreferences.NormalizeMonitor(domainpreferences.Monitor{
		ColumnWidths: map[string]int{key: width},
	})
	width = normalized.ColumnWidths[key]
	if width == s.Width(key) {
		s.requestRedraw()
		return
	}
	previous, had := s.widths[key]
	s.widths[key] = width
	if err := s.saveConfiguration(); err != nil {
		if had {
			s.widths[key] = previous
		} else {
			delete(s.widths, key)
		}
		s.problem = ProblemSave
	}
	s.requestRedraw()
}

// Editing reports whether the panel editor is open.
func (s *State) Editing() bool { return s.editing }

// Creating reports whether the open editor makes a new panel.
func (s *State) Creating() bool { return s.editing && s.creating }

// Form returns the panel editor's form. It is valid while Editing.
func (s *State) Form() *Form { return &s.form }

// FormHostSelected reports whether a Host is selected in the editor.
func (s *State) FormHostSelected(id string) bool { return s.formHosts[id] }

// ToggleFormHost selects or deselects a Host in the editor.
func (s *State) ToggleFormHost(id string) {
	if s.formHosts[id] {
		s.RemoveFormHost(id)
		return
	}
	s.formHosts[id] = true
	s.form.HostIDs = append(s.form.HostIDs, id)
	s.clearHostProblem()
}

// RemoveFormHost removes a Host from the editor's selection.
func (s *State) RemoveFormHost(id string) {
	delete(s.formHosts, id)
	for index, existing := range s.form.HostIDs {
		if existing == id {
			s.form.HostIDs = append(s.form.HostIDs[:index], s.form.HostIDs[index+1:]...)
			break
		}
	}
	s.requestRedraw()
}

// MoveFormHost moves a selected Host within the saved order.
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
		s.requestRedraw()
		return
	}
}

func (s *State) clearHostProblem() {
	if s.problem == ProblemHostRequired {
		s.problem = ProblemNone
	}
}

// Picking reports whether the editor's Host picker is open.
func (s *State) Picking() bool { return s.editing && s.picking }

// BeginPickHosts opens the Host picker over the editor.
func (s *State) BeginPickHosts() {
	if !s.editing {
		return
	}
	s.picking = true
	s.pickerSearch = ""
	s.requestRedraw()
}

// EndPickHosts returns from the Host picker to the editor.
func (s *State) EndPickHosts() {
	s.picking = false
	s.requestRedraw()
}

// PickerHosts returns the Vault Hosts the picker lists, filtered by its
// search as the surface's search filters a panel.
func (s *State) PickerHosts() []domainvault.Host {
	query := strings.ToLower(strings.TrimSpace(s.pickerSearch))
	out := make([]domainvault.Host, 0, len(s.hostItems))
	for _, host := range s.hostItems {
		if query == "" || hostMatches(host, query) {
			out = append(out, host)
		}
	}
	return out
}

// BeginCreatePanel opens the editor with a new panel.
func (s *State) BeginCreatePanel() {
	s.detailID = ""
	s.editing = true
	s.creating = true
	s.picking = false
	s.problem = ProblemNone
	s.form = Form{
		ID:       s.newPanelID(),
		View:     domainpreferences.MonitorViewCharts,
		Interval: strconv.Itoa(domainpreferences.DefaultMonitorInterval),
		Range:    strconv.Itoa(15),
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
	s.detailID = ""
	s.editing = true
	s.creating = false
	s.picking = false
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
	s.closeEditor()
	s.problem = ProblemNone
	s.requestRedraw()
}

func (s *State) closeEditor() {
	s.editing = false
	s.creating = false
	s.picking = false
	s.form = Form{}
	s.formHosts = make(map[string]bool)
}

// SavePanel validates the form and persists it through the settings store.
// Saving keeps configuration only: a started panel resamples its new Hosts,
// and a new panel waits for Start.
func (s *State) SavePanel() {
	name := strings.TrimSpace(s.form.Name)
	if name == "" {
		s.problem = ProblemNameRequired
		s.requestRedraw()
		return
	}
	if len(s.form.HostIDs) == 0 {
		s.problem = ProblemHostRequired
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
	if !s.commitPanels(next, panel.ID) {
		return
	}
	s.closeEditor()
	s.problem = ProblemNone
	s.restartActive()
	s.requestRedraw()
}

// DuplicatePanel saves a copy of the active panel named by copyName, with a
// number appended while the name is taken, and displays it. The copy starts
// stopped.
func (s *State) DuplicatePanel(copyName func(name string) string) {
	panel, ok := s.findPanel(s.activeID)
	if !ok {
		s.problem = ProblemNoPanel
		s.requestRedraw()
		return
	}
	base := copyName(panel.Name)
	name := base
	for suffix := 2; s.nameTaken(name); suffix++ {
		name = base + " " + strconv.Itoa(suffix)
	}
	copied := panel
	copied.ID = s.newPanelID()
	copied.Name = name
	copied.HostIDs = append([]string(nil), panel.HostIDs...)
	next := append(append([]domainpreferences.MonitorPanel(nil), s.panels...), copied)
	if !s.commitPanels(next, copied.ID) {
		return
	}
	s.closeEditor()
	s.detailID = ""
	s.problem = ProblemNone
	s.requestRedraw()
}

func (s *State) nameTaken(name string) bool {
	for _, panel := range s.panels {
		if strings.EqualFold(strings.TrimSpace(panel.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// commitPanels persists next and displays active, pausing the previous
// panel. It restores the previous configuration when the store fails.
func (s *State) commitPanels(next []domainpreferences.MonitorPanel, active string) bool {
	previous := append([]domainpreferences.MonitorPanel(nil), s.panels...)
	previousActive := s.activeID
	s.panels = next
	s.activeID = active
	if err := s.saveConfiguration(); err != nil {
		s.panels = previous
		s.activeID = previousActive
		s.problem = ProblemSave
		s.requestRedraw()
		return false
	}
	if previousActive != active {
		s.stopRunning()
	}
	return true
}

// PendingDelete returns the panel awaiting delete confirmation.
func (s *State) PendingDelete() (domainpreferences.MonitorPanel, bool) {
	return s.findPanel(s.deleteID)
}

// RequestDeletePanel asks to confirm deleting the active panel.
func (s *State) RequestDeletePanel() {
	if _, ok := s.findPanel(s.activeID); !ok {
		s.problem = ProblemNoPanel
		s.requestRedraw()
		return
	}
	s.deleteID = s.activeID
	s.requestRedraw()
}

// CancelDeletePanel closes the delete confirmation.
func (s *State) CancelDeletePanel() {
	s.deleteID = ""
	s.requestRedraw()
}

// ConfirmDeletePanel deletes the panel awaiting confirmation and stops its
// sampling.
func (s *State) ConfirmDeletePanel() {
	panel, ok := s.findPanel(s.deleteID)
	s.deleteID = ""
	if !ok {
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
	active := s.activeID
	if active == panel.ID {
		active = ""
		if len(next) > 0 {
			active = next[0].ID
		}
	}
	if panel.ID == s.runningPanel {
		s.stopRunning()
	}
	if !s.commitPanels(next, active) {
		return
	}
	delete(s.started, panel.ID)
	if s.service != nil {
		s.service.Stop(panel.ID)
	}
	s.closeEditor()
	s.detailID = ""
	s.problem = ProblemNone
	if s.started[s.activeID] && !s.Running() {
		s.startActive()
	}
	s.requestRedraw()
}

// UpdatePanelInterval changes the active panel's refresh cadence, persists
// it, and resamples a running panel at the new cadence.
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

// UpdatePanelRange changes the active panel's trend window and persists it.
func (s *State) UpdatePanelRange(minutes int) {
	panel, ok := s.findPanel(s.activeID)
	if !ok || panel.Range == minutes {
		return
	}
	panel.Range = minutes
	s.applyPanelChange(panel)
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

func (s *State) applyPanelChange(panel domainpreferences.MonitorPanel) {
	previous := append([]domainpreferences.MonitorPanel(nil), s.panels...)
	s.panels = s.replacePanel(panel)
	if err := s.saveConfiguration(); err != nil {
		s.panels = previous
		s.problem = ProblemSave
	}
}

// DetailID returns the Host whose detail panel is open, or "".
func (s *State) DetailID() string { return s.detailID }

// OpenDetail opens a live Host's detail panel. A Host that is not live has
// nothing to detail, as in the original.
func (s *State) OpenDetail(id string) {
	if s.DisplayStatus(id) != domainmonitor.StatusLive {
		return
	}
	s.closeEditor()
	s.detailID = id
	s.requestRedraw()
}

// CloseDetail closes the detail panel.
func (s *State) CloseDetail() {
	s.detailID = ""
	s.requestRedraw()
}

// OpenHost opens a Host's Session.
func (s *State) OpenHost(id string) {
	host, ok := s.hostByID[id]
	if !ok || s.openTerminal == nil {
		s.problem = ProblemOpenTerminal
		s.requestRedraw()
		return
	}
	if err := s.openTerminal(host); err != nil {
		s.problem = ProblemOpenTerminal
	}
	s.requestRedraw()
}

// DismissProblem clears the current failure.
func (s *State) DismissProblem() {
	s.problem = ProblemNone
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

func (s *State) stopActive() { s.stopRunning() }

// stopRunning ends whichever panel samples, the displayed one or one just
// replaced by another.
func (s *State) stopRunning() {
	if s.service != nil && s.runningPanel != "" {
		s.service.Stop(s.runningPanel)
	}
	s.runningPanel = ""
}

// restartActive resamples the displayed panel when the user started it.
func (s *State) restartActive() {
	s.stopRunning()
	if s.started[s.activeID] {
		s.startActive()
	}
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
