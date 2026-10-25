package preferences

// SysManagerTab identifies one independently refreshed system-manager tab.
type SysManagerTab string

const (
	SysManagerOverview  SysManagerTab = "overview"
	SysManagerProcesses SysManagerTab = "processes"
	SysManagerPorts     SysManagerTab = "ports"
	SysManagerServices  SysManagerTab = "services"
)

// SysManagerTabs lists the core tabs in the original's display order.
var SysManagerTabs = []SysManagerTab{
	SysManagerOverview, SysManagerProcesses, SysManagerPorts, SysManagerServices,
}

// SysManagerIntervalChoices are the refresh cadences offered for every tab.
// They match the monitoring choices so a user learns one set of values.
var SysManagerIntervalChoices = []int{1, 2, 3, 5, 10, 30, 60}

// The original refreshed overview, processes, and ports every three seconds
// and services every five.
const (
	DefaultSysManagerOverviewInterval  = 3
	DefaultSysManagerProcessesInterval = 3
	DefaultSysManagerPortsInterval     = 3
	DefaultSysManagerServicesInterval  = 5
)

// SysManager is the persisted system-manager configuration. Runtime samples
// and the selected Host never live here.
type SysManager struct {
	OverviewInterval  int `json:"overviewInterval"`
	ProcessesInterval int `json:"processesInterval"`
	PortsInterval     int `json:"portsInterval"`
	ServicesInterval  int `json:"servicesInterval"`
}

// DefaultSysManager returns the original's initial refresh cadences.
func DefaultSysManager() SysManager {
	return SysManager{
		OverviewInterval:  DefaultSysManagerOverviewInterval,
		ProcessesInterval: DefaultSysManagerProcessesInterval,
		PortsInterval:     DefaultSysManagerPortsInterval,
		ServicesInterval:  DefaultSysManagerServicesInterval,
	}
}

// NormalizeSysManager repairs an interval that is not one of the offered
// choices, falling back to the tab's default.
func NormalizeSysManager(manager SysManager) SysManager {
	manager.OverviewInterval = normalizeSysManagerInterval(
		manager.OverviewInterval, DefaultSysManagerOverviewInterval)
	manager.ProcessesInterval = normalizeSysManagerInterval(
		manager.ProcessesInterval, DefaultSysManagerProcessesInterval)
	manager.PortsInterval = normalizeSysManagerInterval(
		manager.PortsInterval, DefaultSysManagerPortsInterval)
	manager.ServicesInterval = normalizeSysManagerInterval(
		manager.ServicesInterval, DefaultSysManagerServicesInterval)
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
	}
	return NormalizeSysManager(m)
}

func normalizeSysManagerInterval(value, fallback int) int {
	for _, choice := range SysManagerIntervalChoices {
		if value == choice {
			return value
		}
	}
	return fallback
}
