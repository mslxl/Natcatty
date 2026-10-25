package sysmanager

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

// View returns the System manager surface.
func View(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		s.Flush()
		m := locales.In(c.Locale())
		t := c.Theme()

		header(c, s, m, t)
		if len(s.Hosts()) == 0 {
			emptyState(c, m.SysManagerNoHosts(), t)
			return
		}
		if _, ok := s.CurrentHost(); !ok {
			emptyState(c, m.SysManagerNoHostSelected(), t)
			return
		}
		tabs(c, s, m)
		tabContent(c, s, m, t)
		confirmation(c, s, m)
	}
}

func header(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	state := s.TabState(s.Tab())
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, m.SysManagerTitle()).FontSize(20).Bold()
		ui.Column(c).Grow(1)
		text, color := statusText(m, state, t)
		ui.Text(c, text).FontSize(12).TextColor(color).NoWrap()
	})

	if len(s.Hosts()) == 0 {
		return
	}
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		labels, byLabel := hostOptions(s)
		selected := ""
		if host, ok := s.CurrentHost(); ok {
			selected = s.HostLabel(host)
			if _, duplicate := byLabel[selected]; duplicate {
				selected = selected + " — " + host.Hostname
			}
		}
		chosen := selected
		ui.Select(c, &chosen, labels).Grow(1).MinWidth(120).Label(m.SysManagerHost())
		if chosen != selected {
			if id, ok := byLabel[chosen]; ok {
				s.SetHost(id)
				c.Invalidate()
			}
		}
	})

	ui.Row(c).FillWidth().Gap(8).Children(func() {
		choices := make([]string, 0, len(domainpreferences.SysManagerIntervalChoices))
		for _, seconds := range domainpreferences.SysManagerIntervalChoices {
			choices = append(choices, m.SysManagerSeconds(seconds))
		}
		current := m.SysManagerSeconds(s.Interval())
		chosen := current
		ui.Select(c, &chosen, choices).Width(84).Label(m.SysManagerRefresh())
		if chosen != current {
			for index, label := range choices {
				if label == chosen {
					s.SetInterval(domainpreferences.SysManagerIntervalChoices[index])
					break
				}
			}
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerRefreshNow()).Key("sysmanager-refresh").Clicked() {
			s.Refresh()
			c.Invalidate()
		}
		ui.Column(c).Grow(1)
		if state.Status == appsysmanager.StatusLive && !state.At.IsZero() {
			age := s.now().Sub(state.At)
			if age < 2*time.Second {
				age = 0
			}
			ui.Text(c, m.SysManagerUpdatedSeconds(int(age/time.Second))).
				FontSize(11).TextColor(t.TextMuted).NoWrap()
			if state.Latency > 0 {
				ui.Text(c, m.SysManagerLatency()+" "+m.SysManagerLatencyValue(int(state.Latency/time.Millisecond))).
					FontSize(11).TextColor(t.TextMuted).NoWrap()
			}
		}
	})

	if problem := problemText(m, s.Problem()); problem != "" {
		ui.Text(c, problem).FontSize(12).TextColor(t.Danger)
	}
	if notice := actionNotice(s, m); notice != "" {
		ui.Text(c, notice).FontSize(12).TextColor(actionNoticeColor(s, t))
	}
}

func hostOptions(s *State) ([]string, map[string]string) {
	labels := make([]string, 0, len(s.Hosts()))
	byLabel := make(map[string]string, len(s.Hosts()))
	seen := make(map[string]int, len(s.Hosts()))
	for _, host := range s.Hosts() {
		label := s.HostLabel(host)
		seen[label]++
	}
	for _, host := range s.Hosts() {
		label := s.HostLabel(host)
		if seen[label] > 1 {
			label = label + " — " + host.Hostname
		}
		labels = append(labels, label)
		byLabel[label] = string(host.ID)
	}
	return labels, byLabel
}

func tabs(c *ui.Context, s *State, m locales.Messages) {
	index := s.TabIndex()
	chosen := index
	ui.Tabs(c, &chosen,
		m.SysManagerTabOverview(),
		m.SysManagerTabProcesses(),
		m.SysManagerTabPorts(),
		m.SysManagerTabServices(),
	).FillWidth()
	if chosen != index && chosen >= 0 && chosen < len(appsysmanager.Tabs) {
		s.SetTab(appsysmanager.Tabs[chosen])
		c.Invalidate()
	}
}

func tabContent(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	state := s.TabState(s.Tab())
	switch s.Tab() {
	case appsysmanager.TabOverview:
		if state.Status != appsysmanager.StatusLive || state.Overview.Hostname == "" {
			statusPanel(c, s, state, m, t)
			return
		}
		overview(c, state.Overview, m, t)
	case appsysmanager.TabProcesses:
		if state.Status != appsysmanager.StatusLive {
			statusPanel(c, s, state, m, t)
			return
		}
		toolbar(c, s, m, t, m.SysManagerSearchProcesses(),
			m.SysManagerProcessesCount(len(s.DisplayProcesses())))
		rows := s.DisplayProcesses()
		if len(rows) == 0 {
			emptyState(c, emptyText(len(state.Processes), m.SysManagerNoProcesses(), m.SysManagerNoMatches()), t)
			return
		}
		processTable(c, s, rows, m)
		processActions(c, s, m, t)
	case appsysmanager.TabPorts:
		if state.Status != appsysmanager.StatusLive {
			statusPanel(c, s, state, m, t)
			return
		}
		toolbar(c, s, m, t, m.SysManagerSearchPorts(),
			m.SysManagerPortsCount(len(s.DisplayPorts())))
		rows := s.DisplayPorts()
		if len(rows) == 0 {
			emptyState(c, emptyText(len(state.Ports), m.SysManagerNoPorts(), m.SysManagerNoMatches()), t)
			return
		}
		portTable(c, s, rows, m)
		portActions(c, s, m, t)
	case appsysmanager.TabServices:
		if state.Status != appsysmanager.StatusLive {
			statusPanel(c, s, state, m, t)
			return
		}
		toolbar(c, s, m, t, m.SysManagerSearchServices(),
			m.SysManagerServicesCount(len(s.DisplayServices())))
		rows := s.DisplayServices()
		if len(rows) == 0 {
			emptyState(c, emptyText(len(state.Services), m.SysManagerNoServices(), m.SysManagerNoMatches()), t)
			return
		}
		serviceTable(c, s, rows, m, t)
		serviceActions(c, s, m, t)
	}
}

func toolbar(c *ui.Context, s *State, m locales.Messages, t *ui.Theme,
	placeholder, summary string) {
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		query := s.search
		ui.SearchField(c, &query).Placeholder(placeholder).Grow(1).MinWidth(120)
		if query != s.search {
			s.SetSearch(query)
			c.Invalidate()
		}
		ui.Text(c, summary).FontSize(11).TextColor(t.TextMuted).NoWrap()
	})
}

func emptyText(total int, none, noMatches string) string {
	if total == 0 {
		return none
	}
	return noMatches
}

func emptyState(c *ui.Context, message string, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Text(c, message).FontSize(12).TextColor(t.TextMuted)
	})
}

func statusPanel(c *ui.Context, s *State, state appsysmanager.TabState, m locales.Messages, t *ui.Theme) {
	text, color := statusText(m, state, t)
	ui.Column(c).Grow(1).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Gap(6).Children(func() {
		ui.Text(c, text).FontSize(14).Bold().TextColor(color)
		if reason := reasonText(m, state.Reason); reason != "" {
			ui.Text(c, reason).FontSize(12).TextColor(t.TextMuted)
		}
		if state.Status == appsysmanager.StatusIdle ||
			state.Status == appsysmanager.StatusConnecting {
			return
		}
		if ui.Button(c, m.SysManagerRefreshNow()).Key("sysmanager-retry").Clicked() {
			s.Refresh()
			c.Invalidate()
		}
	})
}

func processTable(c *ui.Context, s *State, rows []appsysmanager.Process, m locales.Messages) {
	columns := []ui.TableColumn{
		{Title: m.SysManagerPid(), ID: "pid", Width: 56, MinWidth: 44, Align: ui.End, Sortable: true},
		{Title: m.SysManagerUser(), ID: "user", Width: 76, MinWidth: 56, Sortable: true},
		{Title: m.SysManagerCPU(), ID: "cpu", Width: 76, MinWidth: 64, Align: ui.End, Sortable: true},
		{Title: m.SysManagerMemory(), ID: "mem", Width: 56, MinWidth: 48, Align: ui.End, Sortable: true},
		{Title: m.SysManagerCommand(), ID: "command", MinWidth: 120, Sortable: true},
	}
	ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
		if row < 0 || row >= len(rows) {
			return
		}
		process := rows[row]
		switch col {
		case 0:
			ui.Text(c, strconv.Itoa(process.PID)).FontSize(12)
		case 1:
			ui.Text(c, process.User).FontSize(12).SingleLine()
		case 2:
			ui.Text(c, percent(process.CPUPercent)).FontSize(12)
		case 3:
			ui.Text(c, percent(process.MemPercent)).FontSize(12)
		case 4:
			ui.Text(c, process.Command).FontSize(12).SingleLine()
		}
	}).Grow(1).FillWidth()
}

func portTable(c *ui.Context, s *State, rows []appsysmanager.Port, m locales.Messages) {
	columns := []ui.TableColumn{
		{Title: m.SysManagerPort(), ID: "port", Width: 58, MinWidth: 46, Align: ui.End},
		{Title: m.SysManagerProtocol(), ID: "protocol", Width: 92, MinWidth: 72},
		{Title: m.SysManagerAddress(), ID: "address", MinWidth: 80},
		{Title: m.SysManagerPid(), ID: "pid", Width: 58, MinWidth: 46, Align: ui.End},
		{Title: m.SysManagerProcess(), ID: "process", MinWidth: 80},
	}
	ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
		if row < 0 || row >= len(rows) {
			return
		}
		port := rows[row]
		switch col {
		case 0:
			ui.Text(c, strconv.Itoa(port.Port)).FontSize(12)
		case 1:
			ui.Text(c, port.Protocol).FontSize(12)
		case 2:
			ui.Text(c, port.Address).FontSize(12).SingleLine()
		case 3:
			ui.Text(c, pidText(port.PID)).FontSize(12)
		case 4:
			ui.Text(c, port.ProcessName).FontSize(12).SingleLine()
		}
	}).Grow(1).FillWidth()
}

func serviceTable(c *ui.Context, s *State, rows []appsysmanager.Unit, m locales.Messages, t *ui.Theme) {
	columns := []ui.TableColumn{
		{Title: m.SysManagerUnit(), ID: "unit", MinWidth: 140},
		{Title: m.SysManagerScope(), ID: "scope", Width: 64, MinWidth: 54},
		{Title: m.SysManagerActive(), ID: "active", Width: 70, MinWidth: 58},
		{Title: m.SysManagerDescription(), ID: "description", MinWidth: 100},
	}
	ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
		if row < 0 || row >= len(rows) {
			return
		}
		unit := rows[row]
		switch col {
		case 0:
			ui.Text(c, unit.Name).FontSize(12).SingleLine()
		case 1:
			ui.Text(c, scopeText(m, unit.Scope)).FontSize(12)
		case 2:
			// The selected row inherits the accent's high-contrast foreground,
			// so a state color from the normal background would be illegible.
			text := ui.Text(c, unit.ActiveState).FontSize(12).SingleLine()
			if s.selected != row {
				color := t.TextMuted
				switch unit.ActiveState {
				case "failed":
					color = t.Danger
				case "active":
					color = t.Text
				}
				text.TextColor(color)
			}
		case 3:
			text := ui.Text(c, unit.Description).FontSize(12).SingleLine()
			if s.selected != row {
				text.TextColor(t.TextMuted)
			}
		}
	}).Grow(1).FillWidth()
}

func processActions(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	process, ok := s.SelectedProcess()
	if !ok {
		ui.Text(c, m.SysManagerProcessHint()).FontSize(11).TextColor(t.TextMuted)
		return
	}
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, m.SysManagerSelectedProcess(process.PID, process.Command)).
			FontSize(11).TextColor(t.TextMuted).Grow(1).SingleLine()
		stopped := strings.Contains(strings.ToUpper(process.Stat), "T")
		if !stopped {
			if ui.Button(c, m.SysManagerStop()).Key("sysmanager-process-stop").Clicked() {
				request(s, s.RequestSignal(process.PID, "STOP"))
				c.Invalidate()
			}
		} else {
			if ui.Button(c, m.SysManagerContinue()).Key("sysmanager-process-continue").Clicked() {
				request(s, s.RequestSignal(process.PID, "CONT"))
				c.Invalidate()
			}
		}
		if ui.Button(c, m.SysManagerTerminate()).Key("sysmanager-process-terminate").Clicked() {
			request(s, s.RequestSignal(process.PID, "TERM"))
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerKill()).Key("sysmanager-process-kill").Clicked() {
			request(s, s.RequestSignal(process.PID, "KILL"))
			c.Invalidate()
		}
	})
}

func portActions(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	port, ok := s.SelectedPort()
	if !ok {
		ui.Text(c, m.SysManagerPortHint()).FontSize(11).TextColor(t.TextMuted)
		return
	}
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, m.SysManagerSelectedPort(port.Protocol, port.Address, port.Port, port.PID)).
			FontSize(11).TextColor(t.TextMuted).Grow(1).SingleLine()
		if port.PID > 0 {
			if ui.Button(c, m.SysManagerTerminate()).Key("sysmanager-port-terminate").Clicked() {
				request(s, s.RequestPortTerminate(port))
				c.Invalidate()
			}
		} else {
			ui.Text(c, m.SysManagerPortOwnerUnknown()).FontSize(11).TextColor(t.TextMuted)
		}
	})
}

func serviceActions(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	unit, ok := s.SelectedService()
	if !ok {
		ui.Text(c, m.SysManagerServiceHint()).FontSize(11).TextColor(t.TextMuted)
		return
	}
	scope := scopeText(m, unit.Scope)
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, m.SysManagerSelectedService(unit.Name, scope)).
			FontSize(11).TextColor(t.TextMuted).Grow(1).SingleLine()
		if ui.Button(c, m.SysManagerStart()).Key("sysmanager-service-start").Clicked() {
			request(s, s.RequestServiceAction(unit.Name, appsysmanager.ServiceStart,
				appsysmanager.Scope(unit.Scope)))
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerStop()).Key("sysmanager-service-stop").Clicked() {
			request(s, s.RequestServiceAction(unit.Name, appsysmanager.ServiceStop,
				appsysmanager.Scope(unit.Scope)))
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerRestart()).Key("sysmanager-service-restart").Clicked() {
			request(s, s.RequestServiceAction(unit.Name, appsysmanager.ServiceRestart,
				appsysmanager.Scope(unit.Scope)))
			c.Invalidate()
		}
	})
}

func request(s *State, err error) {
	if err != nil {
		s.problem = ProblemActionData
	}
}

func overview(c *ui.Context, data appsysmanager.Overview, m locales.Messages, t *ui.Theme) {
	ui.Scroll(c).Grow(1).FillWidth().Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Children(func() {
			systemCard(c, data, m, t)
			resourceCard(c, data, m, t)
		})
	})
}

func systemCard(c *ui.Context, data appsysmanager.Overview, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).MinWidth(150).Padding(10).Gap(5).Background(t.Surface).Radius(t.Radius).Children(func() {
		ui.Text(c, m.SysManagerOverviewSystem()).FontSize(13).Bold()
		field(c, m.SysManagerOverviewHostname(), blank(data.Hostname, m), t)
		field(c, m.SysManagerOverviewOS(), blank(data.OSName, m), t)
		field(c, m.SysManagerOverviewKernel(), blank(data.KernelRelease, m), t)
		field(c, m.SysManagerOverviewUptime(), uptimeText(data.UptimeSeconds, m), t)
		field(c, m.SysManagerOverviewLoad(), loadText(data.LoadAverage, m), t)
		field(c, m.SysManagerOverviewCoresLabel(), coresText(data.Cores, m), t)
	})
}

func resourceCard(c *ui.Context, data appsysmanager.Overview, m locales.Messages, t *ui.Theme) {
	memRatio := ratio(data.MemUsedMB, data.MemTotalMB)
	swapRatio := ratio(data.SwapUsedMB, data.SwapTotalMB)
	diskRatio := ratio(data.DiskUsedGB, data.DiskTotalGB)
	ui.Column(c).Grow(1).MinWidth(150).Padding(10).Gap(5).Background(t.Surface).Radius(t.Radius).Children(func() {
		ui.Text(c, m.SysManagerOverviewResources()).FontSize(13).Bold()
		metric(c, m.SysManagerOverviewCPU(), percentText(data.CPUPercent),
			ratio(data.CPUPercent, 100), t)
		metric(c, m.SysManagerOverviewMemory(),
			usedText(data.MemUsedMB, data.MemTotalMB, 1024, m), memRatio, t)
		metric(c, m.SysManagerOverviewSwap(),
			usedText(data.SwapUsedMB, data.SwapTotalMB, 1024, m), swapRatio, t)
		metric(c, m.SysManagerOverviewDisk(),
			usedText(data.DiskUsedGB, data.DiskTotalGB, 1, m), diskRatio, t)
	})
}

func metric(c *ui.Context, label, value string, ratio float64, t *ui.Theme) {
	ui.Column(c).FillWidth().Gap(2).Children(func() {
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, label).FontSize(11).TextColor(t.TextMuted)
			ui.Column(c).Grow(1)
			ui.Text(c, value).FontSize(11).NoWrap()
		})
		if ratio >= 0 {
			ui.Progress(c, ratio).Grow(1)
		}
	})
}

func field(c *ui.Context, label, value string, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).NoWrap()
		ui.Column(c).Grow(1)
		ui.Text(c, value).FontSize(11).NoWrap()
	})
}

func confirmation(c *ui.Context, s *State, m locales.Messages) {
	pending, ok := s.Pending()
	if !ok {
		return
	}
	title, message, confirm := confirmationText(pending, m)
	open := true
	choice := ui.AlertDialog(c, &open, title, message, m.SysManagerCancel(), confirm)
	switch {
	case choice == 1:
		s.ConfirmPending()
		c.Invalidate()
	case choice == 0 || choice == ui.AlertDismissed:
		s.CancelPending()
		c.Invalidate()
	}
}

func confirmationText(pending PendingAction, m locales.Messages) (string, string, string) {
	if pending.Kind == appsysmanager.ActionServiceAction {
		action := serviceActionText(pending.Action, m)
		return action, m.SysManagerConfirmService(action, pending.Unit, pending.Command), action
	}
	signal := pending.Signal
	title := m.SysManagerTerminate()
	switch signal {
	case "STOP":
		title = m.SysManagerStop()
	case "CONT":
		title = m.SysManagerContinue()
	case "KILL":
		title = m.SysManagerKill()
	}
	if pending.Origin == OriginPort {
		return title, m.SysManagerConfirmPort(pending.PID, pending.Address, pending.Port,
			pending.Protocol, pending.Command), title
	}
	return title, m.SysManagerConfirmSignal(signal, pending.PID, pending.Command), title
}

func serviceActionText(action appsysmanager.ServiceAction, m locales.Messages) string {
	switch action {
	case appsysmanager.ServiceStart:
		return m.SysManagerStart()
	case appsysmanager.ServiceStop:
		return m.SysManagerStop()
	case appsysmanager.ServiceRestart:
		return m.SysManagerRestart()
	default:
		return string(action)
	}
}

func statusText(m locales.Messages, state appsysmanager.TabState, t *ui.Theme) (string, ui.Color) {
	switch state.Status {
	case appsysmanager.StatusLive:
		return m.SysManagerStatusLive(), t.Text
	case appsysmanager.StatusConnecting:
		return m.SysManagerStatusConnecting(), t.TextMuted
	case appsysmanager.StatusLogin:
		return m.SysManagerStatusLogin(), t.Danger
	case appsysmanager.StatusError:
		return m.SysManagerStatusError(), t.Danger
	case appsysmanager.StatusUnsupported:
		return m.SysManagerStatusUnsupported(), t.TextMuted
	case appsysmanager.StatusUnavailable:
		return m.SysManagerStatusUnavailable(), t.Danger
	default:
		return m.SysManagerStatusIdle(), t.TextMuted
	}
}

func reasonText(m locales.Messages, reason appsysmanager.Reason) string {
	switch reason {
	case appsysmanager.ReasonHostKey:
		return m.SysManagerReasonHostKey()
	case appsysmanager.ReasonAuth:
		return m.SysManagerReasonAuth()
	case appsysmanager.ReasonNetwork:
		return m.SysManagerReasonNetwork()
	case appsysmanager.ReasonTimeout:
		return m.SysManagerReasonTimeout()
	case appsysmanager.ReasonOS:
		return m.SysManagerReasonOS()
	case appsysmanager.ReasonCancelled:
		return m.SysManagerReasonCancelled()
	case appsysmanager.ReasonInternal:
		return m.SysManagerReasonInternal()
	case appsysmanager.ReasonUnsupported:
		return m.SysManagerReasonUnsupported()
	case appsysmanager.ReasonPermission:
		return m.SysManagerReasonPermission()
	case appsysmanager.ReasonMissingProcesses:
		return m.SysManagerReasonMissingProcesses()
	case appsysmanager.ReasonMissingPorts:
		return m.SysManagerReasonMissingPorts()
	case appsysmanager.ReasonMissingSystemctl:
		return m.SysManagerReasonMissingSystemctl()
	default:
		return ""
	}
}

func problemText(m locales.Messages, problem Problem) string {
	switch problem {
	case ProblemLoadHosts:
		return m.SysManagerProblemLoadHosts()
	case ProblemSave:
		return m.SysManagerProblemSave()
	case ProblemStart:
		return m.SysManagerProblemStart()
	case ProblemNoHost:
		return m.SysManagerProblemNoHost()
	case ProblemActionData:
		return m.SysManagerProblemActionData()
	case ProblemInternal:
		return m.SysManagerProblemInternal()
	default:
		return ""
	}
}

func actionNotice(s *State, m locales.Messages) string {
	if s.ActionBusy() {
		result, _ := s.ActionResult()
		command := result.Command
		if command == "" {
			if pending, ok := s.Pending(); ok {
				command = pending.Command
			}
		}
		return m.SysManagerActionRunning(command)
	}
	if !s.ActionDone() {
		return ""
	}
	if err := s.ActionError(); err != nil {
		if errors.Is(err, context.Canceled) {
			return m.SysManagerActionCancelled()
		}
		var typed *appsysmanager.Error
		if errors.As(err, &typed) && typed.Reason == appsysmanager.ReasonPermission {
			return m.SysManagerActionPermission()
		}
		return m.SysManagerActionFailed()
	}
	return m.SysManagerActionSucceeded(s.ActionCommand())
}

func actionNoticeColor(s *State, t *ui.Theme) ui.Color {
	if s.ActionBusy() {
		return t.Warning
	}
	if s.ActionError() != nil && !errors.Is(s.ActionError(), context.Canceled) {
		return t.Danger
	}
	return t.TextMuted
}

func scopeText(m locales.Messages, scope string) string {
	switch scope {
	case "user":
		return m.SysManagerScopeUser()
	default:
		return m.SysManagerScopeSystem()
	}
}

func percent(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", value)
}

func percentText(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", value)
}

func ratio(value, total float64) float64 {
	if total <= 0 || math.IsNaN(value) || math.IsNaN(total) {
		return -1
	}
	result := value / total
	if result < 0 {
		return 0
	}
	if result > 1 {
		return 1
	}
	return result
}

func usedText(used, total, divisor float64, m locales.Messages) string {
	if total <= 0 {
		return "—"
	}
	return m.SysManagerOverviewUsedOf(sizeText(used, divisor), sizeText(total, divisor))
}

func sizeText(value, divisor float64) string {
	if divisor <= 0 {
		return "—"
	}
	value /= divisor
	switch {
	case value >= 1024:
		return fmt.Sprintf("%.1f TB", value/1024)
	case value >= 1:
		return fmt.Sprintf("%.1f GB", value)
	default:
		return fmt.Sprintf("%.0f MB", value*1024)
	}
}

func uptimeText(seconds float64, m locales.Messages) string {
	if seconds <= 0 || math.IsNaN(seconds) {
		return m.SysManagerUnknown()
	}
	total := int(seconds)
	days := total / 86400
	hours := (total % 86400) / 3600
	minutes := (total % 3600) / 60
	switch {
	case days > 0:
		return m.SysManagerDurationDays(days, hours)
	case hours > 0:
		return m.SysManagerDurationHours(hours, minutes)
	default:
		return m.SysManagerDurationMinutes(minutes)
	}
}

func loadText(load []float64, m locales.Messages) string {
	if len(load) == 0 {
		return m.SysManagerUnknown()
	}
	fields := make([]string, 0, len(load))
	for _, value := range load {
		if math.IsNaN(value) {
			continue
		}
		fields = append(fields, fmt.Sprintf("%.2f", value))
	}
	if len(fields) == 0 {
		return m.SysManagerUnknown()
	}
	return strings.Join(fields, " ")
}

func coresText(cores int, m locales.Messages) string {
	if cores <= 0 {
		return m.SysManagerUnknown()
	}
	return m.SysManagerOverviewCores(cores)
}

func pidText(pid int) string {
	if pid <= 0 {
		return "—"
	}
	return strconv.Itoa(pid)
}

func blank(value string, m locales.Messages) string {
	if strings.TrimSpace(value) == "" {
		return m.SysManagerUnknown()
	}
	return value
}
