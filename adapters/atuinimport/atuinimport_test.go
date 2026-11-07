package atuinimport

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const fullSchema = `CREATE TABLE history (
	id TEXT PRIMARY KEY,
	timestamp INTEGER NOT NULL,
	duration INTEGER,
	exit INTEGER,
	command TEXT NOT NULL,
	cwd TEXT,
	session TEXT,
	hostname TEXT,
	author TEXT,
	author_kind TEXT,
	intent TEXT,
	shell TEXT,
	deleted_at INTEGER,
	extra_note TEXT
)`

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("ping %s: %v", path, err)
	}
	return db
}

func createHistoryDB(t *testing.T, dir, schema string, statements ...string) string {
	t.Helper()
	path := filepath.Join(dir, "history.db")
	db := openTestDB(t, path)
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("insert %q: %v", statement, err)
		}
	}
	return path
}

func TestReadHistoryMapsEveryKnownColumnAndPreservesUnknownOnes(t *testing.T) {
	path := createHistoryDB(t, t.TempDir(), fullSchema,
		`INSERT INTO history (id, timestamp, duration, exit, command, cwd, session,
			hostname, author, author_kind, intent, shell, deleted_at, extra_note)
		 VALUES ('9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d', 200, 250, 7, 'ls -la',
			'/srv/app', '0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e0f', 'web-1.example.test',
			'deploy', 'user', 'inspect', 'zsh', 300, 'carried')`,
		`INSERT INTO history (id, timestamp, command) VALUES ('early', 100, 'uptime')`,
	)

	rows, err := (Reader{}).ReadHistory(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Rows are ordered by (timestamp, id), not by insertion.
	if rows[0].ID != "early" || rows[1].ID != "9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d" {
		t.Fatalf("order = %q, %q", rows[0].ID, rows[1].ID)
	}
	full := rows[1]
	if full.Timestamp != 200 || full.Command != "ls -la" {
		t.Fatalf("required fields = %+v", full)
	}
	if full.Duration == nil || *full.Duration != 250 {
		t.Fatalf("duration = %v", full.Duration)
	}
	if full.Exit == nil || *full.Exit != 7 {
		t.Fatalf("exit = %v", full.Exit)
	}
	if full.CWD == nil || *full.CWD != "/srv/app" || full.Shell == nil || *full.Shell != "zsh" {
		t.Fatalf("cwd/shell = %v/%v", full.CWD, full.Shell)
	}
	if full.Session == nil || *full.Session != "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e0f" ||
		full.Hostname == nil || *full.Hostname != "web-1.example.test" {
		t.Fatalf("session/hostname = %v/%v", full.Session, full.Hostname)
	}
	if full.Author == nil || *full.Author != "deploy" ||
		full.AuthorKind == nil || *full.AuthorKind != "user" ||
		full.Intent == nil || *full.Intent != "inspect" {
		t.Fatalf("author/intent = %+v", full)
	}
	if full.DeletedAt == nil || *full.DeletedAt != 300 {
		t.Fatalf("deleted at = %v", full.DeletedAt)
	}
	if !reflect.DeepEqual(full.Unknown, map[string]string{"extra_note": "carried"}) {
		t.Fatalf("unknown columns = %+v", full.Unknown)
	}

	minimal := rows[0]
	if minimal.Duration != nil || minimal.Exit != nil || minimal.CWD != nil ||
		minimal.Session != nil || minimal.Hostname != nil || minimal.Author != nil ||
		minimal.AuthorKind != nil || minimal.Intent != nil || minimal.Shell != nil ||
		minimal.DeletedAt != nil {
		t.Fatalf("NULL optional columns became values: %+v", minimal)
	}
	if minimal.Unknown["extra_note"] != "" || len(minimal.Unknown) != 1 {
		t.Fatalf("unknown columns = %+v", minimal.Unknown)
	}
}

func TestReadHistoryAcceptsADatabaseWithOnlyRequiredColumns(t *testing.T) {
	path := createHistoryDB(t, t.TempDir(),
		`CREATE TABLE history (id TEXT PRIMARY KEY, timestamp INTEGER NOT NULL, command TEXT NOT NULL)`,
		`INSERT INTO history VALUES ('id-1', 100, 'pwd')`)

	rows, err := (Reader{}).ReadHistory(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "id-1" || rows[0].Command != "pwd" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Unknown != nil {
		t.Fatalf("a minimal schema produced unknown columns: %+v", rows[0].Unknown)
	}
}

func TestReadHistoryFailsClosedOnAnUnknownSchema(t *testing.T) {
	tests := []struct {
		name   string
		schema string
	}{
		{name: "no history table", schema: `CREATE TABLE other (id TEXT)`},
		{name: "no command column", schema: `CREATE TABLE history (id TEXT, timestamp INTEGER)`},
		{name: "no timestamp column", schema: `CREATE TABLE history (id TEXT, command TEXT)`},
	}
	for _, test := range tests {
		dir := t.TempDir()
		path := createHistoryDB(t, dir, test.schema)
		_, err := (Reader{}).ReadHistory(context.Background(), path)
		if !errors.Is(err, ErrUnknownSchema) {
			t.Errorf("%s: error = %v, want ErrUnknownSchema", test.name, err)
		}
	}
}

func TestReadHistoryRejectsAMalformedRowInsteadOfDroppingIt(t *testing.T) {
	tests := []struct {
		name      string
		statement string
	}{
		{name: "empty command", statement: `INSERT INTO history VALUES ('id-1', 100, '  ')`},
		{name: "null command", statement: `INSERT INTO history VALUES ('id-1', 100, NULL)`},
		{name: "null timestamp", statement: `INSERT INTO history VALUES ('id-1', NULL, 'ls')`},
		{name: "zero timestamp", statement: `INSERT INTO history VALUES ('id-1', 0, 'ls')`},
		{name: "empty id", statement: `INSERT INTO history VALUES ('', 100, 'ls')`},
	}
	for _, test := range tests {
		dir := t.TempDir()
		path := createHistoryDB(t, dir,
			`CREATE TABLE history (id TEXT, timestamp INTEGER, command TEXT)`,
			test.statement)
		_, err := (Reader{}).ReadHistory(context.Background(), path)
		if !errors.Is(err, ErrInconsistent) {
			t.Errorf("%s: error = %v, want ErrInconsistent", test.name, err)
		}
	}
}

func TestReadHistoryRejectsAMissingFile(t *testing.T) {
	_, err := (Reader{}).ReadHistory(context.Background(), filepath.Join(t.TempDir(), "missing", "history.db"))
	if err == nil {
		t.Fatal("a missing database was accepted")
	}
}

// A live Atuin keeps recent rows in history.db-wal until a checkpoint. The
// importer's copy includes the side files, so the read sees those rows too.
func TestReadHistorySeesRowsStillInTheWAL(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	db := openTestDB(t, source)
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE history (id TEXT PRIMARY KEY, timestamp INTEGER NOT NULL, command TEXT NOT NULL)`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO history VALUES ('id-1', 100, 'from-wal')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// The writer stays open, so SQLite has not checkpointed the row into the
	// main database. The side files must exist and carry it.
	for _, name := range []string{"source.db-wal", "source.db-shm"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Size() == 0 {
			t.Fatalf("%s = %v, %v; want a non-empty side file", name, info, err)
		}
	}

	copyDir := t.TempDir()
	for _, name := range []string{"source.db", "source.db-wal", "source.db-shm"} {
		copyFile(t, filepath.Join(dir, name), filepath.Join(copyDir, "history.db"+suffix(name)))
	}

	rows, err := (Reader{}).ReadHistory(context.Background(), filepath.Join(copyDir, "history.db"))
	if err != nil {
		t.Fatalf("ReadHistory with WAL: %v", err)
	}
	if len(rows) != 1 || rows[0].Command != "from-wal" {
		t.Fatalf("rows = %+v, want the WAL-resident row", rows)
	}
}

func suffix(name string) string {
	if name == "source.db" {
		return ""
	}
	return name[len("source.db"):]
}

func copyFile(t *testing.T, source, target string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatalf("open %s: %v", source, err)
	}
	defer in.Close()
	out, err := os.Create(target)
	if err != nil {
		t.Fatalf("create %s: %v", target, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("copy %s: %v", source, err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close %s: %v", target, err)
	}
}
