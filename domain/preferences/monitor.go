package preferences

import (
	"strconv"
	"strings"
)

// MonitorIntervalChoices are the refresh cadences the original offered, in
// seconds. The default is five seconds.
var MonitorIntervalChoices = []int{1, 2, 3, 5, 10, 30, 60}

// DefaultMonitorInterval is the original's initial refresh cadence.
const DefaultMonitorInterval = 5

// MonitorRangeChoices are the trend windows the original offered, in minutes.
var MonitorRangeChoices = []int{5, 15, 30}

// DefaultMonitorRange is the original's initial trend window.
const DefaultMonitorRange = 30

// MonitorView is the panel's presentation: a compact list or metric cards
// with trends.
type MonitorView string

const (
	MonitorViewList   MonitorView = "list"
	MonitorViewCharts MonitorView = "charts"
)

// MonitorSort is the order a panel lists its Hosts in. Manual preserves the
// saved order; name sorts by Host label.
type MonitorSort string

const (
	MonitorSortManual MonitorSort = "manual"
	MonitorSortName   MonitorSort = "name"
)

// MonitorPanel is one portable panel: a named set of Hosts and its display
// preferences. Runtime samples never live here.
type MonitorPanel struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	HostIDs     []string    `json:"hostIds"`
	View        MonitorView `json:"view"`
	Interval    int         `json:"interval"`
	Range       int         `json:"range"`
	Sort        MonitorSort `json:"sort"`
}

// Monitor is the persisted monitor configuration. Column layout is shared by
// every panel, matching the original's global table layout state.
type Monitor struct {
	Panels        []MonitorPanel `json:"panels,omitempty"`
	HiddenColumns []string       `json:"hiddenColumns,omitempty"`
	ColumnWidths  map[string]int `json:"columnWidths,omitempty"`
}

// MonitorColumnKeys lists every table column in the original's order.
var MonitorColumnKeys = []string{
	"host", "latency", "tags", "notes", "cpu", "memory", "disks",
	"network", "updated", "status", "terminal",
}

// MonitorColumnDefaults are the original's initial widths in device-
// independent pixels.
var MonitorColumnDefaults = map[string]int{
	"host": 220, "latency": 90, "tags": 150, "notes": 180, "cpu": 120,
	"memory": 120, "disks": 240, "network": 150, "updated": 110,
	"status": 100, "terminal": 56,
}

// monitorOptionalColumns are the only columns a user may hide. Host and
// terminal are structural.
var monitorOptionalColumns = map[string]bool{
	"latency": true, "tags": true, "notes": true, "cpu": true,
	"memory": true, "disks": true, "network": true, "updated": true,
	"status": true,
}

// DefaultMonitor returns the initial monitor configuration.
func DefaultMonitor() Monitor {
	return Monitor{ColumnWidths: cloneMonitorWidths(MonitorColumnDefaults)}
}

// NormalizeMonitor repairs persisted panel and column configuration. Invalid
// ids, empty names, duplicate panel names, and malformed intervals fall back
// to safe values instead of leaving the surface unusable.
func NormalizeMonitor(monitor Monitor) Monitor {
	normalized := Monitor{
		Panels:        normalizeMonitorPanels(monitor.Panels),
		HiddenColumns: normalizeMonitorHiddenColumns(monitor.HiddenColumns),
		ColumnWidths:  normalizeMonitorWidths(monitor.ColumnWidths),
	}
	return normalized
}

func normalizeMonitorPanels(panels []MonitorPanel) []MonitorPanel {
	if len(panels) == 0 {
		return nil
	}
	ids := make(map[string]bool, len(panels))
	out := make([]MonitorPanel, 0, len(panels))
	for _, panel := range panels {
		id := strings.TrimSpace(panel.ID)
		name := strings.TrimSpace(panel.Name)
		if id == "" || name == "" || ids[id] {
			continue
		}
		ids[id] = true
		out = append(out, MonitorPanel{
			ID:          id,
			Name:        truncateRunes(name, 120),
			Description: truncateRunes(strings.TrimSpace(panel.Description), 1000),
			HostIDs:     uniqueStrings(panel.HostIDs),
			View:        normalizeMonitorView(panel.View),
			Interval:    normalizeMonitorInterval(panel.Interval),
			Range:       normalizeMonitorRange(panel.Range),
			Sort:        normalizeMonitorSort(panel.Sort),
		})
	}
	return ensureUniquePanelNames(out)
}

// ensureUniquePanelNames suffixes duplicate panel names with a number so a
// merge from another device cannot make two panels indistinguishable.
func ensureUniquePanelNames(panels []MonitorPanel) []MonitorPanel {
	reserved := make(map[string]bool, len(panels))
	for _, panel := range panels {
		reserved[strings.ToLower(panel.Name)] = true
	}
	seen := make(map[string]bool, len(panels))
	for index := range panels {
		key := strings.ToLower(panels[index].Name)
		if !seen[key] {
			seen[key] = true
			continue
		}
		base := panels[index].Name
		for suffix := 2; ; suffix++ {
			candidate := base + " (" + strconv.Itoa(suffix) + ")"
			candidate = truncateRunes(candidate, 120)
			if !reserved[strings.ToLower(candidate)] {
				reserved[strings.ToLower(candidate)] = true
				seen[strings.ToLower(candidate)] = true
				panels[index].Name = candidate
				break
			}
		}
	}
	return panels
}

func normalizeMonitorView(view MonitorView) MonitorView {
	if view == MonitorViewCharts {
		return MonitorViewCharts
	}
	return MonitorViewList
}

func normalizeMonitorSort(sort MonitorSort) MonitorSort {
	if sort == MonitorSortName {
		return MonitorSortName
	}
	return MonitorSortManual
}

func normalizeMonitorInterval(value int) int {
	for _, choice := range MonitorIntervalChoices {
		if value == choice {
			return value
		}
	}
	return DefaultMonitorInterval
}

func normalizeMonitorRange(value int) int {
	for _, choice := range MonitorRangeChoices {
		if value == choice {
			return value
		}
	}
	return DefaultMonitorRange
}

func normalizeMonitorHiddenColumns(hidden []string) []string {
	if len(hidden) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(hidden))
	var out []string
	for _, key := range hidden {
		if monitorOptionalColumns[key] && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

func normalizeMonitorWidths(widths map[string]int) map[string]int {
	out := cloneMonitorWidths(MonitorColumnDefaults)
	for key, value := range widths {
		if _, ok := MonitorColumnDefaults[key]; !ok {
			continue
		}
		out[key] = clampMonitorWidth(key, value)
	}
	return out
}

func clampMonitorWidth(key string, width int) int {
	fallback := MonitorColumnDefaults[key]
	if width <= 0 {
		return fallback
	}
	minimum := 56
	maximum := 640
	if value, ok := monitorColumnMinimum[key]; ok {
		minimum = value
	}
	if key == "terminal" {
		maximum = 200
	}
	if width < minimum {
		return minimum
	}
	if width > maximum {
		return maximum
	}
	return width
}

var monitorColumnMinimum = map[string]int{
	"host": 160, "latency": 80, "tags": 90, "notes": 110, "cpu": 120,
	"memory": 120, "disks": 220, "network": 140, "updated": 95,
	"status": 90, "terminal": 56,
}

func cloneMonitorWidths(widths map[string]int) map[string]int {
	out := make(map[string]int, len(widths))
	for key, value := range widths {
		out[key] = value
	}
	return out
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
