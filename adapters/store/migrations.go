package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migration is one forward-only schema batch. Its statements and its
// schema_migrations row run in one transaction, so a failure applies none of
// them.
type migration struct {
	Version    int
	Statements []string
}

// migrations is the embedded, ordered migration list. Adding a table means
// adding a file with the next version, never editing an applied one.
var migrations = loadMigrations()

func loadMigrations() []migration {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		panic(err)
	}
	sort.Strings(names)

	list := make([]migration, 0, len(names))
	for _, name := range names {
		base := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		prefix, _, ok := strings.Cut(base, "_")
		if !ok {
			panic("store: migration " + name + " has no version prefix")
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			panic("store: migration " + name + " has a non-numeric version")
		}
		data, err := migrationFiles.ReadFile(name)
		if err != nil {
			panic(err)
		}
		list = append(list, migration{Version: version, Statements: splitStatements(string(data))})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Version < list[j].Version })
	return list
}

// splitStatements splits a migration file into statements. Comments are
// removed first, so a semicolon in prose cannot split a statement. The schema
// has no semicolons inside string literals, so a semicolon is a safe
// separator for the remainder.
func splitStatements(source string) []string {
	parts := strings.Split(stripCommentLines(source), ";")
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		statement := strings.TrimSpace(part)
		if statement == "" {
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}

func stripCommentLines(source string) string {
	lines := strings.Split(source, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func latestMigrationVersion() int {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].Version
}

// migrate applies every migration newer than the recorded version, one
// transaction per version. Re-running is a no-op.
func migrate(ctx context.Context, db *platformsqlite.DB, list []migration) error {
	const createMigrations = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`
	if _, err := db.Execute(ctx, createMigrations); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	rows, err := db.Query(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	current := int(rows.Values[0][0].(int64))

	for _, item := range list {
		if item.Version <= current {
			continue
		}
		statements := make([]platformsqlite.Statement, 0, len(item.Statements)+1)
		for _, sql := range item.Statements {
			statements = append(statements, platformsqlite.Statement{SQL: sql})
		}
		statements = append(statements, platformsqlite.Statement{
			SQL:  `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			Args: []any{item.Version, time.Now().UnixMilli()},
		})
		// user_version is advisory, for diagnostics and other SQLite tools;
		// schema_migrations is the authority. Set it in the same transaction so
		// the two can never disagree.
		statements = append(statements, platformsqlite.Statement{
			SQL: fmt.Sprintf("PRAGMA user_version = %d", item.Version),
		})
		if _, err := db.Transaction(ctx, statements); err != nil {
			return fmt.Errorf("store: migration %d: %w", item.Version, err)
		}
		current = item.Version
	}
	return nil
}
