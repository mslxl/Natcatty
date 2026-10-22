// Package sqlite wraps MyGo's SQLite plugin so adapters can use a database
// without importing MyGo. MyGo may be imported only by ui, platform, and app;
// this package is the platform seam that keeps that rule intact for the store
// adapter.
package sqlite

import (
	"context"
	"sync"
	"time"

	mygosqlite "github.com/egoist/mygo/plugins/sqlite"
)

// DB is one SQLite connection. It is safe from any goroutine; operations
// serialize on the connection.
type DB struct {
	db *mygosqlite.DB

	// gate excludes mutations and ordinary reads from a Consistent snapshot.
	// Ordinary operations take it shared; Consistent takes it exclusively
	// while it runs the caller's queries directly on the connection.
	gate sync.RWMutex
}

// Statement is one parameterized statement in a transaction.
type Statement = mygosqlite.Statement

// Rows holds columns and their values in order. Values are nil, int64,
// float64, string or []byte.
type Rows = mygosqlite.Rows

// Result describes a statement.
type Result = mygosqlite.Result

// QueryFunc runs one query inside a Consistent snapshot. The callback must use
// it instead of calling Query on the same DB, which would deadlock.
type QueryFunc func(ctx context.Context, sql string, args ...any) (Rows, error)

// OpenOptions configure a connection.
type OpenOptions struct {
	// BusyTimeout is how long an operation waits for another connection's
	// lock; five seconds when zero.
	BusyTimeout time.Duration
}

// Open opens path, creating a missing file. The parent directory must already
// exist. The caller closes the returned DB.
func Open(ctx context.Context, path string, opts OpenOptions) (*DB, error) {
	db, err := mygosqlite.Open(ctx, path, mygosqlite.OpenOptions{
		BusyTimeout: opts.BusyTimeout,
	})
	if err != nil {
		return nil, err
	}
	return &DB{db: db}, nil
}

// Execute runs exactly one statement with positional parameters.
func (d *DB) Execute(ctx context.Context, sql string, args ...any) (Result, error) {
	d.gate.RLock()
	defer d.gate.RUnlock()
	return d.db.Execute(ctx, sql, args...)
}

// Query runs exactly one statement and copies its rows.
func (d *DB) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	d.gate.RLock()
	defer d.gate.RUnlock()
	return d.db.Query(ctx, sql, args...)
}

// Transaction runs statements under BEGIN IMMEDIATE. Any error rolls back the
// whole batch.
func (d *DB) Transaction(ctx context.Context, statements []Statement) ([]Result, error) {
	d.gate.RLock()
	defer d.gate.RUnlock()
	return d.db.Transaction(ctx, statements)
}

// Consistent runs fn with exclusive access to the connection, so no Execute,
// Query, or Transaction interleaves with the queries fn runs through query.
// It exists for a logical snapshot assembled from several statements.
func (d *DB) Consistent(ctx context.Context, fn func(ctx context.Context, query QueryFunc) error) error {
	d.gate.Lock()
	defer d.gate.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx, d.db.Query)
}

// Close closes the connection. Repeated calls are harmless.
func (d *DB) Close() error {
	return d.db.Close()
}
