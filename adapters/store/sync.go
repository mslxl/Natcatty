package store

import (
	"context"
	"fmt"

	appsync "github.com/mslxl/natcatty/application/sync"
	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"
)

// syncedTables is every family the cloud payload replaces. Local-only tables
// (known_hosts, secret_values, crypto_keys, workspace, history, settings) are
// deliberately absent: applying a remote payload must not touch them.
var syncedTables = []string{
	"hosts",
	"host_groups",
	"ssh_keys",
	"identities",
	"proxy_profiles",
	"forwarding_rules",
	"snippets",
	"snippet_packages",
	"vault_notes",
	"note_groups",
}

// ApplySync replaces the synced Vault families in one transaction. A decoded
// remote payload is validated statement by statement before the transaction
// starts, so a malformed record cannot delete the local copy. Credentials are
// applied separately by the syncvault adapter; this method never touches the
// field-secret tables.
func (s *Store) ApplySync(ctx context.Context, payload appsync.Payload) error {
	statements := make([]platformsqlite.Statement, 0, 16+len(payload.Hosts)+len(payload.Keys))
	for _, table := range syncedTables {
		statements = append(statements, platformsqlite.Statement{SQL: "DELETE FROM " + quoteIdent(table)})
	}

	add := func(statement platformsqlite.Statement, err error) error {
		if err != nil {
			return err
		}
		statements = append(statements, statement)
		return nil
	}
	for _, host := range payload.Hosts {
		if err := add(hostSaveStatement(host)); err != nil {
			return err
		}
	}
	for _, group := range payload.Groups {
		if err := add(groupSaveStatement(group)); err != nil {
			return err
		}
	}
	for _, key := range payload.Keys {
		if err := add(keySaveStatement(key)); err != nil {
			return err
		}
	}
	for _, identity := range payload.Identities {
		if err := add(identitySaveStatement(identity)); err != nil {
			return err
		}
	}
	for _, profile := range payload.ProxyProfiles {
		if err := add(proxyProfileSaveStatement(profile)); err != nil {
			return err
		}
	}
	for _, rule := range payload.PortForwardingRules {
		if err := add(forwardingRuleSaveStatement(rule)); err != nil {
			return err
		}
	}
	for _, snippet := range payload.Snippets {
		if err := add(snippetSaveStatement(snippet)); err != nil {
			return err
		}
	}
	for _, pkg := range payload.SnippetPackages {
		if err := add(snippetPackageSaveStatement(pkg)); err != nil {
			return err
		}
	}
	for _, note := range payload.Notes {
		if err := add(noteSaveStatement(note)); err != nil {
			return err
		}
	}
	for _, group := range payload.NoteGroups {
		if err := add(noteGroupSaveStatement(group)); err != nil {
			return err
		}
	}
	if _, err := s.db.Transaction(ctx, statements); err != nil {
		return fmt.Errorf("store: apply cloud payload: %w", err)
	}
	s.vaultChanged()
	return nil
}
