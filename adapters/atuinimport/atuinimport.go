// Package atuinimport reads a copied Atuin client history database in
// read-only mode. It never contacts a Host and never writes the database: the
// caller copies history.db plus any WAL/SHM side files into a local temporary
// directory, and this adapter opens that copy with SQL query_only enabled.
//
// The adapter fails closed on an unknown schema and on a copy SQLite reports
// as inconsistent. It preserves every column it understands and returns the
// unknown columns separately, so the caller can label them instead of dropping
// data silently.
package atuinimport

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	// The pure-Go SQLite driver already used by the application's GORM
	// dialector. Importing it directly keeps the importer free of cgo.
	_ "github.com/glebarez/go-sqlite"

	domainhistory "github.com/mslxl/natcatty/domain/history"
)

var (
	// ErrUnknownSchema reports a database that is not an Atuin history
	// database this build can read. The import is skipped and retried later.
	ErrUnknownSchema = errors.New("atuinimport: unknown Atuin history schema")
	// ErrInconsistent reports a copy SQLite rejects or considers damaged. The
	// Host is never modified; a later Session retries the copy.
	ErrInconsistent = errors.New("atuinimport: inconsistent Atuin history copy")
)

// Reader reads Atuin history rows from a local database copy.
type Reader struct{}

// ReadHistory returns every history row ordered by (timestamp, id). An
// unknown schema, a failed integrity check, or a malformed row rejects the
// whole copy so no partial checkpoint is advanced.
func (Reader) ReadHistory(ctx context.Context, path string) ([]domainhistory.AtuinRow, error) {
	db, err := openReadOnly(ctx, path)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("atuinimport: begin read: %w", err)
	}
	defer tx.Rollback()

	if err := quickCheck(ctx, tx); err != nil {
		return nil, err
	}
	columns, err := tableColumns(ctx, tx)
	if err != nil {
		return nil, err
	}
	query, scan, err := buildQuery(columns)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("atuinimport: query history: %w", err)
	}
	defer rows.Close()

	out := make([]domainhistory.AtuinRow, 0)
	for rows.Next() {
		row, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("atuinimport: read history: %w", err)
	}
	return out, nil
}

// openReadOnly opens the local copy with SQLite's read-only URI mode. The
// copy lives in a private temporary directory owned by the importer, so WAL
// recovery cannot touch the Host.
func openReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: empty path", ErrInconsistent)
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	dsn := uri + "?mode=ro&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("atuinimport: open history: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: %v", ErrInconsistent, err)
	}
	return db, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func quickCheck(ctx context.Context, db queryer) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("%w: %v", ErrInconsistent, err)
	}
	if !strings.EqualFold(strings.TrimSpace(result), "ok") {
		return fmt.Errorf("%w: %s", ErrInconsistent, result)
	}
	return nil
}

// column is one row of PRAGMA table_info.
type column struct {
	name string
	kind string
}

// knownColumns is the mapping this build understands, in scan order. Optional
// columns that are absent are selected as NULL so the domain mapping can label
// them unknown.
var knownColumns = []string{
	"id", "timestamp", "duration", "exit", "command", "cwd", "session",
	"hostname", "author", "author_kind", "intent", "shell", "deleted_at",
}

// requiredColumns are the fields without which the copy is not a usable Atuin
// history database.
var requiredColumns = []string{"id", "timestamp", "command"}

func tableColumns(ctx context.Context, db queryer) ([]column, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info("history")`)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnknownSchema, err)
	}
	defer rows.Close()

	var columns []column
	for rows.Next() {
		var (
			index    int
			name     string
			kind     string
			notNull  int
			defaultV any
			primary  int
		)
		if err := rows.Scan(&index, &name, &kind, &notNull, &defaultV, &primary); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnknownSchema, err)
		}
		columns = append(columns, column{name: name, kind: strings.ToUpper(kind)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnknownSchema, err)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("%w: history table not found", ErrUnknownSchema)
	}
	present := make(map[string]bool, len(columns))
	for _, column := range columns {
		present[column.name] = true
	}
	for _, required := range requiredColumns {
		if !present[required] {
			return nil, fmt.Errorf("%w: history table has no %s column", ErrUnknownSchema, required)
		}
	}
	return columns, nil
}

// scanFunc fills one AtuinRow from the current row.
type scanFunc func(*sql.Rows) (domainhistory.AtuinRow, error)

// buildQuery projects the known columns in a stable order and appends every
// unknown column, which the caller preserves and labels.
func buildQuery(columns []column) (string, scanFunc, error) {
	present := make(map[string]bool, len(columns))
	for _, column := range columns {
		present[column.name] = true
	}
	known := make(map[string]bool, len(knownColumns))
	for _, name := range knownColumns {
		known[name] = true
	}
	var unknown []string
	for _, column := range columns {
		if !known[column.name] {
			unknown = append(unknown, column.name)
		}
	}
	sort.Strings(unknown)

	selects := make([]string, 0, len(knownColumns)+len(unknown))
	for _, name := range knownColumns {
		if present[name] {
			selects = append(selects, quoteIdentifier(name))
		} else {
			selects = append(selects, "NULL")
		}
	}
	for _, name := range unknown {
		selects = append(selects, quoteIdentifier(name))
	}
	query := "SELECT " + strings.Join(selects, ", ") +
		` FROM "history" ORDER BY "timestamp" ASC, "id" ASC`

	return query, func(rows *sql.Rows) (domainhistory.AtuinRow, error) {
		var (
			id         sql.NullString
			timestamp  sql.NullInt64
			duration   sql.NullInt64
			exit       sql.NullInt64
			command    sql.NullString
			cwd        sql.NullString
			session    sql.NullString
			hostname   sql.NullString
			author     sql.NullString
			authorKind sql.NullString
			intent     sql.NullString
			shell      sql.NullString
			deletedAt  sql.NullInt64
		)
		targets := []any{
			&id, &timestamp, &duration, &exit, &command, &cwd, &session,
			&hostname, &author, &authorKind, &intent, &shell, &deletedAt,
		}
		extras := make([]any, len(unknown))
		extraTargets := make([]any, len(unknown))
		for index := range extras {
			extraTargets[index] = &extras[index]
		}
		targets = append(targets, extraTargets...)
		if err := rows.Scan(targets...); err != nil {
			return domainhistory.AtuinRow{}, fmt.Errorf("%w: scan history row: %v", ErrInconsistent, err)
		}
		if !id.Valid || strings.TrimSpace(id.String) == "" {
			return domainhistory.AtuinRow{}, fmt.Errorf("%w: row has no history id", ErrInconsistent)
		}
		if !timestamp.Valid || timestamp.Int64 <= 0 {
			return domainhistory.AtuinRow{}, fmt.Errorf("%w: row %q has no timestamp", ErrInconsistent, id.String)
		}
		if !command.Valid || strings.TrimSpace(command.String) == "" {
			return domainhistory.AtuinRow{}, fmt.Errorf("%w: row %q has no command", ErrInconsistent, id.String)
		}
		row := domainhistory.AtuinRow{
			ID:        id.String,
			Timestamp: timestamp.Int64,
			Command:   command.String,
		}
		row.Duration = nullableInt(duration)
		row.Exit = nullableInt(exit)
		row.CWD = nullableString(cwd)
		row.Session = nullableString(session)
		row.Hostname = nullableString(hostname)
		row.Author = nullableString(author)
		row.AuthorKind = nullableString(authorKind)
		row.Intent = nullableString(intent)
		row.Shell = nullableString(shell)
		row.DeletedAt = nullableInt(deletedAt)
		if len(unknown) > 0 {
			row.Unknown = make(map[string]string, len(unknown))
			for index, name := range unknown {
				row.Unknown[name] = columnString(extras[index])
			}
		}
		return row, nil
	}, nil
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copied := value.String
	return &copied
}

func nullableInt(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copied := value.Int64
	return &copied
}

// columnString renders an unknown column's value. Text stays text, integers
// stay decimal, and non-UTF-8 blobs are base64-marked rather than corrupted.
func columnString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		if utf8.Valid(typed) {
			return string(typed)
		}
		return "base64:" + base64.StdEncoding.EncodeToString(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
