package app

// Preservation of the four settings documents the desktop authors. The phone
// never owns themes, key bindings, SFTP bookmarks, or browser bookmarks: every
// upload carries the provider's current value, and a document the desktop
// deleted stays absent. Both the legacy v1 writer and the v2 convergent writer
// are covered, including a rebuild of the phone's sync services.

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	s3fake "github.com/mslxl/natcatty/adapters/s3/fake"
	"github.com/mslxl/natcatty/adapters/store"
	"github.com/mslxl/natcatty/adapters/syncbackup"
	"github.com/mslxl/natcatty/adapters/syncedsettings"
	backupstore "github.com/mslxl/natcatty/adapters/vaultbackup"
	"github.com/mslxl/natcatty/application/preferences"
	appsync "github.com/mslxl/natcatty/application/sync"
	"github.com/mslxl/natcatty/application/terminalsettings"
	"github.com/mslxl/natcatty/application/vaultbackup"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestWayfinderPhoneUploadPreservesDesktopDocuments is the v1 WebDAV case: a
// phone downloads the cloud copy, edits a portable document, and uploads. The
// four desktop-authored documents must survive byte-for-byte.
func TestWayfinderPhoneUploadPreservesDesktopDocuments(t *testing.T) {
	ctx := context.Background()
	documents := map[string]json.RawMessage{
		syncedsettings.KeyThemes:           json.RawMessage(`[{"id":"probe-theme","name":"Probe Theme"}]`),
		syncedsettings.KeyKeybindings:      json.RawMessage(`{"suppressedMenus":["help-menu"]}`),
		syncedsettings.KeySFTPBookmarks:    json.RawMessage(`[{"path":"/probe","label":"Probe"}]`),
		syncedsettings.KeyBrowserBookmarks: json.RawMessage(`[{"id":"probe","title":"Probe","url":"https://example.invalid"}]`),
	}
	for _, scenario := range []struct {
		name     string
		download bool
	}{
		{name: "AfterDownload", download: true},
		{name: "FirstSmartMerge"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			now := time.Now()
			payload := appsync.Payload{
				Hosts: []domainvault.Host{{
					ID: "desktop-probe", Label: "Desktop probe", Hostname: "example.invalid",
					Port: 22, Protocol: domainvault.ProtocolSSH, UpdatedAt: now,
				}},
				NetworkProfiles: []domainvault.NetworkProfile{},
				Settings:        maps.Clone(documents),
				SyncedAt:        now.UnixMilli(),
			}
			if scenario.download {
				payload.Settings[syncedsettings.KeyPreferences] = json.RawMessage(`{"appearance":{"terminalFontSize":19}}`)
			}
			envelope, err := domainsync.EncryptPayload(payload, syncMasterPassword, domainsync.EncryptOptions{})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := domainsync.MarshalFile(envelope)
			if err != nil {
				t.Fatal(err)
			}
			remote := &phoneSyncRemote{file: appsync.RemoteFile{
				ID: "vault-file", ETag: "seed", Content: encoded, UpdatedAt: payload.SyncedAt,
			}}
			device := newSyncDevice(t, ctx, t.TempDir(), "probe-phone", remote)
			if err := device.preferences.Update(func(settings *domainpreferences.Settings) {
				settings.Appearance.AppIcon = "rainbow"
			}); err != nil {
				t.Fatal(err)
			}
			connectSyncDevice(t, ctx, device)
			if scenario.download {
				if err := device.sync.service.SetStrategy(ctx, domainsync.StrategyPreferCloud); err != nil {
					t.Fatal(err)
				}
				result, err := device.sync.service.Sync(ctx, appsync.TriggerManual)
				if err != nil || !result.Downloaded {
					t.Fatalf("initial download: result=%+v error=%v", result, err)
				}
				appearance := device.preferences.Settings().Appearance
				if appearance.TerminalFontSize != 19 || appearance.AppIcon != "rainbow" {
					t.Fatalf("phone appearance after download: font size %v, icon %q", appearance.TerminalFontSize, appearance.AppIcon)
				}
				if err := device.sync.service.SetStrategy(ctx, domainsync.StrategySmartMerge); err != nil {
					t.Fatal(err)
				}
			}
			if err := device.preferences.Update(func(settings *domainpreferences.Settings) {
				settings.Appearance.TerminalFontSize = 24
			}); err != nil {
				t.Fatal(err)
			}
			result, err := device.sync.service.Sync(ctx, appsync.TriggerManual)
			if err != nil || !result.Uploaded {
				t.Fatalf("phone push: result=%+v error=%v", result, err)
			}
			file, _, err := remote.Read(ctx, domainsync.Token{})
			if err != nil {
				t.Fatal(err)
			}
			envelope, err = domainsync.UnmarshalFile(file.Content)
			if err != nil {
				t.Fatal(err)
			}
			plaintext, err := domainsync.DecryptPayload(envelope, syncMasterPassword)
			if err != nil {
				t.Fatal(err)
			}
			var uploaded appsync.Payload
			if err := json.Unmarshal(plaintext, &uploaded); err != nil {
				t.Fatal(err)
			}
			if !containsTerminalFontSize(uploaded.Settings[syncedsettings.KeyPreferences], 24) {
				t.Fatal("uploaded preferences do not contain the phone change")
			}
			assertDesktopDocumentsPreserved(t, documents, uploaded.Settings)
		})
	}
}

func containsTerminalFontSize(raw json.RawMessage, want float64) bool {
	var document struct {
		Appearance struct {
			TerminalFontSize float64 `json:"terminalFontSize"`
		} `json:"appearance"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return false
	}
	return document.Appearance.TerminalFontSize == want
}

func assertDesktopDocumentsPreserved(t *testing.T, expected, uploaded map[string]json.RawMessage) {
	t.Helper()
	for key, want := range expected {
		raw, present := uploaded[key]
		if !present {
			t.Errorf("phone cloud upload omitted %s", key)
			continue
		}
		var gotValue, wantValue any
		if err := json.Unmarshal(raw, &gotValue); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(want, &wantValue); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Errorf("phone cloud upload changed %s", key)
		}
	}
}

// TestWayfinderPhoneS3PreservesDesktopDocuments covers the S3 provider through
// both writers, a desktop deletion, a service rebuild, and the HTTP
// confirmation gate a rebuilt profile re-asks for.
func TestWayfinderPhoneS3PreservesDesktopDocuments(t *testing.T) {
	t.Run("V1", func(t *testing.T) { wayfinderS3Preservation(t, false) })
	t.Run("V2", func(t *testing.T) { wayfinderS3Preservation(t, true) })
}

func wayfinderS3Preservation(t *testing.T, useV2 bool) {
	t.Helper()
	ctx := context.Background()
	server, err := s3fake.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	desktopDocs := map[string]json.RawMessage{
		syncedsettings.KeyThemes:           json.RawMessage(`[]`),
		syncedsettings.KeyKeybindings:      json.RawMessage(`{"suppressedMenus":["help-menu"]}`),
		syncedsettings.KeySFTPBookmarks:    json.RawMessage(`[{"path":"/team","label":"Team"}]`),
		syncedsettings.KeyBrowserBookmarks: json.RawMessage(`[{"id":"team","title":"Team","url":"https://example.invalid","scope":"allHosts","iconMode":"auto","color":"blue","order":1}]`),
	}
	now := time.Now()
	host := domainvault.Host{ID: "wayfinder-desktop", Label: "Desktop", Hostname: "example.invalid", Port: 22, Protocol: domainvault.ProtocolSSH, UpdatedAt: now}
	payload := appsync.Payload{Hosts: []domainvault.Host{host}, NetworkProfiles: []domainvault.NetworkProfile{}, Settings: desktopDocs, SyncedAt: now.UnixMilli()}
	seedWayfinderS3Payload(t, server, payload, 0)

	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "natcatty.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Save(ctx, host); err != nil {
		t.Fatal(err)
	}
	keyBackend := newPhoneTestKeys()
	keys, err := store.OpenSecretStore(ctx, database, keyBackend)
	if err != nil {
		t.Fatal(err)
	}
	prefs := preferences.NewService(store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	if err := prefs.Load(ctx); err != nil {
		t.Fatal(err)
	}
	terminal := terminalsettings.NewService(store.OpenTerminalSettings(filepath.Join(dir, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
		t.Fatal(err)
	}
	backups := syncbackup.New(vaultbackup.NewService(database, backupstore.Open(filepath.Join(dir, "vault-backups"), keyBackend)))
	options := []phoneSyncOption{
		withPhoneSyncTokens(&phoneSyncTokens{}),
		withPhoneSyncDevice("wayfinder-phone", "Phone", "test"),
		withPhoneSyncBackups(backups),
	}
	phone, err := newPhoneSync(ctx, database, keys, prefs, terminal, dir, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(phone.Stop)
	if err := phone.ConnectS3(ctx, syncMasterPassword, appsync.S3Settings{
		Endpoint: server.URL(), Region: s3fake.DefaultRegion, Bucket: s3fake.DefaultBucket,
		AccessKeyID: s3fake.DefaultAccessKeyID, SecretAccessKey: s3fake.DefaultSecretAccessKey,
	}, true); err != nil {
		t.Fatal(err)
	}
	if err := phone.service.SetStrategy(ctx, domainsync.StrategyPreferCloud); err != nil {
		t.Fatal(err)
	}
	result, err := phone.service.Sync(ctx, appsync.TriggerManual)
	if err != nil || !result.Downloaded {
		t.Fatalf("initial download: result=%+v error=%v", result, err)
	}
	if useV2 {
		preview, err := phone.service.SetConvergentEnabled(ctx, true)
		if err != nil || !preview.CanInitialize {
			t.Fatalf("initialize isolated v2 profile: preview=%+v error=%v", preview, err)
		}
		entries, err := backups.List(ctx)
		if err != nil || len(entries) == 0 {
			t.Fatalf("migration protective backups = %v, error=%v", entries, err)
		}
	}
	if err := phone.service.SetStrategy(ctx, domainsync.StrategySmartMerge); err != nil {
		t.Fatal(err)
	}
	if err := prefs.Update(func(p *domainpreferences.Settings) { p.Appearance.TerminalFontSize = 24 }); err != nil {
		t.Fatal(err)
	}
	result, err = phone.service.Sync(ctx, appsync.TriggerManual)
	if err != nil || !result.Uploaded {
		t.Fatalf("phone upload: result=%+v error=%v", result, err)
	}
	uploaded, schema := readWayfinderS3Payload(t, server)
	if useV2 && schema != domainsync.ConvergentSchemaVersion {
		t.Fatalf("cloud snapshot schema = %d, want v2", schema)
	}
	assertDesktopDocumentsPreserved(t, desktopDocs, uploaded.Settings)
	if t.Failed() {
		return
	}

	// Rebuild the phone's services over the same application data. The stored
	// profile keeps its provider but not the master password.
	phone.Stop()
	prefs = preferences.NewService(store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	if err := prefs.Load(ctx); err != nil {
		t.Fatal(err)
	}
	terminal = terminalsettings.NewService(store.OpenTerminalSettings(filepath.Join(dir, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
		t.Fatal(err)
	}
	phone, err = newPhoneSync(ctx, database, keys, prefs, terminal, dir, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(phone.Stop)
	if phone.Snapshot().HasPassword || !phone.Snapshot().Connected {
		t.Fatal("reopened profile must keep its provider and request the master password")
	}
	if err := phone.SetPassword(syncMasterPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.service.Sync(ctx, appsync.TriggerManual); !errors.Is(err, appsync.ErrInsecureEndpoint) {
		t.Fatalf("reopened HTTP profile should require confirmation: %v", err)
	}
	if !phone.service.ConfirmInsecureEndpoint(ctx) {
		t.Fatal("could not confirm the isolated HTTP endpoint")
	}
	before := server.State().Counters
	if _, err := phone.service.Sync(ctx, appsync.TriggerAuto); !errors.Is(err, appsync.ErrInsecureEndpoint) {
		t.Fatalf("confirmed HTTP profile should still reject automatic sync: %v", err)
	}
	if after := server.State().Counters; after.Requests != before.Requests {
		t.Fatalf("automatic HTTP sync made %d requests", after.Requests-before.Requests)
	}
	if err := phone.SetPassword("wrong-synthetic-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.service.Sync(ctx, appsync.TriggerManual); !errors.Is(err, appsync.ErrWrongPassword) {
		t.Fatalf("expected the shared wrong-password error: %v", err)
	}
	if after := server.State().Counters; after.Puts != before.Puts {
		t.Fatalf("the wrong master password caused %d writes", after.Puts-before.Puts)
	}
	if status := phone.Snapshot(); !status.HasPassword || status.Problem != appsync.ProblemWrongPassword {
		t.Fatalf("connected page must distinguish an entered but wrong password: %+v", status)
	}
	if err := phone.SetPassword(syncMasterPassword); err != nil {
		t.Fatal(err)
	}
	noop, err := phone.service.Sync(ctx, appsync.TriggerManual)
	if err != nil || noop.Action != domainsync.ActionNone || noop.Uploaded || noop.Downloaded {
		t.Errorf("unchanged sync after reopening: result=%+v error=%v", noop, err)
	}

	// A concurrent desktop deletion wins for a document the phone does not own.
	deletedKey := syncedsettings.KeyBrowserBookmarks
	delete(uploaded.Settings, deletedKey)
	delete(desktopDocs, deletedKey)
	uploaded.SyncedAt = time.Now().UnixMilli()
	if useV2 {
		envelope, err := domainsync.ParseConvergentEnvelope(uploaded.ConvergentSync)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := domainsync.MaterializeConvergentSnapshot(envelope.State)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Families["settings"] = slices.DeleteFunc(snapshot.Families["settings"],
			func(record domainsync.ConvergentRecord) bool { return record.ID == deletedKey })
		state, changed, err := domainsync.ApplyConvergentSnapshot(envelope.State, snapshot, "wayfinder-desktop", uploaded.SyncedAt, false)
		if err != nil || !changed {
			t.Fatalf("apply desktop deletion: changed=%v error=%v", changed, err)
		}
		envelope, err = domainsync.WrapConvergentEnvelope(state)
		if err != nil {
			t.Fatal(err)
		}
		uploaded.ConvergentSync, err = json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
	}
	seedWayfinderS3Payload(t, server, uploaded, schema)
	if err := prefs.Update(func(p *domainpreferences.Settings) { p.Appearance.TerminalFontSize = 25 }); err != nil {
		t.Fatal(err)
	}
	result, err = phone.service.Sync(ctx, appsync.TriggerManual)
	if err != nil || !result.Uploaded {
		t.Fatalf("phone upload after desktop deletion: result=%+v error=%v", result, err)
	}
	uploaded, _ = readWayfinderS3Payload(t, server)
	if _, present := uploaded.Settings[deletedKey]; present {
		t.Fatalf("phone revived desktop-deleted document %s", deletedKey)
	}
	assertDesktopDocumentsPreserved(t, desktopDocs, uploaded.Settings)
	if !useV2 && !t.Failed() {
		preview, err := phone.service.SetConvergentEnabled(ctx, true)
		if err != nil || !preview.CanInitialize {
			t.Fatalf("migrate after publishing a full v1 anchor: preview=%+v error=%v", preview, err)
		}
		migrated, schema := readWayfinderS3Payload(t, server)
		if schema != domainsync.ConvergentSchemaVersion {
			t.Fatalf("migrated schema = %d, want v2", schema)
		}
		assertDesktopDocumentsPreserved(t, desktopDocs, migrated.Settings)
		if _, present := migrated.Settings[deletedKey]; present {
			t.Fatalf("migration revived desktop-deleted document %s", deletedKey)
		}
	}
}

func seedWayfinderS3Payload(t *testing.T, server *s3fake.Server, payload appsync.Payload, schema int) {
	t.Helper()
	file, err := domainsync.EncryptPayload(payload, syncMasterPassword, domainsync.EncryptOptions{SyncSchemaVersion: schema})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := domainsync.MarshalFile(file)
	if err != nil {
		t.Fatal(err)
	}
	server.SeedObject(domainsync.SyncFileName, encoded)
}

func readWayfinderS3Payload(t *testing.T, server *s3fake.Server) (appsync.Payload, int) {
	t.Helper()
	upload, found := server.ObjectContent(domainsync.SyncFileName)
	if !found {
		t.Fatal("missing S3 cloud object")
	}
	file, err := domainsync.UnmarshalFile(upload)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := domainsync.DecryptPayload(file, syncMasterPassword)
	if err != nil {
		t.Fatal(err)
	}
	var payload appsync.Payload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		t.Fatal(err)
	}
	return payload, file.Meta.SyncSchemaVersion
}
