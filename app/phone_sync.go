// Phone Vault-sync wiring. The phone builds the same cloud-sync application
// graph as the desktop over the same SQLite Vault and the same versioned
// settings documents: adaptersyncvault carries the synced Vault families and
// adaptersyncedsettings carries only the documents domainpreferences.Portable
// and domainterminal.Portable leave intact. A portable change made on the
// phone therefore travels in the shared schema, while interface style, app
// icon, filesystem paths, and other device-local fields stay in this phone's
// files.
//
// The phone offers the two providers it can configure without a browser:
// WebDAV and S3-compatible storage. The setup surface lives on the Settings
// page; this file owns the services behind it.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	adapters3 "github.com/mslxl/natcatty/adapters/s3"
	"github.com/mslxl/natcatty/adapters/store"
	adaptersyncedsettings "github.com/mslxl/natcatty/adapters/syncedsettings"
	adaptersyncstore "github.com/mslxl/natcatty/adapters/syncstore"
	adaptersyncvault "github.com/mslxl/natcatty/adapters/syncvault"
	adapterwebdav "github.com/mslxl/natcatty/adapters/webdav"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsync "github.com/mslxl/natcatty/application/sync"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// phoneSync owns the phone's Vault sync engine. It implements the Settings
// page's seam and adds the hooks the composition root needs: NotifyLocalChange
// for a portable settings write and Stop for shutdown.
type phoneSync struct {
	service *appsync.Service
}

var _ phoneshell.VaultSyncService = (*phoneSync)(nil)

// phoneSyncDeps is the composition-root seam. The zero value is not usable;
// newPhoneSync fills the production adapters first and options replace them in
// tests.
type phoneSyncDeps struct {
	configs  appsync.ConfigStore
	tokens   appsync.TokenStore
	remotes  map[string]appsync.Remote
	provider string

	deviceID   string
	deviceName string
	version    string

	// backups is the shared protective-backup port. Conflict resolution
	// refuses to overwrite either side without it.
	backups appsync.Backups
	// appliedSnapshot refreshes the live phone surfaces after a downloaded
	// cloud copy has been written: Host and SSH Key lists reload, and live
	// terminals re-read the appearance document.
	appliedSnapshot func()
	// extraOptions lets a test install shared-service seams (for example a
	// shorter debounce) without changing the production graph.
	extraOptions []appsync.Option
}

type phoneSyncOption func(*phoneSyncDeps)

func withPhoneSyncBackups(backups appsync.Backups) phoneSyncOption {
	return func(d *phoneSyncDeps) {
		if backups != nil {
			d.backups = backups
		}
	}
}

func withPhoneSyncAppliedSnapshot(fn func()) phoneSyncOption {
	return func(d *phoneSyncDeps) {
		if fn != nil {
			d.appliedSnapshot = fn
		}
	}
}

func withPhoneSyncOptions(options ...appsync.Option) phoneSyncOption {
	return func(d *phoneSyncDeps) { d.extraOptions = append(d.extraOptions, options...) }
}

func withPhoneSyncConfigs(configs appsync.ConfigStore) phoneSyncOption {
	return func(d *phoneSyncDeps) { d.configs = configs }
}

func withPhoneSyncTokens(tokens appsync.TokenStore) phoneSyncOption {
	return func(d *phoneSyncDeps) { d.tokens = tokens }
}

func withPhoneSyncRemote(provider string, remote appsync.Remote) phoneSyncOption {
	return func(d *phoneSyncDeps) {
		if d.remotes == nil {
			d.remotes = map[string]appsync.Remote{}
		}
		d.remotes[appsync.NormalizeProvider(provider)] = remote
	}
}

func withPhoneSyncProvider(provider string) phoneSyncOption {
	return func(d *phoneSyncDeps) { d.provider = appsync.NormalizeProvider(provider) }
}

func withPhoneSyncDevice(id, name, version string) phoneSyncOption {
	return func(d *phoneSyncDeps) {
		d.deviceID = id
		d.deviceName = name
		d.version = version
	}
}

// newPhoneSync builds the phone's cloud-sync engine over the shared store and
// the two settings services. It loads the persisted profile before returning,
// so the Settings page shows the truth after a relaunch: an unconfigured
// profile, a connected profile that needs its master password again, or a
// stored provider that this build cannot open.
//
// userData is the device's application-data directory; the non-secret profile
// and the provider credential are kept apart exactly as on the desktop.
func newPhoneSync(ctx context.Context, database *store.Store, secrets *store.SecretStore,
	preferences *apppreferences.Service, terminal *appterminalsettings.Service,
	userData string, opts ...phoneSyncOption) (*phoneSync, error) {
	deps := phoneSyncDeps{
		configs: adaptersyncstore.OpenConfig(filepath.Join(userData, "cloud-sync.json")),
		tokens:  adaptersyncstore.NewTokenStore(platformsecrets.System{}),
		remotes: map[string]appsync.Remote{
			appsync.ProviderWebDAV: adapterwebdav.New(adapterwebdav.Options{}),
			appsync.ProviderS3:     adapters3.New(adapters3.Options{}),
		},
		provider:   appsync.ProviderWebDAV,
		deviceName: "phone",
	}
	for _, opt := range opts {
		opt(&deps)
	}
	primary := deps.remotes[deps.provider]
	if primary == nil {
		return nil, fmt.Errorf("phone sync: no remote registered for %q", deps.provider)
	}
	profile, err := deps.configs.Load(ctx)
	if err != nil {
		return nil, err
	}
	deviceID := deps.deviceID
	if deviceID == "" {
		deviceID = profile.DeviceID
	}
	if deviceID == "" {
		deviceID = phoneSyncDeviceID()
	}
	settings := adaptersyncedsettings.New(preferences, terminal, nil, nil, nil, nil)
	vault := adaptersyncvault.New(database, secrets,
		adaptersyncvault.WithSettings(settings))

	options := []appsync.Option{
		appsync.WithProvider(deps.provider),
		appsync.WithDevice(deviceID, deps.deviceName, deps.version),
		// Themes, key bindings, SFTP bookmarks, and browser bookmarks are
		// authored on the desktop. The phone carries the current cloud value
		// through unchanged and never publishes, revives, or deletes them.
		appsync.WithUnmanagedSettingsDocuments(
			adaptersyncedsettings.KeyThemes,
			adaptersyncedsettings.KeyKeybindings,
			adaptersyncedsettings.KeySFTPBookmarks,
			adaptersyncedsettings.KeyBrowserBookmarks,
		),
	}
	if deps.backups != nil {
		options = append(options, appsync.WithBackups(deps.backups))
	}
	if deps.appliedSnapshot != nil {
		options = append(options, appsync.WithAppliedSnapshot(deps.appliedSnapshot))
	}
	options = append(options, deps.extraOptions...)
	for provider, remote := range deps.remotes {
		if provider == deps.provider {
			continue
		}
		options = append(options, appsync.WithRemote(provider, remote))
	}
	service := appsync.NewService(primary, vault, deps.configs, deps.tokens, options...)
	if err := service.Load(ctx); err != nil {
		return nil, err
	}
	return &phoneSync{service: service}, nil
}

// Snapshot is the state the Settings page renders.
func (p *phoneSync) Snapshot() appsync.Snapshot {
	if p == nil || p.service == nil {
		return appsync.Snapshot{}
	}
	return p.service.Snapshot()
}

// SetPassword keeps the master password for this process only.
func (p *phoneSync) SetPassword(password string) error {
	if p == nil || p.service == nil {
		return appsync.ErrNotConfigured
	}
	return p.service.SetPassword(password)
}

// SetAutoSync persists the automatic-sync preference.
func (p *phoneSync) SetAutoSync(ctx context.Context, enabled bool) error {
	if p == nil || p.service == nil {
		return appsync.ErrNotConfigured
	}
	return p.service.SetAutoSync(ctx, enabled)
}

// ConnectWebDAV validates and stores a WebDAV collection and its credential.
func (p *phoneSync) ConnectWebDAV(ctx context.Context, password string,
	settings appsync.WebDAVSettings, confirmInsecure bool) error {
	if p == nil || p.service == nil {
		return appsync.ErrNotConfigured
	}
	return p.service.ConnectWebDAV(ctx, password, settings, confirmInsecure)
}

// ConnectS3 validates and stores an S3-compatible bucket and its signing
// material.
func (p *phoneSync) ConnectS3(ctx context.Context, password string,
	settings appsync.S3Settings, confirmInsecure bool) error {
	if p == nil || p.service == nil {
		return appsync.ErrNotConfigured
	}
	return p.service.ConnectS3(ctx, password, settings, confirmInsecure)
}

// SyncNow triggers one manual run on its own goroutine. The page polls
// Snapshot, so the frame that handled the tap never waits on the network.
func (p *phoneSync) SyncNow() {
	if p == nil || p.service == nil {
		return
	}
	go func() { _, _ = p.service.Sync(context.Background(), appsync.TriggerManual) }()
}

// Disconnect removes the provider credential and the profile connection.
func (p *phoneSync) Disconnect(ctx context.Context) error {
	if p == nil || p.service == nil {
		return appsync.ErrNotConfigured
	}
	return p.service.Disconnect(ctx)
}

// NotifyLocalChange schedules an automatic sync after a portable settings or
// Vault write. The engine debounces and coalesces it.
func (p *phoneSync) NotifyLocalChange() {
	if p == nil || p.service == nil {
		return
	}
	p.service.NotifyLocalChange()
}

// Stop ends the debounce timer and waits for a running sync to leave.
func (p *phoneSync) Stop() {
	if p == nil || p.service == nil {
		return
	}
	p.service.Stop()
}

// phoneSyncDeviceID is the stable identity a new profile records. The value is
// persisted by the first successful connect, so later launches reuse it.
func phoneSyncDeviceID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("phone-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes[:])
}
