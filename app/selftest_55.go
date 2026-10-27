//go:build selftest

package app

// Ticket 55 self-test. Press F11, then 5, then 5.
//
// The check runs the production sync service and CRDT domain against two real
// encrypted SQLite Vaults, two independent sync profiles, the real config,
// token, and backup adapters, and one fake provider shared by both devices:
//
//   - v1 migrates to v2, writes a protective backup first, publishes an
//     explicit schema marker, and all three verification rounds report their
//     comparison and outcome;
//   - a second device adopts the v2 cloud copy, both devices exchange records,
//     concurrent edits of one field retain a multi-value register, and both
//     replicas converge to byte-identical canonical state;
//   - repeated syncs after convergence perform zero provider writes and leave
//     the provider bytes unchanged;
//   - unknown, newer, and partially migrated local records are refused with
//     the provider bytes intact and no local mutation;
//   - a forged same-vector remote state is reported as divergence, produces
//     inspect/restore/resync actions, and is never overwritten automatically.
//
// A repeated F11,5,5 resets all state and starts from a fresh temporary
// directory. The scenario runs in one goroutine; its step polls the result
// channel with the scenario deadline as the hard bound. No step sleeps.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-55.json.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/selftest"
)

const (
	selftest55Ticket   = 55
	selftest55Timeout  = 120 * time.Second
	selftest55Password = "ticket 55 master password"
)

type selftest55 struct {
	started  bool
	done     bool
	deadline time.Time
	cancel   context.CancelFunc
	result   chan selftest55Result
	report   selftest55Report
	err      error
}

type selftest55Report struct {
	MigrationMarker         int
	MigrationProviderSchema int
	MigrationWrites         int
	BackupCreated           bool
	RoundCount              int
	RoundKinds              string
	RoundOutcomes           string
	AdoptWrites             int
	DeviceAHosts            string
	DeviceBHosts            string
	ConcurrentWinner        string
	ConflictCount           int
	SameReplicaHash         bool
	SameCloudHash           bool
	RepeatedWritesA         int
	RepeatedWritesB         int
	RepeatedActionA         string
	RepeatedActionB         string
	RefusedCode             string
	RefusedRemoteUnchanged  bool
	RefusedWrites           int
	RefusedMarker           int
	FutureRefusedCode       string
	FutureRefusedWrites     int
	PartialRefused          bool
	DivergenceKind          string
	DivergenceActions       int
	DivergenceWrites        int
	DivergenceRemoteSame    bool
}

type selftest55Result struct {
	report selftest55Report
	err    error
}

func (t *selftest55) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest55Ticket,
		Name:   "Convergent sync v2: migration, three rounds, convergence, divergence",
		Steps: []selftest.Step{
			{Name: "run-headless-scenario", Run: t.scenarioStep},
			{Name: "three-verification-rounds", Run: t.roundsStep},
			{Name: "fail-closed-migration", Run: t.migrationStep},
			{Name: "convergence-and-divergence", Run: t.divergenceStep},
		},
	}
}

func (t *selftest55) scenarioStep(*ui.Context) selftest.Outcome {
	if t.done {
		t.started = false
		t.done = false
		t.report = selftest55Report{}
		t.err = nil
		t.result = nil
	}
	if !t.started {
		t.started = true
		t.deadline = time.Now().Add(selftest55Timeout)
		ctx, cancel := context.WithTimeout(context.Background(), selftest55Timeout)
		t.cancel = cancel
		t.result = make(chan selftest55Result, 1)
		go func() {
			report, err := runSelftest55(ctx)
			t.result <- selftest55Result{report: report, err: err}
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
			"real SQLite Vaults, two sync profiles, three verification rounds, fail-closed migration, multi-value convergence, zero-write repeats, and a typed divergence report",
			"application/sync convergent + domain/sync + adapters/syncvault + adapters/syncstore + adapters/syncbackup")
	default:
		if !time.Now().Before(t.deadline) {
			t.done = true
			t.err = fmt.Errorf("the ticket 55 scenario did not finish in %s", selftest55Timeout)
			if t.cancel != nil {
				t.cancel()
			}
			return selftest.Failed(t.err)
		}
		return pending(t.deadline, "the headless convergent migration and convergence scenario")
	}
}

func (t *selftest55) roundsStep(*ui.Context) selftest.Outcome {
	if t.err != nil {
		return selftest.Failed(t.err)
	}
	if t.report.RoundCount != 3 ||
		t.report.RoundKinds != "manifest,entities,state" ||
		t.report.RoundOutcomes != "verified,verified,verified" {
		return selftest.Failed(fmt.Errorf("rounds = %d kinds=%s outcomes=%s",
			t.report.RoundCount, t.report.RoundKinds, t.report.RoundOutcomes))
	}
	return selftest.Passed(
		fmt.Sprintf("rounds=%d kinds=%s outcomes=%s", t.report.RoundCount, t.report.RoundKinds, t.report.RoundOutcomes),
		"application/sync roundSatisfied (manifest VVs, per-entity hashes, full-state hash)")
}

func (t *selftest55) migrationStep(*ui.Context) selftest.Outcome {
	if t.err != nil {
		return selftest.Failed(t.err)
	}
	if !t.report.RefusedRemoteUnchanged || t.report.RefusedWrites != 0 || t.report.RefusedMarker != 0 {
		return selftest.Failed(fmt.Errorf("v1 refusal code=%s unchanged=%v writes=%d marker=%d",
			t.report.RefusedCode, t.report.RefusedRemoteUnchanged, t.report.RefusedWrites, t.report.RefusedMarker))
	}
	if t.report.FutureRefusedCode != "unsupported-newer-schema" || t.report.FutureRefusedWrites != 0 {
		return selftest.Failed(fmt.Errorf("future refusal code=%s writes=%d",
			t.report.FutureRefusedCode, t.report.FutureRefusedWrites))
	}
	if !t.report.PartialRefused {
		return selftest.Failed(errors.New("a partially migrated local record was not refused"))
	}
	return selftest.Passed(
		fmt.Sprintf("no-baseline=%s future=%s partial=refused provider-writes=0",
			t.report.RefusedCode, t.report.FutureRefusedCode),
		"application/sync readConvergentRemote + planConvergentMigration + loadConvergentStateStrict")
}

func (t *selftest55) divergenceStep(*ui.Context) selftest.Outcome {
	if t.err != nil {
		return selftest.Failed(t.err)
	}
	if !t.report.SameReplicaHash || !t.report.SameCloudHash {
		return selftest.Failed(errors.New("two device replicas did not converge to identical state"))
	}
	if t.report.RepeatedWritesA != 0 || t.report.RepeatedWritesB != 0 {
		return selftest.Failed(fmt.Errorf("repeated sync writes A=%d B=%d",
			t.report.RepeatedWritesA, t.report.RepeatedWritesB))
	}
	if t.report.DivergenceKind == "" || t.report.DivergenceActions != 3 ||
		!t.report.DivergenceRemoteSame || t.report.DivergenceWrites != 1 {
		return selftest.Failed(fmt.Errorf("divergence kind=%s actions=%d remote-same=%v writes=%d",
			t.report.DivergenceKind, t.report.DivergenceActions,
			t.report.DivergenceRemoteSame, t.report.DivergenceWrites))
	}
	return selftest.Passed(
		fmt.Sprintf("converged=%v conflict-registers=%d repeats=A:%d/B:%d divergence=%s actions=%d unchanged=%v",
			t.report.SameReplicaHash, t.report.ConflictCount,
			t.report.RepeatedWritesA, t.report.RepeatedWritesB,
			t.report.DivergenceKind, t.report.DivergenceActions, t.report.DivergenceRemoteSame),
		"application/sync three-round syncConvergent + DetectDivergence + adapter Vaults")
}

func runSelftest55(ctx context.Context) (report selftest55Report, err error) {
	directory, err := os.MkdirTemp("", "natcatty-selftest-55-")
	if err != nil {
		return report, fmt.Errorf("create a temporary Vault directory: %w", err)
	}
	defer os.RemoveAll(directory)

	if err := runSelftest55Convergence(ctx, directory, &report); err != nil {
		return report, err
	}
	if err := runSelftest55Refusals(ctx, directory, &report); err != nil {
		return report, err
	}
	if err := runSelftest55Divergence(ctx, directory, &report); err != nil {
		return report, err
	}
	return report, nil
}

type selftest55Rig struct {
	dir      string
	database *adapterstore.Store
	secrets  *adapterstore.SecretStore
	backups  *appbackup.Service
	service  *appsync.Service
	remote   *selftest55Remote
	configs  *adaptersyncstore.ConfigStore
}

func newSelftest55Rig(ctx context.Context, parent, name, deviceID string, remote *selftest55Remote, opts ...appsync.Option) (*selftest55Rig, error) {
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
		AccountID: "selftest-55", Strategy: domainsync.StrategySmartMerge,
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
	options := []appsync.Option{
		appsync.WithDevice(deviceID, "Self-test Device "+deviceID, "selftest"),
		appsync.WithBackups(adaptersyncbackup.New(backupService)),
	}
	options = append(options, opts...)
	service := appsync.NewService(remote, adaptersyncvault.New(database, secrets), configs, tokens, options...)
	rig := &selftest55Rig{
		dir: dir, database: database, secrets: secrets, backups: backupService,
		service: service, remote: remote, configs: configs,
	}
	if err := service.Load(ctx); err != nil {
		rig.close()
		return nil, fmt.Errorf("load the sync profile: %w", err)
	}
	if err := service.SetPassword(selftest55Password); err != nil {
		rig.close()
		return nil, err
	}
	return rig, nil
}

func (r *selftest55Rig) close() {
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

func (r *selftest55Rig) save(ctx context.Context, id, label, hostname string) error {
	return r.database.Save(ctx, selftest54Host(id, label, hostname, time.UnixMilli(1000)))
}

func (r *selftest55Rig) hosts(ctx context.Context) ([]domainvault.Host, error) {
	return r.database.List(ctx)
}

func (r *selftest55Rig) hostList(ctx context.Context) (string, error) {
	hosts, err := r.hosts(ctx)
	if err != nil {
		return "", err
	}
	return selftest55HostList(hosts), nil
}

func selftest55HostList(hosts []domainvault.Host) string {
	parts := make([]string, 0, len(hosts))
	for _, host := range hosts {
		parts = append(parts, string(host.ID)+"="+host.Label)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func selftest55State(ctx context.Context, configs *adaptersyncstore.ConfigStore) (domainsync.ConvergentState, error) {
	config, err := configs.Load(ctx)
	if err != nil {
		return domainsync.ConvergentState{}, err
	}
	if config.Convergent.SchemaVersion != domainsync.ConvergentSchemaVersion {
		return domainsync.ConvergentState{}, fmt.Errorf("local marker = %d", config.Convergent.SchemaVersion)
	}
	file, err := domainsync.UnmarshalFile([]byte(config.Convergent.Replica))
	if err != nil {
		return domainsync.ConvergentState{}, err
	}
	plaintext, err := domainsync.DecryptPayload(file, selftest55Password)
	if err != nil {
		return domainsync.ConvergentState{}, err
	}
	envelope, err := domainsync.ParseConvergentEnvelope(plaintext)
	if err != nil {
		return domainsync.ConvergentState{}, err
	}
	return envelope.State, nil
}

func selftest55CloudState(remote *selftest55Remote) (domainsync.ConvergentState, error) {
	content, ok := remote.content()
	if !ok {
		return domainsync.ConvergentState{}, errors.New("the cloud copy does not exist")
	}
	file, err := domainsync.UnmarshalFile(content)
	if err != nil {
		return domainsync.ConvergentState{}, err
	}
	plaintext, err := domainsync.DecryptPayload(file, selftest55Password)
	if err != nil {
		return domainsync.ConvergentState{}, err
	}
	var payload appsync.Payload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return domainsync.ConvergentState{}, err
	}
	envelope, err := domainsync.ParseConvergentEnvelope(payload.ConvergentSync)
	if err != nil {
		return domainsync.ConvergentState{}, err
	}
	return envelope.State, nil
}

func selftest55Seal(payload appsync.Payload, schema int, updatedAt int64, device string) ([]byte, error) {
	envelope, err := domainsync.EncryptPayload(payload, selftest55Password, domainsync.EncryptOptions{
		Version: 1, UpdatedAt: updatedAt, DeviceID: "other-device", DeviceName: device, AppVersion: "selftest",
		SyncSchemaVersion: schema,
	})
	if err != nil {
		return nil, err
	}
	return domainsync.MarshalFile(envelope)
}

type selftest55Remote struct {
	mu      sync.Mutex
	file    appsync.RemoteFile
	exists  bool
	etagSeq int
	writes  int
	reads   int
}

func newSelftest55Remote() *selftest55Remote { return &selftest55Remote{} }

func (r *selftest55Remote) User(context.Context, domainsync.Token) (appsync.Grant, error) {
	return appsync.Grant{ID: "selftest-55", Login: "selftest@example.test"}, nil
}

func (r *selftest55Remote) Read(context.Context, domainsync.Token) (appsync.RemoteFile, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if !r.exists {
		return appsync.RemoteFile{}, false, nil
	}
	file := r.file
	file.Content = append([]byte(nil), r.file.Content...)
	return file, true, nil
}

func (r *selftest55Remote) Write(_ context.Context, _ domainsync.Token, current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *selftest55Remote) install(content []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	updatedAt := time.Now().UnixMilli()
	if envelope, err := domainsync.UnmarshalFile(content); err == nil {
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

func (r *selftest55Remote) content() ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.exists {
		return nil, false
	}
	return append([]byte(nil), r.file.Content...), true
}

func (r *selftest55Remote) writeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writes
}

func (r *selftest55Remote) readCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

func runSelftest55Convergence(ctx context.Context, directory string, report *selftest55Report) error {
	remote := newSelftest55Remote()
	deviceA, err := newSelftest55Rig(ctx, directory, "device-a", "selftest-55-device-a", remote)
	if err != nil {
		return err
	}
	defer deviceA.close()
	deviceB, err := newSelftest55Rig(ctx, directory, "device-b", "selftest-55-device-b", remote)
	if err != nil {
		return err
	}
	defer deviceB.close()

	if err := deviceA.save(ctx, "h1", "A", "a.example"); err != nil {
		return err
	}
	beforeBackups, err := deviceA.backups.List(ctx)
	if err != nil {
		return err
	}
	preview, err := deviceA.service.SetConvergentEnabled(ctx, true)
	if err != nil {
		return fmt.Errorf("enable convergent sync: %w", err)
	}
	if !preview.CanInitialize {
		return fmt.Errorf("migration preview = %+v", preview)
	}
	afterBackups, err := deviceA.backups.List(ctx)
	if err != nil {
		return err
	}
	report.BackupCreated = len(afterBackups) > len(beforeBackups)
	if !report.BackupCreated {
		return errors.New("migration did not create a protective backup")
	}
	configA, err := deviceA.configs.Load(ctx)
	if err != nil {
		return err
	}
	report.MigrationMarker = configA.Convergent.SchemaVersion
	report.MigrationProviderSchema = preview.ProviderSchemaVersion
	if report.MigrationMarker != domainsync.ConvergentSchemaVersion || !configA.Convergent.Enabled {
		return fmt.Errorf("migration config = %+v", configA.Convergent)
	}
	infoA := deviceA.service.Snapshot().Convergent
	report.MigrationWrites = infoA.Writes
	infoRounds(infoA, report)
	if report.MigrationWrites != 1 {
		return fmt.Errorf("migration writes = %d", report.MigrationWrites)
	}
	if !selftest55HasHost(deviceA, ctx, "h1") {
		return errors.New("migration lost the local host")
	}
	cloudA, err := selftest55CloudState(remote)
	if err != nil {
		return err
	}
	hashCloudA, err := domainsync.ConvergentStateHash(cloudA)
	if err != nil {
		return err
	}
	stateLocalA, err := selftest55State(ctx, deviceA.configs)
	if err != nil {
		return fmt.Errorf("load device A state after migration: %w", err)
	}
	hashLocalA, err := domainsync.ConvergentStateHash(stateLocalA)
	if err != nil {
		return err
	}
	if hashCloudA != hashLocalA {
		return errors.New("migrated provider state differs from the local replica")
	}

	if _, err := deviceB.service.SetConvergentEnabled(ctx, true); err != nil {
		return fmt.Errorf("device B adopts v2: %w", err)
	}
	report.AdoptWrites = deviceB.service.Snapshot().Convergent.Writes
	if report.AdoptWrites != 0 {
		return fmt.Errorf("adopting device wrote %d times", report.AdoptWrites)
	}
	if !selftest55HasHost(deviceB, ctx, "h1") {
		return errors.New("device B did not adopt the cloud host")
	}
	if err := deviceB.save(ctx, "h2", "B", "b.example"); err != nil {
		return err
	}
	if _, err := deviceB.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("device B uploads h2: %w", err)
	}
	if _, err := deviceA.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("device A downloads h2: %w", err)
	}
	report.DeviceAHosts, _ = deviceA.hostList(ctx)
	report.DeviceBHosts, _ = deviceB.hostList(ctx)
	if report.DeviceAHosts != report.DeviceBHosts {
		return fmt.Errorf("hosts differ A=%s B=%s", report.DeviceAHosts, report.DeviceBHosts)
	}

	// Both devices edit h1 while offline. The register retains both values and
	// the deterministic winner is identical on both replicas.
	if err := deviceA.save(ctx, "h1", "A-concurrent", "a.example"); err != nil {
		return err
	}
	if err := deviceB.save(ctx, "h1", "B-concurrent", "a.example"); err != nil {
		return err
	}
	if _, err := deviceA.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("device A concurrent sync: %w", err)
	}
	if _, err := deviceB.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("device B concurrent sync: %w", err)
	}
	if _, err := deviceA.service.Sync(ctx, appsync.TriggerManual); err != nil {
		return fmt.Errorf("device A reconverges: %w", err)
	}
	stateA, err := selftest55State(ctx, deviceA.configs)
	if err != nil {
		return fmt.Errorf("load device A state: %w", err)
	}
	stateB, err := selftest55State(ctx, deviceB.configs)
	if err != nil {
		return fmt.Errorf("load device B state: %w", err)
	}
	hashA, err := domainsync.ConvergentStateHash(stateA)
	if err != nil {
		return err
	}
	hashB, err := domainsync.ConvergentStateHash(stateB)
	if err != nil {
		return err
	}
	cloudState, err := selftest55CloudState(remote)
	if err != nil {
		return err
	}
	hashCloud, err := domainsync.ConvergentStateHash(cloudState)
	if err != nil {
		return err
	}
	report.SameReplicaHash = hashA == hashB
	report.SameCloudHash = hashA == hashCloud
	report.ConflictCount = domainsync.ConvergentConflictCount(stateA)
	if !report.SameReplicaHash || !report.SameCloudHash {
		return errors.New("device replicas or cloud state did not converge byte-identically")
	}
	if report.ConflictCount == 0 {
		return errors.New("concurrent writes did not retain a multi-value register")
	}
	report.DeviceAHosts, _ = deviceA.hostList(ctx)
	report.DeviceBHosts, _ = deviceB.hostList(ctx)
	if report.DeviceAHosts != report.DeviceBHosts {
		return fmt.Errorf("concurrent winners differ A=%s B=%s", report.DeviceAHosts, report.DeviceBHosts)
	}
	labels := map[string]bool{}
	hosts, err := deviceA.hosts(ctx)
	if err != nil {
		return err
	}
	for _, host := range hosts {
		if string(host.ID) == "h1" {
			labels[host.Label] = true
			report.ConcurrentWinner = host.Label
		}
	}
	if !labels["A-concurrent"] && !labels["B-concurrent"] {
		return errors.New("the deterministic winner is not one of the concurrent values")
	}

	infoFinal := deviceA.service.Snapshot().Convergent
	infoRounds(infoFinal, report)
	writesBefore := remote.writeCount()
	readBefore := remote.readCount()
	resultA, err := deviceA.service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return fmt.Errorf("device A repeated sync: %w", err)
	}
	resultB, err := deviceB.service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return fmt.Errorf("device B repeated sync: %w", err)
	}
	report.RepeatedWritesA = resultA.Writes
	report.RepeatedWritesB = resultB.Writes
	report.RepeatedActionA = string(resultA.Action)
	report.RepeatedActionB = string(resultB.Action)
	if resultA.Writes != 0 || resultB.Writes != 0 || remote.writeCount() != writesBefore {
		return fmt.Errorf("repeated sync wrote: A=%d B=%d provider=%d->%d",
			resultA.Writes, resultB.Writes, writesBefore, remote.writeCount())
	}
	if remote.readCount() <= readBefore {
		return errors.New("repeated sync did not execute verification reads")
	}
	return nil
}

func infoRounds(info appsync.ConvergentInfo, report *selftest55Report) {
	report.RoundCount = len(info.Rounds)
	kinds := make([]string, 0, len(info.Rounds))
	outcomes := make([]string, 0, len(info.Rounds))
	for _, round := range info.Rounds {
		kinds = append(kinds, round.Kind)
		outcomes = append(outcomes, round.Outcome)
	}
	report.RoundKinds = strings.Join(kinds, ",")
	report.RoundOutcomes = strings.Join(outcomes, ",")
}

func selftest55HasHost(rig *selftest55Rig, ctx context.Context, id string) bool {
	hosts, err := rig.hosts(ctx)
	if err != nil {
		return false
	}
	for _, host := range hosts {
		if string(host.ID) == id {
			return true
		}
	}
	return false
}

func runSelftest55Refusals(ctx context.Context, directory string, report *selftest55Report) error {
	remote := newSelftest55Remote()
	rig, err := newSelftest55Rig(ctx, directory, "refusals", "selftest-55-refusals", remote)
	if err != nil {
		return err
	}
	defer rig.close()
	if err := rig.save(ctx, "h1", "Local", "local.example"); err != nil {
		return err
	}
	content, err := selftest55Seal(appsync.Payload{
		Hosts:    []domainvault.Host{selftest54Host("remote", "Remote", "remote.example", time.UnixMilli(9000))},
		SyncedAt: 9000,
	}, 0, 9000, "Other Device")
	if err != nil {
		return err
	}
	remote.install(content)
	before := append([]byte(nil), content...)
	preview, err := rig.service.SetConvergentEnabled(ctx, true)
	if !errors.Is(err, appsync.ErrConvergentMigration) {
		return fmt.Errorf("no-baseline refusal error = %v", err)
	}
	if len(preview.BlockedReasons) == 0 {
		return errors.New("no-baseline refusal had no typed reason")
	}
	report.RefusedCode = preview.BlockedReasons[0].Code
	after, _ := remote.content()
	report.RefusedRemoteUnchanged = string(after) == string(before)
	report.RefusedWrites = remote.writeCount()
	config, err := rig.configs.Load(ctx)
	if err != nil {
		return err
	}
	report.RefusedMarker = config.Convergent.SchemaVersion
	if !report.RefusedRemoteUnchanged || report.RefusedWrites != 0 || report.RefusedMarker != 0 {
		return fmt.Errorf("no-baseline refusal mutated state: unchanged=%v writes=%d marker=%d",
			report.RefusedRemoteUnchanged, report.RefusedWrites, report.RefusedMarker)
	}

	future, err := selftest55Seal(appsync.Payload{
		Hosts:    []domainvault.Host{selftest54Host("future", "Future", "future.example", time.UnixMilli(9000))},
		SyncedAt: 9000,
	}, 3, 9000, "Future Device")
	if err != nil {
		return err
	}
	remote.install(future)
	preview, err = rig.service.SetConvergentEnabled(ctx, true)
	if !errors.Is(err, appsync.ErrConvergentMigration) {
		return fmt.Errorf("future-schema refusal error = %v", err)
	}
	found := false
	for _, reason := range preview.BlockedReasons {
		found = found || reason.Code == "unsupported-newer-schema"
	}
	if !found {
		return fmt.Errorf("future blocked reasons = %+v", preview.BlockedReasons)
	}
	report.FutureRefusedCode = "unsupported-newer-schema"
	report.FutureRefusedWrites = remote.writeCount()
	if report.FutureRefusedWrites != 0 {
		return errors.New("future-schema refusal wrote the provider")
	}

	config, err = rig.configs.Load(ctx)
	if err != nil {
		return err
	}
	config.Convergent = appsync.ConvergentConfig{
		SchemaVersion: domainsync.ConvergentSchemaVersion,
		Enabled:       true,
	}
	if err := rig.configs.Save(ctx, config); err != nil {
		return err
	}
	if err := rig.service.Load(ctx); err != nil {
		return err
	}
	writesBefore := remote.writeCount()
	_, err = rig.service.Sync(ctx, appsync.TriggerManual)
	report.PartialRefused = errors.Is(err, appsync.ErrConvergentCorrupt)
	if !report.PartialRefused {
		return fmt.Errorf("partial migration error = %v", err)
	}
	if remote.writeCount() != writesBefore {
		return errors.New("partial migration reached the provider")
	}
	return nil
}

func runSelftest55Divergence(ctx context.Context, directory string, report *selftest55Report) error {
	remote := newSelftest55Remote()
	node := "selftest-55-divergence"
	rig, err := newSelftest55Rig(ctx, directory, "divergence", node, remote,
		appsync.WithClock(func() time.Time { return time.UnixMilli(20000) }))
	if err != nil {
		return err
	}
	defer rig.close()
	if err := rig.save(ctx, "h1", "original", "original.example"); err != nil {
		return err
	}
	if _, err := rig.service.SetConvergentEnabled(ctx, true); err != nil {
		return fmt.Errorf("divergence device migration: %w", err)
	}
	localState, err := selftest55State(ctx, rig.configs)
	if err != nil {
		return err
	}

	forgedPayload := appsync.Payload{
		Hosts:    []domainvault.Host{selftest54Host("h1", "forged", "original.example", time.UnixMilli(20000))},
		SyncedAt: 20000,
	}
	raw, err := json.Marshal(forgedPayload)
	if err != nil {
		return err
	}
	var familiesRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw, &familiesRaw); err != nil {
		return err
	}
	snapshot, err := domainsync.ParseConvergentSnapshot(familiesRaw, domainsync.DefaultConvergentFamilies())
	if err != nil {
		return err
	}
	forgedState, err := domainsync.CreateConvergentStateFromSnapshot(snapshot, node, 20000)
	if err != nil {
		return err
	}
	if !domainsync.VersionVectorsEqual(localState.Version, forgedState.Version) {
		return errors.New("the forged state does not share the local version vector")
	}
	hashLocal, err := domainsync.ConvergentStateHash(localState)
	if err != nil {
		return err
	}
	hashForged, err := domainsync.ConvergentStateHash(forgedState)
	if err != nil {
		return err
	}
	if hashLocal == hashForged {
		return errors.New("the forged state is identical to the local state")
	}
	envelope, err := domainsync.WrapConvergentEnvelope(forgedState)
	if err != nil {
		return err
	}
	envelopeData, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	forgedPayload.ConvergentSync = envelopeData
	content, err := selftest55Seal(forgedPayload, 2, 30000, "Forged Device")
	if err != nil {
		return err
	}
	remote.install(content)
	before, _ := remote.content()
	writesBefore := remote.writeCount()
	_, err = rig.service.Sync(ctx, appsync.TriggerManual)
	if !errors.Is(err, appsync.ErrConvergentDivergence) {
		return fmt.Errorf("divergence error = %v", err)
	}
	after, _ := remote.content()
	report.DivergenceRemoteSame = string(after) == string(before)
	report.DivergenceWrites = remote.writeCount()
	snapshotInfo := rig.service.Snapshot()
	if snapshotInfo.Problem != appsync.ProblemConvergentDivergence || snapshotInfo.Convergent.Phase != "diverged" {
		return fmt.Errorf("divergence snapshot problem=%q info=%+v", snapshotInfo.Problem, snapshotInfo.Convergent)
	}
	divergence := snapshotInfo.Convergent.Divergence
	if divergence == nil {
		return errors.New("divergence report is missing")
	}
	report.DivergenceKind = divergence.Kind
	report.DivergenceActions = len(divergence.Actions)
	if report.DivergenceKind == "" || report.DivergenceActions != 3 || !report.DivergenceRemoteSame ||
		report.DivergenceWrites != writesBefore {
		return fmt.Errorf("divergence report = %+v (remote same=%v writes=%d->%d)",
			divergence, report.DivergenceRemoteSame, writesBefore, report.DivergenceWrites)
	}
	return nil
}
