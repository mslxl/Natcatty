// Package store is the SQLite adapter for the Vault. One database lives in the
// app's user-data directory; this package owns its schema and repositories.
package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	appvault "github.com/mslxl/natcatty/application/vault"
	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"
)

// Store is the app's single SQLite connection. Its methods are safe from any
// goroutine; the platform database seam serializes operations on the
// connection.
type Store struct {
	db *platformsqlite.DB

	changeMu sync.Mutex
	onChange func()
}

// SetChangeHook installs the callback that runs after a Vault-mutating write
// commits. The local-backup service uses it to queue an automatic backup; the
// hook must not block. It is never called for workspace, history, terminal
// settings, or a restore.
func (s *Store) SetChangeHook(fn func()) {
	s.changeMu.Lock()
	s.onChange = fn
	s.changeMu.Unlock()
}

func (s *Store) vaultChanged() {
	s.changeMu.Lock()
	fn := s.onChange
	s.changeMu.Unlock()
	if fn != nil {
		fn()
	}
}

var (
	_ appvault.HostRepository         = (*Store)(nil)
	_ appvault.KeyRepository          = (*Store)(nil)
	_ appvault.IdentityRepository     = (*Store)(nil)
	_ appvault.GroupRepository        = (*Store)(nil)
	_ appvault.ProxyProfileRepository = (*Store)(nil)
	_ appvault.SnippetRepository      = (*Store)(nil)
)

// schemaVersion identifies the AutoMigrate schema used by this build. It is
// kept in backup metadata even though the schema is no longer migration-file
// driven.
const schemaVersion = 1

// Open opens path, configures the connection, and applies the schema declared
// by the GORM models.
// The caller closes the Store at app shutdown.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := platformsqlite.Open(ctx, path, platformsqlite.OpenOptions{BusyTimeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("store: open database: %w", err)
	}

	store := &Store{db: db}
	if err := db.Configure(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.AutoMigrate(ctx, schemaModels...); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: auto-migrate schema: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// SchemaVersion reports the AutoMigrate schema version carried in backups.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return schemaVersion, nil
}

func (s *Store) journalMode(ctx context.Context) (string, error) {
	return s.db.JournalMode(ctx)
}

// Close closes the database connection.
func (s *Store) Close() error { return s.db.Close() }
