package sync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func payloadWithHosts(hosts ...domainvault.Host) Payload {
	return Payload{Hosts: hosts, SyncedAt: 1000}
}

func host(id, label, hostname string) domainvault.Host {
	return domainvault.Host{
		ID: model.HostID(id), Label: label, Hostname: hostname,
		Port: 22, Protocol: domainvault.ProtocolSSH, UpdatedAt: time.UnixMilli(1000),
	}
}

func serviceForStrategy(t *testing.T, strategy domainsync.Strategy, backups *fakeBackups) (*Service, *fakeRemote, *fakeVault, *memoryConfigs) {
	t.Helper()
	service, remote, vault, configs, _, _ := testServiceWith(t, WithBackups(backups))
	if err := service.SetStrategy(context.Background(), strategy); err != nil {
		t.Fatal(err)
	}
	return service, remote, vault, configs
}

// establishAnchor uploads base and returns the cloud state that follow-up
// changes diverge from.
func establishAnchor(t *testing.T, service *Service, remote *fakeRemote, base Payload) {
	t.Helper()
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatal(err)
	}
	_ = base
	_ = remote
}

func TestSmartMergeCombinesBothSidesUsingTheStoredAnchor(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, backups)
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	establishAnchor(t, service, remote, base)

	if configs.config.AnchorVersion != AnchorVersion || configs.config.SyncBase == "" || configs.config.BaseSignature == "" {
		t.Fatalf("anchor = %+v", configs.config)
	}

	// Local changes the label; another device changes the hostname. Only a
	// stored base can combine two different field edits.
	vault.set(payloadWithHosts(host("h1", "Local", "base.example")))
	remote.mu.Lock()
	remote.file = RemoteFile{
		ID: "gist-1", ETag: "etag-remote", UpdatedAt: 9000,
		Content: encryptForTest(t, payloadWithHosts(host("h1", "Base", "remote.example")), "secret", 9000),
	}
	remote.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != domainsync.ActionMerge || !result.Uploaded || !result.Downloaded {
		t.Fatalf("result = %+v", result)
	}
	merged := remote.payload(t, "secret")
	if len(merged.Hosts) != 1 || merged.Hosts[0].Label != "Local" || merged.Hosts[0].Hostname != "remote.example" {
		t.Fatalf("merged cloud host = %+v", merged.Hosts)
	}
	vault.mu.Lock()
	localHost := vault.payload.Hosts[0]
	vault.mu.Unlock()
	if localHost.Label != "Local" || localHost.Hostname != "remote.example" {
		t.Fatalf("merged local host = %+v", localHost)
	}
	if remote.writeCount() != 2 {
		t.Fatalf("writes = %d, want initial upload plus merge", remote.writeCount())
	}
	if backups.createdCount() == 0 {
		t.Fatal("merge did not take a protective local backup")
	}
}

// TestSmartMergeCarriesSettingsThroughTheService proves the deep merge is not
// only reachable through the pure function: the service merges a payload's
// settings, applies the merged snapshot, and publishes it. The real Vault
// adapter intentionally omits device-local settings from its snapshot (ticket
// 49 and spec section Cloud sync), so the engine is exercised here with a
// payload that carries settings.
func TestSmartMergeCarriesSettingsThroughTheService(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, _ := serviceForStrategy(t, domainsync.StrategySmartMerge, backups)
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	base.Settings = settingsFromJSON(t, `{
		"theme":{"mode":"light","accent":"blue","nested":{"a":1,"b":2}},
		"columns":3
	}`)
	vault.set(base)
	establishAnchor(t, service, remote, base)

	local := payloadWithHosts(host("h1", "Base", "base.example"))
	local.Settings = settingsFromJSON(t, `{
		"theme":{"mode":"dark","accent":"blue","nested":{"a":1,"b":2}},
		"columns":4,
		"unknownLocal":"kept"
	}`)
	remotePayload := payloadWithHosts(host("h1", "Base", "base.example"))
	remotePayload.Settings = settingsFromJSON(t, `{
		"theme":{"mode":"light","accent":"red","nested":{"a":1,"b":5}},
		"columns":3,
		"unknownRemote":"kept"
	}`)
	vault.set(local)
	remote.mu.Lock()
	remote.file = RemoteFile{
		ID: "gist-1", ETag: "etag-remote", UpdatedAt: 9000,
		Content: encryptForTest(t, remotePayload, "secret", 9000),
	}
	remote.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != domainsync.ActionMerge {
		t.Fatalf("result = %+v", result)
	}
	merged := remote.payload(t, "secret")
	settings := settingsToStringMap(t, merged.Settings)
	theme := settings["theme"].(map[string]any)
	if theme["mode"] != "dark" || theme["accent"] != "red" {
		t.Fatalf("merged theme = %v", theme)
	}
	nested := theme["nested"].(map[string]any)
	if nested["a"] != json.Number("1") || nested["b"] != json.Number("5") {
		t.Fatalf("merged nested settings = %v", nested)
	}
	if settings["columns"] != json.Number("4") ||
		settings["unknownLocal"] != "kept" || settings["unknownRemote"] != "kept" {
		t.Fatalf("merged settings = %v", settings)
	}
	vault.mu.Lock()
	applied := vault.payload.Settings
	vault.mu.Unlock()
	if !settingsEqual(applied, merged.Settings) {
		t.Fatalf("the merged settings were not applied locally: %v", applied)
	}
}

// TestSmartMergeSameFieldClashUsesLocalWithoutAPendingConflict documents the
// original strategy semantics: smartMerge reports a same-field clash in
// MergeResult but resolves it in favor of this device and completes the merge.
// A durable, user-resolved conflict is reserved for a lost conditional write,
// where neither side's complete copy is a safe automatic winner.
func TestSmartMergeSameFieldClashUsesLocalWithoutAPendingConflict(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, _ := serviceForStrategy(t, domainsync.StrategySmartMerge, backups)
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	establishAnchor(t, service, remote, base)

	vault.set(payloadWithHosts(host("h1", "Local", "base.example")))
	remote.mu.Lock()
	remote.file = RemoteFile{
		ID: "gist-1", ETag: "etag-remote", UpdatedAt: 9000,
		Content: encryptForTest(t, payloadWithHosts(host("h1", "Cloud", "base.example")), "secret", 9000),
	}
	remote.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != domainsync.ActionMerge {
		t.Fatalf("result = %+v", result)
	}
	if merged := remote.payload(t, "secret"); merged.Hosts[0].Label != "Local" {
		t.Fatalf("same-field clash did not keep the local value: %+v", merged.Hosts)
	}
	if snapshot := service.Snapshot(); snapshot.Conflict != nil || snapshot.Problem == ProblemConflict {
		t.Fatalf("a smartMerge field clash became a pending conflict: %+v", snapshot)
	}
}

func TestSmartMergePreserveKeepsAOneSidedDeletion(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, _ := serviceForStrategy(t, domainsync.StrategySmartMergePreserve, backups)
	base := payloadWithHosts(
		host("h1", "One", "one.example"),
		host("h2", "Two", "two.example"),
	)
	vault.set(base)
	establishAnchor(t, service, remote, base)

	// Local deletes h2 while the other device edits h1. Smart-merge-preserve
	// must not propagate the one-sided deletion.
	vault.set(payloadWithHosts(host("h1", "One", "one.example")))
	remote.mu.Lock()
	remote.file = RemoteFile{
		ID: "gist-1", ETag: "etag-remote", UpdatedAt: 9000,
		Content: encryptForTest(t, payloadWithHosts(
			host("h1", "One", "edited.example"),
			host("h2", "Two", "two.example"),
		), "secret", 9000),
	}
	remote.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != domainsync.ActionMerge {
		t.Fatalf("result = %+v", result)
	}
	merged := remote.payload(t, "secret")
	if len(merged.Hosts) != 2 {
		t.Fatalf("preserve merge dropped an entity: %+v", merged.Hosts)
	}
	byID := map[string]domainvault.Host{}
	for _, item := range merged.Hosts {
		byID[string(item.ID)] = item
	}
	if byID["h1"].Hostname != "edited.example" || byID["h2"].Label != "Two" {
		t.Fatalf("merged hosts = %+v", byID)
	}
}

func TestSmartMergeDeletesAOneSidedDeletion(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, _ := serviceForStrategy(t, domainsync.StrategySmartMerge, backups)
	base := payloadWithHosts(
		host("h1", "One", "one.example"),
		host("h2", "Two", "two.example"),
	)
	vault.set(base)
	establishAnchor(t, service, remote, base)

	vault.set(payloadWithHosts(host("h1", "One", "one.example")))
	remote.mu.Lock()
	remote.file = RemoteFile{
		ID: "gist-1", ETag: "etag-remote", UpdatedAt: 9000,
		Content: encryptForTest(t, payloadWithHosts(
			host("h1", "One", "edited.example"),
			host("h2", "Two", "two.example"),
		), "secret", 9000),
	}
	remote.mu.Unlock()

	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatal(err)
	}
	merged := remote.payload(t, "secret")
	if len(merged.Hosts) != 1 || merged.Hosts[0].ID != "h1" {
		t.Fatalf("smartMerge should propagate the deletion: %+v", merged.Hosts)
	}
}

func TestPreferCloudAppliesTheCloudCopyOnATwoSidedChange(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, _ := serviceForStrategy(t, domainsync.StrategyPreferCloud, backups)
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	establishAnchor(t, service, remote, base)

	vault.set(payloadWithHosts(host("h1", "Local", "local.example")))
	remote.mu.Lock()
	remote.file = RemoteFile{
		ID: "gist-1", ETag: "etag-remote", UpdatedAt: 9000,
		Content: encryptForTest(t, payloadWithHosts(host("h1", "Cloud", "cloud.example")), "secret", 9000),
	}
	remote.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != domainsync.ActionDownload || !result.Downloaded || result.Uploaded {
		t.Fatalf("result = %+v", result)
	}
	vault.mu.Lock()
	label := vault.payload.Hosts[0].Label
	vault.mu.Unlock()
	if label != "Cloud" {
		t.Fatalf("local label = %q", label)
	}
	if remote.writeCount() != 1 {
		t.Fatalf("writes = %d, preferCloud must not upload", remote.writeCount())
	}
	if backups.createdCount() != 1 {
		t.Fatalf("protective backups = %d, want 1", backups.createdCount())
	}
}

func TestPreferLocalUploadsTheLocalCopyOnATwoSidedChange(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, _ := serviceForStrategy(t, domainsync.StrategyPreferLocal, backups)
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	establishAnchor(t, service, remote, base)

	vault.set(payloadWithHosts(host("h1", "Local", "local.example")))
	remote.mu.Lock()
	remote.file = RemoteFile{
		ID: "gist-1", ETag: "etag-remote", UpdatedAt: 9000,
		Content: encryptForTest(t, payloadWithHosts(host("h1", "Cloud", "cloud.example")), "secret", 9000),
	}
	remote.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != domainsync.ActionUpload || !result.Uploaded || result.Downloaded {
		t.Fatalf("result = %+v", result)
	}
	uploaded := remote.payload(t, "secret")
	if uploaded.Hosts[0].Label != "Local" {
		t.Fatalf("uploaded label = %q", uploaded.Hosts[0].Label)
	}
	if remote.writeCount() != 2 {
		t.Fatalf("writes = %d, want initial upload plus local win", remote.writeCount())
	}
}

func TestShrinkGuardBlocksRestoreAndForceFlow(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, configs, _, _ := testServiceWith(t, WithBackups(backups))
	base := payloadWithHosts(
		host("h1", "One", "one.example"),
		host("h2", "Two", "two.example"),
		host("h3", "Three", "three.example"),
		host("h4", "Four", "four.example"),
	)
	vault.set(base)
	establishAnchor(t, service, remote, base)
	before := remote.writeCount()

	// Deleting three of four crosses both the absolute minimum and the 50%
	// ratio. Neither trigger may write.
	vault.set(payloadWithHosts(host("h1", "One", "one.example")))
	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrShrinkGuard) {
		t.Fatalf("manual error = %v, want ErrShrinkGuard", err)
	}
	snapshot := service.Snapshot()
	if snapshot.Problem != ProblemShrinkGuard || snapshot.Guard.Kind != GuardShrink || snapshot.Guard.Lost != 3 {
		t.Fatalf("guard snapshot = %+v", snapshot)
	}
	if _, err := service.Sync(context.Background(), TriggerAuto); !errors.Is(err, ErrShrinkGuard) {
		t.Fatalf("auto error = %v, want ErrShrinkGuard", err)
	}
	if remote.writeCount() != before {
		t.Fatal("the shrink overwrote the cloud copy")
	}

	// Restore brings back the cloud copy without writing.
	if _, err := service.RestoreGuard(context.Background()); err != nil {
		t.Fatal(err)
	}
	if remote.writeCount() != before {
		t.Fatal("restore wrote to the provider")
	}
	vault.mu.Lock()
	restored := len(vault.payload.Hosts)
	vault.mu.Unlock()
	if restored != 4 {
		t.Fatalf("restored hosts = %d, want 4", restored)
	}

	// A confirmed force push is the only way through, and it retains the copy
	// it replaces.
	vault.set(payloadWithHosts(host("h1", "One", "one.example")))
	if _, err := service.ForcePush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if payload := remote.payload(t, "secret"); len(payload.Hosts) != 1 {
		t.Fatalf("forced cloud payload = %+v", payload)
	}
	if len(configs.config.RemoteBackups) != 1 {
		t.Fatalf("retained cloud copies = %d, want 1", len(configs.config.RemoteBackups))
	}
	if backups.createdCount() == 0 {
		t.Fatal("restore did not take a protective local backup")
	}
}

func TestGuardDoesNotBlockOrdinarySmallDeletes(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, _, _, _ := testServiceWith(t, WithBackups(backups))
	base := payloadWithHosts(
		host("h1", "One", "one.example"),
		host("h2", "Two", "two.example"),
		host("h3", "Three", "three.example"),
		host("h4", "Four", "four.example"),
		host("h5", "Five", "five.example"),
	)
	vault.set(base)
	establishAnchor(t, service, remote, base)

	vault.set(payloadWithHosts(
		host("h1", "One", "one.example"),
		host("h2", "Two", "two.example"),
		host("h3", "Three", "three.example"),
	))
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("two deletions of five should sync: %v", err)
	}
	if payload := remote.payload(t, "secret"); len(payload.Hosts) != 3 {
		t.Fatalf("cloud payload = %+v", payload)
	}
}

func TestAnchorPersistsOnlyCiphertext(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, configs, _, _ := testServiceWith(t, WithBackups(backups))
	payload := payloadWithHosts(host("h1", "Very Secret Label", "secret.internal"))
	payload.Secrets = []SecretValue{{EntityType: "host", EntityID: "h1", Field: "password", Value: "super-secret-token"}}
	vault.set(payload)
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(configs.config)
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range []string{"super-secret-token", "Very Secret Label", "secret.internal"} {
		if strings.Contains(string(encoded), plaintext) {
			t.Fatalf("cloud-sync.json leaked %q: %s", plaintext, encoded)
		}
	}
	base := service.loadBase(context.Background())
	if base == nil {
		t.Fatal("the encrypted anchor did not decode")
	}
	if len(base.Hosts) != 1 || len(base.Secrets) != 1 {
		t.Fatalf("anchor payload = %+v", base)
	}
	_ = remote
}

func TestConflictSurvivesARestartAndResolvesIdempotently(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, configs, tokens, waiter := testServiceWith(t, WithBackups(backups))
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	establishAnchor(t, service, remote, base)

	vault.set(payloadWithHosts(host("h1", "Local edit", "base.example")))
	remote.mu.Lock()
	remote.conflictNext = true
	remote.conflictFile = RemoteFile{
		ID: "gist-1", ETag: "etag-cloud", UpdatedAt: 12000,
		Content: encryptForTest(t, payloadWithHosts(host("h1", "Cloud edit", "cloud.example")), "secret", 12000),
	}
	remote.mu.Unlock()
	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want a surfaced conflict", err)
	}

	// A new Service over the same persisted profile must still see it.
	restarted := NewService(remote, vault, configs, tokens,
		WithWaiter(waiter),
		WithClock(func() time.Time { return time.UnixMilli(5000) }),
		WithDevice("device-1", "Test Device", "1.0"),
		WithBackups(backups),
	)
	if err := restarted.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.SetPassword("secret"); err != nil {
		t.Fatal(err)
	}
	snapshot := restarted.Snapshot()
	if snapshot.Status != StatusConflict || snapshot.Conflict == nil || len(snapshot.Conflict.Items) == 0 {
		t.Fatalf("restarted snapshot = %+v", snapshot)
	}
	if _, err := restarted.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrConflict) {
		t.Fatalf("restarted sync error = %v, want ErrConflict", err)
	}

	result, err := restarted.ResolveConflict(context.Background(), ResolutionRemote)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded {
		t.Fatalf("result = %+v", result)
	}
	vault.mu.Lock()
	label := vault.payload.Hosts[0].Label
	vault.mu.Unlock()
	if label != "Cloud edit" {
		t.Fatalf("resolved label = %q", label)
	}
	// Applying the same resolution again is a no-op success.
	if _, err := restarted.ResolveConflict(context.Background(), ResolutionRemote); err != nil {
		t.Fatalf("second resolution = %v, want idempotent success", err)
	}
	if configs.config.PendingConflict != nil {
		t.Fatal("the conflict was not cleared")
	}
}

func TestConflictMergeResolutionIsOfferedOnlyByMergeStrategies(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, _ := serviceForStrategy(t, domainsync.StrategyPreferLocal, backups)
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	establishAnchor(t, service, remote, base)
	vault.set(payloadWithHosts(host("h1", "Local", "base.example")))
	remote.mu.Lock()
	remote.conflictNext = true
	remote.conflictFile = RemoteFile{
		ID: "gist-1", ETag: "etag-cloud", UpdatedAt: 12000,
		Content: encryptForTest(t, payloadWithHosts(host("h1", "Cloud", "cloud.example")), "secret", 12000),
	}
	remote.mu.Unlock()
	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want a surfaced conflict", err)
	}
	if snapshot := service.Snapshot(); snapshot.Conflict == nil || snapshot.Conflict.CanMerge {
		t.Fatalf("preferLocal conflict offers merge: %+v", snapshot.Conflict)
	}
	if _, err := service.ResolveConflict(context.Background(), ResolutionMerge); !errors.Is(err, ErrResolutionUnsupported) {
		t.Fatalf("merge resolution error = %v, want ErrResolutionUnsupported", err)
	}
}

func TestBackupListAndRestoreLocalAndRemote(t *testing.T) {
	backups := &fakeBackups{list: []BackupInfo{
		{ID: "local-old", CreatedAt: 100, Reason: "vault_change"},
		{ID: "local-new", CreatedAt: 300, Reason: "before_restore"},
	}}
	service, remote, vault, configs, _, _ := testServiceWith(t, WithBackups(backups))
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	establishAnchor(t, service, remote, base)

	remotePayload := payloadWithHosts(host("h1", "Retained cloud", "old.example"))
	retained := RemoteBackup{
		ID: "remote-1", CreatedAt: 200, Reason: "merge",
		RemoteID: "gist-1", RemoteETag: "etag-old", RemoteUpdatedAt: 8000,
		Device: "Other", Version: 2,
		Content: encryptForTest(t, remotePayload, "secret", 8000),
	}
	service.mu.Lock()
	service.config.RemoteBackups = []RemoteBackup{retained}
	service.mu.Unlock()

	if err := service.LoadBackups(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := service.Snapshot()
	if snapshot.BackupsProblem != ProblemNone || len(snapshot.Backups) != 3 {
		t.Fatalf("backups = %+v, problem = %q", snapshot.Backups, snapshot.BackupsProblem)
	}
	if snapshot.Backups[0].ID != "local-new" || snapshot.Backups[1].ID != "remote-1" || snapshot.Backups[2].ID != "local-old" {
		t.Fatalf("backup order = %+v", snapshot.Backups)
	}
	if snapshot.Backups[1].Source != BackupSourceRemote || snapshot.Backups[1].SizeBytes == 0 {
		t.Fatalf("remote backup info = %+v", snapshot.Backups[1])
	}

	// Restoring a local backup delegates to the backup service and makes the
	// restored Vault a local change.
	if err := service.RestoreBackup(context.Background(), BackupSourceLocal, "local-new"); err != nil {
		t.Fatal(err)
	}
	backups.mu.Lock()
	restored := append([]string(nil), backups.restored...)
	backups.mu.Unlock()
	if len(restored) != 1 || restored[0] != "local-new" {
		t.Fatalf("restored = %+v", restored)
	}
	if configs.config.LocalSignature != "" {
		t.Fatal("a restored local backup must be treated as a local change")
	}

	// Restoring a retained cloud copy applies its decrypted payload after a
	// protective backup and never writes to the provider.
	writes := remote.writeCount()
	if err := service.RestoreBackup(context.Background(), BackupSourceRemote, "remote-1"); err != nil {
		t.Fatal(err)
	}
	vault.mu.Lock()
	label := vault.payload.Hosts[0].Label
	vault.mu.Unlock()
	if label != "Retained cloud" {
		t.Fatalf("restored remote label = %q", label)
	}
	if remote.writeCount() != writes {
		t.Fatal("restoring a retained cloud copy wrote to the provider")
	}
	if backups.createdCount() == 0 {
		t.Fatal("restoring a retained cloud copy took no protective backup")
	}
}

func TestLoadBackupsReportsALocalListFailure(t *testing.T) {
	backups := &fakeBackups{listErr: errors.New("disk unavailable")}
	service, _, _, _, _, _ := testServiceWith(t, WithBackups(backups))
	if err := service.LoadBackups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot := service.Snapshot(); snapshot.BackupsProblem != ProblemLocal {
		t.Fatalf("problem = %q", snapshot.BackupsProblem)
	}
}

func TestConflictResolutionRequiresABackupPort(t *testing.T) {
	service, remote, vault, _, _, _ := testService(t)
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	establishAnchor(t, service, remote, base)
	vault.set(payloadWithHosts(host("h1", "Local", "base.example")))
	remote.mu.Lock()
	remote.conflictNext = true
	remote.conflictFile = RemoteFile{
		ID: "gist-1", ETag: "etag-cloud", UpdatedAt: 12000,
		Content: encryptForTest(t, payloadWithHosts(host("h1", "Cloud", "cloud.example")), "secret", 12000),
	}
	remote.mu.Unlock()
	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want a surfaced conflict", err)
	}
	if _, err := service.ResolveConflict(context.Background(), ResolutionRemote); !errors.Is(err, ErrBackupUnavailable) {
		t.Fatalf("resolution error = %v, want ErrBackupUnavailable", err)
	}
	vault.mu.Lock()
	label := vault.payload.Hosts[0].Label
	vault.mu.Unlock()
	if label != "Local" {
		t.Fatalf("a refused resolution changed the Vault: %q", label)
	}
}
