package store

import (
	"context"
	"fmt"

	appsync "github.com/mslxl/natcatty/application/sync"
	"gorm.io/gorm"
)

// ApplySync replaces the synced Vault families in one ORM transaction. Local
// tables such as secrets, workspace, and history are deliberately untouched.
func (s *Store) ApplySync(ctx context.Context, payload appsync.Payload) error {
	hosts := make([]hostSchema, 0, len(payload.Hosts))
	for _, value := range payload.Hosts {
		model, err := hostModel(value)
		if err != nil {
			return err
		}
		hosts = append(hosts, model)
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
			&forwardingRuleSchema{}, &snippetSchema{}, &snippetPackageSchema{}, &noteSchema{}, &noteGroupSchema{},
		} {
			if err := deleteAll(tx, model); err != nil {
				return err
			}
		}
		for index := range hosts {
			if err := upsertModel(tx, &hosts[index], "id", "label", "hostname", "port", "username", "protocol", "group_path", "pinned", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range groups {
			if err := upsertModel(tx, &groups[index], "path", "notes", "username", "port", "protocol", "auth_method", "identity_id", "key_id", "use_ssh_agent", "requires_mfa", "legacy_algorithms", "skip_ecdsa_host_key", "proxy_profile_id", "order_key", "updated_at", "data"); err != nil {
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
		return nil
	}); err != nil {
		return fmt.Errorf("store: apply cloud payload: %w", err)
	}
	s.vaultChanged()
	return nil
}
