// Package sqlite wraps GORM's pure-Go SQLite dialector so adapters can use a
// database without depending on the ORM or driver details.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	gormsqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB is one SQLite connection. It is safe from any goroutine; operations
// serialize on the connection.
type DB struct {
	gorm *gorm.DB
	sql  *sql.DB

	// gate excludes mutations and ordinary reads from a Consistent snapshot.
	// Ordinary operations take it shared; Consistent takes it exclusively
	// while it runs the caller's queries directly on the connection.
	gate sync.RWMutex
}

// Statement is one parameterized statement in a transaction.
type Statement struct {
	SQL  string
	Args []any
}

// Rows holds columns and their values in order. Values are nil, int64,
// float64, string or []byte.
type Rows struct {
	Columns []string
	Values  [][]any
}

// Result describes a statement.
type Result struct {
	Changes      int64
	LastInsertID int64
}

// QueryFunc runs one query inside a Consistent snapshot. The callback must use
// it instead of calling Query on the same DB, which would deadlock.
type QueryFunc func(ctx context.Context, sql string, args ...any) (Rows, error)

// ORMFunc runs a GORM operation while holding the database's shared gate.
// Store repositories use it for every data operation so snapshots cannot
// interleave with writes.
type ORMFunc func(*gorm.DB) error

// OpenOptions configure a connection.
type OpenOptions struct {
	// BusyTimeout is how long an operation waits for another connection's
	// lock; five seconds when zero.
	BusyTimeout time.Duration
}

// Open opens path, creating a missing file. The parent directory must already
// exist. The caller closes the returned DB.
func Open(ctx context.Context, path string, opts OpenOptions) (*DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gormDB, err := gorm.Open(gormsqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, err
	}
	// Store owns one logical connection. Keeping the pool at one connection
	// preserves the snapshot and BEGIN IMMEDIATE semantics of the old seam.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)
	db := &DB{
		gorm: gormDB,
		sql:  sqlDB,
	}
	busyTimeout := opts.BusyTimeout
	if busyTimeout == 0 {
		busyTimeout = 5 * time.Second
	}
	milliseconds := busyTimeout.Milliseconds()
	if milliseconds < 0 {
		milliseconds = 0
	}
	if _, err := db.Execute(ctx, fmt.Sprintf(`PRAGMA busy_timeout = %d`, milliseconds)); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// AutoMigrate applies GORM's schema description to the database.
func (d *DB) AutoMigrate(ctx context.Context, models ...any) error {
	return d.gorm.WithContext(ctx).AutoMigrate(models...)
}

// Configure applies the SQLite pragmas required by the single-connection
// store. These are driver-level settings rather than ORM data operations.
func (d *DB) Configure(ctx context.Context) error {
	for _, pragma := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA temp_store = MEMORY`,
	} {
		if _, err := d.Execute(ctx, pragma); err != nil {
			return fmt.Errorf("sqlite: %s: %w", pragma, err)
		}
	}
	rows, err := d.Query(ctx, `PRAGMA journal_mode = WAL`)
	if err != nil {
		return fmt.Errorf("sqlite: enable WAL: %w", err)
	}
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return fmt.Errorf("sqlite: WAL pragma returned no mode")
	}
	mode, ok := rows.Values[0][0].(string)
	if !ok {
		return fmt.Errorf("sqlite: WAL pragma returned %T", rows.Values[0][0])
	}
	if mode != "wal" {
		if _, err := d.Execute(ctx, `PRAGMA journal_mode = DELETE`); err != nil {
			return fmt.Errorf("sqlite: enable rollback journal: %w", err)
		}
	}
	return nil
}

// JournalMode reports the active SQLite journal mode for diagnostics.
func (d *DB) JournalMode(ctx context.Context) (string, error) {
	rows, err := d.Query(ctx, `PRAGMA journal_mode`)
	if err != nil {
		return "", err
	}
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return "", fmt.Errorf("sqlite: journal mode pragma returned no mode")
	}
	mode, ok := rows.Values[0][0].(string)
	if !ok {
		return "", fmt.Errorf("sqlite: journal mode pragma returned %T", rows.Values[0][0])
	}
	return mode, nil
}

// ORM runs one GORM operation under the same gate as the legacy database
// facade. The callback must not call ORM again on this DB.
func (d *DB) ORM(ctx context.Context, fn ORMFunc) error {
	d.gate.RLock()
	defer d.gate.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(d.gorm.WithContext(ctx))
}

// ORMTransaction runs one GORM transaction under the database gate.
func (d *DB) ORMTransaction(ctx context.Context, fn func(*gorm.DB) error) error {
	d.gate.RLock()
	defer d.gate.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.gorm.WithContext(ctx).Transaction(fn)
}

// ORMConsistent runs GORM reads with exclusive access to the connection.
func (d *DB) ORMConsistent(ctx context.Context, fn func(*gorm.DB) error) error {
	d.gate.Lock()
	defer d.gate.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(d.gorm.WithContext(ctx))
}

func (d *DB) execute(ctx context.Context, sqlText string, args ...any) (Result, error) {
	result, err := d.sql.ExecContext(ctx, sqlText, args...)
	if err != nil {
		return Result{}, err
	}
	changes, err := result.RowsAffected()
	if err != nil {
		return Result{}, err
	}
	lastInsertID, err := result.LastInsertId()
	if err != nil {
		return Result{}, err
	}
	return Result{Changes: changes, LastInsertID: lastInsertID}, nil
}

func (d *DB) query(ctx context.Context, sqlText string, args ...any) (Rows, error) {
	rows, err := d.sql.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return Rows{}, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return Rows{}, err
	}
	values := make([][]any, 0)
	for rows.Next() {
		raw := make([]any, len(columns))
		dest := make([]any, len(columns))
		for index := range raw {
			dest[index] = &raw[index]
		}
		if err := rows.Scan(dest...); err != nil {
			return Rows{}, err
		}
		values = append(values, normalizeRow(raw))
	}
	if err := rows.Err(); err != nil {
		return Rows{}, err
	}
	return Rows{Columns: columns, Values: values}, nil
}

func normalizeRow(raw []any) []any {
	row := make([]any, len(raw))
	for index, value := range raw {
		switch typed := value.(type) {
		case []byte:
			copied := make([]byte, len(typed))
			copy(copied, typed)
			row[index] = copied
		default:
			row[index] = typed
		}
	}
	return row
}

func (d *DB) transaction(ctx context.Context, statements []Statement) ([]Result, error) {
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	results := make([]Result, 0, len(statements))
	for index, statement := range statements {
		result, err := conn.ExecContext(ctx, statement.SQL, statement.Args...)
		if err != nil {
			return nil, fmt.Errorf("sqlite: statement %d: %w", index+1, err)
		}
		changes, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		lastInsertID, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		results = append(results, Result{Changes: changes, LastInsertID: lastInsertID})
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	committed = true
	return results, nil
}

func (d *DB) Execute(ctx context.Context, sqlText string, args ...any) (Result, error) {
	d.gate.RLock()
	defer d.gate.RUnlock()
	return d.execute(ctx, sqlText, args...)
}

// Query runs exactly one statement and copies its rows.
func (d *DB) Query(ctx context.Context, sqlText string, args ...any) (Rows, error) {
	d.gate.RLock()
	defer d.gate.RUnlock()
	return d.query(ctx, sqlText, args...)
}

// Transaction runs statements under BEGIN IMMEDIATE. Any error rolls back the
// whole batch.
func (d *DB) Transaction(ctx context.Context, statements []Statement) ([]Result, error) {
	d.gate.RLock()
	defer d.gate.RUnlock()
	return d.transaction(ctx, statements)
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
	return fn(ctx, d.query)
}

// Close closes the connection. Repeated calls are harmless.
func (d *DB) Close() error {
	if d.sql == nil {
		return nil
	}
	return d.sql.Close()
}
