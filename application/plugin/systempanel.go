package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// SystemPanel is the host-owned coordinator for plugin System panel sections.
// It owns the single Extensions tab's data: it enumerates the sections a live
// plugin declared, activates sampling only for the surfaces the UI reports
// visible, enforces the push and refresh cadences, caches the last good
// payload, and dispatches plugin actions. It never executes a command: the
// only host-side data source is the fixed collector port.
type SystemPanel struct {
	fibers  func() []PanelFiber
	source  PanelSnapshotSource
	cadence PanelCadence
	audit   AuditSink
	now     func() time.Time

	mu         sync.Mutex
	localizeFn func(PluginRef, string, string) string
	surfaces   map[domainplugin.PanelSurface]*panelSurfaceState
	sections   map[panelSectionKey]*panelSectionState
	inflight   map[string]bool
	lastPush   map[string]time.Time
	snapshots  map[panelSnapshotKey]*panelSnapshotCache
	passes     map[string]*panelPassState
	listeners  map[int]func()
	nextID     int
	running    bool
	closed     bool
}

// PanelSnapshotSource collects the built-in collectors for one Host. The app
// implements it over the fixed application/sysmanager command templates; the
// coordinator never passes a command.
type PanelSnapshotSource interface {
	CollectPanelSnapshot(ctx context.Context, hostID string,
		collectors []domainplugin.PanelCollector) (domainplugin.PanelSnapshot, error)
}

// PanelCadence resolves the Host's configured refresh interval for one
// built-in collector. The app implements it over the persisted System manager
// preferences, so a snapshot section follows the same 1/2/3/5/10/30/60 s
// cadence the built-in tabs sample at. A non-positive result falls back to the
// built-in tab default.
type PanelCadence func(collector domainplugin.PanelCollector) time.Duration

// PanelDeps wires the coordinator's ports.
type PanelDeps struct {
	Source  PanelSnapshotSource
	Cadence PanelCadence
	Audit   AuditSink
	Now     func() time.Time
}

// PanelFiber is one live plugin instance visible to the System panel. Invoke
// runs a reserved host-to-guest command; it is nil when the fiber is not
// active.
type PanelFiber struct {
	Ref      PluginRef
	Manifest domainplugin.Manifest
	Active   bool
	Invoke   func(context.Context, string) error
}

// panelFiberInvokeTimeout bounds one refresh or action dispatch.
const panelFiberInvokeTimeout = domainplugin.PanelRefreshTimeout

// panelTickInterval is the coordinator's scheduling granularity. Each section
// keeps its own cadence; the tick only decides when a cadence has elapsed.
const panelTickInterval = 250 * time.Millisecond

type panelSectionKey struct {
	plugin    string
	sectionID string
	hostID    string
}

type panelSectionState struct {
	state     domainplugin.PanelState
	detail    string
	payload   *domainplugin.PanelPayload
	sampledAt time.Time
	failures  int
	// lastAttempt is the last host-initiated refresh start.
	lastAttempt time.Time
	// awaiting marks a fast or snapshot section whose refresh is in flight; a
	// payload is accepted only while it is set.
	awaiting bool
	updated  bool
	rejected int
}

type panelSurfaceState struct {
	active bool
	hostID string
}

type panelSnapshotKey struct {
	hostID    string
	collector domainplugin.PanelCollector
}

// panelSnapshotCache is one collector's last good sample. sampledAt is the
// source-reported collection time returned to the plugin; at is the panel
// clock when the pass that produced it started, used only to decide whether
// the collector's configured cadence elapsed.
type panelSnapshotCache struct {
	payload   json.RawMessage
	skipped   string
	sampledAt time.Time
	at        time.Time
}

// panelPassState enforces one collection pass at a time per Host and at least
// the Host's fastest configured collector cadence between pass starts.
type panelPassState struct {
	last time.Time
	busy bool
}

// maxPanelSectionEntriesPerPlugin bounds the section/host states one plugin
// can create. A plugin declares at most MaxPanelSections sections, and two
// surfaces can be visible, so the legitimate maximum is small; the headroom
// absorbs a Host switch in progress.
const maxPanelSectionEntriesPerPlugin = 4 * domainplugin.MaxPanelSections

// NewSystemPanel returns a coordinator over the supplied ports. Bind supplies
// the live fiber source; until then the panel renders no sections.
func NewSystemPanel(deps PanelDeps) *SystemPanel {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	audit := deps.Audit
	if audit == nil {
		audit = NopAudit{}
	}
	return &SystemPanel{
		source:    deps.Source,
		cadence:   deps.Cadence,
		audit:     audit,
		now:       now,
		surfaces:  make(map[domainplugin.PanelSurface]*panelSurfaceState),
		sections:  make(map[panelSectionKey]*panelSectionState),
		inflight:  make(map[string]bool),
		lastPush:  make(map[string]time.Time),
		snapshots: make(map[panelSnapshotKey]*panelSnapshotCache),
		passes:    make(map[string]*panelPassState),
		listeners: make(map[int]func()),
	}
}

// Bind attaches the live fiber source. The manager binds its container; tests
// bind a fake.
func (p *SystemPanel) Bind(fibers func() []PanelFiber) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.fibers = fibers
	p.mu.Unlock()
}

// Subscribe registers a redraw listener and returns its unsubscribe function.
func (p *SystemPanel) Subscribe(fn func()) func() {
	if p == nil || fn == nil {
		return func() {}
	}
	p.mu.Lock()
	id := p.nextID
	p.nextID++
	p.listeners[id] = fn
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.listeners, id)
		p.mu.Unlock()
	}
}

func (p *SystemPanel) notify() {
	p.mu.Lock()
	listeners := make([]func(), 0, len(p.listeners))
	for _, listener := range p.listeners {
		listeners = append(listeners, listener)
	}
	p.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

// ActivatePanel makes one surface visible on hostID. A host change for that
// surface discards the previous Host's payloads, so one Host's data never
// renders under another. It also wakes every declared section once so a push
// plugin learns that its section is visible.
func (p *SystemPanel) ActivatePanel(surface domainplugin.PanelSurface, hostID string) {
	if p == nil || !surface.Valid() || hostID == "" {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	state := p.surfaces[surface]
	if state == nil {
		state = &panelSurfaceState{}
		p.surfaces[surface] = state
	}
	previous := state.hostID
	state.active = true
	state.hostID = hostID
	if previous != "" && previous != hostID && !p.hostActiveLocked(previous) {
		p.dropHostLocked(previous)
	}
	p.mu.Unlock()

	p.ensureLoop()
	p.wakeSurface(surface, hostID)
	p.notify()
}

// DeactivatePanel hides one surface and stops its sampling within one tick.
// Cached payloads stay until no surface uses their Host.
func (p *SystemPanel) DeactivatePanel(surface domainplugin.PanelSurface) {
	if p == nil || !surface.Valid() {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	state := p.surfaces[surface]
	if state == nil || !state.active {
		p.mu.Unlock()
		return
	}
	state.active = false
	hostID := state.hostID
	state.hostID = ""
	if hostID != "" && !p.hostActiveLocked(hostID) {
		p.dropHostLocked(hostID)
	}
	p.mu.Unlock()
	p.notify()
}

// RefreshPanel wakes every declared section of the visible surface on hostID.
// It is the surface's Refresh now action; a push section learns that the user
// asked for an immediate sample.
func (p *SystemPanel) RefreshPanel(surface domainplugin.PanelSurface, hostID string) {
	if p == nil || !surface.Valid() || hostID == "" {
		return
	}
	p.mu.Lock()
	state := p.surfaces[surface]
	active := !p.closed && state != nil && state.active && state.hostID == hostID
	p.mu.Unlock()
	if !active {
		return
	}
	p.wakeSurface(surface, hostID)
	p.notify()
}

// SetLocalize installs the host contribution localizer. Section titles and
// action labels resolve on every PanelSections call, so a locale change needs
// no plugin reload.
func (p *SystemPanel) SetLocalize(fn func(PluginRef, string, string) string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.localizeFn = fn
	p.mu.Unlock()
}

func (p *SystemPanel) localizeContribution(ref PluginRef,
	key, literal string) string {
	if p == nil {
		return literal
	}
	p.mu.Lock()
	fn := p.localizeFn
	p.mu.Unlock()
	if fn == nil || key == "" {
		return literal
	}
	return fn(ref, key, literal)
}

// localizedSection returns a section copy whose title and action labels are
// resolved for the current host locale. The manifest itself is never mutated.
func (p *SystemPanel) localizedSection(ref PluginRef,
	section domainplugin.PanelSection) domainplugin.PanelSection {
	section.Actions = append([]domainplugin.PanelAction(nil), section.Actions...)
	section.Title = p.localizeContribution(ref, section.TitleKey, section.Title)
	for i := range section.Actions {
		action := &section.Actions[i]
		action.Label = p.localizeContribution(ref, action.LabelKey, action.Label)
	}
	return section
}

// PanelSections returns the sections a surface should render, in manifest
// order. The side surface keeps only compact sections; the manager shows
// every section. A surface that is not active returns nothing: an inactive tab
// never holds a stale cross-Host view.
func (p *SystemPanel) PanelSections(surface domainplugin.PanelSurface,
	hostID string) []domainplugin.PanelSectionView {
	if p == nil || !surface.Valid() || hostID == "" {
		return nil
	}
	p.mu.Lock()
	state := p.surfaces[surface]
	active := !p.closed && state != nil && state.active && state.hostID == hostID
	p.mu.Unlock()
	if !active {
		return nil
	}
	fibers := p.fiberSnapshot()
	views := make([]domainplugin.PanelSectionView, 0)
	for _, fiber := range fibers {
		if !fiber.Active || fiber.Ref.ID == "" {
			continue
		}
		for _, section := range fiber.Manifest.Contributions.SystemPanel {
			if !panelSectionOnSurface(section, surface) {
				continue
			}
			view := domainplugin.PanelSectionView{
				Plugin:  panelPlugin(fiber.Ref),
				Section: p.localizedSection(fiber.Ref, section),
				State:   domainplugin.PanelStateLoading,
			}
			p.mu.Lock()
			cached := p.sections[panelSectionKey{
				plugin: refKey(fiber.Ref), sectionID: section.ID, hostID: hostID,
			}]
			if cached != nil {
				view.State = cached.state
				if view.State == "" {
					view.State = domainplugin.PanelStateLoading
				}
				view.Detail = cached.detail
				view.Payload = cached.payload.Clone()
				view.SampledAt = cached.sampledAt
				view.Rejected = cached.rejected
			}
			p.mu.Unlock()
			views = append(views, view)
		}
	}
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		if left.Section.Order != right.Section.Order {
			return left.Section.Order < right.Section.Order
		}
		if left.Plugin.Name != right.Plugin.Name {
			return left.Plugin.Name < right.Plugin.Name
		}
		if left.Plugin.ID != right.Plugin.ID {
			return left.Plugin.ID < right.Plugin.ID
		}
		return left.Section.ID < right.Section.ID
	})
	return views
}

// Update accepts one section payload from a plugin. hostID must name an active
// surface: a section targets its surface's Host and never aggregates across
// Hosts. Push sections are rate-limited to ten updates per second with a
// 100 ms floor; fast and snapshot sections accept a payload only while a
// host-initiated refresh is in flight, so a plugin cannot drive their cadence.
// A rejected payload never replaces the last good one.
func (p *SystemPanel) Update(ref PluginRef, hostID, sectionID string, raw []byte) error {
	if p == nil {
		return &ServiceError{Service: ServiceSystemPanel, Method: "update",
			Code: ServiceCodeInvalid, Message: "the System panel is not configured"}
	}
	fiber, section, ok := p.declaredSection(ref, sectionID)
	if !ok {
		return &ServiceError{Service: ServiceSystemPanel, Method: "update",
			Code: ServiceCodeNotFound, Message: fmt.Sprintf("section %q is not declared", sectionID)}
	}
	if !fiber.Active {
		return &ServiceError{Service: ServiceSystemPanel, Method: "update",
			Code: ServiceCodeConflict, Message: "the plugin instance is not active"}
	}
	// Reject an unknown Host before decoding or creating any per-section
	// state, so a plugin cannot grow the section/host map by inventing IDs.
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return &ServiceError{Service: ServiceSystemPanel, Method: "update",
			Code: ServiceCodeFault, Message: ErrServiceUnavailable.Error()}
	}
	if !p.hostActiveLocked(hostID) {
		p.mu.Unlock()
		return &ServiceError{Service: ServiceSystemPanel, Method: "update",
			Code: ServiceCodeConflict, Message: "no System panel surface is visible on this Host"}
	}
	p.mu.Unlock()

	payload, err := domainplugin.DecodePanelPayload(raw, section)
	if err != nil {
		p.reject(ref, hostID, sectionID)
		return &ServiceError{Service: ServiceSystemPanel, Method: "update",
			Code: ServiceCodeInvalid, Message: err.Error(), Cause: err}
	}
	now := p.now().UTC()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return &ServiceError{Service: ServiceSystemPanel, Method: "update",
			Code: ServiceCodeFault, Message: ErrServiceUnavailable.Error()}
	}
	// Re-check under the lock: the Host may have been deactivated while the
	// payload was decoded. A stale Host never creates state.
	if !p.hostActiveLocked(hostID) {
		p.mu.Unlock()
		return &ServiceError{Service: ServiceSystemPanel, Method: "update",
			Code: ServiceCodeConflict, Message: "no System panel surface is visible on this Host"}
	}
	key := panelSectionKey{plugin: refKey(ref), sectionID: sectionID, hostID: hostID}
	state := p.sectionStateLocked(refKey(ref), key)
	if state == nil {
		p.mu.Unlock()
		return &ServiceError{Service: ServiceSystemPanel, Method: "update",
			Code:    ServiceCodeLimit,
			Message: "the plugin has reached its System panel section limit"}
	}
	if section.Refresh == domainplugin.PanelRefreshPush {
		if last := p.lastPush[refKey(ref)]; !last.IsZero() &&
			now.Sub(last) < domainplugin.PanelPushFloor {
			state.rejected++
			p.mu.Unlock()
			return &ServiceError{Service: ServiceSystemPanel, Method: "update",
				Code: ServiceCodeLimit,
				Message: fmt.Sprintf("updates are limited to one per %s",
					domainplugin.PanelPushFloor)}
		}
		p.lastPush[refKey(ref)] = now
	} else if !state.awaiting {
		state.rejected++
		p.mu.Unlock()
		return &ServiceError{Service: ServiceSystemPanel, Method: "update",
			Code:    ServiceCodeInvalid,
			Message: "a fast or snapshot section updates only during a host refresh"}
	}
	state.payload = payload.Clone()
	state.sampledAt = now
	state.failures = 0
	state.updated = true
	state.detail = ""
	if len(payload.Items) == 0 {
		state.state = domainplugin.PanelStateEmpty
	} else {
		state.state = domainplugin.PanelStateLive
	}
	p.mu.Unlock()
	p.notify()
	return nil
}

// reject counts one over-limit payload for diagnostics without touching the
// last good payload. It never creates state for an unknown Host.
func (p *SystemPanel) reject(ref PluginRef, hostID, sectionID string) {
	p.mu.Lock()
	if !p.closed && p.hostActiveLocked(hostID) {
		p.bumpRejectedLocked(ref, hostID, sectionID)
	}
	p.mu.Unlock()
}

func (p *SystemPanel) bumpRejectedLocked(ref PluginRef, hostID, sectionID string) {
	key := panelSectionKey{plugin: refKey(ref), sectionID: sectionID, hostID: hostID}
	state := p.sectionStateLocked(refKey(ref), key)
	if state == nil {
		return
	}
	state.rejected++
}

// sectionStateLocked returns key's state, creating it only while plugin stays
// under its bounded section/host map. A nil result means the plugin reached
// the limit and no state was created. The caller must hold p.mu.
func (p *SystemPanel) sectionStateLocked(plugin string, key panelSectionKey) *panelSectionState {
	if state := p.sections[key]; state != nil {
		return state
	}
	count := 0
	for existing := range p.sections {
		if existing.plugin == plugin {
			count++
		}
	}
	if count >= maxPanelSectionEntriesPerPlugin {
		return nil
	}
	state := &panelSectionState{state: domainplugin.PanelStateLoading}
	p.sections[key] = state
	return state
}

// RunPanelAction dispatches one declared action to its owning plugin. The
// action must belong to a section that plugin declared; there is no path to a
// built-in row or a built-in action. A destructive action is refused unless
// the UI has already confirmed it, and every request, denial, cancellation,
// and outcome is audited.
func (p *SystemPanel) RunPanelAction(ctx context.Context, request domainplugin.PanelActionRequest) error {
	if p == nil {
		return errors.New("plugin: the System panel is not configured")
	}
	if !request.Surface.Valid() || request.HostID == "" {
		return errors.New("plugin: a panel action requires a surface and Host")
	}
	p.mu.Lock()
	active := !p.closed && p.hostActiveLocked(request.HostID)
	p.mu.Unlock()
	if !active {
		return errors.New("plugin: no System panel surface is visible on this Host")
	}
	ref := pluginRef(request.Plugin)
	fiber, section, ok := p.declaredSection(ref, request.SectionID)
	if !ok {
		p.auditAction(ref, request, "not-found")
		return fmt.Errorf("plugin: section %q is not declared", request.SectionID)
	}
	if !fiber.Active {
		p.auditAction(ref, request, "inactive")
		return errors.New("plugin: the plugin instance is not active")
	}
	action, ok := panelActionByID(section, request.ActionID)
	if !ok {
		p.auditAction(ref, request, "unknown-action")
		return fmt.Errorf("plugin: action %q is not declared", request.ActionID)
	}
	if action.Destructive && !request.Confirmed {
		p.auditAction(ref, request, "denied-unconfirmed")
		return &ServiceError{Service: ServiceSystemPanel, Method: "action",
			Code: ServiceCodeDenied, Permission: domainplugin.PermissionSystemPanel,
			Message: "a destructive action requires host confirmation"}
	}
	if request.RowKey != "" && !p.rowExists(ref, request.HostID, section.ID, request.RowKey) {
		p.auditAction(ref, request, "unknown-row")
		return errors.New("plugin: the action's row is not in the section's last payload")
	}
	p.auditAction(ref, request, "request")
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, panelFiberInvokeTimeout)
	defer cancel()
	err := fiber.Invoke(callCtx, domainplugin.EncodePanelActionCommand(domainplugin.PanelActionCommand{
		SectionID: section.ID, ActionID: action.ID, RowKey: request.RowKey,
	}))
	switch {
	case errors.Is(callCtx.Err(), context.Canceled):
		p.auditAction(ref, request, "cancelled")
	case errors.Is(callCtx.Err(), context.DeadlineExceeded):
		p.auditAction(ref, request, "timeout")
	case err != nil:
		p.auditAction(ref, request, "error")
	default:
		p.auditAction(ref, request, "ok")
	}
	return err
}

// Snapshot returns the built-in collectors' normalized output for one Host.
// Sampling happens only while a surface is visible on that Host. Each
// collector follows the interval configured for its built-in tab (the same
// preferences the tabs use), and a Host never starts two collection passes
// closer together than its fastest configured collector, so rotating the
// requested set cannot exceed the host cadence. Until a collector's interval
// elapses, its last good sample is returned with its original sampled_at and
// Stale set. A failed collection degrades to the cache instead of losing it.
func (p *SystemPanel) Snapshot(ctx context.Context, hostID string,
	collectors []domainplugin.PanelCollector) (domainplugin.PanelSnapshot, error) {
	if p == nil {
		return domainplugin.PanelSnapshot{}, ErrServiceUnavailable
	}
	if hostID == "" {
		return domainplugin.PanelSnapshot{}, &ServiceError{Service: ServiceSystemPanel,
			Method: "snapshot", Code: ServiceCodeInvalid, Message: "a Host ID is required"}
	}
	requested, err := normalizePanelCollectors(collectors)
	if err != nil {
		return domainplugin.PanelSnapshot{}, &ServiceError{Service: ServiceSystemPanel,
			Method: "snapshot", Code: ServiceCodeInvalid, Message: err.Error()}
	}
	if p.source == nil {
		return domainplugin.PanelSnapshot{}, &ServiceError{Service: ServiceSystemPanel,
			Method: "snapshot", Code: ServiceCodeFault,
			Message: ErrServiceUnavailable.Error(), Cause: ErrServiceUnavailable}
	}
	now := p.now().UTC()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return domainplugin.PanelSnapshot{}, &ServiceError{Service: ServiceSystemPanel,
			Method: "snapshot", Code: ServiceCodeFault,
			Message: ErrServiceUnavailable.Error(), Cause: ErrServiceUnavailable}
	}
	if !p.hostActiveLocked(hostID) {
		result, _ := p.assembleSnapshotLocked(hostID, requested)
		p.mu.Unlock()
		result.Stale = true
		return result, nil
	}
	due, allowed := p.dueCollectorsLocked(hostID, requested, now)
	if !allowed || len(due) == 0 {
		// Cached data is fresh for its own cadence, but modulo a Host minimum
		// or an in-flight pass it was not collected for this request.
		result, _ := p.assembleSnapshotLocked(hostID, requested)
		result.Stale = true
		p.mu.Unlock()
		return result, nil
	}
	pass := p.passes[hostID]
	if pass == nil {
		pass = &panelPassState{}
		p.passes[hostID] = pass
	}
	// Record the pass before calling out so concurrent or subsequent requests
	// cannot start another one inside the Host minimum.
	pass.last = now
	pass.busy = true
	p.mu.Unlock()

	callCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), domainplugin.PanelRefreshTimeout)
	defer cancel()
	collected, collectErr := p.source.CollectPanelSnapshot(callCtx, hostID, due)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return domainplugin.PanelSnapshot{}, &ServiceError{Service: ServiceSystemPanel,
			Method: "snapshot", Code: ServiceCodeFault,
			Message: ErrServiceUnavailable.Error(), Cause: ErrServiceUnavailable}
	}
	if collectErr == nil {
		p.storeSnapshotLocked(hostID, due, collected, now)
	}
	pass.busy = false
	result, cached := p.assembleSnapshotLocked(hostID, requested)
	p.mu.Unlock()
	if collectErr == nil {
		result.Stale = len(due) < len(requested)
		return result, nil
	}
	if cached {
		result.Stale = true
		return result, nil
	}
	return domainplugin.PanelSnapshot{}, &ServiceError{Service: ServiceSystemPanel,
		Method: "snapshot", Code: ServiceCodeFault,
		Message: collectErr.Error(), Cause: collectErr}
}

// collectorInterval returns the configured cadence for one collector, never
// faster than the snapshot floor so a malformed preference cannot turn the
// snapshot service into an SSH storm.
func (p *SystemPanel) collectorInterval(collector domainplugin.PanelCollector) time.Duration {
	interval := time.Duration(0)
	if p.cadence != nil {
		interval = p.cadence(collector)
	}
	if interval <= 0 {
		interval = defaultPanelCollectorInterval(collector)
	}
	if interval < domainplugin.PanelSnapshotFloor {
		interval = domainplugin.PanelSnapshotFloor
	}
	return interval
}

// defaultPanelCollectorInterval mirrors the built-in tabs' initial cadences:
// three seconds for every collector except services and Docker at five.
func defaultPanelCollectorInterval(collector domainplugin.PanelCollector) time.Duration {
	switch collector {
	case domainplugin.PanelCollectorServices, domainplugin.PanelCollectorDocker:
		return 5 * time.Second
	default:
		return 3 * time.Second
	}
}

// hostCollectorMinimum is the fastest configured built-in collector cadence.
// It is the per-Host floor between any two collection passes: a plugin cannot
// exceed the host's own sampling rate by rotating the requested collector set.
func (p *SystemPanel) hostCollectorMinimum() time.Duration {
	minimum := time.Duration(0)
	for _, collector := range domainplugin.PanelCollectors {
		interval := p.collectorInterval(collector)
		if minimum == 0 || interval < minimum {
			minimum = interval
		}
	}
	return minimum
}

// dueCollectorsLocked returns the requested collectors whose own cadence
// elapsed, or allowed false while another pass is in flight or the Host
// minimum has not elapsed. The caller must hold p.mu.
func (p *SystemPanel) dueCollectorsLocked(hostID string,
	requested []domainplugin.PanelCollector, now time.Time) ([]domainplugin.PanelCollector, bool) {
	if pass := p.passes[hostID]; pass != nil {
		if pass.busy {
			return nil, false
		}
		if !pass.last.IsZero() && now.Sub(pass.last) < p.hostCollectorMinimum() {
			return nil, false
		}
	}
	due := make([]domainplugin.PanelCollector, 0, len(requested))
	for _, collector := range requested {
		cached := p.snapshots[panelSnapshotKey{hostID: hostID, collector: collector}]
		if cached == nil || now.Sub(cached.at) >= p.collectorInterval(collector) {
			due = append(due, collector)
		}
	}
	return due, true
}

// storeSnapshotLocked caches the results of one pass for the collectors it
// collected, preserving the source's sampled_at for later cached replies.
func (p *SystemPanel) storeSnapshotLocked(hostID string,
	due []domainplugin.PanelCollector, result domainplugin.PanelSnapshot, at time.Time) {
	sampledAt := result.SampledAt
	if sampledAt.IsZero() {
		sampledAt = at
	}
	for _, collector := range due {
		payload, hasPayload := result.Collectors[string(collector)]
		reason, hasReason := result.Skipped[string(collector)]
		if !hasPayload && !hasReason {
			// The source named neither a payload nor a reason. Keep the last
			// good sample rather than replacing it with an empty one.
			continue
		}
		cached := &panelSnapshotCache{at: at, sampledAt: sampledAt}
		if hasPayload {
			cached.payload = append(json.RawMessage(nil), payload...)
		}
		if hasReason {
			cached.skipped = reason
		}
		p.snapshots[panelSnapshotKey{hostID: hostID, collector: collector}] = cached
	}
}

// assembleSnapshotLocked builds the reply for requested from the
// per-collector cache. SampledAt is the oldest cached collection time, so a
// mixed-age reply never claims to be fresher than its oldest data. The bool
// reports whether any requested collector has a cached sample.
func (p *SystemPanel) assembleSnapshotLocked(hostID string,
	requested []domainplugin.PanelCollector) (domainplugin.PanelSnapshot, bool) {
	result := domainplugin.PanelSnapshot{
		HostID:     hostID,
		Collectors: make(map[string]json.RawMessage, len(requested)),
		Skipped:    make(map[string]string),
	}
	cachedAny := false
	for _, collector := range requested {
		cached := p.snapshots[panelSnapshotKey{hostID: hostID, collector: collector}]
		if cached == nil {
			continue
		}
		cachedAny = true
		if cached.payload != nil {
			result.Collectors[string(collector)] = append(json.RawMessage(nil), cached.payload...)
		}
		if cached.skipped != "" {
			result.Skipped[string(collector)] = cached.skipped
		}
		if !cached.sampledAt.IsZero() &&
			(result.SampledAt.IsZero() || cached.sampledAt.Before(result.SampledAt)) {
			result.SampledAt = cached.sampledAt
		}
	}
	return result, cachedAny
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func normalizePanelCollectors(collectors []domainplugin.PanelCollector) ([]domainplugin.PanelCollector, error) {
	if len(collectors) == 0 {
		return nil, errors.New("at least one collector is required")
	}
	seen := make(map[domainplugin.PanelCollector]bool, len(collectors))
	out := make([]domainplugin.PanelCollector, 0, len(collectors))
	for _, collector := range collectors {
		if !domainplugin.ValidPanelCollector(string(collector)) {
			return nil, fmt.Errorf("unknown collector %q", collector)
		}
		if seen[collector] {
			continue
		}
		seen[collector] = true
		out = append(out, collector)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// wakeSurface dispatches one refresh to every declared section of every live
// plugin, so a push plugin learns that its section became visible. A plugin
// already in flight is skipped rather than queued.
func (p *SystemPanel) wakeSurface(surface domainplugin.PanelSurface, hostID string) {
	for _, fiber := range p.fiberSnapshot() {
		if !fiber.Active || fiber.Invoke == nil {
			continue
		}
		sections := make([]domainplugin.PanelSection, 0,
			len(fiber.Manifest.Contributions.SystemPanel))
		for _, section := range fiber.Manifest.Contributions.SystemPanel {
			if panelSectionOnSurface(section, surface) {
				sections = append(sections, section)
			}
		}
		if len(sections) == 0 {
			continue
		}
		ref := refKey(fiber.Ref)
		now := p.now().UTC()
		p.mu.Lock()
		if p.closed || p.inflight[ref] {
			p.mu.Unlock()
			continue
		}
		p.inflight[ref] = true
		for _, section := range sections {
			key := panelSectionKey{plugin: ref, sectionID: section.ID, hostID: hostID}
			state := p.sectionStateLocked(ref, key)
			if state == nil {
				continue
			}
			state.updated = false
			if section.Refresh != domainplugin.PanelRefreshPush {
				state.awaiting = true
			}
			state.lastAttempt = now
		}
		p.mu.Unlock()
		go func(fiber PanelFiber, sections []domainplugin.PanelSection) {
			for _, section := range sections {
				err := p.invokeRefresh(fiber, surface, hostID, section)
				if section.Refresh != domainplugin.PanelRefreshPush {
					p.finishSectionRefresh(panelSectionKey{
						plugin: ref, sectionID: section.ID, hostID: hostID,
					}, err)
				}
			}
			p.mu.Lock()
			p.inflight[ref] = false
			p.mu.Unlock()
			p.notify()
		}(fiber, sections)
	}
}

// invokeRefresh sends one reserved refresh command with the section timeout.
func (p *SystemPanel) invokeRefresh(fiber PanelFiber, surface domainplugin.PanelSurface,
	hostID string, section domainplugin.PanelSection) error {
	ctx, cancel := context.WithTimeout(context.Background(), panelFiberInvokeTimeout)
	defer cancel()
	return fiber.Invoke(ctx, domainplugin.EncodePanelRefreshCommand(
		domainplugin.PanelRefreshRequest{
			Surface: surface, HostID: hostID, SectionID: section.ID,
		}))
}

func (p *SystemPanel) fiberSnapshot() []PanelFiber {
	p.mu.Lock()
	fibers := p.fibers
	p.mu.Unlock()
	if fibers == nil {
		return nil
	}
	return fibers()
}

func (p *SystemPanel) declaredSection(ref PluginRef, sectionID string) (PanelFiber, domainplugin.PanelSection, bool) {
	for _, fiber := range p.fiberSnapshot() {
		if !samePluginRef(fiber.Ref, ref) {
			continue
		}
		for _, section := range fiber.Manifest.Contributions.SystemPanel {
			if section.ID == sectionID {
				return fiber, section, true
			}
		}
		return PanelFiber{}, domainplugin.PanelSection{}, false
	}
	return PanelFiber{}, domainplugin.PanelSection{}, false
}

func (p *SystemPanel) rowExists(ref PluginRef, hostID, sectionID, rowKey string) bool {
	p.mu.Lock()
	state := p.sections[panelSectionKey{plugin: refKey(ref), sectionID: sectionID, hostID: hostID}]
	var payload *domainplugin.PanelPayload
	if state != nil {
		payload = state.payload.Clone()
	}
	p.mu.Unlock()
	if payload == nil {
		return false
	}
	for _, item := range payload.Items {
		if item.Kind != domainplugin.PanelDescriptorTable {
			continue
		}
		for _, row := range item.TableRows {
			if row.Key == rowKey {
				return true
			}
		}
	}
	return false
}

// ensureLoop starts the scheduling loop when a surface is active.
func (p *SystemPanel) ensureLoop() {
	p.mu.Lock()
	if p.closed || p.running || !p.anyActiveLocked() {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()
	go p.loop()
}

func (p *SystemPanel) loop() {
	ticker := time.NewTicker(panelTickInterval)
	defer ticker.Stop()
	for range ticker.C {
		p.mu.Lock()
		if p.closed || !p.anyActiveLocked() {
			p.running = false
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		p.sampleDue()
	}
}

func (p *SystemPanel) anyActiveLocked() bool {
	for _, surface := range p.surfaces {
		if surface.active {
			return true
		}
	}
	return false
}

func (p *SystemPanel) hostActiveLocked(hostID string) bool {
	for _, surface := range p.surfaces {
		if surface.active && surface.hostID == hostID {
			return true
		}
	}
	return false
}

func (p *SystemPanel) dropHostLocked(hostID string) {
	for key := range p.sections {
		if key.hostID == hostID {
			delete(p.sections, key)
		}
	}
	for key := range p.snapshots {
		if key.hostID == hostID {
			delete(p.snapshots, key)
		}
	}
	delete(p.passes, hostID)
}

// sampleDue starts a refresh for every fast or snapshot section whose cadence
// elapsed. Push sections are never polled. Exactly one refresh may be in
// flight per plugin instance; missed ticks are skipped, never queued.
func (p *SystemPanel) sampleDue() {
	now := p.now().UTC()
	type due struct {
		surface domainplugin.PanelSurface
		hostID  string
		fiber   PanelFiber
		section domainplugin.PanelSection
	}
	var starts []due
	fibers := p.fiberSnapshot()
	p.mu.Lock()
	for surface, state := range p.surfaces {
		if !state.active {
			continue
		}
		for _, fiber := range fibers {
			if !fiber.Active || fiber.Invoke == nil {
				continue
			}
			for _, section := range fiber.Manifest.Contributions.SystemPanel {
				if section.Refresh == domainplugin.PanelRefreshPush ||
					!panelSectionOnSurface(section, surface) {
					continue
				}
				key := panelSectionKey{plugin: refKey(fiber.Ref),
					sectionID: section.ID, hostID: state.hostID}
				current := p.sections[key]
				if current != nil {
					if current.awaiting {
						continue
					}
					delay := p.panelSectionInterval(section, current.payload)
					if current.failures >= 2 {
						delay *= 2
					}
					if !current.lastAttempt.IsZero() && now.Sub(current.lastAttempt) < delay {
						continue
					}
				}
				if p.inflight[refKey(fiber.Ref)] {
					continue
				}
				starts = append(starts, due{surface: surface, hostID: state.hostID,
					fiber: fiber, section: section})
			}
		}
	}
	p.mu.Unlock()
	for _, item := range starts {
		p.startRefresh(item.surface, item.hostID, item.fiber, item.section)
	}
}

// startRefresh marks one section in flight and dispatches the reserved
// refresh command off the caller's goroutine.
func (p *SystemPanel) startRefresh(surface domainplugin.PanelSurface, hostID string,
	fiber PanelFiber, section domainplugin.PanelSection) {
	ref := refKey(fiber.Ref)
	key := panelSectionKey{plugin: ref, sectionID: section.ID, hostID: hostID}
	p.mu.Lock()
	if p.closed || p.inflight[ref] || fiber.Invoke == nil {
		p.mu.Unlock()
		return
	}
	state := p.sectionStateLocked(ref, key)
	if state == nil || state.awaiting {
		p.mu.Unlock()
		return
	}
	p.inflight[ref] = true
	state.awaiting = true
	state.updated = false
	state.lastAttempt = p.now().UTC()
	p.mu.Unlock()

	go func() {
		err := p.invokeRefresh(fiber, surface, hostID, section)
		p.finishSectionRefresh(key, err)
		p.mu.Lock()
		p.inflight[ref] = false
		p.mu.Unlock()
		p.notify()
	}()
}

// finishSectionRefresh degrades one section on a timeout, a dispatch error, or
// a missing reply. The last good payload stays rendered; the next attempt
// backs off after two consecutive failures. The caller clears the plugin's
// in-flight marker.
func (p *SystemPanel) finishSectionRefresh(key panelSectionKey, err error) {
	p.mu.Lock()
	state := p.sections[key]
	if state == nil {
		p.mu.Unlock()
		return
	}
	state.awaiting = false
	updated := state.updated
	state.updated = false
	if !updated {
		state.failures++
		if state.payload != nil {
			state.state = domainplugin.PanelStateStale
		} else {
			state.state = domainplugin.PanelStateError
		}
		if err != nil {
			state.detail = err.Error()
		} else {
			state.detail = "the section did not reply before the refresh completed"
		}
	}
	p.mu.Unlock()
	p.notify()
}

func (p *SystemPanel) auditAction(ref PluginRef, request domainplugin.PanelActionRequest, result string) {
	if p.audit == nil {
		return
	}
	reason := fmt.Sprintf("systempanel.action:%s:%s:%s", request.SectionID, request.ActionID, result)
	scope := domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: request.HostID}
	p.audit.Record(domainplugin.AuditEvent{
		Time:       p.now().UTC(),
		Event:      domainplugin.AuditService,
		PluginID:   ref.ID,
		Source:     ref.Source,
		Version:    ref.Version,
		Permission: domainplugin.PermissionSystemPanel,
		Scope:      &scope,
		Reason:     reason,
	})
}

// Close stops the scheduling loop and releases listeners. Cached payloads are
// discarded.
func (p *SystemPanel) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.listeners = make(map[int]func())
	p.sections = make(map[panelSectionKey]*panelSectionState)
	p.snapshots = make(map[panelSnapshotKey]*panelSnapshotCache)
	p.passes = make(map[string]*panelPassState)
	p.mu.Unlock()
}

// panelSectionInterval applies the section's declared cadence. A snapshot
// section also follows the Host's configured collectors: the host never wakes
// it faster than its fastest built-in collector, so the plugin cannot make the
// Host collect faster than the tabs already do.
func (p *SystemPanel) panelSectionInterval(section domainplugin.PanelSection,
	payload *domainplugin.PanelPayload) time.Duration {
	delay := panelEffectiveInterval(section, payload)
	if section.Refresh != domainplugin.PanelRefreshSnapshot {
		return delay
	}
	if minimum := p.hostCollectorMinimum(); delay < minimum {
		delay = minimum
	}
	return delay
}

// panelEffectiveInterval applies the payload-shape default: a fast section
// that declares no interval refreshes tables at the 1 s fast default, the same
// as any other payload shape. Explicit intervals keep their declared value
// above the 500 ms floor. Snapshot sections use their declared interval here;
// panelSectionInterval adds the Host collector cadence.
func panelEffectiveInterval(section domainplugin.PanelSection,
	payload *domainplugin.PanelPayload) time.Duration {
	delay := domainplugin.IntervalFor(section)
	if section.Refresh != domainplugin.PanelRefreshFast || section.Interval != 0 ||
		payload == nil || delay >= domainplugin.PanelTableDefault {
		return delay
	}
	for _, item := range payload.Items {
		if item.Kind == domainplugin.PanelDescriptorTable {
			return domainplugin.PanelTableDefault
		}
	}
	return delay
}

// panelSectionOnSurface reports whether a surface renders the section. The
// side panel keeps only sections that declare the compact surface, so a
// section the side never draws is never sampled for it either.
func panelSectionOnSurface(section domainplugin.PanelSection,
	surface domainplugin.PanelSurface) bool {
	return surface != domainplugin.PanelSurfaceSide || section.Compact
}

func panelActionByID(section domainplugin.PanelSection, id string) (domainplugin.PanelAction, bool) {
	for _, action := range section.Actions {
		if action.ID == id {
			return action, true
		}
	}
	return domainplugin.PanelAction{}, false
}

func panelPlugin(ref PluginRef) domainplugin.PanelPlugin {
	return domainplugin.PanelPlugin{
		ID: ref.ID, Name: ref.Name, Version: ref.Version,
		Source: ref.Source, Development: ref.Development,
	}
}

func pluginRef(plugin domainplugin.PanelPlugin) PluginRef {
	return PluginRef{
		ID: plugin.ID, Name: plugin.Name, Version: plugin.Version,
		Source: plugin.Source, Development: plugin.Development,
	}
}

func refKey(ref PluginRef) string {
	if ref.Development {
		return string(ref.Source) + "\x00development\x00" + ref.ID
	}
	return string(ref.Source) + "\x00" + ref.ID
}

func samePluginRef(left, right PluginRef) bool {
	return left.ID == right.ID && left.Source == right.Source &&
		left.Development == right.Development
}
