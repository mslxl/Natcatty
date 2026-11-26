package app

// Conflict recovery and commit notification on the phone. Both reuse the
// shared appsync.Service: protective backups, ResolveConflict, the applied
// snapshot callback, and the debounced automatic sync are the same code the
// desktop runs. These tests drive the phone composition over a real SQLite
// Vault and an in-memory provider.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	"github.com/mslxl/natcatty/adapters/syncbackup"
	backupstore "github.com/mslxl/natcatty/adapters/vaultbackup"
	appsync "github.com/mslxl/natcatty/application/sync"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/application/vaultbackup"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// wayfinderConflictRemote makes the next conditional write observe a newer
// copy from another device, which is the durable conflict path.
type wayfinderConflictRemote struct {
	phoneSyncRemote
	next appsync.RemoteFile
}

func (r *wayfinderConflictRemote) Write(ctx context.Context, token domainsync.Token, current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	if r.next.ID != "" {
		r.mu.Lock()
		r.file, r.next = r.next, appsync.RemoteFile{}
		r.mu.Unlock()
	}
	return r.phoneSyncRemote.Write(ctx, token, current, content)
}

func newWayfinderRecoveryPhone(t *testing.T, remote *wayfinderConflictRemote, withBackups bool, options ...phoneSyncOption) (*phoneSync, *store.Store, *store.SecretStore, *syncbackup.Adapter) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	keyBackend := newPhoneTestKeys()
	secrets, err := store.OpenSecretStore(ctx, database, keyBackend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })
	backups := syncbackup.New(vaultbackup.NewService(database, backupstore.Open(filepath.Join(dir, "backups"), keyBackend)))
	phoneOptions := []phoneSyncOption{
		withPhoneSyncTokens(&phoneSyncTokens{}),
		withPhoneSyncRemote(appsync.ProviderWebDAV, remote),
		withPhoneSyncDevice("wayfinder-phone-recovery", "Phone", "test"),
	}
	phoneOptions = append(phoneOptions, options...)
	if withBackups {
		phoneOptions = append(phoneOptions, withPhoneSyncBackups(backups))
	}
	phone, err := newPhoneSync(ctx, database, secrets, nil, nil, dir, phoneOptions...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(phone.Stop)
	if err := phone.ConnectWebDAV(ctx, syncRefreshPassword, appsync.WebDAVSettings{
		Endpoint: "https://example.invalid/vault", Username: "fixture", Password: "synthetic",
	}, false); err != nil {
		t.Fatal(err)
	}
	return phone, database, secrets, backups
}

// TestWayfinderPhoneSharesConflictBackupAndRefresh proves conflict recovery is
// the shared desktop flow: without a backup adapter the overwrite is refused;
// with one, choosing the cloud copy takes a restore point first, applies and
// refreshes the live list, and preserves an open editor's draft.
func TestWayfinderPhoneSharesConflictBackupAndRefresh(t *testing.T) {
	for _, withBackups := range []bool{false, true} {
		name := "MissingBackupRefusesOverwrite"
		if withBackups {
			name = "CloudChoiceCanBeRestored"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			remote := &wayfinderConflictRemote{}
			var state *vaultstate.State
			refresh := appliedSnapshotRefresh(func() *vaultstate.State { return state })
			phone, database, secrets, backups := newWayfinderRecoveryPhone(t, remote, withBackups,
				withPhoneSyncOptions(appsync.WithAppliedSnapshot(refresh)))
			now := time.Now()
			local := syncRefreshHost("host-fixture", "initial", now)
			if err := database.Save(ctx, local); err != nil {
				t.Fatal(err)
			}
			if result, err := phone.service.Sync(ctx, appsync.TriggerManual); err != nil || !result.Uploaded {
				t.Fatalf("initial upload: %+v, %v", result, err)
			}
			local.Label, local.UpdatedAt = "local-edit", now.Add(time.Second)
			if err := database.Save(ctx, local); err != nil {
				t.Fatal(err)
			}
			ref := appvault.HostSecretRef(local.ID, appvault.FieldPassword)
			if err := secrets.Set(ctx, ref, "synthetic-before-conflict"); err != nil {
				t.Fatal(err)
			}
			state = vaultstate.NewState(appvault.NewService(database, appvault.WithSecrets(secrets)))
			if err := state.Reload(); err != nil {
				t.Fatal(err)
			}
			if !state.BeginEdit(local.ID) {
				t.Fatal("could not open editor")
			}
			state.Form().Label = "unsaved-buffer"

			cloud := local
			cloud.Label, cloud.UpdatedAt = "cloud-edit", now.Add(2*time.Second)
			content, err := syncRefreshEnvelope([]domainvault.Host{cloud}, cloud.UpdatedAt.UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			remote.next = appsync.RemoteFile{ID: "vault-file", ETag: "peer-etag", UpdatedAt: cloud.UpdatedAt.UnixMilli(), Content: content}
			if _, err := phone.service.Sync(ctx, appsync.TriggerManual); !errors.Is(err, appsync.ErrConflict) {
				t.Fatalf("expected conditional-write conflict, got %v", err)
			}
			if got := phone.Snapshot(); got.Status != appsync.StatusConflict || got.Conflict == nil {
				t.Fatalf("conflict was not surfaced: %+v", got)
			}
			if got := phone.Snapshot().HasPassword; !got {
				t.Fatal("lost the fixture master password")
			}
			result, err := phone.service.ResolveConflict(ctx, appsync.ResolutionRemote)
			if !withBackups {
				host, getErr := database.Get(ctx, local.ID)
				if !errors.Is(err, appsync.ErrBackupUnavailable) || getErr != nil || host.Label != local.Label {
					t.Fatalf("unguarded resolution: result=%+v error=%v label=%q read error=%v", result, err, host.Label, getErr)
				}
				return
			}
			if err != nil || !result.Downloaded || result.Uploaded {
				t.Fatalf("cloud resolution: %+v, %v", result, err)
			}
			entries, err := backups.List(ctx)
			if err != nil || len(entries) != 1 {
				t.Fatalf("protective backups: %v, %v", entries, err)
			}
			frameVaultState(state)
			if firstVaultLabel(state) != cloud.Label || !state.Editing() || state.Form().Label != "unsaved-buffer" {
				t.Fatal("cloud apply did not refresh the live list while preserving the unsaved editor")
			}
			if _, err := secrets.Get(ctx, ref); !errors.Is(err, appvault.ErrSecretNotFound) {
				t.Fatalf("cloud apply did not remove the absent fixture secret: %v", err)
			}
			if result, err := phone.service.Sync(ctx, appsync.TriggerManual); err != nil || result.Action != domainsync.ActionNone {
				t.Fatalf("sync after cloud apply: %+v, %v", result, err)
			}
			if err := phone.service.RestoreBackup(ctx, appsync.BackupSourceLocal, entries[0].ID); err != nil {
				t.Fatal(err)
			}
			frameVaultState(state)
			if firstVaultLabel(state) != local.Label {
				t.Fatalf("restored label = %q", firstVaultLabel(state))
			}
			if value, err := secrets.Get(ctx, ref); err != nil || value != "synthetic-before-conflict" {
				t.Fatalf("restored fixture secret did not match: %v", err)
			}
			if remote.writes != 1 {
				t.Fatalf("cloud writes = %d, want the initial upload only", remote.writes)
			}
		})
	}
}

// TestWayfinderPhoneCommitNotificationSources separates the local commit
// sources: a Vault record and a Host field secret each schedule an automatic
// sync; direct session/device-local secret writes and failed writes do not;
// Known Hosts reconcile without changing the cloud copy; and a remote apply
// refreshes the live surfaces without queuing another sync.
func TestWayfinderPhoneCommitNotificationSources(t *testing.T) {
	ctx := context.Background()
	remote := &wayfinderConflictRemote{}
	applied := 0
	phone, database, secrets, _ := newWayfinderRecoveryPhone(t, remote, false,
		withPhoneSyncOptions(
			appsync.WithDebounce(15*time.Millisecond),
			appsync.WithAppliedSnapshot(func() { applied++ }),
		))
	readCount := func() int {
		remote.mu.Lock()
		defer remote.mu.Unlock()
		return remote.reads
	}
	waitForSyncedReads := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for readCount() < want || phone.Snapshot().Status != appsync.StatusSynced {
			if time.Now().After(deadline) {
				t.Fatalf("reads = %d, want at least %d with synced status, got %+v", readCount(), want, phone.Snapshot())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	writes := func() int {
		remote.mu.Lock()
		defer remote.mu.Unlock()
		return remote.writes
	}
	local := syncRefreshHost("host-fixture", "local", time.Now())
	if err := database.Save(ctx, local); err != nil {
		t.Fatal(err)
	}
	if err := phone.service.SetAutoSync(ctx, true); err != nil {
		t.Fatal(err)
	}
	// Turning auto sync on schedules one run. It publishes the Host that is
	// already in the Vault; drain it before counting the edits below.
	waitForSyncedReads(1)
	commits := 0
	database.SetChangeHook(func() {
		commits++
		phone.NotifyLocalChange()
	})
	cloudWrites := writes()
	local.Label, local.UpdatedAt = "local-edit", time.Now().Add(time.Second)
	before := readCount()
	if err := database.Save(ctx, local); err != nil {
		t.Fatal(err)
	}
	waitForSyncedReads(before + 1)
	if commits != 1 || writes() != cloudWrites+1 {
		t.Fatalf("ordinary commit: hook calls=%d writes=%d, want 1 and %d", commits, writes(), cloudWrites+1)
	}

	// A Host field secret is committed through the Vault service, which also
	// records the Host's HasPassword marker. That commit schedules the same
	// automatic sync that carries the secret.
	vault := appvault.NewService(database, appvault.WithSecrets(secrets))
	before = readCount()
	if err := vault.SetSecret(ctx, local.ID, appvault.FieldPassword, "synthetic-secret-only"); err != nil {
		t.Fatal(err)
	}
	waitForSyncedReads(before + 1)
	if commits != 2 || writes() != cloudWrites+2 {
		t.Fatalf("field-secret commit: hook calls=%d writes=%d, want 2 and %d", commits, writes(), cloudWrites+2)
	}
	if value, err := secrets.Get(ctx, appvault.HostSecretRef(local.ID, appvault.FieldPassword)); err != nil || value != "synthetic-secret-only" {
		t.Fatalf("field secret did not persist: %v", err)
	}
	before = readCount()
	if err := vault.DeleteSecret(ctx, local.ID, appvault.FieldPassword); err != nil {
		t.Fatal(err)
	}
	waitForSyncedReads(before + 1)
	if commits != 3 {
		t.Fatalf("field-secret delete hook calls = %d, want 3", commits)
	}

	// Direct session/device-local secret writes are not Vault record commits.
	before, cloudWrites = readCount(), writes()
	sessionRef := appvault.ETSessionSecretRef("resume-fixture", appvault.FieldETSessionID)
	if err := secrets.Set(ctx, sessionRef, "synthetic-session"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Delete(ctx, sessionRef); err != nil {
		t.Fatal(err)
	}
	if commits != 3 || writes() != cloudWrites {
		t.Fatalf("session secret changed sync state: hook calls=%d writes=%d", commits, writes())
	}
	assertNoReadsBeyond(t, readCount, before)

	// A failed write fires nothing.
	if err := database.Delete(ctx, model.HostID("missing-host")); err == nil {
		t.Fatal("deleting a missing Host succeeded")
	}
	if commits != 3 {
		t.Fatalf("a failed delete fired the record hook: %d", commits)
	}

	// Known Hosts are local-only in the payload, so their commit reconciles to
	// no cloud change.
	before, cloudWrites = readCount(), writes()
	if err := database.UpsertKnownHost(ctx, domainconn.KnownHost{
		ID: "known-fixture", Hostname: "known.example", Port: 22,
		KeyType: "ssh-ed25519", PublicKey: "AAAAC3Nza", Fingerprint: "AAAA",
		DiscoveredAt: time.Now(), LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	waitForSyncedReads(before + 1)
	if writes() != cloudWrites {
		t.Fatalf("Known Hosts changed the cloud copy: writes %d -> %d", cloudWrites, writes())
	}

	// A remote apply refreshes the phone surfaces exactly once and must not
	// queue a follow-up automatic sync behind the manual run that applied it.
	cloud := local
	cloud.Label, cloud.UpdatedAt = "cloud-download", time.Now().Add(time.Minute)
	content, err := syncRefreshEnvelope([]domainvault.Host{cloud}, cloud.UpdatedAt.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	remote.file = appsync.RemoteFile{ID: "vault-file", ETag: "download-etag", UpdatedAt: cloud.UpdatedAt.UnixMilli(), Content: content}
	remote.mu.Unlock()
	before = readCount()
	if result, err := phone.service.Sync(ctx, appsync.TriggerManual); err != nil || !result.Downloaded {
		t.Fatalf("cloud download: %+v, %v", result, err)
	}
	if applied != 1 {
		t.Fatalf("applied snapshot callback ran %d times, want exactly one", applied)
	}
	assertNoReadsBeyond(t, readCount, before+1)
}

// assertNoReadsBeyond observes a bounded window and fails if an automatic
// sync starts on its own after want reads. The deadline is the test's hard
// bound; each iteration checks the real request counter.
func assertNoReadsBeyond(t *testing.T, reads func() int, want int) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := reads(); got > want {
			t.Fatalf("an unrequested automatic sync ran: reads = %d, want at most %d", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
