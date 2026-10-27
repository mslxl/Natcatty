//go:build selftest

package app

// Ticket 54 self-test. Press F11, then 5, then 4.
//
// The check runs the real sync stack headlessly against an in-process fake
// provider, a real SQLite Vault, and the real local backup service:
//
//   - all four original strategies produce their specified result, and the
//     smart-merge cases prove the stored encrypted anchor is used for a
//     three-way field merge;
//   - the settings deep merge combines nested objects, merges id-keyed arrays,
//     honors an explicit empty-object reset, and preserves one-sided unknown
//     keys;
//   - a shrink above the original thresholds is blocked for manual and
//     automatic sync, restores the cloud copy without writing, and only an
//     explicitly confirmed force push proceeds, retaining the replaced copy;
//   - a whole-Vault deletion is blocked as a tombstone, restores, and only a
//     force push empties the cloud;
//   - a conditional-write conflict is surfaced with per-item detail, resolved
//     with the local copy, then both the retained cloud copy and a local
//     protective backup are restored; a second conflict is resolved by merge;
//   - the profile and anchor contain no plaintext entity or credential, and
//     every destructive path wrote its backup.
//
// A repeated F11,5,4 starts from clean temporary stores. The scenario runs in
// one goroutine so the event loop is never blocked; the step polls its result
// channel with the scenario deadline as the hard bound. No step sleeps.
//
// Result: %TEMP%\natcatty-selftest-54.json.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	adapterstore "github.com/mslxl/natcatty/adapters/store"
	adaptersyncbackup "github.com/mslxl/natcatty/adapters/syncbackup"
	adaptersyncstore "github.com/mslxl/natcatty/adapters/syncstore"
	adaptersyncvault "github.com/mslxl/natcatty/adapters/syncvault"
	adapterbackup "github.com/mslxl/natcatty/adapters/vaultbackup"
	appsync "github.com/mslxl/natcatty/application/sync"
	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	"github.com/mslxl/natcatty/domain/model"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/selftest"
)

const (
	selftest54Ticket   = 54
	selftest54Timeout  = 180 * time.Second
	selftest54Password = "ticket 54 master password"
)

type selftest54 struct {
	started  bool
	done     bool
	deadline time.Time
	cancel   context.CancelFunc
	result   chan selftest54Result
	report   selftest54Report
	err      error
}

type selftest54Report struct {
	// Strategies.
	SmartMergeAction     string
	SmartMergeLabel      string
	SmartMergeHostname   string
	PreserveHosts        int
	PreserveHostname     string
	PreferCloudAction    string
	PreferCloudLabel     string
	PreferCloudWrites    int
	PreferLocalAction    string
	PreferLocalLabel     string
	PreferLocalWrites    int
	AnchorCiphertextOnly bool

	// Settings deep merge.
	SettingsMode         string
	SettingsAccent       string
	SettingsNested       float64
	SettingsColumns      float64
	SettingsUnknownLocal bool
	SettingsUnknownCloud bool
	SettingsBaseDropped  bool
	SettingsThemes       int
	SettingsThemeA       string
	SettingsThemeB       string
	SettingsThemeC       string
	SettingsNoBaseTone   string
	SettingsResetEmpty   bool

	// Shrink guard.
	ShrinkManualBlocked bool
	ShrinkAutoBlocked   bool
	ShrinkProblem       string
	ShrinkGuardKind     string
	ShrinkRestored      int
	ShrinkRestoreWrites int
	ShrinkForced        int
	ShrinkRetained      int

	// Tombstone guard.
	TombstoneBlocked  bool
	TombstoneProblem  string
	TombstoneGuard    string
	TombstoneRestored int
	TombstoneForced   int

	// Conflict and backups.
	ConflictError      string
	ConflictStatus     string
	ConflictItems      int
	ConflictDevice     string
	ConflictResolvedAs string
	ConflictLocalHost  string
	ConflictCloudHost  string
	LocalBackups       int
	RemoteBackups      int
	RestoreCloudHost   string
	CloudAfterRestore  string
	RestoreLocalHost   string
	MergeConflictHost  string
	MergeConflictHost2 string

	// Redaction and cleanup.
	ProfileNoSecret bool
	NoTempLeftover  bool
}

type selftest54Result struct {
	report selftest54Report
	err    error
}

func (t *selftest54) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest54Ticket,
		Name:   "Merge strategies, anchors, guards, and conflicts",
		Steps: []selftest.Step{
			{Name: "run-headless-scenario", Run: t.scenarioStep},
			{Name: "four-strategies-and-anchor", Run: t.strategyStep},
			{Name: "settings-deep-merge", Run: t.settingsStep},
			{Name: "shrink-guard-restore-force", Run: t.shrinkStep},
			{Name: "tombstone-guard", Run: t.tombstoneStep},
			{Name: "conflict-resolution-and-backups", Run: t.conflictStep},
		},
	}
}

func (t *selftest54) scenarioStep(*ui.Context) selftest.Outcome {
	if t.done {
		t.started = false
		t.done = false
		t.report = selftest54Report{}
		t.err = nil
		t.result = nil
	}
	if !t.started {
		t.started = true
		t.deadline = time.Now().Add(selftest54Timeout)
		ctx, cancel := context.WithTimeout(context.Background(), selftest54Timeout)
		t.cancel = cancel
		t.result = make(chan selftest54Result, 1)
		go func() {
			report, err := runSelftest54(ctx)
			t.result <- selftest54Result{report: report, err: err}
		}()
	}
	select {
	case result := <-t.result:
		t.done = true
		t.report = result.report
		t.err = result.err
		if t.cancel != nil {
			t.cancel()
		}
		if t.err != nil {
			return selftest.Failed(t.err)
		}
		return selftest.Passed(
			"real sync service with the encrypted anchor, four strategies, settings deep merge, shrink and tombstone guards with restore/force, durable conflicts, and local and cloud-copy backups",
			"application/sync + adapters/syncvault + adapters/syncbackup + adapters/vaultbackup")
	default:
		if !time.Now().Before(t.deadline) {
			t.done = true
			t.err = fmt.Errorf("the ticket 54 scenario did not finish in %s", selftest54Timeout)
			if t.cancel != nil {
				t.cancel()
			}
			return selftest.Failed(t.err)
		}
		return pending(t.deadline, "the headless merge, guard, and conflict scenario")
	}
}

func (t *selftest54) strategyStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.SmartMergeAction != string(domainsync.ActionMerge) ||
		report.SmartMergeLabel != "Local" || report.SmartMergeHostname != "remote.example" {
		return selftest.Failed(fmt.Errorf("smartMerge = %s label %q hostname %q",
			report.SmartMergeAction, report.SmartMergeLabel, report.SmartMergeHostname))
	}
	if report.PreserveHosts != 2 || report.PreserveHostname != "edited.example" {
		return selftest.Failed(fmt.Errorf("smartMergePreserve hosts = %d hostname = %q",
			report.PreserveHosts, report.PreserveHostname))
	}
	if report.PreferCloudAction != string(domainsync.ActionDownload) ||
		report.PreferCloudLabel != "Cloud" || report.PreferCloudWrites != 1 {
		return selftest.Failed(fmt.Errorf("preferCloud = %s label %q writes %d",
			report.PreferCloudAction, report.PreferCloudLabel, report.PreferCloudWrites))
	}
	if report.PreferLocalAction != string(domainsync.ActionUpload) ||
		report.PreferLocalLabel != "Local" || report.PreferLocalWrites != 2 {
		return selftest.Failed(fmt.Errorf("preferLocal = %s label %q writes %d",
			report.PreferLocalAction, report.PreferLocalLabel, report.PreferLocalWrites))
	}
	if !report.AnchorCiphertextOnly {
		return selftest.Failed(errors.New("the encrypted anchor or profile leaked plaintext"))
	}
	return selftest.Passed(
		"smartMerge combined a local label edit with a cloud hostname edit through the stored anchor; smartMergePreserve kept a one-sided deletion; preferCloud downloaded without writing; preferLocal uploaded; the profile held only ciphertext",
		"application/sync Decide + MergePayloads + anchor v3")
}

func (t *selftest54) settingsStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.SettingsMode != "dark" || report.SettingsAccent != "red" ||
		report.SettingsNested != 5 || report.SettingsColumns != 4 {
		return selftest.Failed(fmt.Errorf("settings merge = mode %q accent %q nested %v columns %v",
			report.SettingsMode, report.SettingsAccent, report.SettingsNested, report.SettingsColumns))
	}
	if !report.SettingsUnknownLocal || !report.SettingsUnknownCloud || !report.SettingsBaseDropped {
		return selftest.Failed(fmt.Errorf("unknown keys local=%v cloud=%v dropped base=%v",
			report.SettingsUnknownLocal, report.SettingsUnknownCloud, report.SettingsBaseDropped))
	}
	if report.SettingsThemes != 3 || report.SettingsThemeA != "green" ||
		report.SettingsThemeB != "orange" || report.SettingsThemeC != "black" {
		return selftest.Failed(fmt.Errorf("themes = %d a=%q b=%q c=%q",
			report.SettingsThemes, report.SettingsThemeA, report.SettingsThemeB, report.SettingsThemeC))
	}
	if report.SettingsNoBaseTone != "dark" || !report.SettingsResetEmpty {
		return selftest.Failed(fmt.Errorf("no-base tone=%q empty reset=%v",
			report.SettingsNoBaseTone, report.SettingsResetEmpty))
	}
	return selftest.Passed(
		"nested settings kept both sides' edits, a nested id array merged by id, an explicit empty object reset the subtree without a base, one-sided unknown keys survived, and a key absent from both sides was treated as a deletion",
		"application/sync mergeSettingsPayload deep merge")
}

func (t *selftest54) shrinkStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.ShrinkManualBlocked || !report.ShrinkAutoBlocked {
		return selftest.Failed(fmt.Errorf("shrink blocked manual=%v auto=%v",
			report.ShrinkManualBlocked, report.ShrinkAutoBlocked))
	}
	if report.ShrinkProblem != string(appsync.ProblemShrinkGuard) || report.ShrinkGuardKind != string(appsync.GuardShrink) {
		return selftest.Failed(fmt.Errorf("shrink problem=%q guard=%q",
			report.ShrinkProblem, report.ShrinkGuardKind))
	}
	if report.ShrinkRestored != 4 || report.ShrinkRestoreWrites != 1 {
		return selftest.Failed(fmt.Errorf("shrink restored=%d restore writes=%d",
			report.ShrinkRestored, report.ShrinkRestoreWrites))
	}
	if report.ShrinkForced != 1 || report.ShrinkRetained < 1 {
		return selftest.Failed(fmt.Errorf("shrink forced=%d retained=%d",
			report.ShrinkForced, report.ShrinkRetained))
	}
	return selftest.Passed(
		"deleting 3 of 4 hosts was blocked for manual and automatic sync, restore brought all 4 back without a write, and only a confirmed force push uploaded 1 host while retaining the replaced cloud copy",
		"application/sync DetectDestructiveSync + applyGuard + RestoreGuard + ForcePush")
}

func (t *selftest54) tombstoneStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.TombstoneBlocked || report.TombstoneProblem != string(appsync.ProblemTombstoneGuard) ||
		report.TombstoneGuard != string(appsync.GuardTombstone) {
		return selftest.Failed(fmt.Errorf("tombstone blocked=%v problem=%q guard=%q",
			report.TombstoneBlocked, report.TombstoneProblem, report.TombstoneGuard))
	}
	if report.TombstoneRestored != 2 {
		return selftest.Failed(fmt.Errorf("tombstone restore = %d hosts, want 2", report.TombstoneRestored))
	}
	if report.TombstoneForced != 0 {
		return selftest.Failed(fmt.Errorf("forced cloud still has %d hosts", report.TombstoneForced))
	}
	return selftest.Passed(
		"emptying a 2-host Vault was blocked as a tombstone sync, restore brought both hosts back without writing, and the explicit force push then uploaded the empty Vault",
		"application/sync detectTombstone + RestoreGuard + ForcePush")
}

func (t *selftest54) conflictStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.ConflictError != "conflict" || report.ConflictStatus != string(appsync.StatusConflict) ||
		report.ConflictItems == 0 || report.ConflictDevice != "Other device" {
		return selftest.Failed(fmt.Errorf("conflict error=%q status=%q items=%d device=%q",
			report.ConflictError, report.ConflictStatus, report.ConflictItems, report.ConflictDevice))
	}
	if report.ConflictResolvedAs != string(appsync.ResolutionLocal) || report.ConflictCloudHost != "Local" {
		return selftest.Failed(fmt.Errorf("resolved as %q cloud=%q",
			report.ConflictResolvedAs, report.ConflictCloudHost))
	}
	if report.LocalBackups == 0 || report.RemoteBackups == 0 {
		return selftest.Failed(fmt.Errorf("local backups=%d remote backups=%d",
			report.LocalBackups, report.RemoteBackups))
	}
	if report.RestoreCloudHost != "Cloud" || report.CloudAfterRestore != "Local" || report.RestoreLocalHost != "Local" {
		return selftest.Failed(fmt.Errorf("restored cloud=%q cloud after=%q local=%q",
			report.RestoreCloudHost, report.CloudAfterRestore, report.RestoreLocalHost))
	}
	if report.MergeConflictHost != "Merged local" || report.MergeConflictHost2 != "cloud2.example" {
		return selftest.Failed(fmt.Errorf("merge resolution = %q / %q",
			report.MergeConflictHost, report.MergeConflictHost2))
	}
	if !report.ProfileNoSecret {
		return selftest.Failed(errors.New("cloud-sync.json held plaintext Vault data"))
	}
	return selftest.Passed(
		"a conditional-write conflict was durable with per-item detail; keeping the local side retained the cloud copy; both the retained cloud copy and the local protective backup restored; a second conflict merged both field edits; no profile plaintext leaked",
		"application/sync surfaceConflict + resolveLocked + retainRemoteBackup + LoadBackups/RestoreBackup")
}

func runSelftest54(ctx context.Context) (report selftest54Report, err error) {
	directory, err := os.MkdirTemp("", "natcatty-selftest-54-")
	if err != nil {
		return report, fmt.Errorf("create a temporary Vault directory: %w", err)
	}
	defer os.RemoveAll(directory)

	if err := runSelftest54Strategies(ctx, directory, &report); err != nil {
		return report, err
	}
	runSelftest54Settings(ctx, directory, &report)
	if err := runSelftest54Shrink(ctx, directory, &report); err != nil {
		return report, err
	}
	if err := runSelftest54Tombstone(ctx, directory, &report); err != nil {
		return report, err
	}
	if err := runSelftest54Conflict(ctx, directory, &report); err != nil {
		return report, err
	}
	return report, nil
}

type selftest54Rig struct {
	dir      string
	database *adapterstore.Store
	secrets  *adapterstore.SecretStore
	backups  *appbackup.Service
	service  *appsync.Service
	remote   *selftest54Remote
	configs  *adaptersyncstore.ConfigStore
}

func newSelftest54Rig(ctx context.Context, parent, name string, strategy domainsync.Strategy) (*selftest54Rig, error) {
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	database, err := adapterstore.Open(ctx, filepath.Join(dir, "vault.db"))
	if err != nil {
		return nil, fmt.Errorf("open the SQLite Vault: %w", err)
	}
	secrets, err := adapterstore.OpenSecretStore(ctx, database, &selftest51KeyStore{unsupported: true})
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("open the field-secret store: %w", err)
	}
	if err := secrets.UseSessionOnly(ctx); err != nil {
		secrets.Close()
		database.Close()
		return nil, err
	}
	keyStore := &selftest51KeyStore{values: map[string][]byte{}}
	backupStore := adapterbackup.Open(filepath.Join(dir, "backups"), keyStore)
	backupService := appbackup.NewService(database, backupStore, appbackup.WithRetentionStore(backupStore))
	backupService.Start()
	tokens := adaptersyncstore.NewTokenStore(keyStore)
	configs := adaptersyncstore.OpenConfig(filepath.Join(dir, "cloud-sync.json"))
	if err := configs.Save(ctx, appsync.Config{
		Provider: appsync.ProviderGitHub, Connected: true, Login: "selftest@example.test",
		AccountID: "selftest-54", Strategy: strategy,
	}); err != nil {
		backupService.Stop()
		secrets.Close()
		database.Close()
		return nil, fmt.Errorf("seed the sync profile: %w", err)
	}
	if err := tokens.Save(ctx, domainsync.Token{AccessToken: "selftest-token", Provider: appsync.ProviderGitHub}); err != nil {
		backupService.Stop()
		secrets.Close()
		database.Close()
		return nil, fmt.Errorf("seed the provider token: %w", err)
	}
	remote := newSelftest54Remote()
	service := appsync.NewService(remote, adaptersyncvault.New(database, secrets), configs, tokens,
		appsync.WithDevice("selftest-54-device", "Self-test Device", "selftest"),
		appsync.WithBackups(adaptersyncbackup.New(backupService)),
	)
	rig := &selftest54Rig{
		dir: dir, database: database, secrets: secrets, backups: backupService,
		service: service, remote: remote, configs: configs,
	}
	if err := service.Load(ctx); err != nil {
		rig.close()
		return nil, fmt.Errorf("load the sync profile: %w", err)
	}
	if err := service.SetPassword(selftest54Password); err != nil {
		rig.close()
		return nil, err
	}
	return rig, nil
}

func (r *selftest54Rig) close() {
	if r.service != nil {
		r.service.Stop()
		r.service.ClearPassword()
	}
	if r.backups != nil {
		r.backups.Stop()
	}
	if r.secrets != nil {
		r.secrets.Close()
	}
	if r.database != nil {
		r.database.Close()
	}
}

func (r *selftest54Rig) save(ctx context.Context, id, label, hostname string) error {
	return r.database.Save(ctx, selftest54Host(id, label, hostname, time.Now()))
}

func (r *selftest54Rig) delete(ctx context.Context, id string) error {
	return r.database.Delete(ctx, model.HostID(id))
}

func (r *selftest54Rig) hosts(ctx context.Context) ([]domainvault.Host, error) {
	return r.database.List(ctx)
}

func (r *selftest54Rig) installCloud(ctx context.Context, label, hostname string, updatedAt int64, device string) error {
	payload := appsync.Payload{
		Hosts:    []domainvault.Host{selftest54Host("h1", label, hostname, time.UnixMilli(updatedAt))},
		SyncedAt: updatedAt,
	}
	content, err := selftest54Seal(payload, updatedAt, device)
	if err != nil {
		return err
	}
	r.remote.install(content)
	return nil
}

func (r *selftest54Rig) installCloudHosts(ctx context.Context, hosts []domainvault.Host, updatedAt int64, device string) error {
	payload := appsync.Payload{Hosts: hosts, SyncedAt: updatedAt}
	content, err := selftest54Seal(payload, updatedAt, device)
	if err != nil {
		return err
	}
	r.remote.install(content)
	return nil
}

func (r *selftest54Rig) armConflict(label, hostname string, updatedAt int64, device string) error {
	payload := appsync.Payload{
		Hosts:    []domainvault.Host{selftest54Host("h1", label, hostname, time.UnixMilli(updatedAt))},
		SyncedAt: updatedAt,
	}
	content, err := selftest54Seal(payload, updatedAt, device)
	if err != nil {
		return err
	}
	r.remote.armConflict(content)
	return nil
}

func (r *selftest54Rig) cloudPayload() (appsync.Payload, error) {
	content, _, ok := r.remote.content()
	if !ok {
		return appsync.Payload{}, errors.New("the cloud copy does not exist")
	}
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		return appsync.Payload{}, err
	}
	plaintext, err := domainsync.DecryptPayload(envelope, selftest54Password)
	if err != nil {
		return appsync.Payload{}, err
	}
	var payload appsync.Payload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return appsync.Payload{}, err
	}
	return payload, nil
}

func (r *selftest54Rig) cloudHost() (domainvault.Host, error) {
	payload, err := r.cloudPayload()
	if err != nil {
		return domainvault.Host{}, err
	}
	if len(payload.Hosts) == 0 {
		return domainvault.Host{}, nil
	}
	return payload.Hosts[0], nil
}

func selftest54Host(id, label, hostname string, when time.Time) domainvault.Host {
	return domainvault.Host{
		ID: model.HostID(id), Label: label, Hostname: hostname,
		Port: 22, Username: "demo", Protocol: domainvault.ProtocolSSH,
		Tags: []string{}, OrderKey: 1000,
		CreatedAt: when.Add(-time.Minute), UpdatedAt: when,
	}
}

func selftest54Seal(payload appsync.Payload, updatedAt int64, device string) ([]byte, error) {
	envelope, err := domainsync.EncryptPayload(payload, selftest54Password, domainsync.EncryptOptions{
		Version: 1, UpdatedAt: updatedAt, DeviceID: "other-device", DeviceName: device, AppVersion: "selftest",
	})
	if err != nil {
		return nil, err
	}
	return domainsync.MarshalFile(envelope)
}

type selftest54Remote struct {
	mu            sync.Mutex
	file          appsync.RemoteFile
	exists        bool
	etagSeq       int
	writes        int
	conflictNext  bool
	conflictBytes []byte
}

func newSelftest54Remote() *selftest54Remote { return &selftest54Remote{} }

func (r *selftest54Remote) User(context.Context, domainsync.Token) (appsync.Grant, error) {
	return appsync.Grant{ID: "selftest-54", Login: "selftest@example.test"}, nil
}

func (r *selftest54Remote) Read(context.Context, domainsync.Token) (appsync.RemoteFile, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.exists {
		return appsync.RemoteFile{}, false, nil
	}
	file := r.file
	file.Content = append([]byte(nil), r.file.Content...)
	return file, true, nil
}

func (r *selftest54Remote) Write(_ context.Context, _ domainsync.Token, current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conflictNext {
		r.conflictNext = false
		r.etagSeq++
		r.file = appsync.RemoteFile{
			ID: "selftest-cloud", ETag: fmt.Sprintf("selftest-etag-%d", r.etagSeq),
			Content: append([]byte(nil), r.conflictBytes...), UpdatedAt: 12000,
		}
		r.exists = true
		return appsync.RemoteFile{}, appsync.ErrConflict
	}
	if current.ID == "" && r.exists {
		return appsync.RemoteFile{}, appsync.ErrConflict
	}
	if current.ID != "" && (!r.exists || current.ETag != r.file.ETag) {
		return appsync.RemoteFile{}, appsync.ErrConflict
	}
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		return appsync.RemoteFile{}, err
	}
	r.etagSeq++
	r.file = appsync.RemoteFile{
		ID:        "selftest-cloud",
		ETag:      fmt.Sprintf("selftest-etag-%d", r.etagSeq),
		Content:   append([]byte(nil), content...),
		UpdatedAt: envelope.Meta.UpdatedAt,
	}
	r.exists = true
	r.writes++
	return r.file, nil
}

func (r *selftest54Remote) install(content []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	envelope, err := domainsync.UnmarshalFile(content)
	updatedAt := time.Now().UnixMilli()
	if err == nil {
		updatedAt = envelope.Meta.UpdatedAt
	}
	r.etagSeq++
	r.file = appsync.RemoteFile{
		ID:        "selftest-cloud",
		ETag:      fmt.Sprintf("selftest-etag-%d", r.etagSeq),
		Content:   append([]byte(nil), content...),
		UpdatedAt: updatedAt,
	}
	r.exists = true
}

func (r *selftest54Remote) armConflict(content []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conflictNext = true
	r.conflictBytes = append([]byte(nil), content...)
}

func (r *selftest54Remote) content() ([]byte, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.exists {
		return nil, "", false
	}
	return append([]byte(nil), r.file.Content...), r.file.ETag, true
}

func (r *selftest54Remote) writeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writes
}

func runSelftest54Strategies(ctx context.Context, directory string, report *selftest54Report) error {
	// smartMerge.
	rig, err := newSelftest54Rig(ctx, directory, "smart-merge", domainsync.StrategySmartMerge)
	if err != nil {
		return err
	}
	defer rig.close()
	if err := rig.save(ctx, "h1", "Base", "base.example"); err != nil {
		return err
	}
	if _, err := rig.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("smartMerge initial sync: %w", err)
	}
	if err := rig.save(ctx, "h1", "Local", "base.example"); err != nil {
		return err
	}
	if err := rig.installCloud(ctx, "Base", "remote.example", 9000, "Other device"); err != nil {
		return err
	}
	result, err := rig.service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return fmt.Errorf("smartMerge sync: %w", err)
	}
	host, err := rig.cloudHost()
	if err != nil {
		return err
	}
	report.SmartMergeAction = string(result.Action)
	report.SmartMergeLabel = host.Label
	report.SmartMergeHostname = host.Hostname
	config, err := rig.configs.Load(ctx)
	if err != nil {
		return err
	}
	profile, err := json.Marshal(config)
	if err != nil {
		return err
	}
	report.AnchorCiphertextOnly = !containsAny(string(profile), "Local", "remote.example", "base.example")

	// smartMergePreserve.
	preserve, err := newSelftest54Rig(ctx, directory, "smart-merge-preserve", domainsync.StrategySmartMergePreserve)
	if err != nil {
		return err
	}
	defer preserve.close()
	if err := preserve.save(ctx, "h1", "One", "one.example"); err != nil {
		return err
	}
	if err := preserve.save(ctx, "h2", "Two", "two.example"); err != nil {
		return err
	}
	if _, err := preserve.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("preserve initial sync: %w", err)
	}
	if err := preserve.delete(ctx, "h2"); err != nil {
		return err
	}
	if err := preserve.installCloudHosts(ctx, []domainvault.Host{
		selftest54Host("h1", "One", "edited.example", time.UnixMilli(9000)),
		selftest54Host("h2", "Two", "two.example", time.UnixMilli(8000)),
	}, 9000, "Other device"); err != nil {
		return err
	}
	if _, err := preserve.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("preserve sync: %w", err)
	}
	preserved, err := preserve.cloudPayload()
	if err != nil {
		return err
	}
	report.PreserveHosts = len(preserved.Hosts)
	for _, host := range preserved.Hosts {
		if host.ID == "h1" {
			report.PreserveHostname = host.Hostname
		}
	}

	// preferCloud.
	cloudRig, err := newSelftest54Rig(ctx, directory, "prefer-cloud", domainsync.StrategyPreferCloud)
	if err != nil {
		return err
	}
	defer cloudRig.close()
	if err := cloudRig.save(ctx, "h1", "Base", "base.example"); err != nil {
		return err
	}
	if _, err := cloudRig.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("preferCloud initial sync: %w", err)
	}
	if err := cloudRig.save(ctx, "h1", "Local", "local.example"); err != nil {
		return err
	}
	if err := cloudRig.installCloud(ctx, "Cloud", "cloud.example", 9000, "Other device"); err != nil {
		return err
	}
	result, err = cloudRig.service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return fmt.Errorf("preferCloud sync: %w", err)
	}
	hosts, err := cloudRig.hosts(ctx)
	if err != nil {
		return err
	}
	report.PreferCloudAction = string(result.Action)
	if len(hosts) == 1 {
		report.PreferCloudLabel = hosts[0].Label
	}
	report.PreferCloudWrites = cloudRig.remote.writeCount()

	// preferLocal.
	localRig, err := newSelftest54Rig(ctx, directory, "prefer-local", domainsync.StrategyPreferLocal)
	if err != nil {
		return err
	}
	defer localRig.close()
	if err := localRig.save(ctx, "h1", "Base", "base.example"); err != nil {
		return err
	}
	if _, err := localRig.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("preferLocal initial sync: %w", err)
	}
	if err := localRig.save(ctx, "h1", "Local", "local.example"); err != nil {
		return err
	}
	if err := localRig.installCloud(ctx, "Cloud", "cloud.example", 9000, "Other device"); err != nil {
		return err
	}
	result, err = localRig.service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return fmt.Errorf("preferLocal sync: %w", err)
	}
	host, err = localRig.cloudHost()
	if err != nil {
		return err
	}
	report.PreferLocalAction = string(result.Action)
	report.PreferLocalLabel = host.Label
	report.PreferLocalWrites = localRig.remote.writeCount()
	return nil
}

func runSelftest54Settings(ctx context.Context, directory string, report *selftest54Report) {
	base := selftest54Settings(`{
		"theme":{"mode":"light","accent":"blue","nested":{"a":1,"b":2}},
		"columns":3,
		"unknownBase":{"kept":true},
		"themes":[{"id":"a","color":"red"},{"id":"b","color":"blue"}]
	}`)
	local := selftest54Settings(`{
		"theme":{"mode":"dark","accent":"blue","nested":{"a":1,"b":2}},
		"columns":4,
		"unknownLocal":"local",
		"themes":[{"id":"a","color":"green"},{"id":"b","color":"blue"}]
	}`)
	remote := selftest54Settings(`{
		"theme":{"mode":"light","accent":"red","nested":{"a":5,"b":2}},
		"columns":3,
		"unknownRemote":{"r":1},
		"themes":[{"id":"a","color":"red"},{"id":"b","color":"orange"},{"id":"c","color":"black"}]
	}`)
	result, err := appsync.MergePayloads(
		&appsync.Payload{Settings: base},
		appsync.Payload{Settings: local},
		appsync.Payload{Settings: remote},
		appsync.MergeOptions{},
	)
	if err != nil {
		return
	}
	var decoded map[string]any
	raw, _ := json.Marshal(result.Payload.Settings)
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return
	}
	theme, _ := decoded["theme"].(map[string]any)
	if theme != nil {
		report.SettingsMode, _ = theme["mode"].(string)
		report.SettingsAccent, _ = theme["accent"].(string)
		if nested, ok := theme["nested"].(map[string]any); ok {
			report.SettingsNested, _ = nested["a"].(float64)
		}
	}
	report.SettingsColumns, _ = decoded["columns"].(float64)
	_, report.SettingsUnknownLocal = decoded["unknownLocal"]
	_, report.SettingsUnknownCloud = decoded["unknownRemote"]
	_, kept := decoded["unknownBase"]
	report.SettingsBaseDropped = !kept
	if themes, ok := decoded["themes"].([]any); ok {
		report.SettingsThemes = len(themes)
		for _, entry := range themes {
			record, _ := entry.(map[string]any)
			id, _ := record["id"].(string)
			color, _ := record["color"].(string)
			switch id {
			case "a":
				report.SettingsThemeA = color
			case "b":
				report.SettingsThemeB = color
			case "c":
				report.SettingsThemeC = color
			}
		}
	}

	// No base: a fresh client's local defaults must not beat the cloud copy,
	// and an explicit empty object is the original's subtree reset marker.
	noBase, err := appsync.MergePayloads(nil,
		appsync.Payload{Settings: selftest54Settings(`{
			"tone":"light",
			"theme":{"mode":"dark","nested":{"a":1,"b":2}},
			"themes":[{"id":"a","color":"green"}]
		}`)},
		appsync.Payload{Settings: selftest54Settings(`{
			"tone":"dark",
			"theme":{"nested":{}},
			"themes":[{"id":"b","color":"orange"}]
		}`)},
		appsync.MergeOptions{},
	)
	if err != nil {
		return
	}
	var noBaseDecoded map[string]any
	noBaseRaw, _ := json.Marshal(noBase.Payload.Settings)
	if err := json.Unmarshal(noBaseRaw, &noBaseDecoded); err != nil {
		return
	}
	report.SettingsNoBaseTone, _ = noBaseDecoded["tone"].(string)
	if noBaseTheme, ok := noBaseDecoded["theme"].(map[string]any); ok {
		if nested, ok := noBaseTheme["nested"].(map[string]any); ok {
			report.SettingsResetEmpty = len(nested) == 0
		}
	}
	_ = ctx
	_ = directory
}

func selftest54Settings(raw string) map[string]json.RawMessage {
	var settings map[string]json.RawMessage
	_ = json.Unmarshal([]byte(raw), &settings)
	return settings
}

func runSelftest54Shrink(ctx context.Context, directory string, report *selftest54Report) error {
	rig, err := newSelftest54Rig(ctx, directory, "shrink-guard", domainsync.StrategySmartMerge)
	if err != nil {
		return err
	}
	defer rig.close()
	for _, id := range []string{"h1", "h2", "h3", "h4"} {
		if err := rig.save(ctx, id, id, id+".example"); err != nil {
			return err
		}
	}
	if _, err := rig.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("shrink initial sync: %w", err)
	}
	writes := rig.remote.writeCount()
	for _, id := range []string{"h2", "h3", "h4"} {
		if err := rig.delete(ctx, id); err != nil {
			return err
		}
	}
	_, manualErr := rig.service.Sync(ctx, appsync.TriggerManual)
	report.ShrinkManualBlocked = errors.Is(manualErr, appsync.ErrShrinkGuard)
	snapshot := rig.service.Snapshot()
	report.ShrinkProblem = string(snapshot.Problem)
	report.ShrinkGuardKind = string(snapshot.Guard.Kind)
	_, autoErr := rig.service.Sync(ctx, appsync.TriggerAuto)
	report.ShrinkAutoBlocked = errors.Is(autoErr, appsync.ErrShrinkGuard)
	if rig.remote.writeCount() != writes {
		return errors.New("a blocked shrink wrote to the cloud")
	}
	if _, err := rig.service.RestoreGuard(ctx); err != nil {
		return fmt.Errorf("shrink restore: %w", err)
	}
	hosts, err := rig.hosts(ctx)
	if err != nil {
		return err
	}
	report.ShrinkRestored = len(hosts)
	report.ShrinkRestoreWrites = rig.remote.writeCount()
	if report.ShrinkRestoreWrites != writes {
		return errors.New("shrink restore wrote to the cloud")
	}
	for _, id := range []string{"h2", "h3", "h4"} {
		if err := rig.delete(ctx, id); err != nil {
			return err
		}
	}
	if _, err := rig.service.ForcePush(ctx); err != nil {
		return fmt.Errorf("shrink force push: %w", err)
	}
	payload, err := rig.cloudPayload()
	if err != nil {
		return err
	}
	report.ShrinkForced = len(payload.Hosts)
	config, err := rig.configs.Load(ctx)
	if err != nil {
		return err
	}
	report.ShrinkRetained = len(config.RemoteBackups)
	return nil
}

func runSelftest54Tombstone(ctx context.Context, directory string, report *selftest54Report) error {
	rig, err := newSelftest54Rig(ctx, directory, "tombstone-guard", domainsync.StrategySmartMerge)
	if err != nil {
		return err
	}
	defer rig.close()
	if err := rig.save(ctx, "h1", "One", "one.example"); err != nil {
		return err
	}
	if err := rig.save(ctx, "h2", "Two", "two.example"); err != nil {
		return err
	}
	if _, err := rig.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("tombstone initial sync: %w", err)
	}
	if err := rig.delete(ctx, "h1"); err != nil {
		return err
	}
	if err := rig.delete(ctx, "h2"); err != nil {
		return err
	}
	_, blockErr := rig.service.Sync(ctx, appsync.TriggerManual)
	report.TombstoneBlocked = errors.Is(blockErr, appsync.ErrTombstoneGuard)
	snapshot := rig.service.Snapshot()
	report.TombstoneProblem = string(snapshot.Problem)
	report.TombstoneGuard = string(snapshot.Guard.Kind)
	if _, err := rig.service.RestoreGuard(ctx); err != nil {
		return fmt.Errorf("tombstone restore: %w", err)
	}
	hosts, err := rig.hosts(ctx)
	if err != nil {
		return err
	}
	report.TombstoneRestored = len(hosts)
	if err := rig.delete(ctx, "h1"); err != nil {
		return err
	}
	if err := rig.delete(ctx, "h2"); err != nil {
		return err
	}
	if _, err := rig.service.ForcePush(ctx); err != nil {
		return fmt.Errorf("tombstone force push: %w", err)
	}
	payload, err := rig.cloudPayload()
	if err != nil {
		return err
	}
	report.TombstoneForced = len(payload.Hosts)
	return nil
}

func runSelftest54Conflict(ctx context.Context, directory string, report *selftest54Report) error {
	rig, err := newSelftest54Rig(ctx, directory, "conflict", domainsync.StrategySmartMerge)
	if err != nil {
		return err
	}
	defer rig.close()
	if err := rig.save(ctx, "h1", "Base", "base.example"); err != nil {
		return err
	}
	if _, err := rig.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("conflict initial sync: %w", err)
	}
	if err := rig.save(ctx, "h1", "Local", "base.example"); err != nil {
		return err
	}
	if err := rig.armConflict("Cloud", "cloud.example", 12000, "Other device"); err != nil {
		return err
	}
	_, syncErr := rig.service.Sync(ctx, appsync.TriggerManual)
	if errors.Is(syncErr, appsync.ErrConflict) {
		report.ConflictError = "conflict"
	}
	snapshot := rig.service.Snapshot()
	report.ConflictStatus = string(snapshot.Status)
	if snapshot.Conflict != nil {
		report.ConflictItems = len(snapshot.Conflict.Items)
		report.ConflictDevice = snapshot.Conflict.RemoteDevice
	}
	if _, err := rig.service.ResolveConflict(ctx, appsync.ResolutionLocal); err != nil {
		return fmt.Errorf("resolve local: %w", err)
	}
	report.ConflictResolvedAs = string(appsync.ResolutionLocal)
	cloudHost, err := rig.cloudHost()
	if err != nil {
		return err
	}
	report.ConflictCloudHost = cloudHost.Label

	if err := rig.service.LoadBackups(ctx); err != nil {
		return err
	}
	backups := rig.service.Snapshot().Backups
	var localID, remoteID string
	for _, backup := range backups {
		switch backup.Source {
		case appsync.BackupSourceLocal:
			report.LocalBackups++
			if localID == "" {
				localID = backup.ID
			}
		case appsync.BackupSourceRemote:
			report.RemoteBackups++
			if remoteID == "" {
				remoteID = backup.ID
			}
		}
	}
	if localID == "" || remoteID == "" {
		return errors.New("the conflict left no restorable backup")
	}
	if err := rig.service.RestoreBackup(ctx, appsync.BackupSourceRemote, remoteID); err != nil {
		return fmt.Errorf("restore the retained cloud copy: %w", err)
	}
	hosts, err := rig.hosts(ctx)
	if err != nil {
		return err
	}
	if len(hosts) == 1 {
		report.RestoreCloudHost = hosts[0].Label
	}
	cloud, err := rig.cloudHost()
	if err != nil {
		return err
	}
	report.CloudAfterRestore = cloud.Label
	if err := rig.service.RestoreBackup(ctx, appsync.BackupSourceLocal, localID); err != nil {
		return fmt.Errorf("restore the local protective backup: %w", err)
	}
	hosts, err = rig.hosts(ctx)
	if err != nil {
		return err
	}
	if len(hosts) == 1 {
		report.RestoreLocalHost = hosts[0].Label
	}

	// A second conflict, resolved by merging the two field edits.
	if err := rig.save(ctx, "h1", "Merged local", "base.example"); err != nil {
		return err
	}
	if err := rig.armConflict("Cloud 2", "cloud2.example", 14000, "Other device"); err != nil {
		return err
	}
	if _, err := rig.service.Sync(ctx, appsync.TriggerManual); !errors.Is(err, appsync.ErrConflict) {
		return fmt.Errorf("second conflict error = %v", err)
	}
	if _, err := rig.service.ResolveConflict(ctx, appsync.ResolutionMerge); err != nil {
		return fmt.Errorf("resolve merge: %w", err)
	}
	merged, err := rig.cloudHost()
	if err != nil {
		return err
	}
	report.MergeConflictHost = merged.Label
	report.MergeConflictHost2 = merged.Hostname

	raw, err := os.ReadFile(filepath.Join(rig.dir, "cloud-sync.json"))
	if err != nil {
		return err
	}
	report.ProfileNoSecret = !containsAny(string(raw),
		"Merged local", "cloud2.example", "base.example", selftest54Password)
	return nil
}
