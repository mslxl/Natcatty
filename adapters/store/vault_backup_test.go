package store

import (
	"context"
	"testing"
	"time"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
)

func testHost(id, label string) domainvault.Host {
	now := time.UnixMilli(1_700_000_000_000)
	return domainvault.Host{
		ID: model.HostID(id), Label: label, Hostname: label + ".example.com",
		Port: 22, Username: "root", Group: "prod", Tags: []string{"web"},
		Protocol: domainvault.ProtocolSSH, CreatedAt: now, UpdatedAt: now,
	}
}

func TestSnapshotCoversEveryVaultTable(t *testing.T) {
	store, _ := openTestStore(t)
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	version, _ := store.SchemaVersion(context.Background())
	if snapshot.SchemaVersion != version {
		t.Fatalf("schema = %d, want %d", snapshot.SchemaVersion, version)
	}
	if len(snapshot.Tables) != len(domainbackup.Tables) {
		t.Fatalf("tables = %d, want %d", len(snapshot.Tables), len(domainbackup.Tables))
	}
	for _, table := range snapshot.Tables {
		if len(vaultColumns[table.Name]) == 0 {
			t.Fatalf("table %q has no column whitelist", table.Name)
		}
		if len(table.Rows) != 0 {
			t.Fatalf("new Vault table %q has rows", table.Name)
		}
	}
	if !snapshot.Empty() {
		t.Fatal("a new Vault snapshot is not empty")
	}
	if err := store.Validate(context.Background(), snapshot); err != nil {
		t.Fatalf("Validate of a fresh snapshot: %v", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, testHost("h1", "alpha")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveGroup(ctx, domainvault.Group{
		Path: "prod", Username: "deploy", CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(2),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNote(ctx, domainvault.Note{
		ID: "n1", Title: "runbook", Group: "prod", Content: "# steps",
		CreatedAt: time.UnixMilli(3), UpdatedAt: time.UnixMilli(4),
	}); err != nil {
		t.Fatal(err)
	}
	// BLOB and NULL storage classes must survive the logical snapshot.
	if _, err := store.db.Execute(ctx, `INSERT INTO crypto_keys (
			version, purpose, created_at, retired_at, wrapped_key, wrap_nonce,
			wrap_kdf, wrap_salt, wrap_time, wrap_memory, wrap_parallelism
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, "field", 10, nil, []byte{1, 2, 3}, []byte{4, 5}, "argon2id",
		[]byte{6, 7}, 3, 65536, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Execute(ctx, `INSERT INTO secret_values (
			entity_type, entity_id, field, alg, key_version, nonce, ciphertext, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"host", "h1", "password", "aes-256-gcm", 1, []byte{9}, []byte{8, 7}, 11); err != nil {
		t.Fatal(err)
	}

	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := domainbackup.CanonicalBytes(before)
	if err != nil {
		t.Fatal(err)
	}

	// Replace the current Vault with unrelated data, then restore.
	if err := store.Delete(ctx, model.HostID("h1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, testHost("current", "current")); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(ctx, before); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := domainbackup.CanonicalBytes(after)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Fatalf("restored snapshot differs\nbefore: %s\nafter:  %s", beforeBytes, afterBytes)
	}
	hosts, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].ID != "h1" {
		t.Fatalf("hosts after restore = %+v", hosts)
	}
}

func TestRestoreRollsBackWhenAConstraintFails(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, testHost("h1", "alpha")); err != nil {
		t.Fatal(err)
	}
	good, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Add a current host that must survive a failed restore.
	if err := store.Save(ctx, testHost("current", "current")); err != nil {
		t.Fatal(err)
	}
	current, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	currentBytes, _ := domainbackup.CanonicalBytes(current)

	// Break the backup by duplicating a primary key. domainbackup.Validate
	// deliberately permits this so the database constraint is what rejects it.
	broken := good
	for index, table := range broken.Tables {
		if table.Name != domainbackup.TableHosts {
			continue
		}
		broken.Tables[index].Rows = append(table.Rows, table.Rows[0])
	}
	if err := store.Restore(ctx, broken); err == nil {
		t.Fatal("Restore accepted duplicate primary keys")
	}

	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, _ := domainbackup.CanonicalBytes(after)
	if string(currentBytes) != string(afterBytes) {
		t.Fatalf("failed restore changed the Vault\nbefore: %s\nafter:  %s", currentBytes, afterBytes)
	}
	hosts, _ := store.List(ctx)
	if len(hosts) != 2 {
		t.Fatalf("hosts after rollback = %+v", hosts)
	}
}

func TestValidateRejectsIncompleteOrForeignSnapshots(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	missing := snapshot
	missing.Tables = append([]domainbackup.Table(nil), snapshot.Tables[1:]...)
	if err := store.Validate(ctx, missing); err == nil {
		t.Error("Validate accepted a snapshot missing a table")
	}

	foreign := snapshot
	foreign.Tables = append([]domainbackup.Table(nil), snapshot.Tables...)
	for index, table := range foreign.Tables {
		if table.Name != domainbackup.TableHosts {
			continue
		}
		table.Rows = []domainbackup.Row{{
			"id": domainbackup.Text("h1"), "unknown": domainbackup.Text("x"),
		}}
		foreign.Tables[index] = table
	}
	if err := store.Validate(ctx, foreign); err == nil {
		t.Error("Validate accepted an unknown column")
	}

	wrongVersion := snapshot
	wrongVersion.SchemaVersion++
	if err := store.Validate(ctx, wrongVersion); err == nil {
		t.Error("Validate accepted a different schema version")
	}
}

func TestChangeHookFiresOnlyAfterVaultWrites(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	calls := 0
	store.SetChangeHook(func() { calls++ })

	if err := store.Save(ctx, testHost("h1", "alpha")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls after Save = %d, want 1", calls)
	}
	if err := store.Delete(ctx, model.HostID("missing")); err == nil {
		t.Fatal("deleting a missing Host succeeded")
	}
	if calls != 1 {
		t.Fatalf("a failed delete fired the hook (%d calls)", calls)
	}
	if err := store.SaveConnectionLog(ctx, domainhistory.ConnectionLog{
		ID: "log-1", HostID: "h1", HostLabel: "alpha", Hostname: "alpha.example.com",
		Username: "root", Kind: "ssh", Status: domainhistory.ConnectionConnected,
		StartedAt: time.UnixMilli(5), EndedAt: time.UnixMilli(6),
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("history fired the backup hook (%d calls)", calls)
	}

	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("restore fired the backup hook (%d calls)", calls)
	}

	now := time.UnixMilli(7)
	if err := store.SaveImportBatch(ctx, appvault.ImportBatch{
		Hosts: []domainvault.Host{{
			ID: model.HostID("h2"), Label: "beta", Hostname: "beta.example.com",
			Protocol: domainvault.ProtocolSSH, CreatedAt: now, UpdatedAt: now,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("import batch calls = %d, want 2", calls)
	}
}
