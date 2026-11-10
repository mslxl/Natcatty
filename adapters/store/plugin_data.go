package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
	"gorm.io/gorm"
)

var _ appplugin.PluginDataRepository = (*Store)(nil)

// PluginDataHostRecord is one Host's host-opaque pluginData, without any other
// Host field.
type PluginDataHostRecord struct {
	ID         string
	PluginData plugindata.Map
}

// ListPluginDataHosts returns every Host ID and its pluginData. It reads only
// the aggregate JSON, so unknown Host properties are irrelevant here.
func (s *Store) ListPluginDataHosts(ctx context.Context) ([]PluginDataHostRecord, error) {
	var rows []dataSchemaRow
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return tx.Model(&hostSchema{}).Select("data").Find(&rows).Error
	}); err != nil {
		return nil, fmt.Errorf("store: list Host plugin data: %w", err)
	}
	records := make([]PluginDataHostRecord, 0, len(rows))
	for _, row := range rows {
		var decoded struct {
			ID         string         `json:"id"`
			PluginData plugindata.Map `json:"pluginData,omitempty"`
		}
		if err := json.Unmarshal([]byte(row.Data), &decoded); err != nil {
			return nil, fmt.Errorf("store: decode Host plugin data: %w", err)
		}
		records = append(records, PluginDataHostRecord{ID: decoded.ID, PluginData: decoded.PluginData})
	}
	return records, nil
}

// SetHostPluginData replaces one Host's pluginData map and persists it through
// the ordinary Host save path, which sanitizes and quarantines invalid
// namespaces.
func (s *Store) SetHostPluginData(ctx context.Context, hostID string, data plugindata.Map) error {
	host, err := s.Get(ctx, model.HostID(hostID))
	if err != nil {
		return err
	}
	host.PluginData = data
	return s.Save(ctx, host)
}

// PluginDataProfileRecord is one DataProfile's host-opaque pluginData, without
// any other profile field.
type PluginDataProfileRecord struct {
	ID         string
	Provider   string
	SecretRefs map[string]string
	PluginData plugindata.Map
}

// ListPluginDataProfiles returns every DataProfile ID and its pluginData.
func (s *Store) ListPluginDataProfiles(ctx context.Context) ([]PluginDataProfileRecord, error) {
	var rows []dataSchemaRow
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return tx.Model(&dataProfileSchema{}).Select("data").Find(&rows).Error
	}); err != nil {
		return nil, fmt.Errorf("store: list data profile plugin data: %w", err)
	}
	records := make([]PluginDataProfileRecord, 0, len(rows))
	for _, row := range rows {
		var decoded struct {
			ID         string            `json:"id"`
			Provider   string            `json:"provider"`
			SecretRefs map[string]string `json:"secretRefs,omitempty"`
			PluginData plugindata.Map    `json:"pluginData,omitempty"`
		}
		if err := json.Unmarshal([]byte(row.Data), &decoded); err != nil {
			return nil, fmt.Errorf("store: decode data profile plugin data: %w", err)
		}
		records = append(records, PluginDataProfileRecord{
			ID: decoded.ID, Provider: decoded.Provider,
			SecretRefs: decoded.SecretRefs, PluginData: decoded.PluginData,
		})
	}
	return records, nil
}

// PluginDataRecords implements the application plugin-data repository: one
// read-only view over both record families.
func (s *Store) PluginDataRecords(ctx context.Context) ([]appplugin.PluginDataRecord, error) {
	hosts, err := s.ListPluginDataHosts(ctx)
	if err != nil {
		return nil, err
	}
	profiles, err := s.ListPluginDataProfiles(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]appplugin.PluginDataRecord, 0, len(hosts)+len(profiles))
	for _, host := range hosts {
		records = append(records, appplugin.PluginDataRecord{
			Kind: plugindata.RecordHost, ID: host.ID, Data: host.PluginData,
		})
	}
	for _, profile := range profiles {
		records = append(records, appplugin.PluginDataRecord{
			Kind: plugindata.RecordDataProfile, ID: profile.ID,
			Provider: profile.Provider, SecretRefs: profile.SecretRefs,
			Data: profile.PluginData,
		})
	}
	return records, nil
}

// PluginDataDiagnostics implements the application repository's quarantine
// diagnostic view.
func (s *Store) PluginDataDiagnostics(ctx context.Context) ([]plugindata.Diagnostic, error) {
	return s.PluginDataQuarantine(ctx)
}

// SetDataProfilePluginData replaces one profile's pluginData map and persists
// it through the ordinary profile save path.
func (s *Store) SetDataProfilePluginData(ctx context.Context, profileID string, data plugindata.Map) error {
	profiles := NewDataProfiles(s)
	profile, err := profiles.Get(ctx, profileID)
	if err != nil {
		return err
	}
	profile.PluginData = data
	return profiles.Save(ctx, profile)
}

// PurgePluginData removes every namespace owned by pluginID from every Host
// and DataProfile, and drops that plugin's quarantine rows. The caller is
// responsible for the explicit confirmation the UI collects before this call.
// A record with a damaged pluginData container cannot be addressed; the purge
// fails with an error for that record rather than reporting success while
// leaving the namespace behind. A successful purge notifies the Vault-change
// hook like any other committed write.
func (s *Store) PurgePluginData(ctx context.Context, pluginID string) error {
	if pluginID == "" {
		return nil
	}
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		if err := purgeNamespaceTable(tx, domainbackup.TableHosts, pluginID); err != nil {
			return err
		}
		if err := purgeNamespaceTable(tx, domainbackup.TableDataProfiles, pluginID); err != nil {
			return err
		}
		return tx.Where("plugin_id = ?", pluginID).
			Delete(&pluginDataQuarantineSchema{}).Error
	}); err != nil {
		return err
	}
	s.vaultChanged()
	return nil
}

func purgeNamespaceTable(tx *gorm.DB, table, pluginID string) error {
	var rows []struct {
		ID   string `gorm:"column:id"`
		Data string `gorm:"column:data"`
	}
	if err := tx.Table(table).Select("id, data").Find(&rows).Error; err != nil {
		return fmt.Errorf("store: read %s for plugin data purge: %w", table, err)
	}
	for _, row := range rows {
		clean, removed, err := removeNamespace(row.Data, pluginID)
		if err != nil {
			return fmt.Errorf("store: purge %s %s: %w", table, row.ID, err)
		}
		// A variant spelling is rewritten to the canonical property even when
		// the purged namespace was invisible in a shadowed container. Update
		// the row whenever the bytes changed.
		if !removed && clean == row.Data {
			continue
		}
		if err := tx.Table(table).Where("id = ?", row.ID).
			Update("data", clean).Error; err != nil {
			return fmt.Errorf("store: purge %s %s: %w", table, row.ID, err)
		}
	}
	return nil
}

// removeNamespace deletes one pluginData key while preserving every other
// record property and namespace verbatim. A case-variant top-level spelling is
// the same property to encoding/json, so every spelling is resolved with the
// same last-wins semantics and all of them are dropped before the canonical
// property is written. Removing the last namespace drops the property itself;
// the canonical representation of an empty container is absence, never {} or
// null, so a following v2 sync never sees a container it must normalize.
func removeNamespace(data, pluginID string) (string, bool, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &top); err != nil {
		return data, false, err
	}
	properties, err := plugindata.ScanPluginDataProperties([]byte(data))
	if err != nil {
		return data, false, err
	}
	if !properties.Found {
		return data, false, nil
	}
	// A non-canonical spelling or a repeated property is the same pluginData
	// property to encoding/json, and every occurrence but the last is invisible
	// to the decoder while its bytes remain in the row. Rewrite the aggregate
	// even when the purged namespace is absent, so a variant or shadowed
	// container cannot survive the purge unvalidated.
	variantSpellings := len(properties.Keys) > 1 ||
		properties.Keys[0] != plugindata.JSONField
	repeatedProperty := properties.DuplicateRecordProperty
	rewrite := variantSpellings || repeatedProperty
	encoded := properties.Value
	dropSpellings := func() {
		for key := range top {
			if plugindata.IsPluginDataProperty(key) {
				delete(top, key)
			}
		}
	}
	if string(bytes.TrimSpace(encoded)) == "null" {
		// A null container owns no namespace. The canonical representation of a
		// container without a surviving namespace is the absent property, so
		// purge drops the spelling instead of rewriting it to another empty
		// form the v2 envelope comparison would have to normalize again.
		dropSpellings()
		out, err := marshalNoEscape(top)
		if err != nil {
			return data, false, err
		}
		return string(out), false, nil
	}
	var namespaces map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &namespaces); err != nil || namespaces == nil {
		// A damaged pluginData container has no addressable namespace. Do not
		// report success while silently leaving the plugin's data in place.
		return data, false, fmt.Errorf("pluginData is not an object")
	}
	removed := false
	if _, exists := namespaces[pluginID]; exists {
		delete(namespaces, pluginID)
		removed = true
	}
	if !removed && !rewrite && len(namespaces) > 0 {
		return data, false, nil
	}
	// An object that just lost its last namespace is canonicalized to the
	// absent property for the same reason as a null container: {} and absent
	// must never both mean "no surviving namespace".
	dropSpellings()
	if len(namespaces) > 0 {
		cleaned, err := marshalNoEscape(namespaces)
		if err != nil {
			return data, false, err
		}
		top[plugindata.JSONField] = cleaned
	}
	out, err := marshalNoEscape(top)
	if err != nil {
		return data, false, err
	}
	return string(out), removed, nil
}

// PluginDataQuarantine returns the host-local quarantined namespaces as
// diagnostics. Raw namespace bytes are deliberately not part of this view.
func (s *Store) PluginDataQuarantine(ctx context.Context) ([]plugindata.Diagnostic, error) {
	var rows []pluginDataQuarantineSchema
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return tx.Order("record_kind ASC, record_id ASC, plugin_id ASC").Find(&rows).Error
	}); err != nil {
		return nil, fmt.Errorf("store: list plugin data quarantine: %w", err)
	}
	diagnostics := make([]plugindata.Diagnostic, 0, len(rows))
	for _, row := range rows {
		diagnostics = append(diagnostics, plugindata.Diagnostic{
			RecordKind:    plugindata.RecordKind(row.RecordKind),
			RecordID:      row.RecordID,
			PluginID:      row.PluginID,
			SchemaVersion: int(row.SchemaVersion),
			Fields:        int(row.FieldCount),
			State:         plugindata.StateQuarantined,
			Reason:        plugindata.Reason(row.Reason),
			Detail:        row.Detail,
		})
	}
	return diagnostics, nil
}

// QuarantinedNamespace returns the raw bytes of one quarantined namespace, if
// this build retained them.
func (s *Store) QuarantinedNamespace(ctx context.Context, kind plugindata.RecordKind,
	recordID, pluginID string) ([]byte, bool, error) {
	// Quarantine rows store the bounded record ID and namespace spellings.
	// Bound the lookup the same way so an imported over-long ID or Plugin ID
	// stays addressable and a clear matches the row the save wrote.
	recordID = plugindata.BoundRecordID(recordID)
	pluginID = plugindata.BoundPluginID(pluginID)
	var row pluginDataQuarantineSchema
	err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return tx.Where("record_kind = ? AND record_id = ? AND plugin_id = ?",
			string(kind), recordID, pluginID).First(&row).Error
	})
	if err == gorm.ErrRecordNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: read plugin data quarantine: %w", err)
	}
	return []byte(row.Data), true, nil
}

// saveQuarantines reconciles one record's quarantine rows inside tx.
//
// A namespace that is valid and present in clean is no longer quarantined; an
// entry rejected by this save is written once. A row for a namespace that is
// still absent from the clean record is preserved, so an ordinary Host edit or
// a later sync does not erase the diagnostic for data the user never repaired
// or explicitly deleted. A record-level container quarantine (empty PluginID)
// is cleared whenever a record with a decodable container is saved.
func saveQuarantines(tx *gorm.DB, kind plugindata.RecordKind, recordID string,
	clean plugindata.Map, entries []plugindata.Quarantine) error {

	// recordID comes from an imported payload or snapshot. Bound it before it
	// becomes a quarantine-row key or a diagnostic label.
	recordID = plugindata.BoundRecordID(recordID)
	incoming := quarantineRows(entries)
	var existing []pluginDataQuarantineSchema
	if err := tx.Where("record_kind = ? AND record_id = ?", string(kind), recordID).
		Find(&existing).Error; err != nil {
		return fmt.Errorf("store: read plugin data quarantine: %w", err)
	}
	for _, row := range existing {
		_, present := clean[row.PluginID]
		_, replaced := incoming[row.PluginID]
		if !present && !replaced && row.PluginID != "" {
			// Still absent: the namespace is neither valid nor explicitly
			// deleted, so its diagnostic survives.
			continue
		}
		if err := tx.Where("record_kind = ? AND record_id = ? AND plugin_id = ?",
			string(kind), recordID, row.PluginID).
			Delete(&pluginDataQuarantineSchema{}).Error; err != nil {
			return fmt.Errorf("store: clear plugin data quarantine: %w", err)
		}
	}
	pluginIDs := make([]string, 0, len(incoming))
	for pluginID := range incoming {
		pluginIDs = append(pluginIDs, pluginID)
	}
	sort.Strings(pluginIDs)
	now := time.Now().UnixMilli()
	for _, pluginID := range pluginIDs {
		entry := incoming[pluginID]
		row := pluginDataQuarantineSchema{
			RecordKind: string(kind), RecordID: recordID, PluginID: entry.PluginID,
			SchemaVersion: int64(entry.SchemaVersion), FieldCount: int64(entry.Fields),
			SizeBytes: int64(entry.SizeBytes), Reason: string(entry.Reason),
			Detail: entry.Detail, Data: string(entry.Raw), CreatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("store: save plugin data quarantine: %w", err)
		}
	}
	return nil
}

// quarantineRows bounds the attacker-controlled diagnostic labels and raw
// bytes, and collapses entries that truncate to the same Plugin ID so a
// pathological namespace key cannot make the primary key collide.
func quarantineRows(entries []plugindata.Quarantine) map[string]plugindata.Quarantine {
	rows := make(map[string]plugindata.Quarantine, len(entries))
	for _, entry := range entries {
		entry.PluginID = plugindata.BoundPluginID(entry.PluginID)
		entry.Detail = plugindata.BoundText(entry.Detail, plugindata.MaxDiagnosticDetailBytes)
		if len(entry.Raw) > plugindata.MaxRecordBytes {
			entry.Raw = append([]byte(nil), entry.Raw[:plugindata.MaxRecordBytes]...)
		}
		rows[entry.PluginID] = entry
	}
	return rows
}

// ClearPluginDataQuarantine removes one namespace's quarantine row. The
// application calls it when the user explicitly deletes a namespace, which is
// the one case an absent namespace is a deletion rather than an unchanged
// record.
func (s *Store) ClearPluginDataQuarantine(ctx context.Context, kind plugindata.RecordKind,
	recordID, pluginID string) error {

	// Quarantine rows store the bounded record ID and namespace spellings.
	// Bound the lookup the same way so an imported over-long ID or Plugin ID
	// stays clearable.
	recordID = plugindata.BoundRecordID(recordID)
	pluginID = plugindata.BoundPluginID(pluginID)
	err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		return tx.Where("record_kind = ? AND record_id = ? AND plugin_id = ?",
			string(kind), recordID, pluginID).
			Delete(&pluginDataQuarantineSchema{}).Error
	})
	if err != nil {
		return fmt.Errorf("store: clear plugin data quarantine: %w", err)
	}
	return nil
}

// sanitizeHostPluginData returns the record's clean map as the value to store
// and the invalid namespaces as quarantines. It is the strict edit path.
func sanitizeHostPluginData(host domainvault.Host) (domainvault.Host, []plugindata.Quarantine, error) {
	clean, quarantined, err := plugindata.Sanitize(host.PluginData)
	if err != nil {
		return domainvault.Host{}, nil, err
	}
	host.PluginData = clean
	return host, quarantined, nil
}

// sanitizeDataProfilePluginData returns the profile's clean map as the value
// to store and the invalid namespaces as quarantines. It is the strict edit
// path.
func sanitizeDataProfilePluginData(profile domainplugin.DataProfile) (domainplugin.DataProfile, []plugindata.Quarantine, error) {
	clean, quarantined, err := plugindata.Sanitize(profile.PluginData)
	if err != nil {
		return domainplugin.DataProfile{}, nil, err
	}
	profile.PluginData = clean
	return profile, quarantined, nil
}

// sanitizeHostPluginDataLenient is the import path: an over-limit valid
// namespace is quarantined deterministically instead of failing the sync.
func sanitizeHostPluginDataLenient(host domainvault.Host) (domainvault.Host, []plugindata.Quarantine, error) {
	clean, quarantined, err := plugindata.SanitizeLenient(host.PluginData)
	if err != nil {
		return domainvault.Host{}, nil, err
	}
	host.PluginData = clean
	return host, quarantined, nil
}

// sanitizeDataProfilePluginDataLenient is the import path for profiles.
func sanitizeDataProfilePluginDataLenient(profile domainplugin.DataProfile) (domainplugin.DataProfile, []plugindata.Quarantine, error) {
	clean, quarantined, err := plugindata.SanitizeLenient(profile.PluginData)
	if err != nil {
		return domainplugin.DataProfile{}, nil, err
	}
	profile.PluginData = clean
	return profile, quarantined, nil
}

// sanitizeSnapshotRecords validates pluginData on every record-bearing table
// a restore writes. It runs before the restore transaction so an invalid
// namespace is quarantined individually. An over-limit valid namespace is
// quarantined deterministically too: one bad record must not abort the whole
// restore and leave zero Hosts applied.
type snapshotQuarantine struct {
	kind    plugindata.RecordKind
	record  string
	entries []plugindata.Quarantine
}

func sanitizeSnapshotRecords(snapshot domainbackup.Snapshot) (domainbackup.Snapshot, []snapshotQuarantine, error) {
	// Copy the tables and rows so sanitizing never mutates the caller's
	// snapshot: the service may keep it for a fingerprint or a retry.
	clean := domainbackup.Snapshot{
		SchemaVersion: snapshot.SchemaVersion,
		Tables:        make([]domainbackup.Table, len(snapshot.Tables)),
	}
	for index, table := range snapshot.Tables {
		rows := make([]domainbackup.Row, len(table.Rows))
		for rowIndex, row := range table.Rows {
			copied := make(domainbackup.Row, len(row))
			for column, value := range row {
				copied[column] = value
			}
			rows[rowIndex] = copied
		}
		clean.Tables[index] = domainbackup.Table{Name: table.Name, Rows: rows}
	}
	snapshot = clean
	var quarantines []snapshotQuarantine
	for tableIndex := range snapshot.Tables {
		table := &snapshot.Tables[tableIndex]
		var kind plugindata.RecordKind
		switch table.Name {
		case domainbackup.TableHosts:
			kind = plugindata.RecordHost
		case domainbackup.TableDataProfiles:
			kind = plugindata.RecordDataProfile
		default:
			continue
		}
		for rowIndex := range table.Rows {
			row := table.Rows[rowIndex]
			recordID := ""
			if id, ok := row["id"]; ok && id.Kind == domainbackup.KindText {
				recordID = id.Text
			}
			// Both record families store their aggregate as TEXT. Any other
			// storage class is a malformed snapshot, not something to restore
			// without the structural validation this loop exists to provide.
			dataValue, ok := row["data"]
			if !ok || dataValue.Kind != domainbackup.KindText {
				return domainbackup.Snapshot{}, nil, fmt.Errorf(
					"restore %s %s: data column is not text", table.Name, recordID)
			}
			clean, entries, err := plugindata.SanitizeRecord(kind, recordID, []byte(dataValue.Text))
			if err != nil {
				return domainbackup.Snapshot{}, nil, fmt.Errorf("restore %s %s: %w", table.Name, recordID, err)
			}
			if len(entries) == 0 && string(clean) == dataValue.Text {
				continue
			}
			row["data"] = domainbackup.Text(string(clean))
			table.Rows[rowIndex] = row
			if len(entries) > 0 {
				quarantines = append(quarantines, snapshotQuarantine{
					kind: kind, record: recordID, entries: entries,
				})
			}
		}
	}
	return snapshot, quarantines, nil
}

// recordQuarantines writes every sanitized record's quarantine rows and
// clears stale diagnostics for records that are now clean.
func recordQuarantines(tx *gorm.DB, quarantines []snapshotQuarantine) error {
	if err := deleteAll(tx, &pluginDataQuarantineSchema{}); err != nil {
		return err
	}
	for _, entry := range quarantines {
		if err := saveQuarantines(tx, entry.kind, entry.record, nil, entry.entries); err != nil {
			return err
		}
	}
	return nil
}
