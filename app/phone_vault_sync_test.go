package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	adaptersyncstore "github.com/mslxl/natcatty/adapters/syncstore"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsync "github.com/mslxl/natcatty/application/sync"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
)

// phoneSyncRemote is an in-memory WebDAV collection. It implements the
// provider port and the WebDAV connector, so both the phone's upload and the
// desktop's download run through the real appsync.Service state machine; only
// the network is replaced.
type phoneSyncRemote struct {
	mu     sync.Mutex
	file   appsync.RemoteFile
	writes int
	reads  int
}

var _ appsync.Remote = (*phoneSyncRemote)(nil)
var _ appsync.WebDAVConnector = (*phoneSyncRemote)(nil)

func (r *phoneSyncRemote) Prepare(settings appsync.WebDAVSettings) (appsync.WebDAVSettings, bool, error) {
	if settings.Endpoint == "" {
		return settings, false, appsync.ErrInvalidEndpoint
	}
	if settings.Username == "" {
		return settings, false, appsync.ErrInvalidEndpoint
	}
	return settings, false, nil
}

func (r *phoneSyncRemote) Connect(_ context.Context,
	settings appsync.WebDAVSettings) (domainsync.Token, appsync.Grant, error) {
	token := domainsync.Token{
		Provider:  appsync.ProviderWebDAV,
		TokenType: "WebDAV",
		WebDAV: &domainsync.WebDAVCredential{
			Endpoint: settings.Endpoint,
			AuthType: settings.AuthType,
			Username: settings.Username,
			Password: settings.Password,
		},
	}
	return token, appsync.Grant{ID: "account-1", Login: settings.Username}, nil
}

func (r *phoneSyncRemote) User(context.Context, domainsync.Token) (appsync.Grant, error) {
	return appsync.Grant{ID: "account-1", Login: "alice"}, nil
}

func (r *phoneSyncRemote) Read(context.Context, domainsync.Token) (appsync.RemoteFile, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.file.ID == "" {
		return appsync.RemoteFile{}, false, nil
	}
	file := r.file
	file.Content = append([]byte(nil), r.file.Content...)
	return file, true, nil
}

func (r *phoneSyncRemote) Write(_ context.Context, _ domainsync.Token,
	current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current.ID != "" && current.ETag != r.file.ETag {
		return appsync.RemoteFile{}, appsync.ErrConflict
	}
	r.writes++
	r.file = appsync.RemoteFile{
		ID:        "vault-file",
		ETag:      fmt.Sprintf("etag-%d", r.writes),
		Content:   append([]byte(nil), content...),
		UpdatedAt: time.Now().UnixMilli(),
	}
	return r.file, nil
}

func (r *phoneSyncRemote) hasCopy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.file.ID != ""
}

// phoneSyncTokens is the provider-credential store. The production wiring
// puts this material in the OS credential store; the test keeps it in memory.
type phoneSyncTokens struct {
	mu    sync.Mutex
	token domainsync.Token
	set   bool
}

var _ appsync.TokenStore = (*phoneSyncTokens)(nil)

func (s *phoneSyncTokens) Load(context.Context) (domainsync.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set {
		return domainsync.Token{}, appsync.ErrNoToken
	}
	return s.token, nil
}

func (s *phoneSyncTokens) Save(_ context.Context, token domainsync.Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
	s.set = true
	return nil
}

func (s *phoneSyncTokens) Clear(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = domainsync.Token{}
	s.set = false
	return nil
}

// phoneTestKeys is the OS credential store the field-secret adapter asks for.
type phoneTestKeys struct {
	mu     sync.Mutex
	values map[string][]byte
}

func newPhoneTestKeys() *phoneTestKeys {
	return &phoneTestKeys{values: map[string][]byte{}}
}

func (k *phoneTestKeys) Get(key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	value, ok := k.values[key]
	if !ok {
		return nil, platformsecrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (k *phoneTestKeys) Set(key string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.values[key] = append([]byte(nil), value...)
	return nil
}

func (k *phoneTestKeys) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.values[key]; !ok {
		return platformsecrets.ErrNotFound
	}
	delete(k.values, key)
	return nil
}

// syncDevice is one device's independently built composition-root slice: its
// own SQLite Vault, settings documents, profile file, credential store, and
// appsync.Service — all built by newPhoneSync, the same helper the phone root
// calls.
type syncDevice struct {
	preferences *apppreferences.Service
	terminal    *appterminalsettings.Service
	sync        *phoneSync
}

func newSyncDevice(t *testing.T, ctx context.Context, dir, name string,
	remote *phoneSyncRemote) *syncDevice {
	t.Helper()
	database, err := store.Open(ctx, filepath.Join(dir, "natcatty.db"))
	if err != nil {
		t.Fatalf("%s: open store: %v", name, err)
	}
	t.Cleanup(func() { _ = database.Close() })
	secrets, err := store.OpenSecretStore(ctx, database, newPhoneTestKeys())
	if err != nil {
		t.Fatalf("%s: open secret store: %v", name, err)
	}
	preferences := apppreferences.NewService(
		store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	if err := preferences.Load(ctx); err != nil {
		t.Fatalf("%s: load preferences: %v", name, err)
	}
	terminal := appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(dir, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
		t.Fatalf("%s: load terminal settings: %v", name, err)
	}
	// HasData is true only with a synced record, so each device holds one
	// Host. That is what sends the real service down its decision path rather
	// than the empty-Vault refusal.
	if err := database.Save(ctx, domainvault.Host{
		ID:       model.HostID(name + "-host"),
		Label:    name + " host",
		Hostname: name + ".example",
		Port:     22,
		Protocol: domainvault.ProtocolSSH,
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("%s: save host: %v", name, err)
	}
	synced, err := newPhoneSync(ctx, database, secrets, preferences, terminal, dir,
		withPhoneSyncConfigs(adaptersyncstore.OpenConfig(filepath.Join(dir, "cloud-sync.json"))),
		withPhoneSyncTokens(&phoneSyncTokens{}),
		withPhoneSyncRemote(appsync.ProviderWebDAV, remote),
		withPhoneSyncProvider(appsync.ProviderWebDAV),
		withPhoneSyncDevice(name+"-device", name, "test"),
	)
	if err != nil {
		t.Fatalf("%s: build phone sync: %v", name, err)
	}
	t.Cleanup(synced.Stop)
	return &syncDevice{preferences: preferences, terminal: terminal, sync: synced}
}

const syncMasterPassword = "correct horse battery staple"

func connectSyncDevice(t *testing.T, ctx context.Context, device *syncDevice) {
	t.Helper()
	if err := device.sync.ConnectWebDAV(ctx, syncMasterPassword, appsync.WebDAVSettings{
		Endpoint: "https://dav.example/natcatty/",
		AuthType: domainsync.WebDAVAuthBasic,
		Username: "alice",
		Password: "endpoint-secret",
	}, false); err != nil {
		t.Fatalf("connect WebDAV: %v", err)
	}
}

// TestPhoneVaultSyncCarriesPortableSettingsToADesktop is the delivery proof
// for the phone's Vault sync: a portable Appearance/terminal change made on
// the phone is uploaded through the shared appsync.Service built by
// newPhoneSync, and a desktop configured for the same WebDAV collection
// downloads it. The desktop keeps its own interface style, app icon, and log
// directory because those never left the phone.
func TestPhoneVaultSyncCarriesPortableSettingsToADesktop(t *testing.T) {
	ctx := context.Background()
	remote := &phoneSyncRemote{}

	phone := newSyncDevice(t, ctx, t.TempDir(), "phone", remote)
	connectSyncDevice(t, ctx, phone)

	// A portable change and two device-local choices on the phone.
	if err := phone.preferences.Update(func(s *domainpreferences.Settings) {
		s.Appearance.ThemeMode = domainpreferences.ThemeDark
		s.Appearance.TerminalFontSize = 21
		s.Appearance.InterfaceStyle = domainpreferences.InterfaceMacOS
		s.Appearance.AppIcon = "rainbow"
	}); err != nil {
		t.Fatalf("update phone preferences: %v", err)
	}
	if err := phone.terminal.Update(func(s *domainterminal.Settings) {
		s.PasteProtection = true
		s.OSC52 = domainterminal.ClipboardOff
		s.LogDirectory = "/phone/logs"
	}); err != nil {
		t.Fatalf("update phone terminal settings: %v", err)
	}

	result, err := phone.sync.service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("phone sync: %v", err)
	}
	if !result.Uploaded {
		t.Fatalf("phone sync result = %+v, want an upload", result)
	}
	if !remote.hasCopy() {
		t.Fatal("the phone did not publish a cloud copy")
	}

	// The desktop's own local values, set before the download.
	desktop := newSyncDevice(t, ctx, t.TempDir(), "desktop", remote)
	if err := desktop.preferences.Update(func(s *domainpreferences.Settings) {
		s.Appearance.ThemeMode = domainpreferences.ThemeLight
		s.Appearance.InterfaceStyle = domainpreferences.InterfaceClassic
	}); err != nil {
		t.Fatalf("set desktop preferences: %v", err)
	}
	if err := desktop.terminal.Update(func(s *domainterminal.Settings) {
		s.PasteProtection = false
		s.OSC52 = domainterminal.ClipboardReadWrite
		s.LogDirectory = "/desk/logs"
	}); err != nil {
		t.Fatalf("set desktop terminal settings: %v", err)
	}
	desktopIcon := desktop.preferences.Settings().Appearance.AppIcon
	connectSyncDevice(t, ctx, desktop)
	// Prefer the cloud copy so this test exercises delivery, not a merge.
	if err := desktop.sync.service.SetStrategy(ctx, domainsync.StrategyPreferCloud); err != nil {
		t.Fatalf("set desktop strategy: %v", err)
	}
	result, err = desktop.sync.service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("desktop sync: %v", err)
	}
	if !result.Downloaded {
		t.Fatalf("desktop sync result = %+v, want a download", result)
	}

	// The portable fields arrived.
	got := desktop.preferences.Settings().Appearance
	if got.ThemeMode != domainpreferences.ThemeDark {
		t.Fatalf("desktop ThemeMode = %q, want dark from the phone", got.ThemeMode)
	}
	if got.TerminalFontSize != 21 {
		t.Fatalf("desktop TerminalFontSize = %v, want 21 from the phone", got.TerminalFontSize)
	}
	terminal := desktop.terminal.Settings()
	if !terminal.PasteProtection {
		t.Fatal("desktop PasteProtection = false, want true from the phone")
	}
	if terminal.OSC52 != domainterminal.ClipboardOff {
		t.Fatalf("desktop OSC52 = %q, want off from the phone", terminal.OSC52)
	}

	// The device-local fields stayed where they were.
	if got.InterfaceStyle != domainpreferences.InterfaceClassic {
		t.Fatalf("desktop InterfaceStyle = %q, want its own classic", got.InterfaceStyle)
	}
	if got.AppIcon != desktopIcon {
		t.Fatalf("desktop AppIcon = %q, want its own %q (the phone's rainbow must not travel)", got.AppIcon, desktopIcon)
	}
	if terminal.LogDirectory != "/desk/logs" {
		t.Fatalf("desktop LogDirectory = %q, want its own /desk/logs", terminal.LogDirectory)
	}
}

// TestPhoneVaultSyncProfileSurvivesARelaunch proves the composition root
// reloads the persisted profile: a second newPhoneSync over the same
// application-data directory reports the connected provider and requires the
// master password again instead of forgetting the account.
func TestPhoneVaultSyncProfileSurvivesARelaunch(t *testing.T) {
	ctx := context.Background()
	remote := &phoneSyncRemote{}
	dir := t.TempDir()

	first := newSyncDevice(t, ctx, dir, "phone", remote)
	connectSyncDevice(t, ctx, first)
	if !first.sync.Snapshot().Connected {
		t.Fatal("first service is not connected after ConnectWebDAV")
	}

	database, err := store.Open(ctx, filepath.Join(dir, "natcatty.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	secrets, err := store.OpenSecretStore(ctx, database, newPhoneTestKeys())
	if err != nil {
		t.Fatalf("reopen secret store: %v", err)
	}
	preferences := apppreferences.NewService(
		store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	if err := preferences.Load(ctx); err != nil {
		t.Fatalf("reload preferences: %v", err)
	}
	terminal := appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(dir, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
		t.Fatalf("reload terminal settings: %v", err)
	}
	reloaded, err := newPhoneSync(ctx, database, secrets, preferences, terminal, dir,
		withPhoneSyncConfigs(adaptersyncstore.OpenConfig(filepath.Join(dir, "cloud-sync.json"))),
		withPhoneSyncTokens(&phoneSyncTokens{}),
		withPhoneSyncRemote(appsync.ProviderWebDAV, remote),
		withPhoneSyncProvider(appsync.ProviderWebDAV),
		withPhoneSyncDevice("phone-device", "phone", "test"),
	)
	if err != nil {
		t.Fatalf("rebuild phone sync: %v", err)
	}
	t.Cleanup(reloaded.Stop)

	snapshot := reloaded.Snapshot()
	if !snapshot.Connected {
		t.Fatalf("reloaded snapshot = %+v, want connected", snapshot)
	}
	if snapshot.Provider != appsync.ProviderWebDAV || snapshot.Login != "alice" {
		t.Fatalf("reloaded provider/login = %q/%q, want WebDAV/alice", snapshot.Provider, snapshot.Login)
	}
	if snapshot.HasPassword {
		t.Fatal("the master password must not survive a relaunch in memory")
	}
	if snapshot.Status != appsync.StatusNeedsPassword {
		t.Fatalf("reloaded status = %q, want needs-password", snapshot.Status)
	}
}
