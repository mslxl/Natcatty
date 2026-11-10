package plugin

import (
	"context"
	"sort"
	"time"

	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// InstanceStatsReporter is implemented by runtimes that can report a live
// module's memory footprint. A runtime that cannot report memory leaves the
// diagnostics value at zero rather than guessing.
type InstanceStatsReporter interface {
	MemoryBytes() int64
}

// FaultRecord is one contained trap or deadline in a fiber's bounded fault
// history. The history is host-side only; it can never grow without bound.
type FaultRecord struct {
	Time      time.Time `json:"time"`
	Operation string    `json:"operation"`
	Reason    string    `json:"reason"`
}

// CallTiming aggregates the guest calls of one operation. Durations use the
// injected container clock so tests can observe counts without wall time.
type CallTiming struct {
	Operation string        `json:"operation"`
	Calls     uint64        `json:"calls"`
	Total     time.Duration `json:"total_ns"`
	Last      time.Duration `json:"last_ns"`
	Max       time.Duration `json:"max_ns"`
}

// PluginDiagnostics is the first-party diagnostics view of one Plugin
// Instance. It is not a plugin service and exposes no guest data beyond the
// bounded log ring, timings, and fault reasons.
type PluginDiagnostics struct {
	ID          domainplugin.FiberID `json:"id"`
	Group       string               `json:"group"`
	PluginID    string               `json:"plugin_id"`
	Instance    int                  `json:"instance"`
	Name        string               `json:"name"`
	Version     string               `json:"version"`
	Source      domainplugin.Source  `json:"source"`
	Development bool                 `json:"development"`
	State       domainplugin.State   `json:"state"`
	Reason      string               `json:"reason,omitempty"`

	MissingInjections []string                       `json:"missing_injections,omitempty"`
	ConfigErrors      []domainplugin.SettingsError   `json:"config_errors,omitempty"`
	Grants            []domainplugin.PermissionGrant `json:"grants,omitempty"`

	Effects     int   `json:"effects"`
	MemoryBytes int64 `json:"memory_bytes"`
	MemoryMax   int64 `json:"memory_max_bytes"`

	Logs        []string `json:"logs,omitempty"`
	DroppedLogs int      `json:"dropped_logs,omitempty"`

	Faults  []FaultRecord `json:"faults,omitempty"`
	Timings []CallTiming  `json:"timings,omitempty"`

	Restarts          int    `json:"restarts"`
	ManualRestarts    int    `json:"manual_restarts"`
	AutomaticRestarts int    `json:"automatic_restarts"`
	RestartPending    bool   `json:"restart_pending,omitempty"`
	Generation        uint64 `json:"generation"`

	// MissingKeys counts every i18n fallback lookup. Manifest-referenced keys
	// are validated at install; UI-only keys surface here at runtime.
	MissingKeys []MissingKey `json:"missing_keys,omitempty"`
}

// GroupDiagnostics is one node of the group/fiber tree. Groups are namespaces
// and isolate scopes, never fibers; the Fibers list names the instances loaded
// directly in the group, and Children names the nested groups.
type GroupDiagnostics struct {
	Name     string                    `json:"name"`
	Parent   string                    `json:"parent,omitempty"`
	Defaults map[string]map[string]any `json:"defaults,omitempty"`
	Fibers   []domainplugin.FiberID    `json:"fibers,omitempty"`
	Children []string                  `json:"children,omitempty"`
}

// DiagnosticsReport is the complete first-party diagnostics snapshot: runtime
// mode, the group/fiber tree, and the published service providers.
type DiagnosticsReport struct {
	Runtime  RuntimeStatus       `json:"runtime"`
	Groups   []GroupDiagnostics  `json:"groups"`
	Fibers   []PluginDiagnostics `json:"fibers"`
	Services []ServiceProvider   `json:"services,omitempty"`

	// Cross-plugin service graph. Bindings name the resolved provider (or the
	// typed reason a binding is unbound); PendingConsumers names required
	// injections still waiting. Calls carries payload-free counters.
	ServiceBindings  []ServiceBinding `json:"service_bindings,omitempty"`
	PendingConsumers []ServiceBinding `json:"pending_consumers,omitempty"`
	ServiceCalls     ServiceCallStats `json:"service_calls"`
	Streams          int              `json:"streams"`
	Subscriptions    int              `json:"subscriptions"`

	// PluginData reports provider namespaces that are dormant, newer than the
	// active provider, or quarantined. It carries no field values.
	PluginData []plugindata.Diagnostic `json:"plugin_data,omitempty"`
}

// observeCall records one guest call's duration and refreshes the reported
// memory. It never lets a timing error affect the call itself.
func (f *Fiber) observeCall(operation string, start time.Time, err error, instance Instance) {
	if f == nil {
		return
	}
	now := time.Now
	if f.now != nil {
		now = f.now
	}
	duration := now().Sub(start)
	if duration < 0 {
		duration = 0
	}
	f.mu.Lock()
	timing := f.timings[operation]
	if timing == nil {
		timing = &CallTiming{Operation: operation}
		f.timings[operation] = timing
	}
	timing.Calls++
	timing.Total += duration
	timing.Last = duration
	if duration > timing.Max {
		timing.Max = duration
	}
	if reporter, ok := instance.(InstanceStatsReporter); ok {
		f.memoryBytes = reporter.MemoryBytes()
	}
	f.mu.Unlock()
}

// recordFault appends an unusable-instance failure to the bounded history. A
// call-level guest status or a host-service denial is not a fault and never
// enters the history or changes the lifecycle.
func (f *Fiber) recordFault(operation string, err error) {
	if f == nil || err == nil || !IsUnusable(err) {
		return
	}
	if f.now == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = append(f.faults, FaultRecord{
		Time:      f.now().UTC(),
		Operation: operation,
		Reason:    err.Error(),
	})
	if len(f.faults) > MaxFaultRecords {
		f.faults = append([]FaultRecord(nil), f.faults[len(f.faults)-MaxFaultRecords:]...)
	}
}

// fiberDiagnostics takes one consistent snapshot of a fiber. It copies every
// slice so a later unload cannot mutate a report a caller is rendering.
func fiberDiagnostics(fiber *Fiber) PluginDiagnostics {
	manifest := fiber.manifestSnapshot()
	fiber.mu.Lock()
	defer fiber.mu.Unlock()
	out := PluginDiagnostics{
		ID:                fiber.id,
		Group:             fiber.group,
		PluginID:          fiber.ref.ID,
		Instance:          fiber.instance,
		Name:              fiber.ref.Name,
		Version:           fiber.ref.Version,
		Source:            fiber.ref.Source,
		Development:       fiber.ref.Development,
		State:             fiber.state,
		Reason:            fiber.reason,
		MissingInjections: append([]string(nil), fiber.missingInjections...),
		ConfigErrors:      append([]domainplugin.SettingsError(nil), fiber.configErrors...),
		Effects:           len(fiber.disposers),
		MemoryBytes:       fiber.memoryBytes,
		MemoryMax:         manifest.Wasm.EffectiveMemoryMaxBytes(),
		Logs:              append([]string(nil), fiber.logs...),
		DroppedLogs:       fiber.droppedLogs,
		Faults:            append([]FaultRecord(nil), fiber.faults...),
		Restarts:          fiber.restarts,
		ManualRestarts:    fiber.manualRestarts,
		AutomaticRestarts: fiber.automaticRestarts,
		RestartPending:    fiber.restartPending,
		Generation:        fiber.generation,
	}
	for _, timing := range fiber.timings {
		copy := *timing
		out.Timings = append(out.Timings, copy)
	}
	sort.Slice(out.Timings, func(i, j int) bool {
		return out.Timings[i].Operation < out.Timings[j].Operation
	})
	return out
}

// DiagnosticsReport returns the container's diagnostics snapshot.
func (c *Container) DiagnosticsReport() DiagnosticsReport {
	if c == nil {
		return DiagnosticsReport{Runtime: RuntimeStatus{Engine: EngineUnknown}}
	}
	report := DiagnosticsReport{
		Runtime: c.RuntimeStatus(),
		Fibers:  make([]PluginDiagnostics, 0),
	}
	c.mu.Lock()
	groups := make([]domainplugin.Group, 0, len(c.groups))
	for _, group := range c.groups {
		group.Defaults = cloneGroupDefaults(group.Defaults)
		groups = append(groups, group)
	}
	fibers := make([]*Fiber, 0, len(c.fibers))
	for _, fiber := range c.fibers {
		fibers = append(fibers, fiber)
	}
	c.mu.Unlock()

	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	sort.Slice(fibers, func(i, j int) bool { return fibers[i].id < fibers[j].id })

	groupIndex := make(map[string]int, len(groups)+len(fibers))
	for _, group := range groups {
		groupIndex[group.Name] = len(report.Groups)
		report.Groups = append(report.Groups, GroupDiagnostics{
			Name:     group.Name,
			Parent:   group.Parent,
			Defaults: group.Defaults,
		})
	}
	// Link the tree from the group registry, once per group and before any
	// fibers are visited. A group with N instances therefore appears once in
	// its parent's Children, and a group with no fibers (or only nested
	// children) still appears in the tree.
	for _, group := range groups {
		if group.Parent == "" {
			continue
		}
		parent, ok := groupIndex[group.Parent]
		if !ok {
			continue
		}
		report.Groups[parent].Children = append(report.Groups[parent].Children, group.Name)
	}
	for _, fiber := range fibers {
		entry := fiberDiagnostics(fiber)
		report.Fibers = append(report.Fibers, entry)
		index, ok := groupIndex[entry.Group]
		if !ok {
			// Loading creates every fiber's group in the registry, so this is
			// defensive for a report taken mid-mutation.
			index = len(report.Groups)
			groupIndex[entry.Group] = index
			report.Groups = append(report.Groups, GroupDiagnostics{Name: entry.Group})
		}
		report.Groups[index].Fibers = append(report.Groups[index].Fibers, entry.ID)
	}
	report.Services = c.Services()
	if c.broker != nil {
		graph := c.broker.ServiceGraph()
		report.ServiceBindings = graph.Bindings
		report.PendingConsumers = pendingConsumerBindings(fibers)
		report.ServiceCalls = graph.Calls
		report.Streams = graph.Streams
		report.Subscriptions = graph.Subscriptions
	}
	return report
}

// pendingConsumerBindings renders a required injection PENDING fiber as an
// unbound binding row. The broker only tracks ACTIVE consumers, so the PENDING
// view comes from the fiber's recorded reason and missing services.
func pendingConsumerBindings(fibers []*Fiber) []ServiceBinding {
	out := make([]ServiceBinding, 0)
	for _, fiber := range fibers {
		if fiber.State() != domainplugin.StatePending {
			continue
		}
		fiber.mu.Lock()
		kind := fiber.pendingKind
		reason := fiber.reason
		missing := append([]string(nil), fiber.missingInjections...)
		fiber.mu.Unlock()
		if kind != pendingInjection {
			continue
		}
		for _, service := range missing {
			out = append(out, ServiceBinding{
				Consumer: fiber.id,
				PluginID: fiber.ref.ID,
				Service:  service,
				Reason:   reason,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Consumer != out[j].Consumer {
			return out[i].Consumer < out[j].Consumer
		}
		return out[i].Service < out[j].Service
	})
	return out
}

// PluginDiagnostics returns one fiber's diagnostics by identity.
func (c *Container) PluginDiagnostics(id domainplugin.FiberID) (PluginDiagnostics, bool) {
	if c == nil {
		return PluginDiagnostics{}, false
	}
	fiber, ok := c.Fiber(id)
	if !ok {
		return PluginDiagnostics{}, false
	}
	return fiberDiagnostics(fiber), true
}

// DiagnosticsReport returns the manager's diagnostics snapshot with the
// authority grants attached to each fiber. Grants are host-owned state, so the
// container itself cannot fill them in.
func (m *Manager) DiagnosticsReport() DiagnosticsReport {
	if m == nil || m.container == nil {
		return DiagnosticsReport{Runtime: RuntimeStatus{Engine: EngineUnknown}}
	}
	report := m.container.DiagnosticsReport()
	for i := range report.Fibers {
		entry := &report.Fibers[i]
		entry.Grants = m.authority.Grants(SubjectForInstalled(entry.Source, entry.PluginID))
		entry.MissingKeys = m.services.MissingKeys(entry.ID)
	}
	if m.pluginDataDiag != nil {
		// Diagnostics is a read-only view rendered on demand. A storage
		// failure leaves the section empty rather than failing the whole panel.
		if diagnostics, err := m.pluginDataDiag.Diagnostics(context.Background()); err == nil {
			report.PluginData = diagnostics
		}
	}
	return report
}

// PluginDiagnostics returns one plugin's diagnostics by plugin id, install key,
// or fiber identity. Instances are ordered by identity, so the first match is
// deterministic.
func (m *Manager) PluginDiagnostics(key string) (PluginDiagnostics, bool) {
	if m == nil {
		return PluginDiagnostics{}, false
	}
	for _, entry := range m.DiagnosticsReport().Fibers {
		if entry.PluginID == key || string(entry.Source)+"/"+entry.PluginID == key ||
			string(entry.ID) == key {
			return entry, true
		}
	}
	return PluginDiagnostics{}, false
}

func normalizeRestartBackoff(delay time.Duration) time.Duration {
	if delay < MinRestartBackoff {
		return MinRestartBackoff
	}
	return delay
}
