package plugin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// The System panel contract is data-only. A plugin declares at most eight
// sections in its manifest and updates them with payloads built from the
// closed descriptor set below. The host keeps no preview of a native UI tree:
// there is no node, child, or document type in this vocabulary, and payload
// decoding rejects unknown fields so a plugin cannot smuggle one in.

// PanelRefreshClass is how the host refreshes one section.
type PanelRefreshClass string

const (
	// PanelRefreshPush is plugin-driven. The plugin calls update at up to ten
	// updates per second; the host never polls.
	PanelRefreshPush PanelRefreshClass = "push"
	// PanelRefreshFast is a section the plugin samples itself while the
	// Extensions tab is active. The floor is 500 ms and the default is 1 s.
	PanelRefreshFast PanelRefreshClass = "fast"
	// PanelRefreshSnapshot is backed by the host's built-in collectors and
	// follows their 1-3 s cadence.
	PanelRefreshSnapshot PanelRefreshClass = "snapshot"
)

// Valid reports whether the class belongs to the closed vocabulary.
func (r PanelRefreshClass) Valid() bool {
	switch r {
	case PanelRefreshPush, PanelRefreshFast, PanelRefreshSnapshot:
		return true
	}
	return false
}

// PanelDescriptorKind is the closed set of host-rendered descriptor types.
type PanelDescriptorKind string

const (
	PanelDescriptorMetric     PanelDescriptorKind = "metric"
	PanelDescriptorSection    PanelDescriptorKind = "section"
	PanelDescriptorTable      PanelDescriptorKind = "table"
	PanelDescriptorStatus     PanelDescriptorKind = "status"
	PanelDescriptorAction     PanelDescriptorKind = "action"
	PanelDescriptorTimeseries PanelDescriptorKind = "timeseries"
)

// PanelDescriptorKinds lists the closed descriptor vocabulary in display
// order.
var PanelDescriptorKinds = []PanelDescriptorKind{
	PanelDescriptorMetric, PanelDescriptorSection, PanelDescriptorTable,
	PanelDescriptorStatus, PanelDescriptorAction, PanelDescriptorTimeseries,
}

// Valid reports whether kind belongs to the closed descriptor set.
func (k PanelDescriptorKind) Valid() bool {
	for _, candidate := range PanelDescriptorKinds {
		if candidate == k {
			return true
		}
	}
	return false
}

// PanelStatusLevel is the closed status vocabulary shared by metric and
// status descriptors.
type PanelStatusLevel string

const (
	PanelLevelOK      PanelStatusLevel = "ok"
	PanelLevelWarning PanelStatusLevel = "warning"
	PanelLevelError   PanelStatusLevel = "error"
	PanelLevelUnknown PanelStatusLevel = "unknown"
)

// Valid reports whether level belongs to the closed status vocabulary.
func (l PanelStatusLevel) Valid() bool {
	switch l {
	case PanelLevelOK, PanelLevelWarning, PanelLevelError, PanelLevelUnknown:
		return true
	}
	return false
}

// Host-owned section states. The surface localizes each value.
type PanelState string

const (
	PanelStateLoading        PanelState = "loading"
	PanelStateLive           PanelState = "live"
	PanelStateEmpty          PanelState = "empty"
	PanelStateUnavailable    PanelState = "unavailable"
	PanelStatePermissionDeny PanelState = "permission-denied"
	PanelStateError          PanelState = "error"
	PanelStateStale          PanelState = "stale"
)

// PanelSurface names the host surface that shows the Extensions tab.
type PanelSurface string

const (
	PanelSurfaceSide    PanelSurface = "side"
	PanelSurfaceManager PanelSurface = "manager"
)

// Valid reports whether surface belongs to the closed surface vocabulary.
func (s PanelSurface) Valid() bool {
	return s == PanelSurfaceSide || s == PanelSurfaceManager
}

// PanelSection is one statically declared System-panel contribution. Runtime
// section registration does not exist in v1: the manifest is the only source.
type PanelSection struct {
	ID    string `toml:"id" json:"id"`
	Title string `toml:"title" json:"title"`
	// TitleKey names a catalog message resolved at display time in the host
	// locale. Title stays the literal fallback.
	TitleKey string            `toml:"title_key" json:"title_key,omitempty"`
	Order    int               `toml:"order" json:"order"`
	Refresh  PanelRefreshClass `toml:"refresh" json:"refresh"`
	// Compact marks a section the narrow side panel may show. The manager
	// shows every section.
	Compact bool `toml:"compact" json:"compact"`
	// Interval is the suggested refresh cadence in milliseconds from the
	// closed PanelIntervals vocabulary. Zero uses the payload-shape default.
	// The class floor always wins.
	Interval int `toml:"interval" json:"interval"`
	// MinInterval is the plugin's suggested minimum in milliseconds from the
	// same vocabulary. The host still enforces the class floor.
	MinInterval int           `toml:"min_interval" json:"min_interval"`
	Actions     []PanelAction `toml:"actions" json:"actions"`
}

// PanelAction is one action a plugin declares inside one of its own sections.
// Destructive actions always pass through a host confirmation; the plugin
// cannot attach an action to a built-in row or trigger a built-in action.
type PanelAction struct {
	ID    string `toml:"id" json:"id"`
	Label string `toml:"label" json:"label"`
	// LabelKey names a catalog message resolved at display time in the host
	// locale. Label stays the literal fallback.
	LabelKey    string `toml:"label_key" json:"label_key,omitempty"`
	Destructive bool   `toml:"destructive" json:"destructive"`
}

// PanelRow is one label/value row of a section descriptor.
type PanelRow struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// PanelTableRow is one table row. Key is the stable row identity the action
// callback receives; Cells has exactly one entry per column.
type PanelTableRow struct {
	Key   string   `json:"key"`
	Cells []string `json:"cells"`
}

// PanelDescriptor is one data-only descriptor. Only the fields belonging to
// Kind are meaningful, and every string is a plain-text label or value: there
// is no markup, colour, or drawing field.
type PanelDescriptor struct {
	Kind        PanelDescriptorKind `json:"kind"`
	Label       string              `json:"label,omitempty"`
	Value       string              `json:"value,omitempty"`
	Unit        string              `json:"unit,omitempty"`
	Level       PanelStatusLevel    `json:"level,omitempty"`
	Detail      string              `json:"detail,omitempty"`
	Collapsible bool                `json:"collapsible,omitempty"`
	Rows        []PanelRow          `json:"rows,omitempty"`
	Columns     []string            `json:"columns,omitempty"`
	TableRows   []PanelTableRow     `json:"table_rows,omitempty"`
	// Action references one ID declared in the owning PanelSection.Actions.
	Action string `json:"action,omitempty"`
	// RowKey is the table row a row action belongs to. Empty for a section
	// action.
	RowKey string `json:"row_key,omitempty"`
	// Enabled is the action's enabled state. A missing value means enabled;
	// the field is meaningless for other descriptor kinds.
	Enabled *bool `json:"enabled,omitempty"`
	// Points is a bounded timeseries. Scale, when positive, fixes the display
	// maximum; zero scales to the largest point.
	Points []float64 `json:"points,omitempty"`
	Scale  float64   `json:"scale,omitempty"`
}

// PanelPayload is one section's accepted update.
type PanelPayload struct {
	Items []PanelDescriptor `json:"items"`
}

// Clone returns a deep copy safe to hand to a UI reader.
func (p *PanelPayload) Clone() *PanelPayload {
	if p == nil {
		return nil
	}
	out := &PanelPayload{Items: make([]PanelDescriptor, len(p.Items))}
	for i, item := range p.Items {
		out.Items[i] = item
		out.Items[i].Rows = append([]PanelRow(nil), item.Rows...)
		out.Items[i].Columns = append([]string(nil), item.Columns...)
		out.Items[i].TableRows = make([]PanelTableRow, len(item.TableRows))
		for j, row := range item.TableRows {
			out.Items[i].TableRows[j] = PanelTableRow{
				Key:   row.Key,
				Cells: append([]string(nil), row.Cells...),
			}
		}
		out.Items[i].Points = append([]float64(nil), item.Points...)
	}
	return out
}

// PanelPlugin identifies the plugin behind a System panel section. Source and
// Development keep an installed and a development copy of one id distinct.
type PanelPlugin struct {
	ID          string
	Name        string
	Version     string
	Source      Source
	Development bool
}

// PanelSectionView is the host's read model for one registered section.
type PanelSectionView struct {
	Plugin    PanelPlugin
	Section   PanelSection
	State     PanelState
	Detail    string
	Payload   *PanelPayload
	SampledAt time.Time
	// Rejected is the number of over-limit or unsolicited updates the host
	// discarded for this section. It is a diagnostic counter, never content.
	Rejected int
}

// PanelActionRequest asks the host to dispatch one plugin action. Confirmed is
// set only after the host confirmation named the action, target, and plugin;
// the coordinator refuses a destructive action without it.
type PanelActionRequest struct {
	Plugin    PanelPlugin
	HostID    string
	Surface   PanelSurface
	SectionID string
	ActionID  string
	RowKey    string
	Confirmed bool
}

// PanelRefreshRequest is the host-initiated refresh context. The guest learns
// which Host and surface asked, so its payload is never applied to another
// Host.
type PanelRefreshRequest struct {
	Surface   PanelSurface `json:"surface"`
	HostID    string       `json:"host_id"`
	SectionID string       `json:"section_id"`
}

// PanelActionCommand is the reserved dispatch command for one action. Its
// JSON body is URL-safe base64, so a row key never has to fit a command
// grammar.
type PanelActionCommand struct {
	SectionID string `json:"section_id"`
	ActionID  string `json:"action_id"`
	RowKey    string `json:"row_key,omitempty"`
}

// Reserved command prefixes. A plugin command ID cannot contain a colon, so
// these strings never collide with a declared command.
const (
	panelRefreshCommandPrefix = "systempanel.refresh:"
	panelActionCommandPrefix  = "systempanel.action:"
)

// EncodePanelRefreshCommand returns the reserved refresh dispatch command.
func EncodePanelRefreshCommand(request PanelRefreshRequest) string {
	return panelRefreshCommandPrefix + encodePanelJSON(request)
}

// ParsePanelRefreshCommand decodes a reserved refresh command.
func ParsePanelRefreshCommand(command string) (PanelRefreshRequest, bool) {
	raw, ok := strings.CutPrefix(command, panelRefreshCommandPrefix)
	if !ok {
		return PanelRefreshRequest{}, false
	}
	var request PanelRefreshRequest
	if !decodePanelJSON(raw, &request) {
		return PanelRefreshRequest{}, false
	}
	if request.SectionID == "" || request.HostID == "" || !request.Surface.Valid() {
		return PanelRefreshRequest{}, false
	}
	return request, true
}

// EncodePanelActionCommand returns the reserved action dispatch command.
func EncodePanelActionCommand(command PanelActionCommand) string {
	return panelActionCommandPrefix + encodePanelJSON(command)
}

// ParsePanelActionCommand decodes a reserved action command.
func ParsePanelActionCommand(raw string) (PanelActionCommand, bool) {
	encoded, ok := strings.CutPrefix(raw, panelActionCommandPrefix)
	if !ok {
		return PanelActionCommand{}, false
	}
	var command PanelActionCommand
	if !decodePanelJSON(encoded, &command) || command.SectionID == "" || command.ActionID == "" {
		return PanelActionCommand{}, false
	}
	return command, true
}

func encodePanelJSON(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodePanelJSON(encoded string, target any) bool {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return ensureEOF(decoder) == nil
}

// PanelSnapshot is the Host snapshot service's reply. Collectors carry the
// normalized output of the built-in collectors keyed by collector name.
type PanelSnapshot struct {
	HostID     string                     `json:"host_id"`
	SampledAt  time.Time                  `json:"sampled_at"`
	Collectors map[string]json.RawMessage `json:"collectors"`
	// Skipped names collectors the Host could not run, with the typed reason
	// code. One skipped collector degrades its own section only.
	Skipped map[string]string `json:"skipped,omitempty"`
	// Stale marks a cached sample returned because the collector cadence has
	// not elapsed or the Extensions tab is inactive.
	Stale bool `json:"stale,omitempty"`
}

// PanelCollector is one built-in collector the snapshot service may expose.
type PanelCollector string

const (
	PanelCollectorOverview  PanelCollector = "overview"
	PanelCollectorProcesses PanelCollector = "processes"
	PanelCollectorPorts     PanelCollector = "ports"
	PanelCollectorServices  PanelCollector = "services"
	PanelCollectorTmux      PanelCollector = "tmux"
	PanelCollectorDocker    PanelCollector = "docker"
	PanelCollectorGPU       PanelCollector = "gpu"
)

// PanelCollectors lists the closed collector vocabulary in display order.
var PanelCollectors = []PanelCollector{
	PanelCollectorOverview, PanelCollectorProcesses, PanelCollectorPorts,
	PanelCollectorServices, PanelCollectorTmux, PanelCollectorDocker,
	PanelCollectorGPU,
}

// ValidPanelCollector reports whether name is a declared collector. The
// snapshot service runs only these fixed host-owned templates; a plugin cannot
// name an arbitrary command.
func ValidPanelCollector(name string) bool {
	for _, collector := range PanelCollectors {
		if string(collector) == name {
			return true
		}
	}
	return false
}

// Limits shared by the manifest validator and the host-service boundary.
const (
	// MaxPanelSections is the number of sections one plugin may declare.
	MaxPanelSections = 8
	// MaxPanelActions is the number of actions one section may declare.
	MaxPanelActions = 16
	// MaxPanelItems is the number of descriptors in one payload.
	MaxPanelItems = 64
	// MaxPanelRows is the number of rows in one section or table descriptor.
	MaxPanelRows = 64
	// MaxPanelColumns is the number of columns in one table.
	MaxPanelColumns = 8
	// MaxPanelPoints is the number of points in one timeseries.
	MaxPanelPoints = 300
	// MaxPanelStringBytes bounds every label, value, cell, key, and detail.
	MaxPanelStringBytes = 2 << 10
	// MaxPanelPushBytes caps one push update.
	MaxPanelPushBytes = 4 << 10
	// MaxPanelPayloadBytes caps one fast or snapshot refresh payload.
	MaxPanelPayloadBytes = 256 << 10
	// MaxPanelOrder bounds the sort hint so a malicious manifest cannot use
	// extreme values to disturb the host's ordering.
	MaxPanelOrder = 1 << 20
)

// PanelIntervals is the closed set of user-selectable refresh cadences, in
// milliseconds. A section's Interval and MinInterval must be zero or one of
// these values; the host enforces its class floor on top.
var PanelIntervals = []int{250, 500, 1000, 2000, 3000, 5000, 10000, 30000, 60000}

// ValidPanelInterval reports whether milliseconds is a declared choice.
func ValidPanelInterval(milliseconds int) bool {
	for _, choice := range PanelIntervals {
		if choice == milliseconds {
			return true
		}
	}
	return false
}

// Refresh cadence constants.
const (
	// PanelPushFloor is the minimum distance between two accepted pushes from
	// one plugin.
	PanelPushFloor = 100 * time.Millisecond
	// PanelFastFloor is the minimum fast-section interval.
	PanelFastFloor = 500 * time.Millisecond
	// PanelFastDefault is the default fast-section interval.
	PanelFastDefault = time.Second
	// PanelTableDefault is the default interval for a fast section that
	// declares no interval and renders a table. Fast sections default to one
	// second whatever their payload shape; the 500 ms floor still applies.
	PanelTableDefault = time.Second
	// PanelSnapshotFloor is the minimum snapshot refresh interval.
	PanelSnapshotFloor = time.Second
	// PanelSnapshotCeiling is the default and declared-interval ceiling for a
	// snapshot section. The Host's configured collector cadence can slow the
	// section further; it can never speed it up past this declaration.
	PanelSnapshotCeiling = 3 * time.Second
	// PanelRefreshTimeout bounds one host-initiated section refresh.
	PanelRefreshTimeout = 10 * time.Second
)

// ValidatePanelSection checks one manifest section declaration.
func ValidatePanelSection(section PanelSection) error {
	if !panelIdentifier(section.ID) {
		return fmt.Errorf("system panel section id %q must be lowercase letters, digits, dot, underscore, or hyphen, at most 64 characters", section.ID)
	}
	if section.TitleKey != "" && !ValidMessageKey(section.TitleKey) {
		return fmt.Errorf("system panel section %q: title_key %q does not match [a-z0-9_.-]{1,128}",
			section.ID, section.TitleKey)
	}
	if strings.TrimSpace(section.Title) == "" && section.TitleKey == "" {
		return fmt.Errorf("system panel section %q: title is required unless title_key is set", section.ID)
	}
	if section.Title != "" {
		if err := validatePanelString("title", section.Title); err != nil {
			return fmt.Errorf("system panel section %q: %w", section.ID, err)
		}
	}
	if section.Order < -MaxPanelOrder || section.Order > MaxPanelOrder {
		return fmt.Errorf("system panel section %q: order is outside +/-%d", section.ID, MaxPanelOrder)
	}
	if !section.Refresh.Valid() {
		return fmt.Errorf("system panel section %q: refresh must be push, fast, or snapshot", section.ID)
	}
	if section.Interval != 0 && !ValidPanelInterval(section.Interval) {
		return fmt.Errorf("system panel section %q: interval %d ms is not a declared choice", section.ID, section.Interval)
	}
	if section.MinInterval != 0 && !ValidPanelInterval(section.MinInterval) {
		return fmt.Errorf("system panel section %q: min_interval %d ms is not a declared choice", section.ID, section.MinInterval)
	}
	if section.Refresh == PanelRefreshSnapshot {
		floor := int(PanelSnapshotFloor / time.Millisecond)
		ceiling := int(PanelSnapshotCeiling / time.Millisecond)
		if section.Interval != 0 && (section.Interval < floor || section.Interval > ceiling) {
			return fmt.Errorf("system panel section %q: snapshot interval must be %d-%d ms", section.ID, floor, ceiling)
		}
		if section.MinInterval != 0 && (section.MinInterval < floor || section.MinInterval > ceiling) {
			return fmt.Errorf("system panel section %q: snapshot min_interval must be %d-%d ms", section.ID, floor, ceiling)
		}
	}
	if len(section.Actions) > MaxPanelActions {
		return fmt.Errorf("system panel section %q: at most %d actions are allowed", section.ID, MaxPanelActions)
	}
	seen := make(map[string]bool, len(section.Actions))
	for _, action := range section.Actions {
		if !panelIdentifier(action.ID) {
			return fmt.Errorf("system panel section %q: action id %q is invalid", section.ID, action.ID)
		}
		if seen[action.ID] {
			return fmt.Errorf("system panel section %q: duplicate action %q", section.ID, action.ID)
		}
		seen[action.ID] = true
		if action.LabelKey != "" && !ValidMessageKey(action.LabelKey) {
			return fmt.Errorf("system panel section %q action %q: label_key %q does not match [a-z0-9_.-]{1,128}",
				section.ID, action.ID, action.LabelKey)
		}
		if strings.TrimSpace(action.Label) == "" && action.LabelKey == "" {
			return fmt.Errorf("system panel section %q action %q: label is required unless label_key is set",
				section.ID, action.ID)
		}
		if action.Label != "" {
			if err := validatePanelString("action label", action.Label); err != nil {
				return fmt.Errorf("system panel section %q action %q: %w", section.ID, action.ID, err)
			}
		}
	}
	return nil
}

// IntervalFor returns the effective refresh interval for a section.
func IntervalFor(section PanelSection) time.Duration {
	requested := time.Duration(section.Interval) * time.Millisecond
	minimum := time.Duration(section.MinInterval) * time.Millisecond
	switch section.Refresh {
	case PanelRefreshPush:
		return 0
	case PanelRefreshSnapshot:
		if minimum < PanelSnapshotFloor {
			minimum = PanelSnapshotFloor
		}
		if requested == 0 {
			requested = PanelSnapshotCeiling
		}
		if requested < minimum {
			requested = minimum
		}
		if requested > PanelSnapshotCeiling {
			requested = PanelSnapshotCeiling
		}
		return requested
	default:
		if minimum < PanelFastFloor {
			minimum = PanelFastFloor
		}
		if requested == 0 {
			requested = PanelFastDefault
		}
		if requested < minimum {
			requested = minimum
		}
		return requested
	}
}

// DecodePanelPayload strictly decodes and validates one section update. The
// byte cap follows the section's refresh class, so a push can never exceed the
// 4 KiB push budget. Unknown fields are rejected: there is no native UI tree,
// markup, colour, or drawing escape hatch.
func DecodePanelPayload(raw []byte, section PanelSection) (*PanelPayload, error) {
	limit := MaxPanelPayloadBytes
	if section.Refresh == PanelRefreshPush {
		limit = MaxPanelPushBytes
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("system panel section %q: payload is %d bytes, above the %d byte limit",
			section.ID, len(raw), limit)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var payload PanelPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("system panel section %q: payload is not a valid descriptor document: %w",
			section.ID, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, fmt.Errorf("system panel section %q: %w", section.ID, err)
	}
	if err := ValidatePanelPayload(payload, section); err != nil {
		return nil, err
	}
	return &payload, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("payload contains more than one JSON value")
		}
		return fmt.Errorf("payload has trailing data: %w", err)
	}
	return nil
}

// ValidatePanelPayload checks the closed descriptor vocabulary and every
// count and string bound. section supplies the declared action IDs.
func ValidatePanelPayload(payload PanelPayload, section PanelSection) error {
	if len(payload.Items) > MaxPanelItems {
		return fmt.Errorf("system panel section %q: %d descriptors, above the %d limit",
			section.ID, len(payload.Items), MaxPanelItems)
	}
	declared := make(map[string]PanelAction, len(section.Actions))
	for _, action := range section.Actions {
		declared[action.ID] = action
	}
	for i, item := range payload.Items {
		where := fmt.Sprintf("system panel section %q descriptor %d", section.ID, i)
		if !item.Kind.Valid() {
			return fmt.Errorf("%s: unknown descriptor kind %q", where, item.Kind)
		}
		for field, value := range map[string]string{
			"label": item.Label, "value": item.Value, "unit": item.Unit, "detail": item.Detail,
		} {
			if err := validatePanelString(field, value); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
		}
		if item.Level != "" && !item.Level.Valid() {
			return fmt.Errorf("%s: unknown status level %q", where, item.Level)
		}
		switch item.Kind {
		case PanelDescriptorMetric:
			if strings.TrimSpace(item.Label) == "" {
				return fmt.Errorf("%s: a metric needs a label", where)
			}
		case PanelDescriptorSection:
			if strings.TrimSpace(item.Label) == "" {
				return fmt.Errorf("%s: a section needs a label", where)
			}
			if len(item.Rows) > MaxPanelRows {
				return fmt.Errorf("%s: %d rows, above the %d limit", where, len(item.Rows), MaxPanelRows)
			}
			for _, row := range item.Rows {
				if err := validatePanelString("row label", row.Label); err != nil {
					return fmt.Errorf("%s: %w", where, err)
				}
				if err := validatePanelString("row value", row.Value); err != nil {
					return fmt.Errorf("%s: %w", where, err)
				}
			}
		case PanelDescriptorTable:
			if len(item.Columns) == 0 || len(item.Columns) > MaxPanelColumns {
				return fmt.Errorf("%s: a table needs 1-%d columns", where, MaxPanelColumns)
			}
			for _, column := range item.Columns {
				if err := validatePanelString("column", column); err != nil {
					return fmt.Errorf("%s: %w", where, err)
				}
			}
			if len(item.TableRows) > MaxPanelRows {
				return fmt.Errorf("%s: %d rows, above the %d limit", where, len(item.TableRows), MaxPanelRows)
			}
			keys := make(map[string]bool, len(item.TableRows))
			for _, row := range item.TableRows {
				if err := validatePanelString("row key", row.Key); err != nil {
					return fmt.Errorf("%s: %w", where, err)
				}
				if row.Key == "" {
					return fmt.Errorf("%s: every row needs a stable key", where)
				}
				if keys[row.Key] {
					return fmt.Errorf("%s: duplicate row key %q", where, row.Key)
				}
				keys[row.Key] = true
				if len(row.Cells) != len(item.Columns) {
					return fmt.Errorf("%s: row %q has %d cells, want %d",
						where, row.Key, len(row.Cells), len(item.Columns))
				}
				for _, cell := range row.Cells {
					if err := validatePanelString("cell", cell); err != nil {
						return fmt.Errorf("%s: %w", where, err)
					}
				}
			}
		case PanelDescriptorStatus:
			if strings.TrimSpace(item.Label) == "" {
				return fmt.Errorf("%s: a status needs a label", where)
			}
		case PanelDescriptorAction:
			if _, ok := declared[item.Action]; !ok {
				return fmt.Errorf("%s: action %q is not declared by the section", where, item.Action)
			}
			if err := validatePanelString("row key", item.RowKey); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
		case PanelDescriptorTimeseries:
			if len(item.Points) > MaxPanelPoints {
				return fmt.Errorf("%s: %d points, above the %d limit", where, len(item.Points), MaxPanelPoints)
			}
			if item.Scale < 0 {
				return fmt.Errorf("%s: scale must not be negative", where)
			}
		}
	}
	return nil
}

func validatePanelString(field, value string) error {
	if len(value) > MaxPanelStringBytes {
		return fmt.Errorf("%s is %d bytes, above the %d byte limit", field, len(value), MaxPanelStringBytes)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", field)
	}
	return nil
}

func panelIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for i, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}
