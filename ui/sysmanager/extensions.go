package sysmanager

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// Extensions is the host-owned Extensions tab port. The coordinator owns
// section enumeration, sampling, caching, and action dispatch; the surface
// only renders the descriptors it returns.
type Extensions interface {
	// PanelSections returns the sections for one visible surface and Host.
	// The side surface keeps compact sections only.
	PanelSections(surface domainplugin.PanelSurface, hostID string) []domainplugin.PanelSectionView
	// ActivatePanel marks the surface visible on hostID. The side panel calls
	// it once with the Host captured when the tool opened.
	ActivatePanel(surface domainplugin.PanelSurface, hostID string)
	// DeactivatePanel stops sampling for the surface within one tick.
	DeactivatePanel(surface domainplugin.PanelSurface)
	// RefreshPanel requests an immediate host refresh of every section.
	RefreshPanel(surface domainplugin.PanelSurface, hostID string)
	// RunPanelAction dispatches one declared action to its plugin.
	RunPanelAction(ctx context.Context, request domainplugin.PanelActionRequest) error
	// Subscribe receives a redraw notification when a payload or state
	// changes.
	Subscribe(func()) func()
}

// WithExtensions attaches the host-owned Extensions coordinator.
func WithExtensions(extensions Extensions) Option {
	return func(s *State) { s.extensions = extensions }
}

// Surface returns the System surface the state drives.
func (s *State) Surface() domainplugin.PanelSurface { return s.surface }

// syncExtensions starts sampling only while the Extensions tab is the active
// tab of a visible surface. Leaving the tab or hiding the surface stops it.
func (s *State) syncExtensions() {
	if s.extensions == nil {
		return
	}
	if !s.active || s.tab != appsysmanager.TabExtensions {
		s.extensions.DeactivatePanel(s.surface)
		return
	}
	host, ok := s.CurrentHost()
	if !ok {
		s.extensions.DeactivatePanel(s.surface)
		return
	}
	s.extensions.ActivatePanel(s.surface, string(host.ID))
}

// PanelSections returns the sections for the visible surface and current
// Host.
func (s *State) PanelSections() []domainplugin.PanelSectionView {
	if s.extensions == nil {
		return nil
	}
	host, ok := s.CurrentHost()
	if !ok {
		return nil
	}
	return s.extensions.PanelSections(s.surface, string(host.ID))
}

// RequestPanelAction prepares or runs a plugin action. A destructive action
// waits for the host confirmation; a non-destructive one runs immediately.
// The confirmation names the action, the target, and the plugin.
func (s *State) RequestPanelAction(request domainplugin.PanelActionRequest,
	label, target string, destructive bool) error {
	if s.actionBusy {
		return errors.New("sysmanager: an action is already running")
	}
	host, ok := s.CurrentHost()
	if !ok {
		s.problem = ProblemNoHost
		s.requestRedraw()
		return errors.New("sysmanager: no Host selected")
	}
	if s.extensions == nil {
		s.problem = ProblemInternal
		s.requestRedraw()
		return errors.New("sysmanager: the Extensions tab is unavailable")
	}
	request.HostID = string(host.ID)
	request.Surface = s.surface
	if request.Plugin.Name == "" {
		request.Plugin.Name = request.Plugin.ID
	}
	if destructive {
		s.pending = &PendingAction{
			Kind:             appsysmanager.ActionPanel,
			Host:             host,
			Panel:            &request,
			PanelActionLabel: label,
			PanelTarget:      target,
		}
		s.actionDone = false
		s.requestRedraw()
		return nil
	}
	s.startPanelAction(request, label)
	return nil
}

func (s *State) startPanelAction(request domainplugin.PanelActionRequest, label string) {
	s.actionBusy = true
	s.actionDone = false
	s.actionErr = nil
	s.panelActionLabel = label
	s.actionResult = appsysmanager.ActionResult{Kind: appsysmanager.ActionPanel}
	ctx, cancel := context.WithCancel(context.Background())
	s.actionCancel = cancel
	extensions := s.extensions
	go func() {
		err := extensions.RunPanelAction(ctx, request)
		cancel()
		s.mu.Lock()
		if !s.closed {
			s.outcomes = append(s.outcomes, actionOutcome{
				result: appsysmanager.ActionResult{Kind: appsysmanager.ActionPanel},
				err:    err, label: label,
			})
		}
		s.mu.Unlock()
		s.requestRedraw()
	}()
	s.requestRedraw()
}

// extensionStateText localizes a host-owned section state.
func extensionStateText(m locales.Messages, state domainplugin.PanelState) string {
	switch state {
	case domainplugin.PanelStateLive:
		return m.SysManagerStatusLive()
	case domainplugin.PanelStateEmpty:
		return m.SysManagerExtensionsEmptySection()
	case domainplugin.PanelStateLoading:
		return m.SysManagerExtensionsLoading()
	case domainplugin.PanelStateStale:
		return m.SysManagerExtensionsStale()
	case domainplugin.PanelStatePermissionDeny:
		return m.SysManagerExtensionsPermissionDenied()
	case domainplugin.PanelStateError:
		return m.SysManagerExtensionsError()
	case domainplugin.PanelStateUnavailable:
		return m.SysManagerExtensionsUnavailable()
	default:
		return m.SysManagerExtensionsLoading()
	}
}

// extensionStateColor maps a section state to the theme.
func extensionStateColor(t *ui.Theme, state domainplugin.PanelState) ui.Color {
	switch state {
	case domainplugin.PanelStateError, domainplugin.PanelStatePermissionDeny:
		return uitheme.DangerText(t)
	case domainplugin.PanelStateStale, domainplugin.PanelStateUnavailable:
		return uitheme.WarningText(t)
	case domainplugin.PanelStateLive:
		return t.Text
	default:
		return t.TextMuted
	}
}

func extensionsContent(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	sections := s.PanelSections()
	if len(sections) == 0 {
		emptyState(c, m.SysManagerExtensionsEmpty(), t)
		return
	}
	ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
		for _, section := range sections {
			extensionSection(c, s, section, m, t)
		}
	})
}

func extensionSection(c *ui.Context, s *State, view domainplugin.PanelSectionView,
	m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().Padding(10).Gap(6).
		Background(t.Surface).Radius(t.Radius).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, view.Section.Title).FontSize(13).Bold().Grow(1).SingleLine()
			if age := extensionRefreshAge(s.now(), view.SampledAt, m); age != "" {
				ui.Text(c, age).FontSize(10).TextColor(t.TextMuted).NoWrap()
			}
			ui.Text(c, extensionStateText(m, view.State)).
				FontSize(11).TextColor(extensionStateColor(t, view.State)).NoWrap()
		})
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, m.SysManagerExtensionsByPlugin(pluginDisplayName(view.Plugin))).
				FontSize(10).TextColor(t.TextMuted).SingleLine().Grow(1)
			if view.Rejected > 0 {
				ui.Text(c, m.SysManagerExtensionsRejected(strconv.Itoa(view.Rejected))).
					FontSize(10).TextColor(uitheme.WarningText(t)).NoWrap()
			}
		})
		if view.Payload == nil {
			return
		}
		for _, item := range view.Payload.Items {
			extensionDescriptor(c, s, view, item, m, t)
		}
	})
}

func pluginDisplayName(plugin domainplugin.PanelPlugin) string {
	if name := strings.TrimSpace(plugin.Name); name != "" {
		return name
	}
	return plugin.ID
}

func extensionDescriptor(c *ui.Context, s *State, view domainplugin.PanelSectionView,
	item domainplugin.PanelDescriptor, m locales.Messages, t *ui.Theme) {
	switch item.Kind {
	case domainplugin.PanelDescriptorMetric:
		extensionMetric(c, item, t)
	case domainplugin.PanelDescriptorSection:
		ui.Text(c, item.Label).FontSize(12).Bold().SingleLine()
		for _, row := range item.Rows {
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				ui.Text(c, row.Label).FontSize(11).TextColor(t.TextMuted).NoWrap()
				ui.Column(c).Grow(1)
				ui.Text(c, row.Value).FontSize(11).NoWrap()
			})
		}
	case domainplugin.PanelDescriptorTable:
		extensionTable(c, s, view, item, m, t)
	case domainplugin.PanelDescriptorStatus:
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(7, 7).Radius(4).
				Background(levelColor(t, item.Level)).Shrink(0)
			ui.Text(c, item.Label).FontSize(11).NoWrap()
			if item.Detail != "" {
				ui.Text(c, item.Detail).FontSize(11).TextColor(t.TextMuted).
					SingleLine().Grow(1)
			}
		})
	case domainplugin.PanelDescriptorAction:
		// A row-bound action is rendered once, inside its table row. A
		// section-level action (no row key) stands on its own.
		if item.RowKey == "" {
			extensionAction(c, s, view, item, m, t)
		}
	case domainplugin.PanelDescriptorTimeseries:
		extensionTimeseries(c, item, m, t)
	}
}

func extensionMetric(c *ui.Context, item domainplugin.PanelDescriptor, t *ui.Theme) {
	value := item.Value
	if item.Unit != "" {
		value += " " + item.Unit
	}
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		if item.Level != "" {
			ui.Box(c).Size(7, 7).Radius(4).
				Background(levelColor(t, item.Level)).Shrink(0)
		}
		ui.Text(c, item.Label).FontSize(11).TextColor(t.TextMuted).NoWrap().Grow(1)
		ui.Text(c, value).FontSize(11).NoWrap()
	})
}

func levelColor(t *ui.Theme, level domainplugin.PanelStatusLevel) ui.Color {
	switch level {
	case domainplugin.PanelLevelWarning:
		return uitheme.WarningText(t)
	case domainplugin.PanelLevelError:
		return uitheme.DangerText(t)
	case domainplugin.PanelLevelOK:
		return t.Success
	default:
		return t.TextMuted
	}
}

func extensionTable(c *ui.Context, s *State, view domainplugin.PanelSectionView,
	item domainplugin.PanelDescriptor, m locales.Messages, t *ui.Theme) {
	columns := item.Columns
	if s.compact && len(columns) > compactPanelColumns {
		columns = columns[:compactPanelColumns]
	}
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		for _, column := range columns {
			ui.Text(c, column).FontSize(10).Bold().TextColor(t.TextMuted).
				SingleLine().Grow(1)
		}
	})
	for _, row := range item.TableRows {
		cells := row.Cells
		if len(cells) > len(columns) {
			cells = cells[:len(columns)]
		}
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			for _, cell := range cells {
				ui.Text(c, cell).FontSize(11).SingleLine().Grow(1)
			}
		})
		// Actions declared on this row render inside the row's own section.
		for _, descriptor := range view.Payload.Items {
			if descriptor.Kind != domainplugin.PanelDescriptorAction ||
				descriptor.RowKey != row.Key {
				continue
			}
			extensionAction(c, s, view, descriptor, m, t)
		}
	}
}

// compactPanelColumns limits the side panel's plugin table to the columns its
// width holds; the System manager page shows every declared column.
const compactPanelColumns = 3

func extensionAction(c *ui.Context, s *State, view domainplugin.PanelSectionView,
	item domainplugin.PanelDescriptor, m locales.Messages, t *ui.Theme) {
	action, ok := panelActionDefinition(view.Section, item.Action)
	if !ok {
		return
	}
	label := strings.TrimSpace(action.Label)
	if label == "" {
		label = action.ID
	}
	key := extensionActionKey(view.Plugin, view.Section.ID, action.ID, item.RowKey)
	button := ui.Button(c, label).Key(key)
	if item.Enabled != nil && !*item.Enabled {
		button.Disabled(true)
	}
	if !button.Clicked() {
		return
	}
	request := domainplugin.PanelActionRequest{
		Plugin:    view.Plugin,
		SectionID: view.Section.ID,
		ActionID:  action.ID,
		RowKey:    item.RowKey,
	}
	target := s.panelActionTarget(view, item.RowKey)
	request.Confirmed = !action.Destructive
	if err := s.RequestPanelAction(request, label, target, action.Destructive); err != nil {
		s.problem = ProblemActionData
	}
	c.Invalidate()
}

// extensionActionKey identifies one action button. The plugin source and
// development flag are part of the key so an installed copy and a development
// copy of the same plugin ID and name never share a button identity.
func extensionActionKey(plugin domainplugin.PanelPlugin, sectionID, actionID, rowKey string) string {
	mode := "installed"
	if plugin.Development {
		mode = "development"
	}
	return "sysmanager-panel-action:" + string(plugin.Source) + ":" + mode + ":" +
		plugin.ID + ":" + plugin.Name + ":" + sectionID + ":" + actionID + ":" + rowKey
}

func panelActionDefinition(section domainplugin.PanelSection, id string) (domainplugin.PanelAction, bool) {
	for _, action := range section.Actions {
		if action.ID == id {
			return action, true
		}
	}
	return domainplugin.PanelAction{}, false
}

// panelActionTarget names where an action will land: the Host, the section,
// and the row key when the action belongs to a table row.
func (s *State) panelActionTarget(view domainplugin.PanelSectionView, rowKey string) string {
	host := ""
	if current, ok := s.CurrentHost(); ok {
		host = s.HostLabel(current)
	}
	parts := make([]string, 0, 3)
	if host != "" {
		parts = append(parts, host)
	}
	parts = append(parts, view.Section.Title)
	if rowKey != "" {
		parts = append(parts, rowKey)
	}
	return strings.Join(parts, " · ")
}

func extensionTimeseries(c *ui.Context, item domainplugin.PanelDescriptor,
	m locales.Messages, t *ui.Theme) {
	last := "—"
	if len(item.Points) > 0 {
		last = formatPanelNumber(item.Points[len(item.Points)-1])
	}
	label := item.Label
	if label == "" {
		label = m.SysManagerExtensionsSeries()
	}
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).Grow(1).SingleLine()
		ui.Text(c, last).FontSize(11).NoWrap()
	})
	if len(item.Points) < 2 {
		return
	}
	scale := item.Scale
	if scale <= 0 {
		for _, point := range item.Points {
			if point > scale {
				scale = point
			}
		}
	}
	if scale <= 0 {
		return
	}
	points := append([]float64(nil), item.Points...)
	ui.Box(c).FillWidth().Height(28).DrawOver(func(p *ui.Painter, r ui.Rect) {
		width := r.W / float32(len(points))
		if width < 1 {
			width = 1
		}
		for index, point := range points {
			value := point / scale
			if math.IsNaN(value) || value < 0 {
				value = 0
			}
			if value > 1 {
				value = 1
			}
			height := r.H * float32(value)
			p.Fill(ui.Rect{
				X: r.X + float32(index)*width,
				Y: r.Y + r.H - height,
				W: max(width-1, 1),
				H: height,
			}, t.Accent, 0)
		}
	})
}

func formatPanelNumber(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "—"
	}
	if value == math.Trunc(value) && math.Abs(value) < 1e9 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', 2, 64)
}

// extensionRefreshAge is the visible "updated N seconds ago" text for one
// section when the view chooses to show it.
func extensionRefreshAge(now, sampledAt time.Time, m locales.Messages) string {
	if sampledAt.IsZero() {
		return ""
	}
	age := now.Sub(sampledAt)
	if age < time.Second {
		age = 0
	}
	return m.SysManagerUpdatedSeconds(int(age / time.Second))
}
