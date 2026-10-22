package store

import (
	"context"
	"fmt"
	"strings"

	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"
)

var _ appbackup.Snapshotter = (*Store)(nil)

// vaultColumns is the fixed column whitelist for every Vault table a backup
// carries. A restore builds its INSERT statements only from these names, so a
// tampered snapshot can never reach a column this build does not know about.
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

func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func selectSQL(table string) string {
	quoted := make([]string, len(vaultColumns[table]))
	for index, column := range vaultColumns[table] {
		quoted[index] = quoteIdent(column)
	}
	return "SELECT " + strings.Join(quoted, ", ") + " FROM " + quoteIdent(table) + " ORDER BY rowid"
}

// Snapshot reads every Vault table as one point-in-time image. A Consistent
// gate excludes writes for the whole sequence, so a mutation cannot land
// between two tables and produce a backup that never existed as a state.
func (s *Store) Snapshot(ctx context.Context) (domainbackup.Snapshot, error) {
	var snapshot domainbackup.Snapshot
	err := s.db.Consistent(ctx, func(ctx context.Context, query platformsqlite.QueryFunc) error {
		version, err := readSchemaVersion(ctx, query)
		if err != nil {
			return err
		}
		snapshot = domainbackup.Snapshot{
			SchemaVersion: version,
			Tables:        make([]domainbackup.Table, 0, len(domainbackup.Tables)),
		}
		for _, table := range domainbackup.Tables {
			rows, err := query(ctx, selectSQL(table))
			if err != nil {
				return fmt.Errorf("store: snapshot %s: %w", table, err)
			}
			columns := vaultColumns[table]
			tableRows := make([]domainbackup.Row, 0, len(rows.Values))
			for _, values := range rows.Values {
				if len(values) != len(columns) {
					return fmt.Errorf(
						"store: snapshot %s returned %d columns, want %d", table, len(values), len(columns))
				}
				row := make(domainbackup.Row, len(columns))
				for index, value := range values {
					converted, err := snapshotValue(value)
					if err != nil {
						return fmt.Errorf(
							"store: snapshot %s.%s: %w", table, columns[index], err)
					}
					row[columns[index]] = converted
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
		// Copy: the driver may reuse its buffer after the next query.
		blob := make([]byte, len(typed))
		copy(blob, typed)
		return domainbackup.Blob(blob), nil
	default:
		return domainbackup.Value{}, fmt.Errorf("unsupported value type %T", value)
	}
}

// Validate rejects a snapshot this build cannot restore. It requires every
// Vault table, because a missing table would otherwise survive a restore
// unchanged instead of being replaced by the backup.
func (s *Store) Validate(ctx context.Context, snapshot domainbackup.Snapshot) error {
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if snapshot.SchemaVersion != version {
		return fmt.Errorf("store: snapshot schema %d, database schema %d",
			snapshot.SchemaVersion, version)
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
				return fmt.Errorf("store: snapshot %s row has %d columns, want %d",
					table.Name, len(row), len(columns))
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

// Restore replaces every Vault table in one BEGIN IMMEDIATE transaction. A
// constraint or encoding failure anywhere rolls the whole transaction back, so
// the Vault keeps its previous bytes and stays usable.
func (s *Store) Restore(ctx context.Context, snapshot domainbackup.Snapshot) error {
	if err := s.Validate(ctx, snapshot); err != nil {
		return fmt.Errorf("%w: %v", appbackup.ErrIncompatibleSnapshot, err)
	}
	tables := make(map[string]domainbackup.Table, len(snapshot.Tables))
	for _, table := range snapshot.Tables {
		tables[table.Name] = table
	}
	// Deletes and inserts share one transaction, so a failure after any delete
	// still restores the pre-restore image.
	statements := make([]platformsqlite.Statement, 0, 16)
	for _, table := range domainbackup.Tables {
		statements = append(statements, platformsqlite.Statement{
			SQL: "DELETE FROM " + quoteIdent(table),
		})
	}
	for _, table := range domainbackup.Tables {
		columns := vaultColumns[table]
		placeholders := make([]string, len(columns))
		for index := range columns {
			placeholders[index] = "?"
		}
		insertPrefix := "INSERT INTO " + quoteIdent(table) + " (" +
			joinQuoted(columns) + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
		for _, row := range tables[table].Rows {
			args := make([]any, len(columns))
			for index, column := range columns {
				args[index] = row[column].Any()
			}
			statements = append(statements, platformsqlite.Statement{SQL: insertPrefix, Args: args})
		}
	}
	if _, err := s.db.Transaction(ctx, statements); err != nil {
		return fmt.Errorf("store: restore snapshot: %w", err)
	}
	return nil
}

func joinQuoted(columns []string) string {
	quoted := make([]string, len(columns))
	for index, column := range columns {
		quoted[index] = quoteIdent(column)
	}
	return strings.Join(quoted, ", ")
}
