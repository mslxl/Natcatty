package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Plugin-data failure sentinels. They carry no provider field values.
var (
	// ErrPluginDataUnavailable reports a host port that is not wired.
	ErrPluginDataUnavailable = errors.New("plugin: plugin data host is not configured")
	// ErrPluginDataNotFound reports a Host or DataProfile with no namespace to
	// change.
	ErrPluginDataNotFound = errors.New("plugin: plugin data record not found")
	// ErrPluginDataReadOnly reports an edit while the owning plugin is not
	// ACTIVE or the namespace schema is newer than the provider declares.
	ErrPluginDataReadOnly = errors.New("plugin: plugin data is read-only")
	// ErrPluginDataConfirmationRequired reports a destructive plugin-data
	// change without the explicit confirmation the UI collects.
	ErrPluginDataConfirmationRequired = errors.New("plugin: plugin data change requires confirmation")
	// ErrPluginDataRecordTooLarge reports a namespace set whose valid map
	// exceeds the record limit. Individual invalid namespaces are quarantined
	// instead; only a valid remainder can hit this limit.
	ErrPluginDataRecordTooLarge = errors.New("plugin: plugin data record exceeds the size limit")
)

// PluginDataProviderState is the provider surface one plugin currently
// exposes. Active is true only while a fiber for the plugin is ACTIVE.
type PluginDataProviderState struct {
	Active bool
	// SchemaVersion is the manifest's declared plugin-data schema version,
	// with the version-1 default applied.
	SchemaVersion int
}

// PluginDataStateSource resolves the live state for every plugin. It reads
// lifecycle state only; it never starts, loads, or calls a plugin.
type PluginDataStateSource interface {
	PluginDataProviderStates(ctx context.Context) (map[string]PluginDataProviderState, error)
}

// PluginDataStateSourceFunc adapts a function to PluginDataStateSource.
type PluginDataStateSourceFunc func(ctx context.Context) (map[string]PluginDataProviderState, error)

// PluginDataProviderStates implements PluginDataStateSource.
func (f PluginDataStateSourceFunc) PluginDataProviderStates(ctx context.Context) (map[string]PluginDataProviderState, error) {
	return f(ctx)
}

// PluginDataDiagnosticSource is the diagnostics view a Manager includes in its
// report. It exposes no field values.
type PluginDataDiagnosticSource interface {
	Diagnostics(ctx context.Context) ([]plugindata.Diagnostic, error)
}

// PluginDataRecord is one Host or DataProfile's host-opaque data. SecretRefs
// is populated for DataProfiles only and is used to resolve dormant state; the
// referenced values never leave the secret store here.
type PluginDataRecord struct {
	Kind       plugindata.RecordKind
	ID         string
	Provider   string
	SecretRefs map[string]string
	Data       plugindata.Map
}

// PluginDataRepository is the persistence port for host-opaque namespaces.
// Invalid imported namespaces are handled by the adapter before records reach
// this port, so Set stores an already valid map.
type PluginDataRepository interface {
	PluginDataRecords(ctx context.Context) ([]PluginDataRecord, error)
	SetHostPluginData(ctx context.Context, hostID string, data plugindata.Map) error
	SetDataProfilePluginData(ctx context.Context, profileID string, data plugindata.Map) error
	PurgePluginData(ctx context.Context, pluginID string) error
	PluginDataDiagnostics(ctx context.Context) ([]plugindata.Diagnostic, error)
}

// PluginDataQuarantineStore is the optional repository surface for explicitly
// deleting one quarantined namespace. A quarantine is host-local diagnostic
// state, not record data, so it cannot be removed through the ordinary
// namespace map. Without this surface an explicit delete of a quarantined
// namespace reports ErrPluginDataNotFound rather than deleting the diagnostic.
type PluginDataQuarantineStore interface {
	QuarantinedNamespace(ctx context.Context, kind plugindata.RecordKind,
		recordID, pluginID string) ([]byte, bool, error)
	ClearPluginDataQuarantine(ctx context.Context, kind plugindata.RecordKind,
		recordID, pluginID string) error
}

// PluginDataHost owns the host-side read model and the guarded edits of
// provider-defined fields. It never executes plugin code, resolves a path or
// URL, or dereferences a secret; provider-defined fields grant no capability.
type PluginDataHost struct {
	repo    PluginDataRepository
	states  PluginDataStateSource
	secrets DataProfileSecrets
}

// PluginDataHostOption customizes a PluginDataHost.
type PluginDataHostOption func(*PluginDataHost)

// WithPluginDataSecrets adds the Vault secret port used to detect unresolved
// DataProfile references. Without it every secret reference is treated as
// unresolved/dormant rather than as an empty value.
func WithPluginDataSecrets(secrets DataProfileSecrets) PluginDataHostOption {
	return func(h *PluginDataHost) { h.secrets = secrets }
}

// NewPluginDataHost returns a host over repo and the live lifecycle source.
func NewPluginDataHost(repo PluginDataRepository, states PluginDataStateSource,
	opts ...PluginDataHostOption) *PluginDataHost {

	host := &PluginDataHost{repo: repo, states: states}
	for _, opt := range opts {
		opt(host)
	}
	return host
}

// NamespaceStatus is the read-only view of one namespace for the Host or
// DataProfile details panel.
type NamespaceStatus struct {
	RecordKind    plugindata.RecordKind `json:"recordKind"`
	RecordID      string                `json:"recordId"`
	PluginID      string                `json:"pluginId"`
	SchemaVersion int                   `json:"schemaVersion"`
	Fields        int                   `json:"fields"`
	State         plugindata.State      `json:"state"`
	Reason        plugindata.Reason     `json:"reason,omitempty"`
	Detail        string                `json:"detail,omitempty"`
}

// PluginDataReport is the complete provider-data view plus the quarantined
// namespaces this device retained from an import.
type PluginDataReport struct {
	Namespaces  []NamespaceStatus       `json:"namespaces"`
	Quarantined []plugindata.Diagnostic `json:"quarantined,omitempty"`
}

// Report resolves every namespace's state. An active provider owns an
// editable namespace; a missing, disabled, uninstalled, or non-ACTIVE provider
// leaves it dormant and read-only. A namespace newer than the provider's
// declared schema version is dormant and reported. An unresolved DataProfile
// secret reference makes the profile dormant; the reference is never emptied.
func (h *PluginDataHost) Report(ctx context.Context) (PluginDataReport, error) {
	if h == nil || h.repo == nil {
		return PluginDataReport{}, ErrPluginDataUnavailable
	}
	states, err := h.providerStates(ctx)
	if err != nil {
		return PluginDataReport{}, err
	}
	records, err := h.repo.PluginDataRecords(ctx)
	if err != nil {
		return PluginDataReport{}, err
	}
	report := PluginDataReport{Namespaces: []NamespaceStatus{}}
	for _, record := range records {
		unresolved := h.unresolvedSecretFields(ctx, record)
		pluginIDs := make([]string, 0, len(record.Data))
		for pluginID := range record.Data {
			pluginIDs = append(pluginIDs, pluginID)
		}
		sort.Strings(pluginIDs)
		for _, pluginID := range pluginIDs {
			namespace := record.Data[pluginID]
			version := plugindata.EffectiveSchemaVersion(namespace.SchemaVersion)
			status := NamespaceStatus{
				RecordKind: record.Kind, RecordID: record.ID, PluginID: pluginID,
				SchemaVersion: version, Fields: len(namespace.Fields),
			}
			status.State, status.Reason, status.Detail =
				namespaceState(pluginID, version, states, unresolved)
			report.Namespaces = append(report.Namespaces, status)
		}
		if len(record.Data) == 0 {
			// A profile with no namespace can still carry an unresolved secret
			// reference. Report it under the provider's Plugin ID so the empty
			// map cannot hide the loss; there is no namespace schema version.
			for pluginID, detail := range unresolved {
				report.Namespaces = append(report.Namespaces, NamespaceStatus{
					RecordKind: record.Kind, RecordID: record.ID, PluginID: pluginID,
					State:  plugindata.StateDormant,
					Reason: plugindata.ReasonUnresolvedSecret, Detail: detail,
				})
			}
		}
	}
	quarantined, err := h.repo.PluginDataDiagnostics(ctx)
	if err != nil {
		return PluginDataReport{}, err
	}
	report.Quarantined = quarantined
	sort.Slice(report.Namespaces, func(i, j int) bool {
		left, right := report.Namespaces[i], report.Namespaces[j]
		if left.RecordKind != right.RecordKind {
			return left.RecordKind < right.RecordKind
		}
		if left.RecordID != right.RecordID {
			return left.RecordID < right.RecordID
		}
		return left.PluginID < right.PluginID
	})
	return report, nil
}

// Diagnostics returns only the namespaces that are not editable, plus every
// quarantined namespace. This is the shape the plugin diagnostics view and the
// sync report consume.
func (h *PluginDataHost) Diagnostics(ctx context.Context) ([]plugindata.Diagnostic, error) {
	report, err := h.Report(ctx)
	if err != nil {
		return nil, err
	}
	diagnostics := make([]plugindata.Diagnostic, 0, len(report.Namespaces)+len(report.Quarantined))
	for _, status := range report.Namespaces {
		if status.State == plugindata.StateActive {
			continue
		}
		diagnostics = append(diagnostics, plugindata.Diagnostic{
			RecordKind: status.RecordKind, RecordID: status.RecordID,
			PluginID: status.PluginID, SchemaVersion: status.SchemaVersion,
			Fields: status.Fields, State: status.State,
			Reason: status.Reason, Detail: status.Detail,
		})
	}
	diagnostics = append(diagnostics, report.Quarantined...)
	return diagnostics, nil
}

func namespaceState(pluginID string, version int,
	states map[string]PluginDataProviderState,
	unresolved map[string]string) (plugindata.State, plugindata.Reason, string) {

	if detail, ok := unresolved[pluginID]; ok {
		return plugindata.StateDormant, plugindata.ReasonUnresolvedSecret, detail
	}
	state, ok := states[pluginID]
	if !ok || !state.Active {
		return plugindata.StateDormant, plugindata.ReasonProviderUnavailable, ""
	}
	if version > state.SchemaVersion {
		return plugindata.StateNewer, plugindata.ReasonSchemaNewer,
			fmt.Sprintf("%d>%d", version, state.SchemaVersion)
	}
	return plugindata.StateActive, "", ""
}

// unresolvedSecretFields maps each of the record's plugin IDs to the
// comma-separated secret field names that cannot be resolved on this device.
// A profile without namespaces still reports under its provider's Plugin ID so
// the loss is visible.
func (h *PluginDataHost) unresolvedSecretFields(ctx context.Context,
	record PluginDataRecord) map[string]string {

	if record.Kind != plugindata.RecordDataProfile || len(record.SecretRefs) == 0 {
		return nil
	}
	fields := make([]string, 0, len(record.SecretRefs))
	for field, raw := range record.SecretRefs {
		if h.secrets == nil {
			fields = append(fields, field)
			continue
		}
		if _, err := h.secrets.Get(ctx, raw); err != nil {
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	sort.Strings(fields)
	detail := plugindata.BoundText(strings.Join(fields, ","),
		plugindata.MaxDiagnosticDetailBytes)
	if len(record.Data) == 0 {
		return map[string]string{profilePluginID(record): detail}
	}
	out := make(map[string]string, len(record.Data))
	for pluginID := range record.Data {
		out[pluginID] = detail
	}
	return out
}

// profilePluginID extracts the Plugin ID from a qualified data-provider ID. A
// profile with no namespace is reported under this ID so an unresolved secret
// cannot be hidden.
func profilePluginID(record PluginDataRecord) string {
	pluginID, ok := domainplugin.PluginIDFromDataProviderID(record.Provider)
	if !ok {
		return ""
	}
	return plugindata.BoundText(pluginID, plugindata.MaxDiagnosticPluginIDBytes)
}

// SaveNamespace stores one namespace after validating it against the active
// provider's declared schema version. A provider that is not ACTIVE is
// read-only, so provider-schema validation only ever runs while it is active.
func (h *PluginDataHost) SaveNamespace(ctx context.Context, kind plugindata.RecordKind,
	recordID, pluginID string, namespace plugindata.Namespace) error {

	if h == nil || h.repo == nil {
		return ErrPluginDataUnavailable
	}
	if !plugindata.ValidPluginID(pluginID) {
		return &plugindata.ValidationError{PluginID: pluginID, Reason: plugindata.ReasonNamespace}
	}
	if kind != plugindata.RecordHost && kind != plugindata.RecordDataProfile {
		return &plugindata.ValidationError{PluginID: pluginID, Reason: plugindata.ReasonRecord}
	}
	states, err := h.providerStates(ctx)
	if err != nil {
		return err
	}
	state, ok := states[pluginID]
	if !ok || !state.Active {
		return fmt.Errorf("%w: plugin %s is not active", ErrPluginDataReadOnly, pluginID)
	}
	record, err := h.findRecord(ctx, kind, recordID)
	if err != nil {
		return err
	}
	if existing, ok := record.Data[pluginID]; ok {
		if plugindata.EffectiveSchemaVersion(existing.SchemaVersion) > state.SchemaVersion {
			// A newer provider wrote this namespace. An active older provider
			// must not rewrite or downgrade it.
			return fmt.Errorf("%w: plugin %s namespace is newer than the active provider",
				ErrPluginDataReadOnly, pluginID)
		}
		if len(namespace.Extra) == 0 {
			// Preserve top-level properties this older host does not interpret.
			namespace.Extra = existing.Extra
		}
	}
	// The active provider's declaration is authoritative: an explicit edit
	// writes its current version rather than running an implicit migration.
	namespace.SchemaVersion = state.SchemaVersion
	if err := plugindata.ValidateNamespace(pluginID, namespace); err != nil {
		return err
	}
	if record.Data == nil {
		record.Data = plugindata.Map{}
	}
	updated := plugindata.Clone(record.Data)
	updated[pluginID] = namespace
	if err := plugindata.Validate(updated); err != nil {
		if errors.Is(err, plugindata.ErrRecordTooLarge) {
			return fmt.Errorf("%w: %v", ErrPluginDataRecordTooLarge, err)
		}
		return err
	}
	return h.store(ctx, kind, recordID, updated)
}

// DeleteNamespace removes one namespace. Deleting provider data is a
// host-owned destructive action, allowed even while the provider is absent,
// but only with the explicit confirmation the UI collects. Other namespaces
// and unknown record properties are preserved.
func (h *PluginDataHost) DeleteNamespace(ctx context.Context, kind plugindata.RecordKind,
	recordID, pluginID string, confirmed bool) error {

	if h == nil || h.repo == nil {
		return ErrPluginDataUnavailable
	}
	if !confirmed {
		return ErrPluginDataConfirmationRequired
	}
	if !plugindata.ValidPluginID(pluginID) {
		return &plugindata.ValidationError{PluginID: pluginID, Reason: plugindata.ReasonNamespace}
	}
	record, err := h.findRecord(ctx, kind, recordID)
	if err != nil {
		return err
	}
	if _, ok := record.Data[pluginID]; !ok {
		// The namespace may have been quarantined on import and therefore never
		// entered Data. An explicit delete is the one host action that resolves
		// that diagnostic, so clear the row instead of reporting it missing.
		quarantines, ok := h.repo.(PluginDataQuarantineStore)
		if !ok {
			return ErrPluginDataNotFound
		}
		_, found, err := quarantines.QuarantinedNamespace(ctx, kind, recordID, pluginID)
		if err != nil {
			return err
		}
		if !found {
			return ErrPluginDataNotFound
		}
		return quarantines.ClearPluginDataQuarantine(ctx, kind, recordID, pluginID)
	}
	updated := plugindata.Clone(record.Data)
	delete(updated, pluginID)
	if len(updated) == 0 {
		updated = nil
	} else if err := plugindata.Validate(updated); err != nil {
		return err
	}
	return h.store(ctx, kind, recordID, updated)
}

// Purge deletes every namespace one plugin owns across Hosts and Data
// Profiles. Uninstall keeps this data; only an explicit, confirmed purge
// calls this method.
func (h *PluginDataHost) Purge(ctx context.Context, pluginID string, confirmed bool) error {
	if h == nil || h.repo == nil {
		return ErrPluginDataUnavailable
	}
	if !confirmed {
		return ErrPluginDataConfirmationRequired
	}
	if !plugindata.ValidPluginID(pluginID) {
		return &plugindata.ValidationError{PluginID: pluginID, Reason: plugindata.ReasonNamespace}
	}
	return h.repo.PurgePluginData(ctx, pluginID)
}

func (h *PluginDataHost) providerStates(ctx context.Context) (map[string]PluginDataProviderState, error) {
	if h.states == nil {
		return map[string]PluginDataProviderState{}, nil
	}
	states, err := h.states.PluginDataProviderStates(ctx)
	if err != nil {
		return nil, err
	}
	if states == nil {
		states = map[string]PluginDataProviderState{}
	}
	return states, nil
}

func (h *PluginDataHost) findRecord(ctx context.Context,
	kind plugindata.RecordKind, recordID string) (PluginDataRecord, error) {

	records, err := h.repo.PluginDataRecords(ctx)
	if err != nil {
		return PluginDataRecord{}, err
	}
	for _, record := range records {
		if record.Kind == kind && record.ID == recordID {
			return record, nil
		}
	}
	return PluginDataRecord{}, ErrPluginDataNotFound
}

func (h *PluginDataHost) store(ctx context.Context, kind plugindata.RecordKind,
	recordID string, data plugindata.Map) error {

	switch kind {
	case plugindata.RecordHost:
		return h.repo.SetHostPluginData(ctx, recordID, data)
	case plugindata.RecordDataProfile:
		return h.repo.SetDataProfilePluginData(ctx, recordID, data)
	default:
		return fmt.Errorf("%w: unknown record kind %q", ErrPluginDataNotFound, kind)
	}
}
