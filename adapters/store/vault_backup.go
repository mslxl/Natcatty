package store

import (
	"context"
	"fmt"

	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
	"gorm.io/gorm"
)

var _ appbackup.Snapshotter = (*Store)(nil)

// vaultColumns is the fixed column whitelist for every Vault table a backup
// carries. A restore writes only these names, so a tampered snapshot cannot
// reach a column this build does not know about.
var vaultColumns = map[string][]string{
	domainbackup.TableHosts: {
		"id", "label", "hostname", "port", "username", "protocol",
		"group_path", "pinned", "order_key", "created_at", "updated_at", "data",
	},
	domainbackup.TableGroups: {
		"path", "notes", "username", "port", "protocol", "auth_method",
		"identity_id", "key_id", "use_ssh_agent", "requires_mfa",
		"legacy_algorithms", "skip_ecdsa_host_key", "proxy_profile_id",
		"order_key", "created_at", "updated_at", "data",
	},
	domainbackup.TableKeys: {
		"id", "label", "type", "source", "order_key", "created_at", "updated_at", "data",
	},
	domainbackup.TableIdentities: {
		"id", "label", "username", "auth_method", "key_id", "order_key",
		"created_at", "updated_at", "data",
	},
	domainbackup.TableProxyProfiles: {
		"id", "label", "type", "host", "port", "order_key", "created_at", "updated_at", "data",
	},
	domainbackup.TableKnownHosts: {
		"id", "hostname", "port", "key_type", "fingerprint", "public_key",
		"discovered_at", "last_seen", "converted_to_host_id", "order_key", "data",
	},
	domainbackup.TableForwardingRules: {
		"id", "label", "type", "host_id", "bind_address", "local_port",
		"order_key", "created_at", "updated_at", "data",
	},
	domainbackup.TableSnippets: {
		"id", "label", "package_path", "order_key", "created_at", "updated_at", "data",
	},
	domainbackup.TableSnippetPackages: {
		"path", "order_key", "created_at", "updated_at", "data",
	},
	domainbackup.TableNotes: {
		"id", "title", "group_path", "order_key", "created_at", "updated_at", "data",
	},
	domainbackup.TableNoteGroups: {
		"path", "order_key", "created_at", "updated_at", "data",
	},
	domainbackup.TableSecrets: {
		"entity_type", "entity_id", "field", "alg", "key_version",
		"nonce", "ciphertext", "updated_at",
	},
	domainbackup.TableCryptoKeys: {
		"version", "purpose", "created_at", "retired_at", "wrapped_key",
		"wrap_nonce", "wrap_kdf", "wrap_salt", "wrap_time", "wrap_memory",
		"wrap_parallelism",
	},
}

// Snapshot reads every Vault table as one point-in-time image. GORM's query
// builder loads the whitelisted physical columns while the platform gate keeps
// the sequence consistent across tables.
func (s *Store) Snapshot(ctx context.Context) (domainbackup.Snapshot, error) {
	var snapshot domainbackup.Snapshot
	err := s.db.ORMConsistent(ctx, func(tx *gorm.DB) error {
		snapshot = domainbackup.Snapshot{
			SchemaVersion: schemaVersion,
			Tables:        make([]domainbackup.Table, 0, len(domainbackup.Tables)),
		}
		for _, table := range domainbackup.Tables {
			columns := vaultColumns[table]
			var rows []map[string]any
			if err := tx.Table(table).Select(columns).Order("rowid").Find(&rows).Error; err != nil {
				return fmt.Errorf("store: snapshot %s: %w", table, err)
			}
			tableRows := make([]domainbackup.Row, 0, len(rows))
			for _, values := range rows {
				row := make(domainbackup.Row, len(columns))
				for _, column := range columns {
					converted, err := snapshotValue(values[column])
					if err != nil {
						return fmt.Errorf("store: snapshot %s.%s: %w", table, column, err)
					}
					row[column] = converted
				}
				tableRows = append(tableRows, row)
			}
			snapshot.Tables = append(snapshot.Tables, domainbackup.Table{Name: table, Rows: tableRows})
		}
		return nil
	})
	if err != nil {
		return domainbackup.Snapshot{}, err
	}
	return snapshot, nil
}

func snapshotValue(value any) (domainbackup.Value, error) {
	switch typed := value.(type) {
	case nil:
		return domainbackup.Null(), nil
	case int64:
		return domainbackup.Int(typed), nil
	case float64:
		return domainbackup.Float(typed), nil
	case string:
		return domainbackup.Text(typed), nil
	case []byte:
		blob := make([]byte, len(typed))
		copy(blob, typed)
		return domainbackup.Blob(blob), nil
	default:
		return domainbackup.Value{}, fmt.Errorf("unsupported value type %T", value)
	}
}

// Validate rejects a snapshot this build cannot restore. It requires every
// Vault table, because a missing table would otherwise survive a restore.
func (s *Store) Validate(ctx context.Context, snapshot domainbackup.Snapshot) error {
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if snapshot.SchemaVersion != version {
		return fmt.Errorf("store: snapshot schema %d, database schema %d", snapshot.SchemaVersion, version)
	}
	if err := domainbackup.Validate(snapshot); err != nil {
		return err
	}
	seen := make(map[string]bool, len(snapshot.Tables))
	for _, table := range snapshot.Tables {
		seen[table.Name] = true
		columns := vaultColumns[table.Name]
		allowed := make(map[string]bool, len(columns))
		for _, column := range columns {
			allowed[column] = true
		}
		for _, row := range table.Rows {
			if len(row) != len(columns) {
				return fmt.Errorf("store: snapshot %s row has %d columns, want %d", table.Name, len(row), len(columns))
			}
			for column := range row {
				if !allowed[column] {
					return fmt.Errorf("store: snapshot %s has unknown column %q", table.Name, column)
				}
			}
		}
	}
	for _, table := range domainbackup.Tables {
		if !seen[table] {
			return fmt.Errorf("store: snapshot is missing table %s", table)
		}
	}
	return nil
}

// Restore replaces every Vault table in one ORM transaction. A constraint or
// encoding failure rolls the whole transaction back.
func (s *Store) Restore(ctx context.Context, snapshot domainbackup.Snapshot) error {
	if err := s.Validate(ctx, snapshot); err != nil {
		return fmt.Errorf("%w: %v", appbackup.ErrIncompatibleSnapshot, err)
	}
	tables := make(map[string]domainbackup.Table, len(snapshot.Tables))
	for _, table := range snapshot.Tables {
		tables[table.Name] = table
	}
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		for _, table := range domainbackup.Tables {
			if err := deleteAll(tx, backupTableModel(table)); err != nil {
				return err
			}
		}
		for _, table := range domainbackup.Tables {
			columns := vaultColumns[table]
			for _, row := range tables[table].Rows {
				values := make(map[string]any, len(columns))
				for _, column := range columns {
					values[column] = row[column].Any()
				}
				if err := tx.Table(table).Create(values).Error; err != nil {
					return fmt.Errorf("restore %s: %w", table, err)
				}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("store: restore snapshot: %w", err)
	}
	return nil
}

func backupTableModel(table string) any {
	switch table {
	case domainbackup.TableHosts:
		return &hostSchema{}
	case domainbackup.TableGroups:
		return &hostGroupSchema{}
	case domainbackup.TableKeys:
		return &sshKeySchema{}
	case domainbackup.TableIdentities:
		return &identitySchema{}
	case domainbackup.TableProxyProfiles:
		return &proxyProfileSchema{}
	case domainbackup.TableKnownHosts:
		return &knownHostSchema{}
	case domainbackup.TableForwardingRules:
		return &forwardingRuleSchema{}
	case domainbackup.TableSnippets:
		return &snippetSchema{}
	case domainbackup.TableSnippetPackages:
		return &snippetPackageSchema{}
	case domainbackup.TableNotes:
		return &noteSchema{}
	case domainbackup.TableNoteGroups:
		return &noteGroupSchema{}
	case domainbackup.TableSecrets:
		return &secretValueSchema{}
	case domainbackup.TableCryptoKeys:
		return &cryptoKeySchema{}
	default:
		panic("store: unknown backup table " + table)
	}
}
