package store

import (
	"bytes"
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
	// An empty optional family is omitted so a build that predates it can
	// still read the backup. Every other table is always present.
	required := 0
	for _, table := range domainbackup.Tables {
		if !domainbackup.OptionalTable(table) {
			required++
		}
	}
	if len(snapshot.Tables) != required {
		t.Fatalf("tables = %d, want %d", len(snapshot.Tables), required)
	}
	for _, table := range snapshot.Tables {
		if domainbackup.OptionalTable(table.Name) {
			t.Fatalf("empty optional table %q was emitted", table.Name)
		}
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

func TestNetworkProfileBackupRoundTripAndLegacySnapshotRestore(t *testing.T) {
	store, secrets := openTestSecretStore(t, newFakeKeyStore())
	ctx := context.Background()
	if err := store.Save(ctx, testHost("h1", "alpha")); err != nil {
		t.Fatal(err)
	}
	// A snapshot written before Network Profiles existed has no family at all.
	legacy, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tableRows(legacy, domainbackup.TableNetworkProfiles) != 0 {
		t.Fatal("a Vault without Network Profiles emitted the optional table")
	}
	if err := store.Validate(ctx, legacy); err != nil {
		t.Fatalf("legacy snapshot must validate: %v", err)
	}

	when := time.UnixMilli(1_700_000_000_000).UTC()
	tailscale := domainvault.NetworkProfile{
		ID: "net-1", Label: "tailnet", Kind: domainvault.NetworkKindTailscale,
		Tailscale: &domainvault.TailscaleNetwork{
			Hostname: "app-node", ControlURL: "https://headscale.example.com",
			Tags: []string{"tag:prod"}, HasAuthKey: true,
		},
		OrderKey: 1000, CreatedAt: when, UpdatedAt: when,
	}
	wireguard := domainvault.NetworkProfile{
		ID: "net-2", Label: "wg", Kind: domainvault.NetworkKindWireGuard,
		WireGuard: &domainvault.WireGuardNetwork{
			Addresses: []string{"10.0.0.2/32"}, DNS: []string{"10.0.0.1"}, MTU: 1280,
			HasPrivateKey: true,
			Peers: []domainvault.WireGuardPeer{{
				ID: "peer-1", PublicKey: "peer-public-key-material",
				Endpoint: "vpn.example.com:51820", AllowedIPs: []string{"10.0.0.0/24"},
				PersistentKeepalive: 25, HasPresharedKey: true,
			}},
		},
		OrderKey: 2000, CreatedAt: when, UpdatedAt: when,
	}
	for _, profile := range []domainvault.NetworkProfile{tailscale, wireguard} {
		if err := store.SaveNetworkProfile(ctx, profile); err != nil {
			t.Fatal(err)
		}
	}
	const (
		authPlaintext    = "ts-auth-key-plaintext"
		privatePlaintext = "wg-private-key-plaintext"
		pskPlaintext     = "wg-preshared-key-plaintext"
	)
	// Writing through the real field-secret store proves the snapshot sees
	// ciphertext: these plaintexts exist only inside the encrypted column.
	secretsToStore := []struct {
		ref   appvault.SecretRef
		value string
	}{
		{appvault.NetworkProfileSecretRef("net-1", appvault.FieldNetworkAuthKey), authPlaintext},
		{appvault.NetworkProfileSecretRef("net-2", appvault.FieldNetworkPrivateKey), privatePlaintext},
		{appvault.NetworkPeerSecretRef("peer-1", appvault.FieldNetworkPresharedKey), pskPlaintext},
	}
	for _, row := range secretsToStore {
		if err := secrets.Set(ctx, row.ref, row.value); err != nil {
			t.Fatalf("store %s: %v", row.ref.Field, err)
		}
		if got, err := secrets.Get(ctx, row.ref); err != nil || got != row.value {
			t.Fatalf("read back %s = %q, %v", row.ref.Field, got, err)
		}
	}

	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tableRows(before, domainbackup.TableNetworkProfiles) != 2 {
		t.Fatalf("network_profiles rows = %d", tableRows(before, domainbackup.TableNetworkProfiles))
	}
	encoded, err := domainbackup.CanonicalBytes(before)
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range []string{authPlaintext, privatePlaintext, pskPlaintext} {
		if bytes.Contains(encoded, []byte(plaintext)) {
			t.Fatalf("backup contains plaintext credential %q", plaintext)
		}
	}
	if rows := tableRows(before, domainbackup.TableSecrets); rows != len(secretsToStore) {
		t.Fatalf("secret rows = %d, want %d", rows, len(secretsToStore))
	}

	// Restoring the pre-family snapshot succeeds and leaves no Network Profile
	// rows or credential ciphertext behind.
	if err := store.Restore(ctx, legacy); err != nil {
		t.Fatalf("restore legacy snapshot: %v", err)
	}
	profiles, err := store.ListNetworkProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 0 {
		t.Fatalf("profiles after legacy restore = %+v", profiles)
	}
	afterLegacy, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tableRows(afterLegacy, domainbackup.TableNetworkProfiles) != 0 ||
		tableRows(afterLegacy, domainbackup.TableSecrets) != 0 {
		t.Fatalf("legacy restore left rows: profiles=%d secrets=%d",
			tableRows(afterLegacy, domainbackup.TableNetworkProfiles),
			tableRows(afterLegacy, domainbackup.TableSecrets))
	}

	// The current snapshot reproduces profiles, peers, Has* markers, and the
	// exact secret ciphertext.
	if err := store.Restore(ctx, before); err != nil {
		t.Fatalf("restore current snapshot: %v", err)
	}
	restored, err := store.ListNetworkProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 2 || restored[0].ID != "net-1" || restored[1].ID != "net-2" {
		t.Fatalf("profiles after restore = %+v", restored)
	}
	if !restored[0].Tailscale.HasAuthKey {
		t.Fatal("Tailscale HasAuthKey marker was lost")
	}
	if !restored[1].WireGuard.HasPrivateKey || len(restored[1].WireGuard.Peers) != 1 ||
		!restored[1].WireGuard.Peers[0].HasPresharedKey {
		t.Fatalf("WireGuard markers or peers were lost: %+v", restored[1].WireGuard)
	}
	for _, row := range secretsToStore {
		if got, err := secrets.Get(ctx, row.ref); err != nil || got != row.value {
			t.Fatalf("restored %s = %q, %v; want %q", row.ref.Field, got, err, row.value)
		}
	}
	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := domainbackup.CanonicalBytes(after)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, afterBytes) {
		t.Fatalf("restored snapshot differs\nbefore: %s\nafter:  %s", encoded, afterBytes)
	}
}

func tableRows(snapshot domainbackup.Snapshot, name string) int {
	for _, table := range snapshot.Tables {
		if table.Name == name {
			return len(table.Rows)
		}
	}
	return 0
}

func TestPreviousBuildLocalBackupRestoresAfterHistorySchemaGrowth(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, testHost("h1", "alpha")); err != nil {
		t.Fatal(err)
	}
	// Growing the history schema must not change the Vault backup schema
	// version, or the exact-version check would reject existing backups.
	previous, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if previous.SchemaVersion != schemaVersion {
		t.Fatalf("backup schema version = %d, want the unchanged version %d", previous.SchemaVersion, schemaVersion)
	}

	// History rows added by this build must stay outside the Vault table set.
	if err := store.RecordHistoryEntry(ctx, domainhistory.Entry{
		ID: domainhistory.UUIDv7(), SessionID: "session-1", SessionUUID: "session-uuid",
		Scope: "local", Command: "git status", CWD: "/srv/app", Shell: "zsh",
		RecordedAt: time.UnixMilli(10), FinishedAt: time.UnixMilli(20),
		Duration: 10 * time.Millisecond, ExitCode: 0, HasExitCode: true,
		Origin: "local:deploy", Source: domainhistory.SourceSession,
		CaptureTier: domainhistory.CaptureTierHook,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConnectionLog(ctx, domainhistory.ConnectionLog{
		ID: "log-1", Kind: "ssh", Status: domainhistory.ConnectionConnected,
		StartedAt: time.UnixMilli(5), EndedAt: time.UnixMilli(6),
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range previous.Tables {
		if table.Name == "history_entries" || table.Name == "connection_logs" {
			t.Fatalf("the Vault backup carries the runtime table %q", table.Name)
		}
	}

	// Replace the current Vault, then restore the earlier backup.
	if err := store.Delete(ctx, model.HostID("h1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, testHost("h2", "beta")); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(ctx, previous); err != nil {
		t.Fatalf("restore a previous-build backup: %v", err)
	}
	hosts, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].ID != "h1" {
		t.Fatalf("hosts after restore = %+v, want h1", hosts)
	}
	entries, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Command != "git status" {
		t.Fatalf("history after restore = %+v, want the row preserved", entries)
	}
	logs, err := store.ListConnectionLogs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].ID != "log-1" {
		t.Fatalf("connection logs after restore = %+v, want the row preserved", logs)
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
