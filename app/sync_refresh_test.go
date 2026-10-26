package app

// Regression for ticket 49's manual-download UI staleness: applying a cloud
// copy rewrites the Vault database, but the running Vault state must list the
// new records in the same process, without a restart.
//
// This file intentionally starts from the composition root's seam: a real
// SQLite Vault, the real sync application service and adapters, and a real
// Vault state whose lists the UI renders.

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	adaptersyncvault "github.com/mslxl/natcatty/adapters/syncvault"
	appsync "github.com/mslxl/natcatty/application/sync"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

const syncRefreshPassword = "{{SECRET_q8vd1m2c}} master password"

type syncRefreshKeyStore struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (k *syncRefreshKeyStore) Get(key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	value, ok := k.values[key]
	if !ok {
		return nil, platformsecrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (k *syncRefreshKeyStore) Set(key string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.values[key] = append([]byte(nil), value...)
	return nil
}

func (k *syncRefreshKeyStore) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.values, key)
	return nil
}

type syncRefreshConfigs struct {
	mu     sync.Mutex
	config appsync.Config
}

func (c *syncRefreshConfigs) Load(context.Context) (appsync.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config, nil
}

func (c *syncRefreshConfigs) Save(_ context.Context, config appsync.Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.config = config
	return nil
}

type syncRefreshTokens struct {
	mu    sync.Mutex
	token domainsync.Token
	set   bool
}

func (t *syncRefreshTokens) Load(context.Context) (domainsync.Token, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.set {
		return domainsync.Token{}, appsync.ErrNoToken
	}
	return t.token, nil
}

func (t *syncRefreshTokens) Save(_ context.Context, token domainsync.Token) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token, t.set = token, true
	return nil
}

func (t *syncRefreshTokens) Clear(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token, t.set = domainsync.Token{}, false
	return nil
}

type syncRefreshRemote struct {
	mu     sync.Mutex
	file   appsync.RemoteFile
	exists bool
	writes int
}

func (r *syncRefreshRemote) RequestDeviceCode(context.Context) (appsync.DeviceCode, error) {
	return appsync.DeviceCode{}, errors.New("not used")
}

func (r *syncRefreshRemote) PollDeviceToken(context.Context, appsync.DeviceCode) (domainsync.Token, error) {
	return domainsync.Token{}, errors.New("not used")
}

func (r *syncRefreshRemote) User(context.Context, domainsync.Token) (appsync.Grant, error) {
	return appsync.Grant{}, errors.New("not used")
}

func (r *syncRefreshRemote) Read(context.Context, domainsync.Token) (appsync.RemoteFile, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	file := r.file
	file.Content = append([]byte(nil), r.file.Content...)
	return file, r.exists, nil
}

func (r *syncRefreshRemote) Write(_ context.Context, _ domainsync.Token, _ appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes++
	r.file = appsync.RemoteFile{ID: "gist-refresh", ETag: "etag-refresh", Content: append([]byte(nil), content...)}
	r.exists = true
	return r.file, nil
}

func (r *syncRefreshRemote) serve(hosts []domainvault.Host, updatedAt int64) error {
	content, err := syncRefreshEnvelope(hosts, updatedAt)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.file = appsync.RemoteFile{ID: "gist-refresh", ETag: "etag-cloud", Content: content, UpdatedAt: updatedAt}
	r.exists = true
	r.mu.Unlock()
	return nil
}

func (r *syncRefreshRemote) writeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writes
}

func syncRefreshEnvelope(hosts []domainvault.Host, updatedAt int64) ([]byte, error) {
	payload := appsync.Payload{Hosts: hosts, SyncedAt: updatedAt}
	envelope, err := domainsync.EncryptPayload(payload, syncRefreshPassword, domainsync.EncryptOptions{
		Version:    1,
		UpdatedAt:  updatedAt,
		DeviceID:   "peer-device",
		DeviceName: "Peer Device",
		AppVersion: "test",
	})
	if err != nil {
		return nil, err
	}
	return domainsync.MarshalFile(envelope)
}

func syncRefreshHost(id, label string, when time.Time) domainvault.Host {
	return domainvault.Host{
		ID:        model.HostID(id),
		Label:     label,
		Hostname:  label + ".example.test",
		Port:      22,
		Username:  "demo",
		Protocol:  domainvault.ProtocolSSH,
		Tags:      []string{},
		OrderKey:  1000,
		CreatedAt: when.Add(-time.Minute),
		UpdatedAt: when,
	}
}

// frameVaultState runs the per-frame work the Vault view performs on the UI
// thread before rendering, including the stale-list flush the fix added.
func frameVaultState(state *vaultstate.State) {
	state.FlushTests()
	state.FlushSecurityKeys()
	state.FlushKnownHosts()
	state.FlushLists()
}

func firstVaultLabel(state *vaultstate.State) string {
	if len(state.Items()) == 0 {
		return ""
	}
	return state.Items()[0].Label
}

// TestAppliedDownloadRefreshesTheLiveVaultState applies a newer cloud copy
// through the real sync stack and asserts the running Vault state lists it in
// the same process, after one rendered frame, while an open editor keeps its
// unsaved buffer.
func TestAppliedDownloadRefreshesTheLiveVaultState(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open the Vault database: %v", err)
	}
	defer database.Close()
	secrets, err := store.OpenSecretStore(ctx, database,
		&syncRefreshKeyStore{values: map[string][]byte{}},
		store.WithArgon2Params(1, 1024, 1))
	if err != nil {
		t.Fatalf("open the field-secret store: %v", err)
	}
	defer secrets.Close()

	local := syncRefreshHost("host-local", "gui-alpha", time.UnixMilli(1000))
	if err := database.Save(ctx, local); err != nil {
		t.Fatalf("save the local Host: %v", err)
	}

	vaultService := appvault.NewService(database, appvault.WithSecrets(secrets))
	state := vaultstate.NewState(vaultService)
	if err := state.Reload(); err != nil {
		t.Fatalf("reload the Vault state: %v", err)
	}
	if got := firstVaultLabel(state); got != "gui-alpha" {
		t.Fatalf("initial list = %q, want gui-alpha", got)
	}
	if !state.BeginEdit(local.ID) {
		t.Fatal("cannot open the Host editor")
	}
	state.Form().Label = "unsaved-buffer"

	remote := &syncRefreshRemote{}
	configs := &syncRefreshConfigs{config: appsync.Config{
		Provider: "github", Connected: true, Login: "octocat", Strategy: domainsync.StrategyNewest,
	}}
	tokens := &syncRefreshTokens{token: domainsync.Token{AccessToken: "token"}, set: true}
	notifications := 0
	// The production callback the composition root passes to the sync service.
	refreshVaultState := appliedSnapshotRefresh(func() *vaultstate.State { return state })
	service := appsync.NewService(remote, adaptersyncvault.New(database, secrets), configs, tokens,
		appsync.WithClock(func() time.Time { return time.UnixMilli(5000) }),
		appsync.WithDevice("device-refresh", "Refresh Device", "test"),
		appsync.WithRedrawRequest(func() {}),
		appsync.WithAppliedSnapshot(func() {
			notifications++
			refreshVaultState()
		}),
	)
	if err := service.Load(ctx); err != nil {
		t.Fatalf("load the sync profile: %v", err)
	}
	defer service.Stop()
	if err := service.SetPassword(syncRefreshPassword); err != nil {
		t.Fatalf("set the master password: %v", err)
	}

	// An upload changes only the cloud copy and must not notify.
	uploaded, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("manual upload: %v", err)
	}
	if !uploaded.Uploaded {
		t.Fatalf("sync result = %+v, want an upload", uploaded)
	}
	if notifications != 0 {
		t.Fatalf("an upload notified the Vault state %d times", notifications)
	}

	if err := remote.serve(
		[]domainvault.Host{syncRefreshHost("host-cloud", "cloud-beta", time.UnixMilli(9000))}, 9000); err != nil {
		t.Fatalf("serve the cloud copy: %v", err)
	}
	result, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("manual sync: %v", err)
	}
	if !result.Downloaded {
		t.Fatalf("sync result = %+v, want a download", result)
	}
	if notifications != 1 {
		t.Fatalf("an applied download notified the Vault state %d times, want one", notifications)
	}
	if remote.writeCount() != 1 {
		t.Fatalf("a download wrote: total writes = %d, want the one upload", remote.writeCount())
	}
	if got := firstVaultLabel(state); got != "gui-alpha" {
		t.Fatalf("the sync goroutine mutated the UI-thread list directly: %q", got)
	}

	// A converged sync applies nothing and must not notify again.
	converged, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("sync after download: %v", err)
	}
	if converged.Action != domainsync.ActionNone || notifications != 1 {
		t.Fatalf("no-op sync = %+v, notifications = %d", converged, notifications)
	}

	frameVaultState(state)
	if got := firstVaultLabel(state); got != "cloud-beta" {
		t.Fatalf("the running Vault list = %q after a downloaded snapshot, want cloud-beta", got)
	}
	if !state.Editing() || state.Form().Label != "unsaved-buffer" {
		t.Fatalf("open editor lost its buffer: editing=%v label=%q",
			state.Editing(), state.Form().Label)
	}
	state.Save()
	if !state.Editing() || state.Problem() != vaultstate.ProblemNotFound {
		t.Fatalf("saving the stale buffer: editing=%v problem=%q, want the not-found conflict",
			state.Editing(), state.Problem())
	}
}
