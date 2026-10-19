// Package store is the SQLite adapter for the Vault. One database lives in
// MyGo's user-data directory; this package owns its schema, migrations, and
// Host repository.
package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	appvault "github.com/mslxl/natcatty/application/vault"
	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"
)

// Store is the app's single SQLite connection. Its methods are safe from any
// goroutine; MyGo serializes operations on the connection.
type Store struct {
	db *platformsqlite.DB
}

var (
	_ appvault.HostRepository     = (*Store)(nil)
	_ appvault.KeyRepository      = (*Store)(nil)
	_ appvault.IdentityRepository = (*Store)(nil)
)

// Open opens path, configures the connection, and applies missing migrations.
// The caller closes the Store at app shutdown.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := platformsqlite.Open(ctx, path, platformsqlite.OpenOptions{BusyTimeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("store: open database: %w", err)
	}

	store := &Store{db: db}
	if err := store.configure(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(ctx, db, migrations); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// configure sets the pragmas the single-writer contract depends on. WAL is
// preferred; if the platform refuses it, the connection falls back to the
// rollback journal with the same serialized access.
func (s *Store) configure(ctx context.Context) error {
	// busy_timeout first, so a competing writer does not fail immediately.
	pragmas := []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA temp_store = MEMORY`,
	}
	for _, pragma := range pragmas {
		if _, err := s.db.Execute(ctx, pragma); err != nil {
			return fmt.Errorf("store: %s: %w", pragma, err)
		}
	}

	mode, err := s.journalMode(ctx)
	if err != nil {
		return err
	}
	if mode != "wal" {
		if _, err := s.db.Execute(ctx, `PRAGMA journal_mode = DELETE`); err != nil {
			return fmt.Errorf("store: enable rollback journal: %w", err)
		}
	}
	return nil
}

func (s *Store) journalMode(ctx context.Context) (string, error) {
	rows, err := s.db.Query(ctx, `PRAGMA journal_mode = WAL`)
	if err != nil {
		return "", fmt.Errorf("store: enable WAL: %w", err)
	}
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return "", fmt.Errorf("store: WAL pragma returned no mode")
	}
	mode, ok := rows.Values[0][0].(string)
	if !ok {
		return "", fmt.Errorf("store: WAL pragma returned %T", rows.Values[0][0])
	}
	return strings.ToLower(mode), nil
}

// SchemaVersion reports the applied migration version. schema_migrations is
// the authority; PRAGMA user_version is only a diagnostic mirror.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`)
	if err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return 0, nil
	}
	version, ok := rows.Values[0][0].(int64)
	if !ok {
		return 0, fmt.Errorf("store: schema version is %T", rows.Values[0][0])
	}
	return int(version), nil
}

// Close closes the database connection.
func (s *Store) Close() error { return s.db.Close() }
