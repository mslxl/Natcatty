package preferences

// SysManagerTab identifies one independently refreshed system-manager tab.
type SysManagerTab string

const (
	SysManagerOverview  SysManagerTab = "overview"
	SysManagerProcesses SysManagerTab = "processes"
	SysManagerPorts     SysManagerTab = "ports"
	SysManagerServices  SysManagerTab = "services"
	SysManagerTmux      SysManagerTab = "tmux"
	SysManagerDocker    SysManagerTab = "docker"
	SysManagerGPU       SysManagerTab = "gpu"
)

// SysManagerTabs lists every known tab in the original's default display
// order. The first four are ticket 45's core; tmux, Docker, and GPU follow.
var SysManagerTabs = []SysManagerTab{
	SysManagerOverview, SysManagerProcesses, SysManagerPorts, SysManagerServices,
	SysManagerTmux, SysManagerDocker, SysManagerGPU,
}

// SysManagerLockedTabs lists tabs the user cannot hide. The overview stays
// reachable so the surface never loses its Host summary.
var SysManagerLockedTabs = []SysManagerTab{SysManagerOverview}

// SysManagerIntervalChoices are the refresh cadences offered for every tab.
// They match the monitoring choices so a user learns one set of values.
var SysManagerIntervalChoices = []int{1, 2, 3, 5, 10, 30, 60}

// The original refreshed overview, processes, ports, tmux, and GPU every
// three seconds and services and the Docker list every five.
const (
	DefaultSysManagerOverviewInterval  = 3
	DefaultSysManagerProcessesInterval = 3
	DefaultSysManagerPortsInterval     = 3
	DefaultSysManagerServicesInterval  = 5
	DefaultSysManagerTmuxInterval      = 3
	DefaultSysManagerDockerInterval    = 5
	DefaultSysManagerGPUInterval       = 3
)

// SysManager is the persisted system-manager configuration. Runtime samples
// and the selected Host never live here.
type SysManager struct {
	OverviewInterval  int `json:"overviewInterval"`
	ProcessesInterval int `json:"processesInterval"`
	PortsInterval     int `json:"portsInterval"`
	ServicesInterval  int `json:"servicesInterval"`
	TmuxInterval      int `json:"tmuxInterval"`
	DockerInterval    int `json:"dockerInterval"`
	GPUInterval       int `json:"gpuInterval"`
	// TabOrder lists every known tab in display order. NormalizeSysManager
	// guarantees each known tab appears exactly once.
	TabOrder []string `json:"tabOrder"`
	// HiddenTabs lists tabs the user hid from the strip. The overview is
	// always visible.
	HiddenTabs []string `json:"hiddenTabs"`
}

// DefaultSysManager returns the original's initial refresh cadences and tab
// layout.
func DefaultSysManager() SysManager {
	return SysManager{
		OverviewInterval:  DefaultSysManagerOverviewInterval,
		ProcessesInterval: DefaultSysManagerProcessesInterval,
		PortsInterval:     DefaultSysManagerPortsInterval,
		ServicesInterval:  DefaultSysManagerServicesInterval,
		TmuxInterval:      DefaultSysManagerTmuxInterval,
		DockerInterval:    DefaultSysManagerDockerInterval,
		GPUInterval:       DefaultSysManagerGPUInterval,
		TabOrder:          sysManagerTabStrings(SysManagerTabs),
		HiddenTabs:        []string{},
	}
}

// NormalizeSysManager repairs an interval that is not one of the offered
// choices, a tab order with unknown, duplicate, or missing entries, and a
// hidden list with unknown or locked tabs.
func NormalizeSysManager(manager SysManager) SysManager {
	manager.OverviewInterval = normalizeSysManagerInterval(
		manager.OverviewInterval, DefaultSysManagerOverviewInterval)
	manager.ProcessesInterval = normalizeSysManagerInterval(
		manager.ProcessesInterval, DefaultSysManagerProcessesInterval)
	manager.PortsInterval = normalizeSysManagerInterval(
		manager.PortsInterval, DefaultSysManagerPortsInterval)
	manager.ServicesInterval = normalizeSysManagerInterval(
		manager.ServicesInterval, DefaultSysManagerServicesInterval)
	manager.TmuxInterval = normalizeSysManagerInterval(
		manager.TmuxInterval, DefaultSysManagerTmuxInterval)
	manager.DockerInterval = normalizeSysManagerInterval(
		manager.DockerInterval, DefaultSysManagerDockerInterval)
	manager.GPUInterval = normalizeSysManagerInterval(
		manager.GPUInterval, DefaultSysManagerGPUInterval)
	manager.TabOrder = sysManagerTabStrings(normalizeSysManagerOrder(manager.TabOrder))
	manager.HiddenTabs = normalizeSysManagerHidden(manager.HiddenTabs)
	return manager
}

// Interval returns the saved cadence for tab in seconds.
func (m SysManager) Interval(tab SysManagerTab) int {
	switch tab {
	case SysManagerOverview:
		return m.OverviewInterval
	case SysManagerProcesses:
		return m.ProcessesInterval
	case SysManagerPorts:
		return m.PortsInterval
	case SysManagerServices:
		return m.ServicesInterval
	case SysManagerTmux:
		return m.TmuxInterval
	case SysManagerDocker:
		return m.DockerInterval
	case SysManagerGPU:
		return m.GPUInterval
	default:
		return DefaultSysManagerOverviewInterval
	}
}

// WithInterval returns a copy whose tab cadence is seconds.
func (m SysManager) WithInterval(tab SysManagerTab, seconds int) SysManager {
	switch tab {
	case SysManagerOverview:
		m.OverviewInterval = seconds
	case SysManagerProcesses:
		m.ProcessesInterval = seconds
	case SysManagerPorts:
		m.PortsInterval = seconds
	case SysManagerServices:
		m.ServicesInterval = seconds
	case SysManagerTmux:
		m.TmuxInterval = seconds
	case SysManagerDocker:
		m.DockerInterval = seconds
	case SysManagerGPU:
		m.GPUInterval = seconds
	}
	return NormalizeSysManager(m)
}

// Order returns every known tab in the saved display order.
func (m SysManager) Order() []SysManagerTab {
	order := normalizeSysManagerOrder(m.TabOrder)
	return order
}

// VisibleTabs returns the tabs shown in the strip, in display order.
func (m SysManager) VisibleTabs() []SysManagerTab {
	hidden := make(map[SysManagerTab]bool, len(m.HiddenTabs))
	for _, raw := range normalizeSysManagerHidden(m.HiddenTabs) {
		hidden[SysManagerTab(raw)] = true
	}
	visible := make([]SysManagerTab, 0, len(SysManagerTabs))
	for _, tab := range m.Order() {
		if !hidden[tab] {
			visible = append(visible, tab)
		}
	}
	return visible
}

// Visible reports whether tab is shown in the strip.
func (m SysManager) Visible(tab SysManagerTab) bool {
	for _, candidate := range m.VisibleTabs() {
		if candidate == tab {
			return true
		}
	}
	return false
}

// WithTabOrder returns a copy whose display order is order. Unknown,
// duplicate, and missing tabs are repaired against the canonical order.
func (m SysManager) WithTabOrder(order []SysManagerTab) SysManager {
	m.TabOrder = sysManagerTabStrings(order)
	return NormalizeSysManager(m)
}

// WithTabHidden returns a copy with tab shown or hidden. A locked tab, or an
// unknown tab, is left visible.
func (m SysManager) WithTabHidden(tab SysManagerTab, hidden bool) SysManager {
	next := make([]string, 0, len(SysManagerTabs))
	for _, candidate := range normalizeSysManagerHidden(m.HiddenTabs) {
		if SysManagerTab(candidate) != tab {
			next = append(next, candidate)
		}
	}
	if hidden {
		next = append(next, string(tab))
	}
	m.HiddenTabs = next
	return NormalizeSysManager(m)
}

// MoveTab returns a copy with tab moved delta places (negative is toward the
// front). A move past either end is a no-op.
func (m SysManager) MoveTab(tab SysManagerTab, delta int) SysManager {
	order := m.Order()
	from := -1
	for index, candidate := range order {
		if candidate == tab {
			from = index
			break
		}
	}
	if from < 0 || delta == 0 {
		return NormalizeSysManager(m)
	}
	to := from + delta
	if to < 0 || to >= len(order) {
		return NormalizeSysManager(m)
	}
	order[from], order[to] = order[to], order[from]
	return m.WithTabOrder(order)
}

func sysManagerTabStrings(tabs []SysManagerTab) []string {
	out := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		out = append(out, string(tab))
	}
	return out
}

func knownSysManagerTab(raw string) (SysManagerTab, bool) {
	for _, tab := range SysManagerTabs {
		if string(tab) == raw {
			return tab, true
		}
	}
	return "", false
}

// normalizeSysManagerOrder keeps each known tab once in the requested order,
// then appends any tab the file omitted in canonical order.
func normalizeSysManagerOrder(order []string) []SysManagerTab {
	out := make([]SysManagerTab, 0, len(SysManagerTabs))
	seen := make(map[SysManagerTab]bool, len(SysManagerTabs))
	for _, raw := range order {
		tab, ok := knownSysManagerTab(raw)
		if !ok || seen[tab] {
			continue
		}
		seen[tab] = true
		out = append(out, tab)
	}
	for _, tab := range SysManagerTabs {
		if !seen[tab] {
			out = append(out, tab)
		}
	}
	return out
}

// normalizeSysManagerHidden drops unknown, duplicate, and locked tabs.
func normalizeSysManagerHidden(hidden []string) []string {
	out := make([]string, 0, len(hidden))
	seen := make(map[SysManagerTab]bool, len(hidden))
	for _, raw := range hidden {
		tab, ok := knownSysManagerTab(raw)
		if !ok || seen[tab] || sysManagerTabLocked(tab) {
			continue
		}
		seen[tab] = true
		out = append(out, string(tab))
	}
	return out
}

func sysManagerTabLocked(tab SysManagerTab) bool {
	for _, locked := range SysManagerLockedTabs {
		if locked == tab {
			return true
		}
	}
	return false
}

func normalizeSysManagerInterval(value, fallback int) int {
	for _, choice := range SysManagerIntervalChoices {
		if value == choice {
			return value
		}
	}
	return fallback
}
