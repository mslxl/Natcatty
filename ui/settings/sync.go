package settings

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	"github.com/mslxl/natcatty/ui/locales"
)

// SyncService is the cloud-sync application seam the Settings surface drives.
// The app supplies *appsync.Service; tests supply a fake.
type SyncService interface {
	Snapshot() appsync.Snapshot
	Load(ctx context.Context) error
	SetPassword(password string) error
	ClearPassword()
	SetAutoSync(ctx context.Context, enabled bool) error
	SetStrategy(ctx context.Context, strategy domainsync.Strategy) error
	Connect(ctx context.Context, password string, onDevice func(appsync.DeviceCode)) error
	Disconnect(ctx context.Context) error
	Sync(ctx context.Context, trigger appsync.Trigger) (appsync.Result, error)
}

// ProviderSyncService is the optional capability that lets the setup surface
// choose between GitHub, Google Drive, OneDrive, and WebDAV. Ticket 49's fake
// does not implement it, so the selector is hidden rather than forced into
// that seam.
type ProviderSyncService interface {
	SetProvider(ctx context.Context, provider string) error
}

// WebDAVSyncService is the optional WebDAV capability. The form passes the
// user-entered connection settings explicitly; confirmInsecure is true only
// after the user has seen and accepted the plain-HTTP warning for this exact
// attempt. ConfirmInsecureEndpoint records the warning acceptance for a later
// manual sync of the stored profile in this process.
type WebDAVSyncService interface {
	ConnectWebDAV(ctx context.Context, password string, settings appsync.WebDAVSettings, confirmInsecure bool) error
	WebDAVSettings() appsync.WebDAVSettings
	ConfirmInsecureEndpoint(ctx context.Context) bool
}

// SyncOption customizes a SyncState.
type SyncOption func(*SyncState)

// WithSyncRedrawRequest sets the function called after a worker finishes.
func WithSyncRedrawRequest(fn func()) SyncOption {
	return func(s *SyncState) { s.redraw = fn }
}

// SyncState owns the Settings sync surface. UI-thread fields are mutated only
// on the UI thread; worker goroutines append outcomes to a locked queue that
// Flush applies.
type SyncState struct {
	service SyncService
	redraw  func()

	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	queue []func()

	password   string
	busy       bool
	lastErr    error
	disconnect bool

	// WebDAV form state. The secret fields live only in this UI state for the
	// duration of an attempt and are never part of a snapshot or a log line.
	webdavLoaded   bool
	webdavEndpoint string
	webdavAuthType string
	webdavUsername string
	webdavPassword string
	webdavToken    string
	// pendingConnect and pendingPassword freeze exactly what the user
	// confirmed after the plain-HTTP warning, so an edit behind the warning
	// cannot change what is sent.
	pendingConnect  appsync.WebDAVSettings
	pendingPassword string
	insecurePrompt  bool
	insecureAction  string
	reconfigure     bool
}

// NewSyncState builds the sync surface from the application service.
func NewSyncState(service SyncService, opts ...SyncOption) *SyncState {
	ctx, cancel := context.WithCancel(context.Background())
	state := &SyncState{service: service, ctx: ctx, cancel: cancel}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Shutdown cancels in-flight device polling and syncs.
func (s *SyncState) Shutdown() {
	if s.cancel != nil {
		s.cancel()
	}
}

// Flush applies outcomes queued by goroutines. The view calls it every frame.
func (s *SyncState) Flush() {
	s.mu.Lock()
	queue := s.queue
	s.queue = nil
	s.mu.Unlock()
	for _, fn := range queue {
		fn()
	}
}

func (s *SyncState) post(fn func()) {
	s.mu.Lock()
	s.queue = append(s.queue, fn)
	s.mu.Unlock()
	if s.redraw != nil {
		s.redraw()
	}
}

// Snapshot returns the current application state.
func (s *SyncState) Snapshot() appsync.Snapshot { return s.service.Snapshot() }

// Password returns the master-password field.
func (s *SyncState) Password() string { return s.password }

// SetPassword updates the master-password field.
func (s *SyncState) SetPassword(value string) { s.password = value }

// Busy reports whether a worker is running.
func (s *SyncState) Busy() bool { return s.busy }

// Err returns the last worker error, or nil. The view maps it to a catalog
// string; raw Go error text is never rendered.
func (s *SyncState) Err() error { return s.lastErr }

// Error returns the last worker error text for logs and tests.
func (s *SyncState) Error() string {
	if s.lastErr == nil {
		return ""
	}
	return s.lastErr.Error()
}

// AskDisconnect opens the confirmation.
func (s *SyncState) AskDisconnect() { s.disconnect = true }

// CancelDisconnect closes the confirmation.
func (s *SyncState) CancelDisconnect() { s.disconnect = false }

// Disconnecting reports whether the confirmation is open.
func (s *SyncState) Disconnecting() bool { return s.disconnect }

// Connect starts the provider's authorization with the entered master
// password. For a WebDAV profile it sends the form's connection settings; a
// plain-HTTP endpoint first produces the explicit warning confirmation.
func (s *SyncState) Connect() {
	if s.busy {
		return
	}
	password := s.password
	if password == "" {
		s.lastErr = nil
		return
	}
	if service, ok := s.webDAVService(); ok &&
		appsync.NormalizeProvider(s.service.Snapshot().Provider) == appsync.ProviderWebDAV {
		s.connectWebDAV(service, password)
		return
	}
	s.busy = true
	s.lastErr = nil
	state := s
	go func() {
		err := state.service.SetPassword(password)
		if err == nil {
			err = state.service.Connect(state.ctx, password, nil)
		}
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
				return
			}
			state.password = ""
		})
	}()
}

// webDAVService reports the WebDAV capability of the current service.
func (s *SyncState) webDAVService() (WebDAVSyncService, bool) {
	service, ok := s.service.(WebDAVSyncService)
	return service, ok
}

// connectWebDAV validates the form, then connects with confirmInsecure=false.
// The service's gate returns ErrInsecureEndpoint before storing or sending the
// credential; the view then shows the warning and ConfirmInsecure retries the
// same captured settings.
func (s *SyncState) connectWebDAV(service WebDAVSyncService, password string) {
	settings := s.webDAVSettings()
	authType := domainsync.NormalizeWebDAVAuthType(settings.AuthType)
	valid := settings.Endpoint != ""
	if authType == domainsync.WebDAVAuthBearer {
		valid = valid && settings.Token != ""
	} else {
		valid = valid && settings.Username != "" && settings.Password != ""
	}
	if !valid {
		s.lastErr = appsync.ErrInvalidEndpoint
		return
	}
	s.busy = true
	s.lastErr = nil
	s.pendingConnect = settings
	s.pendingPassword = password
	state := s
	go func() {
		err := state.service.SetPassword(password)
		if err == nil {
			err = service.ConnectWebDAV(state.ctx, password, settings, false)
		}
		state.post(func() {
			state.busy = false
			switch {
			case errors.Is(err, appsync.ErrInsecureEndpoint):
				state.lastErr = err
				state.insecurePrompt = true
				state.insecureAction = "connect"
			case err != nil:
				state.lastErr = err
				state.pendingConnect = appsync.WebDAVSettings{}
				state.pendingPassword = ""
			default:
				state.lastErr = nil
				state.password = ""
				state.webdavPassword = ""
				state.webdavToken = ""
				state.pendingConnect = appsync.WebDAVSettings{}
				state.pendingPassword = ""
				state.reconfigure = false
			}
		})
	}()
}

// webDAVSettings reads the form. Both secrets stay in the UI state; only the
// one selected by the authentication mode is copied.
func (s *SyncState) webDAVSettings() appsync.WebDAVSettings {
	authType := domainsync.NormalizeWebDAVAuthType(s.webdavAuthType)
	settings := appsync.WebDAVSettings{
		Endpoint: strings.TrimSpace(s.webdavEndpoint),
		AuthType: authType,
		Username: strings.TrimSpace(s.webdavUsername),
	}
	if authType == domainsync.WebDAVAuthBearer {
		settings.Token = s.webdavToken
	} else {
		settings.Password = s.webdavPassword
	}
	return settings
}

// EnsureWebDAVForm prefills the non-secret form fields once from the stored
// profile. It runs on the UI thread.
func (s *SyncState) EnsureWebDAVForm() {
	if s.webdavLoaded {
		return
	}
	settings := appsync.WebDAVSettings{}
	if service, ok := s.webDAVService(); ok {
		settings = service.WebDAVSettings()
	}
	snapshot := s.service.Snapshot()
	if settings.Endpoint == "" {
		settings.Endpoint = snapshot.WebDAVEndpoint
	}
	if settings.AuthType == "" {
		settings.AuthType = snapshot.WebDAVAuthType
	}
	if settings.Username == "" {
		settings.Username = snapshot.WebDAVUsername
	}
	s.webdavEndpoint = settings.Endpoint
	s.webdavAuthType = domainsync.NormalizeWebDAVAuthType(settings.AuthType)
	s.webdavUsername = settings.Username
	s.webdavLoaded = true
}

// ResetWebDAVForm forgets the form, including any unsent secret.
func (s *SyncState) ResetWebDAVForm() {
	s.webdavLoaded = false
	s.webdavEndpoint = ""
	s.webdavAuthType = ""
	s.webdavUsername = ""
	s.webdavPassword = ""
	s.webdavToken = ""
	s.pendingConnect = appsync.WebDAVSettings{}
	s.pendingPassword = ""
}

// WebDAV form accessors. They run on the UI thread in response to text edits.
func (s *SyncState) WebDAVEndpoint() string { return s.webdavEndpoint }
func (s *SyncState) WebDAVUsername() string { return s.webdavUsername }
func (s *SyncState) WebDAVAuthType() string {
	return domainsync.NormalizeWebDAVAuthType(s.webdavAuthType)
}
func (s *SyncState) WebDAVPassword() string { return s.webdavPassword }
func (s *SyncState) WebDAVToken() string    { return s.webdavToken }

func (s *SyncState) SetWebDAVEndpoint(value string) {
	s.webdavLoaded = true
	s.webdavEndpoint = value
}

func (s *SyncState) SetWebDAVUsername(value string) {
	s.webdavLoaded = true
	s.webdavUsername = value
}

// SetWebDAVAuthType changes the mode and drops the secret that no longer
// applies, so a stale password can never be sent as a bearer token or vice
// versa.
func (s *SyncState) SetWebDAVAuthType(value string) {
	s.webdavLoaded = true
	s.webdavAuthType = domainsync.NormalizeWebDAVAuthType(value)
	if s.webdavAuthType == domainsync.WebDAVAuthBearer {
		s.webdavPassword = ""
	} else {
		s.webdavToken = ""
	}
}

func (s *SyncState) SetWebDAVPassword(value string) {
	s.webdavLoaded = true
	s.webdavPassword = value
}

func (s *SyncState) SetWebDAVToken(value string) {
	s.webdavLoaded = true
	s.webdavToken = value
}

// InsecureEndpointEntered reports whether the current URL would use plain
// HTTP. It drives the inline warning before any credential is sent; the
// application and adapter still perform the authoritative check.
func (s *SyncState) InsecureEndpointEntered() bool {
	return appsync.EndpointIsInsecure(s.webdavEndpoint)
}

// InsecurePrompt reports that the plain-HTTP warning is awaiting an explicit
// decision.
func (s *SyncState) InsecurePrompt() bool { return s.insecurePrompt }

// InsecureSyncPrompt reports which action follows the warning.
func (s *SyncState) InsecureSyncPrompt() bool { return s.insecureAction == "sync" }

// CancelInsecure dismisses the warning without sending anything.
func (s *SyncState) CancelInsecure() {
	s.insecurePrompt = false
	s.insecureAction = ""
	s.pendingConnect = appsync.WebDAVSettings{}
	s.pendingPassword = ""
	s.webdavPassword = ""
	s.webdavToken = ""
	s.lastErr = nil
}

// ConfirmInsecure is the explicit "Connect anyway" or "Sync anyway" action.
// The connect path reuses the exact settings captured before the warning.
func (s *SyncState) ConfirmInsecure() {
	if s.busy {
		return
	}
	action := s.insecureAction
	s.insecurePrompt = false
	s.insecureAction = ""
	if action == "sync" {
		service, ok := s.webDAVService()
		if !ok {
			s.lastErr = appsync.ErrNotConfigured
			return
		}
		s.busy = true
		s.lastErr = nil
		state := s
		go func() {
			confirmed := service.ConfirmInsecureEndpoint(state.ctx)
			var err error
			if !confirmed {
				err = appsync.ErrInsecureEndpoint
			} else {
				_, err = state.service.Sync(state.ctx, appsync.TriggerManual)
			}
			state.post(func() {
				state.busy = false
				if errors.Is(err, appsync.ErrInsecureEndpoint) {
					state.lastErr = err
					state.insecurePrompt = true
					state.insecureAction = "sync"
					return
				}
				if err != nil {
					state.lastErr = err
				}
			})
		}()
		return
	}

	service, ok := s.webDAVService()
	if !ok {
		s.lastErr = appsync.ErrNotConfigured
		return
	}
	settings := s.pendingConnect
	password := s.pendingPassword
	if settings.Endpoint == "" || password == "" {
		s.lastErr = appsync.ErrPasswordRequired
		return
	}
	s.busy = true
	s.lastErr = nil
	state := s
	go func() {
		err := service.ConnectWebDAV(state.ctx, password, settings, true)
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
				return
			}
			state.password = ""
			state.webdavPassword = ""
			state.webdavToken = ""
			state.pendingConnect = appsync.WebDAVSettings{}
			state.pendingPassword = ""
			state.reconfigure = false
		})
	}()
}

// Reconfigure opens the WebDAV form while the existing credential remains in
// place, so a rejected credential can be corrected without losing the profile
// (or the endpoint) first.
func (s *SyncState) Reconfigure() {
	if s.busy {
		return
	}
	s.reconfigure = true
	s.lastErr = nil
	s.insecurePrompt = false
	s.insecureAction = ""
	s.ResetWebDAVForm()
	s.EnsureWebDAVForm()
}

// CancelReconfigure returns to the connected view without changing anything.
func (s *SyncState) CancelReconfigure() {
	s.reconfigure = false
	s.pendingConnect = appsync.WebDAVSettings{}
	s.pendingPassword = ""
	s.webdavPassword = ""
	s.webdavToken = ""
	s.lastErr = nil
}

// Reconfiguring reports that the WebDAV setup form is shown over an existing
// connection.
func (s *SyncState) Reconfiguring() bool { return s.reconfigure }

// Reconnect starts a fresh authorization without asking for the master
// password again. It is the actionable path out of ProblemReauthorization.
func (s *SyncState) Reconnect() {
	if s.busy {
		return
	}
	s.busy = true
	s.lastErr = nil
	state := s
	go func() {
		err := state.service.Connect(state.ctx, "", nil)
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
			}
		})
	}()
}

// SupportsProviderSelection reports whether the service can switch providers.
func (s *SyncState) SupportsProviderSelection() bool {
	_, ok := s.service.(ProviderSyncService)
	return ok
}

// SetProvider persists the selected provider and clears the old account's
// token and merge anchor on the application side.
func (s *SyncState) SetProvider(provider string) {
	service, ok := s.service.(ProviderSyncService)
	if !ok || s.busy {
		return
	}
	s.busy = true
	s.lastErr = nil
	state := s
	go func() {
		err := service.SetProvider(state.ctx, provider)
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
				return
			}
			state.reconfigure = false
			state.insecurePrompt = false
			state.insecureAction = ""
			state.ResetWebDAVForm()
		})
	}()
}

// Disconnect forgets the token and baseline after confirmation.
func (s *SyncState) Disconnect() {
	if s.busy {
		return
	}
	s.disconnect = false
	s.busy = true
	s.lastErr = nil
	state := s
	go func() {
		err := state.service.Disconnect(state.ctx)
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
				return
			}
			state.reconfigure = false
			state.insecurePrompt = false
			state.insecureAction = ""
			state.ResetWebDAVForm()
		})
	}()
}

// SyncNow runs a manual sync. The service keeps the password after the first
// successful call, so the field may be empty on later syncs.
func (s *SyncState) SyncNow() {
	if s.busy {
		return
	}
	password := s.password
	s.busy = true
	s.lastErr = nil
	state := s
	go func() {
		err := error(nil)
		if password != "" {
			err = state.service.SetPassword(password)
		}
		if err == nil {
			_, err = state.service.Sync(state.ctx, appsync.TriggerManual)
		}
		state.post(func() {
			state.busy = false
			if errors.Is(err, appsync.ErrInsecureEndpoint) {
				// A restored plain-HTTP profile is never synced silently.
				// The warning is shown again and "Sync anyway" records the
				// confirmation for this process before retrying.
				state.lastErr = err
				state.insecurePrompt = true
				state.insecureAction = "sync"
				return
			}
			if err != nil {
				state.lastErr = err
				return
			}
			state.password = ""
		})
	}()
}

// SetAutoSync persists the automatic-sync preference.
func (s *SyncState) SetAutoSync(enabled bool) {
	if s.busy {
		return
	}
	s.busy = true
	state := s
	go func() {
		err := state.service.SetAutoSync(state.ctx, enabled)
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
			}
		})
	}()
}

// SetStrategy persists the conflict strategy.
func (s *SyncState) SetStrategy(strategy domainsync.Strategy) {
	if s.busy {
		return
	}
	s.busy = true
	state := s
	go func() {
		err := state.service.SetStrategy(state.ctx, strategy)
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
			}
		})
	}()
}

// syncSection renders the Sync & Cloud area.
func syncSection(c *ui.Context, state *SyncState, m locales.Messages, t *ui.Theme) {
	state.Flush()
	snapshot := state.Snapshot()
	section(c, m.SettingsSyncTitle(), m.SettingsSyncDescription(), t, func() {
		if !snapshot.Connected || state.Reconfiguring() {
			syncSetup(c, state, snapshot, m, t)
			return
		}
		syncConnected(c, state, snapshot, m, t)
	})
}

func syncSetup(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	if !snapshot.Connected {
		ui.Text(c, m.SettingsSyncNotConnected()).FontSize(12).TextColor(t.TextMuted)
	}
	syncProviderSelector(c, state, snapshot, m)
	if state.InsecurePrompt() {
		// No input or button other than the explicit decision is live while
		// the warning is open, so the endpoint that was confirmed cannot be
		// changed behind it.
		syncInsecurePrompt(c, state, state.WebDAVEndpoint(), m, t)
		return
	}
	switch appsync.NormalizeProvider(snapshot.Provider) {
	case appsync.ProviderGoogleDrive:
		ui.Text(c, m.SettingsSyncGoogleHint()).FontSize(11).TextColor(t.TextMuted)
	case appsync.ProviderOneDrive:
		ui.Text(c, m.SettingsSyncOneDriveHint()).FontSize(11).TextColor(t.TextMuted)
	case appsync.ProviderWebDAV:
		ui.Text(c, m.SettingsSyncWebDAVHint()).FontSize(11).TextColor(t.TextMuted)
		syncWebDAVForm(c, state, m, t)
	}
	syncDeviceCode(c, snapshot, m, t)
	syncPasswordField(c, state, m)
	ui.Text(c, m.SettingsSyncMasterPasswordHint()).FontSize(11).TextColor(t.TextMuted)
	ui.Text(c, m.SettingsSyncSecurityNote()).FontSize(11).TextColor(t.TextMuted)
	syncButtons(c, state, snapshot, m)
	syncStatus(c, state, snapshot, m, t)
}

// syncWebDAVForm renders the endpoint and credential fields. The inline
// plain-HTTP warning appears before the Connect button, so the risk is visible
// before any credential can be sent.
func syncWebDAVForm(c *ui.Context, state *SyncState, m locales.Messages, t *ui.Theme) {
	state.EnsureWebDAVForm()
	ui.Field(c, m.SettingsSyncWebDAVEndpoint(), func() {
		endpoint := state.WebDAVEndpoint()
		if ui.TextInput(c, &endpoint).Width(360).Changed() {
			state.SetWebDAVEndpoint(endpoint)
		}
	})

	authValues := []string{
		domainsync.WebDAVAuthBasic,
		domainsync.WebDAVAuthDigest,
		domainsync.WebDAVAuthBearer,
	}
	authLabels := []string{
		m.SettingsSyncWebDAVAuthBasic(),
		m.SettingsSyncWebDAVAuthDigest(),
		m.SettingsSyncWebDAVAuthBearer(),
	}
	authType := state.WebDAVAuthType()
	selected := authLabels[indexOf(authValues, authType)]
	ui.Field(c, m.SettingsSyncWebDAVAuth(), func() {
		if ui.Select(c, &selected, authLabels).Width(300).Changed() {
			state.SetWebDAVAuthType(authValues[indexOf(authLabels, selected)])
			c.Invalidate()
		}
	})

	if state.WebDAVAuthType() == domainsync.WebDAVAuthBearer {
		ui.Field(c, m.SettingsSyncWebDAVToken(), func() {
			token := state.WebDAVToken()
			if ui.TextInput(c, &token).Password().Width(280).Changed() {
				state.SetWebDAVToken(token)
			}
		})
	} else {
		ui.Field(c, m.SettingsSyncWebDAVUsername(), func() {
			username := state.WebDAVUsername()
			if ui.TextInput(c, &username).Width(280).Changed() {
				state.SetWebDAVUsername(username)
			}
		})
		ui.Field(c, m.SettingsSyncWebDAVPassword(), func() {
			password := state.WebDAVPassword()
			if ui.TextInput(c, &password).Password().Width(280).Changed() {
				state.SetWebDAVPassword(password)
			}
		})
	}

	if state.InsecureEndpointEntered() {
		ui.Column(c).Padding(10).Gap(4).Background(t.Background).Border(1, t.Warning).Children(func() {
			ui.Text(c, m.SettingsSyncWebDAVInsecureWarning()).FontSize(11).TextColor(t.Warning)
		})
	}
}

// syncInsecurePrompt is the explicit decision the plain-HTTP warning demands.
// For a connect it reuses the settings captured before the warning; for a sync
// it records the confirmation in the service and retries.
func syncInsecurePrompt(c *ui.Context, state *SyncState, endpoint string, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Padding(10).Gap(6).Background(t.Background).Border(1, t.Danger).Children(func() {
		if endpoint != "" {
			ui.Text(c, m.SettingsSyncWebDAVEndpointValue(endpoint)).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Text(c, m.SettingsSyncWebDAVInsecureWarning()).FontSize(12).TextColor(t.Danger)
		ui.Row(c).Gap(8).Children(func() {
			label := m.SettingsSyncWebDAVInsecureConfirm()
			if state.InsecureSyncPrompt() {
				label = m.SettingsSyncWebDAVInsecureSyncConfirm()
			}
			if ui.Button(c, label).Disabled(state.Busy()).Clicked() {
				state.ConfirmInsecure()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsCancel()).Disabled(state.Busy()).Clicked() {
				state.CancelInsecure()
				c.Invalidate()
			}
		})
	})
}

// syncProviderSelector appears only when the service can switch providers.
// Choosing one clears the previous provider's token and merge anchor, so the
// next Connect authorizes the new provider from a clean baseline.
func syncProviderSelector(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages) {
	if !state.SupportsProviderSelection() || snapshot.Connected {
		return
	}
	values := []string{appsync.ProviderGitHub, appsync.ProviderGoogleDrive, appsync.ProviderOneDrive, appsync.ProviderWebDAV}
	names := []string{
		m.SettingsSyncProviderGitHub(),
		m.SettingsSyncProviderGoogleDrive(),
		m.SettingsSyncProviderOneDrive(),
		m.SettingsSyncProviderWebDAV(),
	}
	selected := names[indexOf(values, appsync.NormalizeProvider(snapshot.Provider))]
	ui.Field(c, m.SettingsSyncProvider(), func() {
		if ui.Select(c, &selected, names).Width(190).Changed() {
			state.SetProvider(values[indexOf(names, selected)])
			c.Invalidate()
		}
	})
}

func syncProviderName(provider string, m locales.Messages) string {
	switch appsync.NormalizeProvider(provider) {
	case appsync.ProviderGoogleDrive:
		return m.SettingsSyncProviderGoogleDrive()
	case appsync.ProviderOneDrive:
		return m.SettingsSyncProviderOneDrive()
	case appsync.ProviderWebDAV:
		return m.SettingsSyncProviderWebDAV()
	default:
		return m.SettingsSyncProviderGitHub()
	}
}

func webDAVAuthName(authType string, m locales.Messages) string {
	switch domainsync.NormalizeWebDAVAuthType(authType) {
	case domainsync.WebDAVAuthDigest:
		return m.SettingsSyncWebDAVAuthDigest()
	case domainsync.WebDAVAuthBearer:
		return m.SettingsSyncWebDAVAuthBearer()
	default:
		return m.SettingsSyncWebDAVAuthBasic()
	}
}

// syncDeviceCode shows the one-time code while GitHub authorization is in
// flight. Connect starts in the setup view, so the block must render before
// the account is connected.
func syncDeviceCode(c *ui.Context, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	code := snapshot.DeviceCode
	if code.UserCode == "" {
		return
	}
	ui.Column(c).Padding(10).Gap(4).Background(t.Background).Border(1, t.Border).Children(func() {
		ui.Text(c, m.SettingsSyncDeviceCode()).FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, code.UserCode).FontSize(18).Bold()
		ui.Text(c, m.SettingsSyncDeviceCodeHint(code.VerificationURI)).FontSize(11).
			TextColor(t.TextMuted)
	})
}

func syncPasswordField(c *ui.Context, state *SyncState, m locales.Messages) {
	ui.Field(c, m.SettingsSyncMasterPassword(), func() {
		password := state.Password()
		if ui.TextInput(c, &password).Password().Width(280).Changed() {
			state.SetPassword(password)
		}
	})
}

func syncConnected(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Gap(8).Children(func() {
		ui.Text(c, m.SettingsSyncProvider()).FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, syncProviderName(snapshot.Provider, m)).FontSize(11)
	})
	ui.Text(c, m.SettingsSyncConnectedAs(snapshot.Login)).Bold()
	if appsync.NormalizeProvider(snapshot.Provider) == appsync.ProviderWebDAV {
		if snapshot.WebDAVEndpoint != "" {
			ui.Text(c, m.SettingsSyncWebDAVEndpointValue(snapshot.WebDAVEndpoint)).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, m.SettingsSyncWebDAVAuth()).FontSize(11).TextColor(t.TextMuted)
			ui.Text(c, webDAVAuthName(snapshot.WebDAVAuthType, m)).FontSize(11)
		})
	}
	if state.InsecurePrompt() {
		syncInsecurePrompt(c, state, snapshot.WebDAVEndpoint, m, t)
		return
	}
	if snapshot.HasPassword {
		ui.Text(c, m.SettingsSyncReady()).FontSize(11).TextColor(t.TextMuted)
	} else {
		ui.Text(c, m.SettingsSyncNeedsPassword()).FontSize(11).TextColor(t.Warning)
		syncPasswordField(c, state, m)
	}
	syncDeviceCode(c, snapshot, m, t)

	if snapshot.WebDAVInsecure {
		// A plain-HTTP WebDAV profile never auto-syncs; the service also
		// refuses the automatic trigger. Explain why instead of offering a
		// checkbox that would do nothing.
		ui.Text(c, m.SettingsSyncWebDAVInsecureAutoSync()).FontSize(11).TextColor(t.Warning)
	} else {
		ui.Field(c, m.SettingsSyncAutoSync(), func() {
			auto := snapshot.AutoSync
			if ui.Checkbox(c, &auto, "").Changed() {
				state.SetAutoSync(auto)
				c.Invalidate()
			}
		})
	}
	strategies := []string{m.SettingsSyncNewest(), m.SettingsSyncLocal(), m.SettingsSyncRemote()}
	values := []domainsync.Strategy{domainsync.StrategyNewest, domainsync.StrategyLocal, domainsync.StrategyRemote}
	strategy := strategies[syncStrategyIndex(values, snapshot.Strategy)]
	ui.Field(c, m.SettingsSyncConflictStrategy(), func() {
		if ui.Select(c, &strategy, strategies).Width(190).Changed() {
			state.SetStrategy(values[indexOf(strategies, strategy)])
			c.Invalidate()
		}
	})
	ui.Text(c, syncLastSynced(m, snapshot.LastSyncedAt)).FontSize(11).TextColor(t.TextMuted)

	syncButtons(c, state, snapshot, m)
	syncStatus(c, state, snapshot, m, t)

	if state.Disconnecting() {
		ui.Column(c).Padding(10).Gap(8).Background(t.Background).Border(1, t.Danger).Children(func() {
			ui.Text(c, m.SettingsSyncDisconnectConfirm()).FontSize(12)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, m.SettingsSyncDisconnect()).Clicked() {
					state.Disconnect()
					c.Invalidate()
				}
				if ui.Button(c, m.SettingsCancel()).Clicked() {
					state.CancelDisconnect()
					c.Invalidate()
				}
			})
		})
	}
}

func syncButtons(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages) {
	ui.Row(c).Gap(8).Children(func() {
		if !snapshot.Connected || state.Reconfiguring() {
			if ui.Button(c, m.SettingsSyncConnect()).Disabled(state.Busy()).Clicked() {
				state.Connect()
				c.Invalidate()
			}
			if state.Reconfiguring() {
				if ui.Button(c, m.SettingsCancel()).Disabled(state.Busy()).Clicked() {
					state.CancelReconfigure()
					c.Invalidate()
				}
			}
			return
		}
		if ui.Button(c, m.SettingsSyncSyncNow()).Disabled(state.Busy()).Clicked() {
			state.SyncNow()
			c.Invalidate()
		}
		if appsync.NormalizeProvider(snapshot.Provider) == appsync.ProviderWebDAV {
			if ui.Button(c, m.SettingsSyncWebDAVReconfigure()).Disabled(state.Busy()).Clicked() {
				state.Reconfigure()
				c.Invalidate()
			}
		}
		if snapshot.Problem == appsync.ProblemReauthorization {
			if ui.Button(c, m.SettingsSyncReconnect()).Disabled(state.Busy()).Clicked() {
				state.Reconnect()
				c.Invalidate()
			}
		}
		if ui.Button(c, m.SettingsSyncDisconnect()).Disabled(state.Busy()).Clicked() {
			state.AskDisconnect()
			c.Invalidate()
		}
	})
}

func syncStatus(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	if problem := syncProblemText(m, snapshot.Problem); problem != "" {
		ui.Text(c, problem).FontSize(12).TextColor(t.Danger)
		return
	}
	if err := state.Err(); err != nil {
		// Worker errors carry typed sentinels; never render Go error text.
		text := syncProblemText(m, syncProblemForError(err))
		if text == "" {
			text = m.SettingsSyncOperationFailed()
		}
		ui.Text(c, text).FontSize(12).TextColor(t.Danger)
		return
	}
	status := syncStatusText(m, snapshot.Status)
	if status != "" {
		ui.Text(c, status).FontSize(12).TextColor(t.TextMuted)
	}
}

func syncStatusText(m locales.Messages, status appsync.Status) string {
	switch status {
	case appsync.StatusAuthorizing:
		return m.SettingsSyncStatusAuthorizing()
	case appsync.StatusSyncing:
		return m.SettingsSyncStatusSyncing()
	case appsync.StatusSynced:
		return m.SettingsSyncStatusSynced()
	case appsync.StatusFailed:
		return m.SettingsSyncStatusFailed()
	case appsync.StatusNeedsPassword:
		return m.SettingsSyncStatusNeedsPassword()
	default:
		return ""
	}
}

func syncProblemText(m locales.Messages, problem appsync.Problem) string {
	switch problem {
	case appsync.ProblemEmptyVault:
		return m.SettingsSyncProblemEmptyVault()
	case appsync.ProblemConflict:
		return m.SettingsSyncProblemConflict()
	case appsync.ProblemReauthorization:
		return m.SettingsSyncProblemReauthorization()
	case appsync.ProblemCredentials:
		return m.SettingsSyncProblemCredentials()
	case appsync.ProblemForbidden:
		return m.SettingsSyncProblemForbidden()
	case appsync.ProblemTLS:
		return m.SettingsSyncProblemTLS()
	case appsync.ProblemInvalidEndpoint:
		return m.SettingsSyncProblemInvalidEndpoint()
	case appsync.ProblemInsecureEndpoint:
		return m.SettingsSyncProblemInsecureEndpoint()
	case appsync.ProblemRedirect:
		return m.SettingsSyncProblemRedirect()
	case appsync.ProblemWrongPassword:
		return m.SettingsSyncProblemWrongPassword()
	case appsync.ProblemCorrupt:
		return m.SettingsSyncProblemCorrupt()
	case appsync.ProblemUnauthorized:
		return m.SettingsSyncProblemUnauthorized()
	case appsync.ProblemRateLimited:
		return m.SettingsSyncProblemRateLimited()
	case appsync.ProblemNetwork:
		return m.SettingsSyncProblemNetwork()
	case appsync.ProblemAuthorization:
		return m.SettingsSyncProblemAuthorization()
	case appsync.ProblemNotConfigured:
		return m.SettingsSyncProblemNotConfigured()
	case appsync.ProblemPasswordRequired:
		return m.SettingsSyncProblemPasswordRequired()
	case appsync.ProblemAlreadyInProgress:
		return m.SettingsSyncProblemAlreadyInProgress()
	case appsync.ProblemLocal:
		return m.SettingsSyncProblemLocal()
	case appsync.ProblemUnsupported:
		return m.SettingsSyncProblemUnsupported()
	default:
		return ""
	}
}

// syncProblemForError maps a worker error onto the same localized problem
// vocabulary the service stores. Unknown errors fall back to the generic
// operation-failed string.
func syncProblemForError(err error) appsync.Problem {
	switch {
	case errors.Is(err, appsync.ErrEmptyVault):
		return appsync.ProblemEmptyVault
	case errors.Is(err, appsync.ErrAlreadyInProgress):
		return appsync.ProblemAlreadyInProgress
	case errors.Is(err, appsync.ErrPasswordRequired):
		return appsync.ProblemPasswordRequired
	case errors.Is(err, appsync.ErrNotConfigured):
		return appsync.ProblemNotConfigured
	case errors.Is(err, appsync.ErrConflict):
		return appsync.ProblemConflict
	case errors.Is(err, appsync.ErrWrongPassword):
		return appsync.ProblemWrongPassword
	case errors.Is(err, appsync.ErrRemoteCorrupt):
		return appsync.ProblemCorrupt
	case errors.Is(err, appsync.ErrUnsupportedVersion):
		return appsync.ProblemUnsupported
	case errors.Is(err, appsync.ErrReauthorizationRequired):
		return appsync.ProblemReauthorization
	case errors.Is(err, appsync.ErrCredentialsRejected):
		return appsync.ProblemCredentials
	case errors.Is(err, appsync.ErrForbidden):
		return appsync.ProblemForbidden
	case errors.Is(err, appsync.ErrTLS):
		return appsync.ProblemTLS
	case errors.Is(err, appsync.ErrInvalidEndpoint):
		return appsync.ProblemInvalidEndpoint
	case errors.Is(err, appsync.ErrInsecureEndpoint):
		return appsync.ProblemInsecureEndpoint
	case errors.Is(err, appsync.ErrRedirectRefused):
		return appsync.ProblemRedirect
	case errors.Is(err, appsync.ErrUnauthorized):
		return appsync.ProblemUnauthorized
	case errors.Is(err, appsync.ErrRateLimited):
		return appsync.ProblemRateLimited
	case errors.Is(err, appsync.ErrNetwork):
		return appsync.ProblemNetwork
	case errors.Is(err, appsync.ErrAuthorizationDenied), errors.Is(err, appsync.ErrDeviceCodeExpired),
		errors.Is(err, appsync.ErrAuthorizationPending), errors.Is(err, appsync.ErrSlowDown):
		return appsync.ProblemAuthorization
	default:
		return appsync.ProblemNone
	}
}

func syncLastSynced(m locales.Messages, at int64) string {
	if at <= 0 {
		return m.SettingsSyncNeverSynced()
	}
	return m.SettingsSyncLastSynced(formatSyncTime(at))
}

func formatSyncTime(milliseconds int64) string {
	return time.UnixMilli(milliseconds).Local().Format("2006-01-02 15:04")
}

func syncStrategyIndex(values []domainsync.Strategy, value domainsync.Strategy) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}
