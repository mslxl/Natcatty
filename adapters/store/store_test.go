package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "natcatty.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func TestOpenCreatesAndWritesASchemaVersion(t *testing.T) {
	store, _ := openTestStore(t)

	version, err := store.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != latestMigrationVersion() {
		t.Fatalf("schema version = %d, want %d", version, latestMigrationVersion())
	}
	rows, err := store.db.Query(context.Background(), `PRAGMA user_version`)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if got := rows.Values[0][0].(int64); got != int64(latestMigrationVersion()) {
		t.Fatalf("user_version = %d, want %d", got, latestMigrationVersion())
	}

	mode, err := store.journalMode(context.Background())
	if err != nil {
		t.Fatalf("journal mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal mode = %q, want wal", mode)
	}
}

func TestOpeningAnExistingDatabaseDoesNotReapplyMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "natcatty.db")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	version, err := second.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != latestMigrationVersion() {
		t.Fatalf("schema version = %d, want %d", version, latestMigrationVersion())
	}
	rows, err := second.db.Query(context.Background(), `SELECT COUNT(*) FROM schema_migrations`)
	if err != nil {
		t.Fatal(err)
	}
	if got := rows.Values[0][0].(int64); got != int64(latestMigrationVersion()) {
		t.Fatalf("migration rows = %d, want %d", got, latestMigrationVersion())
	}
}

func TestFailedMigrationRollsBackItsBatch(t *testing.T) {
	db, err := platformsqlite.Open(context.Background(), ":memory:", platformsqlite.OpenOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	migrations := []migration{
		{Version: 1, Statements: []string{"CREATE TABLE applied (id TEXT);"}},
		{Version: 2, Statements: []string{
			"CREATE TABLE partial (id TEXT);",
			"INSERT INTO missing_table (id) VALUES ('x');",
		}},
	}
	if err := migrate(context.Background(), db, migrations); err == nil {
		t.Fatal("migrate succeeded, want the broken batch to fail")
	}

	rows, err := db.Query(context.Background(), `SELECT MAX(version) FROM schema_migrations`)
	if err != nil {
		t.Fatal(err)
	}
	if got := rows.Values[0][0].(int64); got != 1 {
		t.Fatalf("migration version = %d, want 1", got)
	}
	if _, err := db.Query(context.Background(), `SELECT * FROM applied`); err != nil {
		t.Fatalf("migration 1 was rolled back: %v", err)
	}
	if _, err := db.Query(context.Background(), `SELECT * FROM partial`); err == nil {
		t.Fatal("migration 2's first statement survived the rollback")
	}
	version, err := db.Query(context.Background(), `PRAGMA user_version`)
	if err != nil {
		t.Fatal(err)
	}
	if got := version.Values[0][0].(int64); got != 1 {
		t.Fatalf("user_version = %d, want 1", got)
	}
}

func TestHostsSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "natcatty.db")

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	host := domainvault.Host{
		ID:        "01HOST",
		Label:     "web",
		Hostname:  "example.com",
		Port:      22,
		Username:  "root",
		Protocol:  domainvault.ProtocolSSH,
		Group:     "prod/web",
		Tags:      []string{"prod", "web"},
		Notes:     "primary",
		Pinned:    true,
		OrderKey:  1000,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := first.Save(ctx, host); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	got, err := second.Get(ctx, host.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != host.ID || got.Label != host.Label || got.Hostname != host.Hostname ||
		got.Port != host.Port || got.Username != host.Username || got.Protocol != host.Protocol ||
		got.Group != host.Group || got.Notes != host.Notes || !got.Pinned || got.OrderKey != host.OrderKey {
		t.Fatalf("Host after reopen = %+v, want %+v", got, host)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "prod" || got.Tags[1] != "web" {
		t.Fatalf("Tags = %v, want [prod web]", got.Tags)
	}
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("timestamps = %v / %v, want %v", got.CreatedAt, got.UpdatedAt, now)
	}
}

func TestDeleteHostLeavesUnrelatedVaultRecords(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)

	first := domainvault.Host{ID: "first", Label: "first", Hostname: "a.example", Port: 22, Protocol: domainvault.ProtocolSSH}
	second := domainvault.Host{ID: "second", Label: "second", Hostname: "b.example", Port: 22, Protocol: domainvault.ProtocolSSH}
	if err := store.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Execute(ctx, `INSERT INTO meta (key, value) VALUES ('device', 'test')`); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, first.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	hosts, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].ID != second.ID {
		t.Fatalf("hosts = %+v, want only %q", hosts, second.ID)
	}

	rows, err := store.db.Query(ctx, `SELECT value FROM meta WHERE key = 'device'`)
	if err != nil || len(rows.Values) != 1 || rows.Values[0][0] != "test" {
		t.Fatalf("unrelated meta row = %+v, %v; want test", rows.Values, err)
	}
}

func TestGetAndDeleteReportMissingHosts(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)

	if _, err := store.Get(ctx, "missing"); !errors.Is(err, appvault.ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, "missing"); !errors.Is(err, appvault.ErrNotFound) {
		t.Fatalf("Delete error = %v, want ErrNotFound", err)
	}
}

func TestListOrdersPinnedHostsThenOrderKeys(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)

	hosts := []domainvault.Host{
		{ID: "c", Label: "charlie", Hostname: "c.example", Port: 22, Protocol: domainvault.ProtocolSSH, OrderKey: 3000},
		{ID: "a", Label: "alpha", Hostname: "a.example", Port: 22, Protocol: domainvault.ProtocolSSH, OrderKey: 1000},
		{ID: "p", Label: "pinned", Hostname: "p.example", Port: 22, Protocol: domainvault.ProtocolSSH, OrderKey: 9000, Pinned: true},
	}
	for _, host := range hosts {
		if err := store.Save(ctx, host); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []model.HostID
	for _, host := range got {
		ids = append(ids, host.ID)
	}
	want := []model.HostID{"p", "a", "c"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}

func TestApplicationServiceCreatesAndDeletesThroughStore(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	service := appvault.NewService(store)

	host, err := service.Create(ctx, domainvault.Draft{
		Label: "web", Hostname: "example.com", Username: "root", Group: "prod",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if host.ID == "" {
		t.Fatal("Create returned an empty Host ID")
	}

	listed, err := service.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != host.ID {
		t.Fatalf("List = %+v, %v; want the created Host", listed, err)
	}

	if err := service.Delete(ctx, host.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	listed, err = service.List(ctx)
	if err != nil || len(listed) != 0 {
		t.Fatalf("List after Delete = %+v, %v; want empty", listed, err)
	}
}
