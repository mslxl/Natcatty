package store

import (
	"bytes"
	"context"
	"testing"
	"time"

	historysync "github.com/mslxl/natcatty/application/historysync"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
)

// TestLocalBackupCarriesAtuinCiphertextButNotHistoryState pins the documented
// boundary: a Local Backup may carry the encrypted Atuin credential because it
// snapshots secret_values wholesale, but it never carries the history
// projections, record log, or device-local metadata. Restoring the backup must
// leave the credential readable under the same field key, leave the runtime
// history state untouched, and carry no plaintext credential.
func TestLocalBackupCarriesAtuinCiphertextButNotHistoryState(t *testing.T) {
	store, secrets := openTestSecretStore(t, newFakeKeyStore())
	ctx := context.Background()

	const token = "gaokmvz-token-sentinel"
	const masterKey = "zjgcjn-key-sentinel"
	tokenRef := appvault.SecretRef{
		EntityType: historysync.SecretEntityType, EntityID: historysync.SecretEntityID,
		Field: historysync.SecretFieldSessionToken,
	}
	keyRef := appvault.SecretRef{
		EntityType: historysync.SecretEntityType, EntityID: historysync.SecretEntityID,
		Field: historysync.SecretFieldMasterKey,
	}
	if err := secrets.Set(ctx, tokenRef, token); err != nil {
		t.Fatalf("store the session token: %v", err)
	}
	if err := secrets.Set(ctx, keyRef, masterKey); err != nil {
		t.Fatalf("store the master key: %v", err)
	}
	if err := store.Save(ctx, testHost("h1", "alpha")); err != nil {
		t.Fatal(err)
	}

	if err := store.RecordHistoryEntry(ctx, domainhistory.Entry{
		ID: domainhistory.UUIDv7(), SessionID: "session-1", Scope: "local",
		Command: "git status", CWD: "/srv/app", Shell: "zsh",
		RecordedAt: time.UnixMilli(10), FinishedAt: time.UnixMilli(20),
		Duration: 10 * time.Millisecond, Origin: "local:deploy",
		Source: domainhistory.SourceSession, CaptureTier: domainhistory.CaptureTierHook,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Execute(ctx, `INSERT INTO history_records (
			id, series_host, tag, idx, recorded_at, version, kind, history_id,
			payload, direction, uploaded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"rec-1", "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeee0", domainhistory.RecordTagHistory,
		0, 10, domainhistory.RecordVersion, domainhistory.RecordKindCreate, "entry-1",
		`{"data":"sentinel"}`, domainhistory.RecordDirectionLocal, 0); err != nil {
		t.Fatal(err)
	}
	writer, err := store.EnsureWriterUUID(ctx, func() string {
		return "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeee0"
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata := domainhistory.Metadata{
		WriterUUID:     writer,
		KeyFingerprint: "sha256:0000000000000000",
		Account: domainhistory.AccountIdentity{
			ServerURL: "https://atuin.example.test", UserID: "user-1",
		},
	}
	if err := store.SaveHistoryMetadata(ctx, metadata); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var tokenCiphertext, keyCiphertext []byte
	for _, table := range snapshot.Tables {
		switch table.Name {
		case "meta", "history_entries", "history_records", "connection_logs":
			t.Fatalf("the Local Backup carries the runtime table %q", table.Name)
		}
		if table.Name != domainbackup.TableSecrets {
			continue
		}
		for _, row := range table.Rows {
			if row["entity_type"].Text != historysync.SecretEntityType {
				continue
			}
			switch row["field"].Text {
			case historysync.SecretFieldSessionToken:
				tokenCiphertext = row["ciphertext"].Blob
			case historysync.SecretFieldMasterKey:
				keyCiphertext = row["ciphertext"].Blob
			}
		}
	}
	if len(tokenCiphertext) == 0 || len(keyCiphertext) == 0 {
		t.Fatal("the Local Backup did not carry the encrypted Atuin credentials")
	}
	for _, ciphertext := range [][]byte{tokenCiphertext, keyCiphertext} {
		if bytes.Contains(ciphertext, []byte(token)) || bytes.Contains(ciphertext, []byte(masterKey)) {
			t.Fatal("the Local Backup carries an Atuin credential in plaintext")
		}
	}
	canonical, err := domainbackup.CanonicalBytes(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte(token)) || bytes.Contains(canonical, []byte(masterKey)) {
		t.Fatal("the backup snapshot contains an Atuin credential in plaintext")
	}

	if err := store.Restore(ctx, snapshot); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for ref, want := range map[appvault.SecretRef]string{
		tokenRef: token,
		keyRef:   masterKey,
	} {
		value, err := secrets.Get(ctx, ref)
		if err != nil || value != want {
			// Print the length, not the restored credential.
			t.Fatalf("Get(%+v) returned %d bytes, err %v; want the restored credential", ref, len(value), err)
		}
	}
	entries, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Command != "git status" {
		t.Fatalf("history entries after restore = %+v, want the runtime row preserved", entries)
	}
	records, err := store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "rec-1" {
		t.Fatalf("history records after restore = %+v, want the record log preserved", records)
	}
	after, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.WriterUUID != metadata.WriterUUID ||
		after.Account != metadata.Account ||
		after.KeyFingerprint != metadata.KeyFingerprint {
		t.Fatalf("metadata after restore = %+v, want device-local state preserved", after)
	}
}
