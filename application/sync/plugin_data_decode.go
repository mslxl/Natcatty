package sync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// decodePayloadLenient decodes a plaintext sync payload so that one malformed
// pluginData namespace or container quarantines only itself. A payload written
// by another client may carry a namespace this build cannot validate; the
// local client must still apply every valid namespace and every sibling
// record, and it must report the rejection rather than fail the whole sync
// forever with ErrRemoteCorrupt.
//
// Every other payload family keeps the strict decoding it had before: only the
// host-opaque pluginData property is host-defined enough to reject piecewise.
func decodePayloadLenient(data []byte) (Payload, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return Payload{}, err
	}
	if top == nil {
		return Payload{}, fmt.Errorf("payload is not an object")
	}
	rawHosts, hasHosts := top["hosts"]
	delete(top, "hosts")
	rawProfiles, hasProfiles := top["dataProfiles"]
	delete(top, "dataProfiles")
	remainder, err := domainsync.MarshalJSON(top)
	if err != nil {
		return Payload{}, err
	}
	var payload Payload
	if err := json.Unmarshal(remainder, &payload); err != nil {
		return Payload{}, err
	}
	if hasHosts {
		var records []json.RawMessage
		if err := json.Unmarshal(rawHosts, &records); err != nil {
			return Payload{}, fmt.Errorf("hosts is not an array: %w", err)
		}
		// Preserve the empty-versus-absent distinction: an explicit empty array
		// must re-encode as [] rather than null, or the payload's signature
		// changes across a seal/load round trip.
		if records != nil {
			payload.Hosts = make([]domainvault.Host, 0, len(records))
		}
		for _, record := range records {
			clean, rejected, err := decodePayloadRecord(plugindata.RecordHost, record)
			if err != nil {
				return Payload{}, err
			}
			var host domainvault.Host
			if err := json.Unmarshal(clean, &host); err != nil {
				return Payload{}, err
			}
			payload.Hosts = append(payload.Hosts, host)
			payload.PluginDataQuarantines = append(payload.PluginDataQuarantines, rejected...)
		}
	}
	if hasProfiles {
		var records []json.RawMessage
		if err := json.Unmarshal(rawProfiles, &records); err != nil {
			return Payload{}, fmt.Errorf("dataProfiles is not an array: %w", err)
		}
		if records != nil {
			payload.DataProfiles = make([]domainplugin.DataProfile, 0, len(records))
		}
		for _, record := range records {
			clean, rejected, err := decodePayloadRecord(plugindata.RecordDataProfile, record)
			if err != nil {
				return Payload{}, err
			}
			var profile domainplugin.DataProfile
			if err := json.Unmarshal(clean, &profile); err != nil {
				return Payload{}, err
			}
			payload.DataProfiles = append(payload.DataProfiles, profile)
			payload.PluginDataQuarantines = append(payload.PluginDataQuarantines, rejected...)
		}
	}
	return payload, nil
}

// decodePayloadRecord decodes one Host or DataProfile record. When the record
// has a pluginData property the container is decoded leniently and re-encoded
// canonically, so a duplicate key or unvalidated bytes can never shadow the
// validated map. A missing or null container is simply empty; a non-object
// container yields one record-level quarantine and the record otherwise
// applies.
func decodePayloadRecord(kind plugindata.RecordKind,
	record json.RawMessage) ([]byte, []RejectedPluginData, error) {

	var top map[string]json.RawMessage
	if err := json.Unmarshal(record, &top); err != nil {
		return nil, nil, err
	}
	if top == nil {
		return nil, nil, fmt.Errorf("%s record is not an object", kind)
	}
	// encoding/json matches the typed pluginData field case-insensitively, so
	// "PluginData" and "PLUGINDATA" are the same property to the decoder.
	// Resolve every spelling with last-wins semantics, then drop all of them
	// and write only the validated canonical property.
	properties, err := plugindata.ScanPluginDataProperties(record)
	if err != nil {
		return nil, nil, err
	}
	if !properties.Found {
		return record, nil, nil
	}
	recordID := ""
	if raw, ok := top["id"]; ok {
		_ = json.Unmarshal(raw, &recordID)
	}
	// The record ID is attacker-controlled in an imported payload; the sync
	// report and quarantine table must not grow with it.
	recordID = plugindata.BoundRecordID(recordID)
	namespaces, quarantined, err := plugindata.DecodeMapLenient(properties.Value)
	if err != nil {
		return nil, nil, err
	}
	rejected := make([]RejectedPluginData, 0, len(quarantined))
	for _, entry := range quarantined {
		rejected = append(rejected, RejectedPluginData{
			Kind:       kind,
			RecordID:   recordID,
			Quarantine: entry,
		})
	}
	for key := range top {
		if plugindata.IsPluginDataProperty(key) {
			delete(top, key)
		}
	}
	if len(namespaces) > 0 {
		clean, err := plugindata.EncodeMap(namespaces)
		if err != nil {
			return nil, nil, err
		}
		top[plugindata.JSONField] = clean
	}
	out, err := domainsync.MarshalJSON(top)
	if err != nil {
		return nil, nil, err
	}
	return out, rejected, nil
}

// normalizeMaterializedPluginData rewrites the HTML escapes that the v2 CRDT's
// canonical register values contain. CanonicalValue encodes strings with
// encoding/json, which spells <, >, and & as six-byte \uXXXX escapes. That is
// a valid JSON string, so a materialized record would otherwise carry the
// escaped spelling into the Vault, where it counts against the 16 KiB field
// and 64 KiB record limits and is no longer byte-stable. Decoding each value
// and re-encoding it with the JSON.stringify-shaped encoder restores the one
// stored byte per character without changing the CRDT state format.
func normalizeMaterializedPluginData(payload *Payload) error {
	for index := range payload.Hosts {
		data, err := normalizePluginDataMap(payload.Hosts[index].PluginData)
		if err != nil {
			return err
		}
		payload.Hosts[index].PluginData = data
	}
	for index := range payload.DataProfiles {
		data, err := normalizePluginDataMap(payload.DataProfiles[index].PluginData)
		if err != nil {
			return err
		}
		payload.DataProfiles[index].PluginData = data
	}
	return nil
}

func normalizePluginDataMap(value plugindata.Map) (plugindata.Map, error) {
	if len(value) == 0 {
		return value, nil
	}
	out := make(plugindata.Map, len(value))
	for pluginID, namespace := range value {
		fields, err := normalizePluginDataFields(namespace.Fields)
		if err != nil {
			return nil, err
		}
		extra, err := normalizePluginDataFields(namespace.Extra)
		if err != nil {
			return nil, err
		}
		namespace.Fields = fields
		namespace.Extra = extra
		out[pluginID] = namespace
	}
	return out, nil
}

func normalizePluginDataFields(values map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if len(values) == 0 {
		return values, nil
	}
	out := make(map[string]json.RawMessage, len(values))
	for key, raw := range values {
		if !containsHTMLEscape(raw) {
			out[key] = raw
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, fmt.Errorf("pluginData field %q: %w", key, err)
		}
		encoded, err := domainsync.MarshalJSON(decoded)
		if err != nil {
			return nil, fmt.Errorf("pluginData field %q: %w", key, err)
		}
		out[key] = encoded
	}
	return out, nil
}

// convergentPluginDataKind names the record family a CRDT collection carries.
func convergentPluginDataKind(family string) (plugindata.RecordKind, bool) {
	switch family {
	case "hosts":
		return plugindata.RecordHost, true
	case "dataProfiles":
		return plugindata.RecordDataProfile, true
	default:
		return "", false
	}
}

// normalizeConvergentRecordPluginData rewrites the HTML escapes a CRDT
// materialized record carries in its pluginData field values. The canonical
// CRDT encoding uses encoding/json, which spells '<', '>', and '&' as six-byte
// \uXXXX escapes; the stored Vault form and the 16 KiB / 64 KiB limits count
// the one-byte character instead. Only a namespace whose values actually
// contain an escape is re-encoded, so every other record stays byte-identical.
func normalizeConvergentRecordPluginData(record json.RawMessage) (json.RawMessage, error) {
	properties, err := plugindata.ScanPluginDataProperties(record)
	if err != nil {
		return nil, err
	}
	if !properties.Found {
		return record, nil
	}
	var rawNamespaces map[string]json.RawMessage
	if err := json.Unmarshal(properties.Value, &rawNamespaces); err != nil || rawNamespaces == nil {
		// A non-object container is rejected by SanitizeRecord with a
		// record-level quarantine; leave its bytes for that step.
		return record, nil
	}
	changed := false
	namespaces := make(map[string]json.RawMessage, len(rawNamespaces))
	for pluginID, rawNamespace := range rawNamespaces {
		namespace, decodeErr := plugindata.DecodeNamespace(rawNamespace)
		if decodeErr != nil {
			namespaces[pluginID] = rawNamespace
			continue
		}
		fields, fieldsErr := normalizePluginDataFields(namespace.Fields)
		if fieldsErr != nil {
			return nil, fieldsErr
		}
		extra, extraErr := normalizePluginDataFields(namespace.Extra)
		if extraErr != nil {
			return nil, extraErr
		}
		if rawMapsEqual(namespace.Fields, fields) && rawMapsEqual(namespace.Extra, extra) {
			namespaces[pluginID] = rawNamespace
			continue
		}
		namespace.Fields, namespace.Extra = fields, extra
		encoded, encodeErr := domainsync.MarshalJSON(namespace)
		if encodeErr != nil {
			return nil, encodeErr
		}
		namespaces[pluginID] = encoded
		changed = true
	}
	if !changed {
		return record, nil
	}
	container, err := domainsync.MarshalJSON(namespaces)
	if err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(record, &top); err != nil {
		return nil, err
	}
	for key := range top {
		if plugindata.IsPluginDataProperty(key) {
			delete(top, key)
		}
	}
	top[plugindata.JSONField] = container
	return domainsync.MarshalJSON(top)
}

func rawMapsEqual(left, right map[string]json.RawMessage) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, ok := right[key]
		if !ok || !bytes.Equal(value, other) {
			return false
		}
	}
	return true
}

// sanitizeConvergentSnapshotPluginData applies the per-namespace pluginData
// quarantine rule to the Host and DataProfile records of a materialized v2
// snapshot. A changed record is canonicalized exactly the way
// ParseConvergentSnapshot canonicalizes a payload record, so the result stays
// byte-comparable with the materialized payload and the crdt envelope.
//
// This is the boundary that keeps one malformed namespace in a remote or
// persisted v2 state from failing the whole payload: the valid namespaces and
// every sibling record survive, and each rejected namespace is returned as a
// diagnostic.
func sanitizeConvergentSnapshotPluginData(snapshot domainsync.ConvergentSnapshot) (domainsync.ConvergentSnapshot, []RejectedPluginData, error) {
	out := domainsync.ConvergentSnapshot{
		Families: make(map[string][]domainsync.ConvergentRecord, len(snapshot.Families)),
	}
	var rejected []RejectedPluginData
	families := make([]string, 0, len(snapshot.Families))
	for family := range snapshot.Families {
		families = append(families, family)
	}
	sort.Strings(families)
	for _, family := range families {
		records := snapshot.Families[family]
		copied := make([]domainsync.ConvergentRecord, len(records))
		copy(copied, records)
		if kind, hasPluginData := convergentPluginDataKind(family); hasPluginData {
			for index := range copied {
				record := &copied[index]
				// The CRDT canonical encoding spells '<', '>', and '&' as
				// six-byte escapes. The limits and the stored form count the
				// one-byte character, so restore the JSON.stringify-shaped
				// spelling before validating; otherwise a legal 3 KiB provider
				// field looks 18 KiB and is quarantined by its own encoding.
				normalized, err := normalizeConvergentRecordPluginData(record.Value)
				if err != nil {
					return domainsync.ConvergentSnapshot{}, nil, err
				}
				clean, entries, err := plugindata.SanitizeRecord(kind, record.ID, normalized)
				if err != nil {
					return domainsync.ConvergentSnapshot{}, nil, err
				}
				for _, entry := range entries {
					rejected = append(rejected, RejectedPluginData{
						Kind:       kind,
						RecordID:   plugindata.BoundRecordID(record.ID),
						Quarantine: entry,
					})
				}
				// Canonicalize the record the same way ParseConvergentSnapshot
				// canonicalizes a payload record, and always, so a caller that
				// compares this snapshot with a payload snapshot sees byte-equal
				// values whether or not a namespace was quarantined.
				canonical, err := domainsync.CanonicalValue(clean)
				if err != nil {
					return domainsync.ConvergentSnapshot{}, nil, err
				}
				record.Value = canonical
			}
		}
		out.Families[family] = copied
	}
	sortRejectedPluginData(rejected)
	return out, rejected, nil
}

// snapshotsEqualIgnoringPluginData compares two materialized snapshots after
// removing every spelling of the host-defined pluginData record property from
// each record of a family whose records actually define that property.
//
// The inline payload picks the last pluginData spelling in DOCUMENT order,
// while the CRDT envelope materializes record fields in SORTED key order. A
// foreign replica can therefore carry the same registers as its inline copy
// and still disagree about which duplicate spelling wins. The Vault-owned
// fields are still comparable once the opaque property is stripped: a family
// or record present on only one side is a mismatch, and any non-pluginData
// difference still fails. The caller follows ADR 0011 and keeps the sanitized
// CRDT state as the source of truth when this predicate holds.
//
// Only Host and DataProfile records own a pluginData field. Every other family
// is compared byte-for-byte, exactly like snapshotsEqual: a settings document
// may legitimately have a property named "pluginData", and a typed family such
// as portForwardingRules cannot represent the property at all, so stripping it
// there would accept a real edit and force an unrepairable rewrite forever.
func snapshotsEqualIgnoringPluginData(left, right domainsync.ConvergentSnapshot) bool {
	families := map[string]bool{}
	for family := range left.Families {
		families[family] = true
	}
	for family := range right.Families {
		families[family] = true
	}
	for family := range families {
		leftRecords := left.Families[family]
		rightRecords := right.Families[family]
		if len(leftRecords) != len(rightRecords) {
			return false
		}
		_, ownsPluginData := convergentPluginDataKind(family)
		byID := make(map[string]json.RawMessage, len(leftRecords))
		for _, record := range leftRecords {
			value := record.Value
			if ownsPluginData {
				stripped, ok := stripPluginDataProperties(record.Value)
				if !ok {
					return false
				}
				value = stripped
			}
			byID[record.ID] = value
		}
		for _, record := range rightRecords {
			value := record.Value
			if ownsPluginData {
				stripped, ok := stripPluginDataProperties(record.Value)
				if !ok {
					return false
				}
				value = stripped
			}
			stored, exists := byID[record.ID]
			if !exists || !bytes.Equal(stored, value) {
				return false
			}
		}
	}
	return true
}

// stripPluginDataProperties deletes every top-level property encoding/json
// would resolve to the typed pluginData field and re-encodes the remainder
// with the same marshaller the rest of this path uses. A record with no such
// property is returned unchanged.
func stripPluginDataProperties(raw json.RawMessage) (json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	found := false
	for key := range object {
		if plugindata.IsPluginDataProperty(key) {
			delete(object, key)
			found = true
		}
	}
	if !found {
		return raw, true
	}
	stripped, err := domainsync.MarshalJSON(object)
	if err != nil {
		return nil, false
	}
	return stripped, true
}

// sanitizedStateSnapshot materializes one v2 state and applies the per-namespace
// quarantine rule. The returned snapshot is what the state would materialize
// as if every malformed namespace had been quarantined.
func sanitizedStateSnapshot(state domainsync.ConvergentState) (domainsync.ConvergentSnapshot, []RejectedPluginData, error) {
	snapshot, err := domainsync.MaterializeConvergentSnapshot(state)
	if err != nil {
		return domainsync.ConvergentSnapshot{}, nil, err
	}
	return sanitizeConvergentSnapshotPluginData(snapshot)
}

// repairConvergentStatePluginData removes malformed namespaces from a v2 state
// with an ordinary causal write, so the repaired state can be sealed locally
// and published without keeping data the Vault will always quarantine. It also
// rewrites every record whose pluginData container sanitization changed in any
// way: a container with no surviving namespace (empty, null, or a case-variant
// spelling of either), a case-variant spelling alongside or instead of the
// canonical property, and a non-canonical last-wins selection. None of those
// are quarantines and none produce a diagnostic, but leaving them in the
// envelope would make it disagree with the sanitized inline payload forever.
func (s *Service) repairConvergentStatePluginData(state domainsync.ConvergentState,
	deviceID string) (domainsync.ConvergentState, bool, []RejectedPluginData, error) {

	materialized, err := domainsync.MaterializeConvergentSnapshot(state)
	if err != nil {
		return state, false, nil, err
	}
	clean, rejected, err := sanitizeConvergentSnapshotPluginData(materialized)
	if err != nil {
		return state, false, nil, err
	}
	if len(rejected) == 0 && !pluginDataCanonicalizationNeeded(materialized, clean) {
		return state, false, nil, nil
	}
	next, changed, err := domainsync.ApplyConvergentSnapshot(
		state, clean, deviceID, s.now().UnixMilli(), false)
	if err != nil {
		return state, false, nil, err
	}
	return next, changed, rejected, nil
}

// pluginDataCanonicalizationNeeded reports whether sanitizing a snapshot
// rewrote any pluginData-bearing record. The sanitizer canonicalizes every
// container: it selects the last-wins property spelling, drops a container
// with no surviving namespace, rewrites a case-variant spelling to the
// canonical property, and re-encodes a non-canonical container. Comparing the
// record values by ID therefore detects every rewrite. Looking only at whether
// a pluginData property disappeared misses a foreign record that keeps a
// canonical container while also carrying a case-variant empty/null register;
// the state then materializes a record that disagrees with the inline payload
// even though a pluginData property still exists.
//
// Both values are the ones MaterializeConvergentSnapshot and
// sanitizeConvergentSnapshotPluginData produce, so an already-canonical record
// compares equal and a repeated sync allocates no repair write.
func pluginDataCanonicalizationNeeded(materialized, sanitized domainsync.ConvergentSnapshot) bool {
	for family, records := range materialized.Families {
		if _, hasPluginData := convergentPluginDataKind(family); !hasPluginData {
			continue
		}
		byID := make(map[string]json.RawMessage, len(sanitized.Families[family]))
		for _, record := range sanitized.Families[family] {
			byID[record.ID] = record.Value
		}
		for _, record := range records {
			value, exists := byID[record.ID]
			if !exists || !bytes.Equal(value, record.Value) {
				return true
			}
			delete(byID, record.ID)
		}
		// Sanitizing never adds a record. A sanitized record with no
		// materialized counterpart means the caller handed mismatched
		// snapshots; repair so the published view is the sanitized one rather
		// than guessing.
		if len(byID) > 0 {
			return true
		}
	}
	// The loop above cannot see a pluginData-bearing family that exists only in
	// the sanitized snapshot. Sanitizing never adds a family either, but treat
	// the asymmetry deliberately instead of publishing it.
	for family, records := range sanitized.Families {
		if _, hasPluginData := convergentPluginDataKind(family); !hasPluginData {
			continue
		}
		if len(records) > 0 && len(materialized.Families[family]) == 0 {
			return true
		}
	}
	return false
}

// mergeRejectedPluginData folds two diagnostic lists into one deterministic
// list, keeping the later entry for a namespace a payload and its envelope both
// rejected.
func mergeRejectedPluginData(existing, additions []RejectedPluginData) []RejectedPluginData {
	if len(existing) == 0 && len(additions) == 0 {
		return nil
	}
	type entryKey struct {
		kind     plugindata.RecordKind
		recordID string
		pluginID string
	}
	byKey := make(map[entryKey]RejectedPluginData, len(existing)+len(additions))
	for _, entry := range existing {
		byKey[entryKey{entry.Kind, entry.RecordID, entry.Quarantine.PluginID}] = entry
	}
	for _, entry := range additions {
		byKey[entryKey{entry.Kind, entry.RecordID, entry.Quarantine.PluginID}] = entry
	}
	merged := make([]RejectedPluginData, 0, len(byKey))
	for _, entry := range byKey {
		merged = append(merged, entry)
	}
	sortRejectedPluginData(merged)
	return merged
}

func sortRejectedPluginData(entries []RejectedPluginData) {
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].Kind != entries[right].Kind {
			return entries[left].Kind < entries[right].Kind
		}
		if entries[left].RecordID != entries[right].RecordID {
			return entries[left].RecordID < entries[right].RecordID
		}
		if entries[left].Quarantine.PluginID != entries[right].Quarantine.PluginID {
			return entries[left].Quarantine.PluginID < entries[right].Quarantine.PluginID
		}
		return entries[left].Quarantine.Reason < entries[right].Quarantine.Reason
	})
}

func containsHTMLEscape(raw []byte) bool {
	for _, escape := range [][]byte{
		[]byte("\\u003c"), []byte("\\u003C"),
		[]byte("\\u003e"), []byte("\\u003E"),
		[]byte("\\u0026"),
	} {
		if bytes.Contains(raw, escape) {
			return true
		}
	}
	return false
}
