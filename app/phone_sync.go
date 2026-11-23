// Phone Vault-sync wiring. The phone builds the same cloud-sync application
// graph as the desktop over the same SQLite Vault and the same versioned
// settings documents: adaptersyncvault carries the synced Vault families and
// adaptersyncedsettings carries only the documents domainpreferences.Portable
// and domainterminal.Portable leave intact. A portable change made on the
// phone therefore travels in the shared schema, while interface style, app
// icon, filesystem paths, and other device-local fields stay in this phone's
// files.
//
// The phone offers the providers it can configure on its own: WebDAV and
// S3-compatible storage take user-entered credentials, and GitHub Gist runs
// the same RFC 8628 device authorization as the desktop. The setup surface
// lives on the Settings page; this file owns the services behind it.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	adaptergithub "github.com/mslxl/natcatty/adapters/github"
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

	// authCancel aborts the GitHub device flow that is in flight. The page
	// starts the flow and follows Snapshot; the access token is written only
	// to the shared credential store by appsync.Service.Connect. authGen tags
	// each attempt so a late goroutine cannot overwrite a newer attempt's
	// state after a cancel or retry.
	authMu       sync.Mutex
	authCancel   context.CancelFunc
	authGen      int
	authCanceled bool
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
			// GitHub Gist uses the shared device authorization and Gist
			// adapter. Both base URLs stay injectable through the same
			// environment overrides the desktop uses, so an acceptance run can
			// point the phone at a local fake without the real network.
			appsync.ProviderGitHub: adaptergithub.New(adaptergithub.Options{
				APIBase:  os.Getenv("NATCATTY_GITHUB_API_BASE"),
				AuthBase: phoneGitHubAuthBase(),
				ClientID: os.Getenv("NATCATTY_GITHUB_CLIENT_ID"),
			}),
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

// Snapshot is the state the Settings page renders. A failed GitHub
// authorization must not leave the spent one-time code in the snapshot, and a
// user cancel maps to the same retryable authorization problem as an expired
// code, so the page can explain both with one sentence.
func (p *phoneSync) Snapshot() appsync.Snapshot {
	if p == nil || p.service == nil {
		return appsync.Snapshot{}
	}
	snapshot := p.service.Snapshot()
	p.authMu.Lock()
	canceled := p.authCanceled
	p.authMu.Unlock()
	if !snapshot.Connected && snapshot.Status == appsync.StatusFailed {
		snapshot.DeviceCode = appsync.DeviceCode{}
		if canceled {
			snapshot.Problem = appsync.ProblemAuthorization
		}
	}
	return snapshot
}

// StartGitHubConnect switches the shared profile to GitHub Gist and starts the
// device authorization on its own goroutine. The engine owns the RFC 8628
// polling loop, so the phone supplies only this composition wiring and the
// page. The access token goes to the system credential store through
// appsync.Service.Connect and never enters the profile or a log line.
func (p *phoneSync) StartGitHubConnect(password string) error {
	if p == nil || p.service == nil {
		return appsync.ErrNotConfigured
	}
	if password == "" {
		return appsync.ErrPasswordRequired
	}
	p.authMu.Lock()
	if p.authCancel != nil {
		p.authMu.Unlock()
		return appsync.ErrAlreadyInProgress
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.authGen++
	gen := p.authGen
	p.authCancel = cancel
	p.authCanceled = false
	p.authMu.Unlock()

	go func() {
		err := p.service.SetProvider(ctx, appsync.ProviderGitHub)
		if err == nil {
			err = p.service.SetPassword(password)
		}
		if err == nil {
			err = p.service.Connect(ctx, password, nil)
		}
		p.authMu.Lock()
		if p.authGen == gen {
			p.authCancel = nil
			if errors.Is(err, context.Canceled) {
				p.authCanceled = true
			}
		}
		p.authMu.Unlock()
		cancel()
	}()
	return nil
}

// clearGitHubAuthorization abandons an in-flight device flow and its cancel
// bookkeeping. It is called when the user connects another provider, so the
// GitHub-specific reason cannot leak into that provider's failure surface.
func (p *phoneSync) clearGitHubAuthorization() {
	if p == nil {
		return
	}
	p.authMu.Lock()
	cancel := p.authCancel
	p.authGen++
	p.authCancel = nil
	p.authCanceled = false
	p.authMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// CancelGitHubConnect aborts an authorization that is in flight. The shared
// engine's fail path records a problem; Snapshot reports it as the retryable
// authorization state. The cancel is released here so a retry can start
// before the abandoned goroutine finishes.
func (p *phoneSync) CancelGitHubConnect() {
	if p == nil {
		return
	}
	p.authMu.Lock()
	cancel := p.authCancel
	if cancel != nil {
		p.authCanceled = true
		p.authCancel = nil
	}
	p.authMu.Unlock()
	if cancel != nil {
		cancel()
	}
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
	// A provider switch abandons any GitHub authorization state, so a later
	// WebDAV problem is not reported as a GitHub sign-in problem.
	p.clearGitHubAuthorization()
	return p.service.ConnectWebDAV(ctx, password, settings, confirmInsecure)
}

// ConnectS3 validates and stores an S3-compatible bucket and its signing
// material.
func (p *phoneSync) ConnectS3(ctx context.Context, password string,
	settings appsync.S3Settings, confirmInsecure bool) error {
	if p == nil || p.service == nil {
		return appsync.ErrNotConfigured
	}
	p.clearGitHubAuthorization()
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
	p.clearGitHubAuthorization()
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

// Stop ends the debounce timer, aborts an in-flight device authorization, and
// waits for a running sync to leave.
func (p *phoneSync) Stop() {
	if p == nil {
		return
	}
	p.CancelGitHubConnect()
	if p.service == nil {
		return
	}
	p.service.Stop()
}

// phoneGitHubAuthBase resolves the OAuth base the same way the desktop does,
// without depending on the desktop composition root, which the mobile build
// tags exclude: one API base override moves both endpoints, and an explicit
// auth base takes precedence.
func phoneGitHubAuthBase() string {
	if auth := os.Getenv("NATCATTY_GITHUB_AUTH_BASE"); auth != "" {
		return auth
	}
	return os.Getenv("NATCATTY_GITHUB_API_BASE")
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
