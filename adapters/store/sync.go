package store

import (
	"context"
	"fmt"
	"sort"

	appsync "github.com/mslxl/natcatty/application/sync"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	"gorm.io/gorm"
)

// syncQuarantineState is one synced record's clean pluginData map plus every
// namespace this apply quarantined for it, whether the rejection came from the
// payload decoder or from the store's structural validation.
type syncQuarantineState struct {
	kind    plugindata.RecordKind
	record  string
	clean   plugindata.Map
	entries map[string]plugindata.Quarantine
}

func syncQuarantineKey(kind plugindata.RecordKind, record string) string {
	return string(kind) + "\x00" + record
}

// ApplySync replaces the synced Vault families in one ORM transaction. Local
// tables such as secrets, workspace, and history are deliberately untouched.
func (s *Store) ApplySync(ctx context.Context, payload appsync.Payload) error {
	states := make(map[string]*syncQuarantineState)
	stateFor := func(kind plugindata.RecordKind, record string) *syncQuarantineState {
		// The record ID is attacker-controlled in an imported payload. Bound it
		// so a payload-decoder rejection and the store-level sanitization of the
		// same record reconcile to one bounded quarantine row.
		record = plugindata.BoundRecordID(record)
		key := syncQuarantineKey(kind, record)
		state, ok := states[key]
		if !ok {
			state = &syncQuarantineState{
				kind: kind, record: record,
				entries: make(map[string]plugindata.Quarantine),
			}
			states[key] = state
		}
		return state
	}
	// A payload decoder rejects a malformed namespace before the typed record
	// exists. Keep those rejections associated with their record so this apply
	// persists them alongside the store-level ones.
	for _, rejected := range payload.PluginDataQuarantines {
		if rejected.Kind != plugindata.RecordHost &&
			rejected.Kind != plugindata.RecordDataProfile {
			continue
		}
		state := stateFor(rejected.Kind, rejected.RecordID)
		state.entries[rejected.Quarantine.PluginID] = rejected.Quarantine
	}
	hosts := make([]hostSchema, 0, len(payload.Hosts))
	for _, value := range payload.Hosts {
		clean, entries, err := sanitizeHostPluginDataLenient(value)
		if err != nil {
			return err
		}
		model, err := hostModel(clean)
		if err != nil {
			return err
		}
		hosts = append(hosts, model)
		state := stateFor(plugindata.RecordHost, string(clean.ID))
		state.clean = clean.PluginData
		for _, entry := range entries {
			state.entries[entry.PluginID] = entry
		}
	}
	dataProfiles := make([]dataProfileSchema, 0, len(payload.DataProfiles))
	for _, value := range payload.DataProfiles {
		clean, entries, err := sanitizeDataProfilePluginDataLenient(value)
		if err != nil {
			return err
		}
		model, err := dataProfileModel(clean)
		if err != nil {
			return err
		}
		dataProfiles = append(dataProfiles, model)
		state := stateFor(plugindata.RecordDataProfile, clean.ID)
		state.clean = clean.PluginData
		for _, entry := range entries {
			state.entries[entry.PluginID] = entry
		}
	}
	groups := make([]hostGroupSchema, 0, len(payload.Groups))
	for _, value := range payload.Groups {
		model, err := groupModel(value)
		if err != nil {
			return err
		}
		groups = append(groups, model)
	}
	keys := make([]sshKeySchema, 0, len(payload.Keys))
	for _, value := range payload.Keys {
		model, err := keyModel(value)
		if err != nil {
			return err
		}
		keys = append(keys, model)
	}
	identities := make([]identitySchema, 0, len(payload.Identities))
	for _, value := range payload.Identities {
		model, err := identityModel(value)
		if err != nil {
			return err
		}
		identities = append(identities, model)
	}
	profiles := make([]proxyProfileSchema, 0, len(payload.ProxyProfiles))
	for _, value := range payload.ProxyProfiles {
		model, err := proxyProfileModel(value)
		if err != nil {
			return err
		}
		profiles = append(profiles, model)
	}
	networkProfiles := make([]networkProfileSchema, 0, len(payload.NetworkProfiles))
	for _, value := range payload.NetworkProfiles {
		model, err := networkProfileModel(value)
		if err != nil {
			return err
		}
		networkProfiles = append(networkProfiles, model)
	}
	rules := make([]forwardingRuleSchema, 0, len(payload.PortForwardingRules))
	for _, value := range payload.PortForwardingRules {
		model, err := forwardingRuleModel(value)
		if err != nil {
			return err
		}
		rules = append(rules, model)
	}
	snippets := make([]snippetSchema, 0, len(payload.Snippets))
	for _, value := range payload.Snippets {
		model, err := snippetModel(value)
		if err != nil {
			return err
		}
		snippets = append(snippets, model)
	}
	packages := make([]snippetPackageSchema, 0, len(payload.SnippetPackages))
	for _, value := range payload.SnippetPackages {
		model, err := snippetPackageModel(value)
		if err != nil {
			return err
		}
		packages = append(packages, model)
	}
	notes := make([]noteSchema, 0, len(payload.Notes))
	for _, value := range payload.Notes {
		model, err := noteModel(value)
		if err != nil {
			return err
		}
		notes = append(notes, model)
	}
	noteGroups := make([]noteGroupSchema, 0, len(payload.NoteGroups))
	for _, value := range payload.NoteGroups {
		model, err := noteGroupModel(value)
		if err != nil {
			return err
		}
		noteGroups = append(noteGroups, model)
	}
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		for _, model := range []any{
			&hostSchema{}, &hostGroupSchema{}, &sshKeySchema{}, &identitySchema{}, &proxyProfileSchema{},
			&networkProfileSchema{}, &dataProfileSchema{}, &forwardingRuleSchema{}, &snippetSchema{},
			&snippetPackageSchema{}, &noteSchema{}, &noteGroupSchema{},
		} {
			if err := deleteAll(tx, model); err != nil {
				return err
			}
		}
		for index := range hosts {
			if err := upsertModel(tx, &hosts[index], "id", "label", "hostname", "port", "username", "protocol", "group_id", "pinned", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range groups {
			if err := upsertModel(tx, &groups[index], "id", "parent_id", "name", "notes", "username", "port", "protocol", "auth_method", "identity_id", "key_id", "use_ssh_agent", "requires_mfa", "legacy_algorithms", "skip_ecdsa_host_key", "proxy_profile_id", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range keys {
			if err := upsertModel(tx, &keys[index], "id", "label", "type", "source", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range identities {
			if err := upsertModel(tx, &identities[index], "id", "label", "username", "auth_method", "key_id", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range profiles {
			if err := upsertModel(tx, &profiles[index], "id", "label", "type", "host", "port", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range networkProfiles {
			if err := upsertModel(tx, &networkProfiles[index], "id", "label", "kind", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range dataProfiles {
			if err := upsertModel(tx, &dataProfiles[index], "id", "provider", "label", "mode", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range rules {
			if err := upsertModel(tx, &rules[index], "id", "label", "type", "host_id", "bind_address", "local_port", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range snippets {
			if err := upsertModel(tx, &snippets[index], "id", "label", "package_path", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range packages {
			if err := upsertModel(tx, &packages[index], "path", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range notes {
			if err := upsertModel(tx, &notes[index], "id", "title", "group_path", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range noteGroups {
			if err := upsertModel(tx, &noteGroups[index], "path", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		return reconcileSyncQuarantines(tx, states)
	}); err != nil {
		return fmt.Errorf("store: apply cloud payload: %w", err)
	}
	s.vaultChanged()
	return nil
}

// reconcileSyncQuarantines updates quarantine rows for a whole sync apply. A
// quarantine row for a record the payload no longer contains is removed with
// that record; every record still present is reconciled through the ordinary
// per-record rule so a diagnostic for a namespace the record still lacks is
// preserved and a repaired namespace clears its row.
func reconcileSyncQuarantines(tx *gorm.DB, states map[string]*syncQuarantineState) error {
	var existing []pluginDataQuarantineSchema
	if err := tx.Find(&existing).Error; err != nil {
		return fmt.Errorf("store: read plugin data quarantine: %w", err)
	}
	for _, row := range existing {
		key := syncQuarantineKey(plugindata.RecordKind(row.RecordKind), row.RecordID)
		if _, ok := states[key]; ok {
			continue
		}
		if err := tx.Where("record_kind = ? AND record_id = ?",
			row.RecordKind, row.RecordID).
			Delete(&pluginDataQuarantineSchema{}).Error; err != nil {
			return fmt.Errorf("store: clear plugin data quarantine: %w", err)
		}
	}
	keys := make([]string, 0, len(states))
	for key := range states {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		state := states[key]
		entries := make([]plugindata.Quarantine, 0, len(state.entries))
		pluginIDs := make([]string, 0, len(state.entries))
		for pluginID := range state.entries {
			pluginIDs = append(pluginIDs, pluginID)
		}
		sort.Strings(pluginIDs)
		for _, pluginID := range pluginIDs {
			entries = append(entries, state.entries[pluginID])
		}
		if err := saveQuarantines(tx, state.kind, state.record, state.clean, entries); err != nil {
			return err
		}
	}
	return nil
}
