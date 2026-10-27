package settings

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appsync "github.com/mslxl/natcatty/application/sync"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
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
// choose between GitHub, Google Drive, OneDrive, WebDAV, and S3. Ticket 49's
// fake does not implement it, so the selector is hidden rather than forced
// into that seam.
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

// pendingKind identifies which provider captured the settings frozen behind an
// outstanding plain-HTTP confirmation. Only one warning can be open at a time.
const (
	pendingKindWebDAV = "webdav"
	pendingKindS3     = "s3"
)

// S3SyncService is the optional S3-compatible capability. The form passes the
// user-entered endpoint, region, bucket, prefix, and signing material
// explicitly; confirmInsecure is true only after the user has seen and
// accepted the plain-HTTP warning for this exact attempt.
type S3SyncService interface {
	ConnectS3(ctx context.Context, password string, settings appsync.S3Settings, confirmInsecure bool) error
	S3Settings() appsync.S3Settings
	ConfirmInsecureEndpoint(ctx context.Context) bool
}

// DestructiveSyncService is the optional guard, conflict, and backup
// capability introduced with issue 54. Ticket 49-53 fakes do not implement it,
// so their surfaces are unchanged; the real service does.
type DestructiveSyncService interface {
	ForcePush(ctx context.Context) (appsync.Result, error)
	RestoreGuard(ctx context.Context) (appsync.Result, error)
	ResolveConflict(ctx context.Context, resolution appsync.Resolution) (appsync.Result, error)
	LoadBackups(ctx context.Context) error
	RestoreBackup(ctx context.Context, source appsync.BackupSource, id string) error
}

// ConvergentSyncService is the optional v2 capability. Fakes without it keep
// the ticket 49-54 surface unchanged; the production service implements it.
type ConvergentSyncService interface {
	SetConvergentEnabled(ctx context.Context, enabled bool) (appsync.ConvergentMigrationPreview, error)
	RestoreLastVerified(ctx context.Context) (appsync.Result, error)
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
	// S3 form state. The access key id, secret access key, and session token
	// live only in this UI state for the duration of an attempt; after a
	// successful connect they are cleared, and they are never part of a
	// snapshot or a log line.
	s3Loaded          bool
	s3Endpoint        string
	s3Region          string
	s3Bucket          string
	s3Prefix          string
	s3AccessKeyID     string
	s3SecretAccessKey string
	s3SessionToken    string
	// pendingS3 and pendingS3Password freeze exactly what the user confirmed
	// after the plain-HTTP warning.
	pendingS3         appsync.S3Settings
	pendingS3Password string
	// pendingKind is pendingKindWebDAV or pendingKindS3 while a warning confirmation is
	// outstanding, so ConfirmInsecure knows which frozen settings to retry.
	pendingKind    string
	insecurePrompt bool
	insecureAction string
	reconfigure    bool
	forceConfirm   bool
	backupsAsked   bool
	// convergentDetails reveals the safe, value-free divergence detail. It
	// starts collapsed so the actionable safe buttons stay prominent.
	convergentDetails bool
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
// password. For a WebDAV or S3 profile it sends the form's connection
// settings; a plain-HTTP endpoint first produces the explicit warning
// confirmation.
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
	if service, ok := s.s3Service(); ok &&
		appsync.NormalizeProvider(s.service.Snapshot().Provider) == appsync.ProviderS3 {
		s.connectS3(service, password)
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

// s3Service reports the S3 capability of the current service.
func (s *SyncState) s3Service() (S3SyncService, bool) {
	service, ok := s.service.(S3SyncService)
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
	s.pendingKind = pendingKindWebDAV
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
				state.pendingKind = ""
			default:
				state.lastErr = nil
				state.password = ""
				state.webdavPassword = ""
				state.webdavToken = ""
				state.pendingConnect = appsync.WebDAVSettings{}
				state.pendingPassword = ""
				state.pendingKind = ""
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

// connectS3 validates the form, then connects with confirmInsecure=false. The
// service's gate returns ErrInsecureEndpoint before storing or sending the
// signing material; the view then shows the warning and ConfirmInsecure retries
// the same captured settings.
func (s *SyncState) connectS3(service S3SyncService, password string) {
	settings := s.s3Settings()
	valid := settings.Endpoint != "" && settings.Region != "" && settings.Bucket != "" &&
		settings.AccessKeyID != "" && settings.SecretAccessKey != ""
	if !valid {
		s.lastErr = appsync.ErrInvalidEndpoint
		return
	}
	s.busy = true
	s.lastErr = nil
	s.pendingS3 = settings
	s.pendingS3Password = password
	s.pendingKind = pendingKindS3
	state := s
	go func() {
		err := state.service.SetPassword(password)
		if err == nil {
			err = service.ConnectS3(state.ctx, password, settings, false)
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
				state.pendingS3 = appsync.S3Settings{}
				state.pendingS3Password = ""
				state.pendingKind = ""
			default:
				state.lastErr = nil
				state.password = ""
				state.s3AccessKeyID = ""
				state.s3SecretAccessKey = ""
				state.s3SessionToken = ""
				state.pendingS3 = appsync.S3Settings{}
				state.pendingS3Password = ""
				state.pendingKind = ""
				state.reconfigure = false
			}
		})
	}()
}

// s3Settings reads the form. All three signing fields stay in the UI state
// until the connect succeeds.
func (s *SyncState) s3Settings() appsync.S3Settings {
	return appsync.S3Settings{
		Endpoint:        strings.TrimSpace(s.s3Endpoint),
		Region:          strings.TrimSpace(s.s3Region),
		Bucket:          strings.TrimSpace(s.s3Bucket),
		Prefix:          strings.TrimSpace(s.s3Prefix),
		AccessKeyID:     strings.TrimSpace(s.s3AccessKeyID),
		SecretAccessKey: s.s3SecretAccessKey,
		SessionToken:    strings.TrimSpace(s.s3SessionToken),
	}
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
	s.pendingKind = ""
}

// EnsureS3Form prefills the non-secret form fields once from the stored
// profile. Access key id, secret access key, and session token are never
// prefilled: they live only in the secure store.
func (s *SyncState) EnsureS3Form() {
	if s.s3Loaded {
		return
	}
	settings := appsync.S3Settings{}
	if service, ok := s.s3Service(); ok {
		settings = service.S3Settings()
	}
	snapshot := s.service.Snapshot()
	if settings.Endpoint == "" {
		settings.Endpoint = snapshot.S3Endpoint
	}
	if settings.Region == "" {
		settings.Region = snapshot.S3Region
	}
	if settings.Bucket == "" {
		settings.Bucket = snapshot.S3Bucket
	}
	if settings.Prefix == "" {
		settings.Prefix = snapshot.S3Prefix
	}
	s.s3Endpoint = settings.Endpoint
	s.s3Region = settings.Region
	s.s3Bucket = settings.Bucket
	s.s3Prefix = settings.Prefix
	s.s3Loaded = true
}

// ResetS3Form forgets the form, including any unsent signing material.
func (s *SyncState) ResetS3Form() {
	s.s3Loaded = false
	s.s3Endpoint = ""
	s.s3Region = ""
	s.s3Bucket = ""
	s.s3Prefix = ""
	s.s3AccessKeyID = ""
	s.s3SecretAccessKey = ""
	s.s3SessionToken = ""
	s.pendingS3 = appsync.S3Settings{}
	s.pendingS3Password = ""
	s.pendingKind = ""
}

// S3 form accessors. They run on the UI thread in response to text edits.
func (s *SyncState) S3Endpoint() string        { return s.s3Endpoint }
func (s *SyncState) S3Region() string          { return s.s3Region }
func (s *SyncState) S3Bucket() string          { return s.s3Bucket }
func (s *SyncState) S3Prefix() string          { return s.s3Prefix }
func (s *SyncState) S3AccessKeyID() string     { return s.s3AccessKeyID }
func (s *SyncState) S3SecretAccessKey() string { return s.s3SecretAccessKey }
func (s *SyncState) S3SessionToken() string    { return s.s3SessionToken }

func (s *SyncState) SetS3Endpoint(value string) {
	s.s3Loaded = true
	s.s3Endpoint = value
}

func (s *SyncState) SetS3Region(value string) {
	s.s3Loaded = true
	s.s3Region = value
}

func (s *SyncState) SetS3Bucket(value string) {
	s.s3Loaded = true
	s.s3Bucket = value
}

func (s *SyncState) SetS3Prefix(value string) {
	s.s3Loaded = true
	s.s3Prefix = value
}

func (s *SyncState) SetS3AccessKeyID(value string) {
	s.s3Loaded = true
	s.s3AccessKeyID = value
}

func (s *SyncState) SetS3SecretAccessKey(value string) {
	s.s3Loaded = true
	s.s3SecretAccessKey = value
}

func (s *SyncState) SetS3SessionToken(value string) {
	s.s3Loaded = true
	s.s3SessionToken = value
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
	if appsync.NormalizeProvider(s.service.Snapshot().Provider) == appsync.ProviderS3 {
		return appsync.EndpointIsInsecure(s.s3Endpoint)
	}
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
	s.pendingS3 = appsync.S3Settings{}
	s.pendingS3Password = ""
	s.pendingKind = ""
	s.webdavPassword = ""
	s.webdavToken = ""
	s.s3AccessKeyID = ""
	s.s3SecretAccessKey = ""
	s.s3SessionToken = ""
	s.lastErr = nil
}

// ConfirmInsecure is the explicit "Connect anyway" or "Sync anyway" action.
// The connect path reuses the exact settings captured before the warning.
func (s *SyncState) ConfirmInsecure() {
	if s.busy {
		return
	}
	action := s.insecureAction
	provider := appsync.NormalizeProvider(s.service.Snapshot().Provider)
	s.insecurePrompt = false
	s.insecureAction = ""
	if action == "sync" {
		var confirm func() bool
		if provider == appsync.ProviderS3 {
			if service, ok := s.s3Service(); ok {
				confirm = func() bool { return service.ConfirmInsecureEndpoint(s.ctx) }
			}
		} else if service, ok := s.webDAVService(); ok {
			confirm = func() bool { return service.ConfirmInsecureEndpoint(s.ctx) }
		}
		if confirm == nil {
			s.lastErr = appsync.ErrNotConfigured
			return
		}
		s.busy = true
		s.lastErr = nil
		state := s
		go func() {
			confirmed := confirm()
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

	if s.pendingKind == pendingKindS3 {
		service, ok := s.s3Service()
		if !ok {
			s.lastErr = appsync.ErrNotConfigured
			return
		}
		settings := s.pendingS3
		password := s.pendingS3Password
		if settings.Endpoint == "" || password == "" {
			s.lastErr = appsync.ErrPasswordRequired
			return
		}
		s.busy = true
		s.lastErr = nil
		state := s
		go func() {
			err := service.ConnectS3(state.ctx, password, settings, true)
			state.post(func() {
				state.busy = false
				if err != nil {
					state.lastErr = err
					return
				}
				state.password = ""
				state.s3AccessKeyID = ""
				state.s3SecretAccessKey = ""
				state.s3SessionToken = ""
				state.pendingS3 = appsync.S3Settings{}
				state.pendingS3Password = ""
				state.pendingKind = ""
				state.reconfigure = false
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
			state.pendingKind = ""
			state.reconfigure = false
		})
	}()
}

// Reconfigure opens the connection form while the existing credential remains
// in place, so a rejected credential can be corrected without losing the
// profile (or the endpoint) first.
func (s *SyncState) Reconfigure() {
	if s.busy {
		return
	}
	s.reconfigure = true
	s.lastErr = nil
	s.insecurePrompt = false
	s.insecureAction = ""
	if appsync.NormalizeProvider(s.service.Snapshot().Provider) == appsync.ProviderS3 {
		s.ResetS3Form()
		s.EnsureS3Form()
		return
	}
	s.ResetWebDAVForm()
	s.EnsureWebDAVForm()
}

// CancelReconfigure returns to the connected view without changing anything.
func (s *SyncState) CancelReconfigure() {
	s.reconfigure = false
	s.pendingConnect = appsync.WebDAVSettings{}
	s.pendingPassword = ""
	s.pendingS3 = appsync.S3Settings{}
	s.pendingS3Password = ""
	s.pendingKind = ""
	s.webdavPassword = ""
	s.webdavToken = ""
	s.s3AccessKeyID = ""
	s.s3SecretAccessKey = ""
	s.s3SessionToken = ""
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

// destructiveService returns the optional guard, conflict, and backup
// capability, if this service has it.
func (s *SyncState) destructiveService() (DestructiveSyncService, bool) {
	service, ok := s.service.(DestructiveSyncService)
	return service, ok
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
			state.ResetS3Form()
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
			state.ResetS3Form()
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

// ForcePushing reports whether the destructive force-push confirmation is
// open. The confirmation is never the default: AskForcePush is only called by
// the explicit second button.
func (s *SyncState) ForcePushing() bool { return s.forceConfirm }

// AskForcePush opens the destructive confirmation.
func (s *SyncState) AskForcePush() { s.forceConfirm = true }

// CancelForcePush closes it without pushing.
func (s *SyncState) CancelForcePush() { s.forceConfirm = false }

// BackupsAsked reports whether the automatic first load has already been
// requested for this surface.
func (s *SyncState) BackupsAsked() bool { return s.backupsAsked }

// ForcePush overrides a shrink or tombstone guard after the explicit
// confirmation. The application service still takes a protective local
// backup and retains the overwritten cloud copy.
func (s *SyncState) ForcePush() {
	service, ok := s.destructiveService()
	if !ok || s.busy {
		return
	}
	s.busy = true
	s.forceConfirm = false
	state := s
	go func() {
		_, err := service.ForcePush(state.ctx)
		state.post(func() {
			state.busy = false
			// A successful force push supersedes the blocked-sync error that
			// opened this dialog; keeping it would claim the upload is still
			// blocked after it reached the provider.
			state.lastErr = err
			state.requestBackups()
		})
	}()
}

// RestoreGuard restores the last good cloud copy after a blocked upload. It
// never writes to the provider.
func (s *SyncState) RestoreGuard() {
	service, ok := s.destructiveService()
	if !ok || s.busy {
		return
	}
	s.busy = true
	s.forceConfirm = false
	state := s
	go func() {
		_, err := service.RestoreGuard(state.ctx)
		state.post(func() {
			state.busy = false
			// The guard is satisfied by the restored copy; the old blocking
			// error must not outlive the successful restore.
			state.lastErr = err
			state.requestBackups()
		})
	}()
}

// SetConvergentEnabled enables v2 (migrating fail-closed on first use) or
// pauses an initialized replica. Pausing never falls back to the legacy
// writer; resuming reuses the existing replica.
func (s *SyncState) SetConvergentEnabled(enabled bool) {
	service, ok := s.service.(ConvergentSyncService)
	if !ok || s.busy {
		return
	}
	s.busy = true
	s.lastErr = nil
	s.convergentDetails = false
	state := s
	go func() {
		_, err := service.SetConvergentEnabled(state.ctx, enabled)
		state.post(func() {
			state.busy = false
			state.lastErr = err
			if err == nil {
				state.requestBackups()
			}
		})
	}()
}

// RestoreLastVerified applies the last provider-verified materialized copy
// locally behind a protective backup. It never writes the provider and never
// mutates the CRDT replica, so it is safe during divergence.
func (s *SyncState) RestoreLastVerified() {
	service, ok := s.service.(ConvergentSyncService)
	if !ok || s.busy {
		return
	}
	s.busy = true
	s.lastErr = nil
	state := s
	go func() {
		_, err := service.RestoreLastVerified(state.ctx)
		state.post(func() {
			state.busy = false
			state.lastErr = err
			if err == nil {
				state.convergentDetails = false
				state.requestBackups()
			}
		})
	}()
}

// ShowConvergentDetails reports whether the safe divergence detail is open.
func (s *SyncState) ShowConvergentDetails() bool { return s.convergentDetails }

// ToggleConvergentDetails opens or closes the value-free divergence detail.
func (s *SyncState) ToggleConvergentDetails() { s.convergentDetails = !s.convergentDetails }

// ResolveConflict applies the user's choice for a surfaced conflict.
func (s *SyncState) ResolveConflict(resolution appsync.Resolution) {
	service, ok := s.destructiveService()
	if !ok || s.busy {
		return
	}
	s.busy = true
	s.forceConfirm = false
	state := s
	go func() {
		_, err := service.ResolveConflict(state.ctx, resolution)
		state.post(func() {
			state.busy = false
			// The resolution replaced the conflict; the pre-resolution error
			// would otherwise stay on screen as if the conflict were pending.
			state.lastErr = err
			state.requestBackups()
		})
	}()
}

// LoadBackups refreshes the restore-point list. It is safe to call on the UI
// thread; the work runs on a worker goroutine.
func (s *SyncState) LoadBackups() { s.requestBackups() }

func (s *SyncState) requestBackups() {
	service, ok := s.destructiveService()
	if !ok || s.busy {
		return
	}
	s.busy = true
	s.backupsAsked = true
	state := s
	go func() {
		err := service.LoadBackups(state.ctx)
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
			}
		})
	}()
}

// RestoreBackup restores one local or cloud-copy restore point. The local path
// delegates to the backup service, which writes a before-restore backup; the
// cloud-copy path decrypts only inside the application service.
func (s *SyncState) RestoreBackup(source appsync.BackupSource, id string) {
	service, ok := s.destructiveService()
	if !ok || s.busy {
		return
	}
	s.busy = true
	state := s
	go func() {
		err := service.RestoreBackup(state.ctx, source, id)
		state.post(func() {
			state.busy = false
			// Restoring succeeded, so an earlier failed-sync error is stale and
			// must not suggest the restore failed.
			state.lastErr = err
			state.requestBackups()
		})
	}()
}

// syncSection renders the Sync & Cloud area.
func syncSection(c *ui.Context, state *SyncState, m locales.Messages, t *ui.Theme) {
	state.Flush()
	snapshot := state.Snapshot()
	section(c, m.SettingsSyncTitle(), m.SettingsSyncDescription(), t, func() {
		if syncSetupShown(state, snapshot) {
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
	provider := appsync.NormalizeProvider(snapshot.Provider)
	if state.InsecurePrompt() {
		// No input or button other than the explicit decision is live while
		// the warning is open, so the endpoint that was confirmed cannot be
		// changed behind it.
		syncInsecurePrompt(c, insecureEndpointText(provider, state.formEndpoint(provider), m),
			insecureWarningFor(provider, m), t)
		return
	}
	switch provider {
	case appsync.ProviderGoogleDrive:
		ui.Text(c, m.SettingsSyncGoogleHint()).FontSize(11).TextColor(t.TextMuted)
	case appsync.ProviderOneDrive:
		ui.Text(c, m.SettingsSyncOneDriveHint()).FontSize(11).TextColor(t.TextMuted)
	case appsync.ProviderWebDAV:
		ui.Text(c, m.SettingsSyncWebDAVHint()).FontSize(11).TextColor(t.TextMuted)
		syncWebDAVForm(c, state, m, t)
	case appsync.ProviderS3:
		ui.Text(c, m.SettingsSyncS3Hint()).FontSize(11).TextColor(t.TextMuted)
		syncS3Form(c, state, m, t)
	}
	syncDeviceCode(c, snapshot, m, t)
	syncPasswordField(c, state, m)
	ui.Text(c, m.SettingsSyncMasterPasswordHint()).FontSize(11).TextColor(t.TextMuted)
	ui.Text(c, m.SettingsSyncSecurityNote()).FontSize(11).TextColor(t.TextMuted)
	// Connect (and the Reconfigure Cancel) is pinned in
	// syncActionsPinnedBar, outside this scrolling form.
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
			ui.Text(c, m.SettingsSyncWebDAVInsecureWarning()).FontSize(11).TextColor(syncWarningTextColor(t))
		})
	}
}

// formEndpoint returns the endpoint currently entered in the provider's form.
func (s *SyncState) formEndpoint(provider string) string {
	if provider == appsync.ProviderS3 {
		return s.S3Endpoint()
	}
	return s.WebDAVEndpoint()
}

func insecureEndpointText(provider, endpoint string, m locales.Messages) string {
	if endpoint == "" {
		return ""
	}
	if provider == appsync.ProviderS3 {
		return m.SettingsSyncS3EndpointValue(endpoint)
	}
	return m.SettingsSyncWebDAVEndpointValue(endpoint)
}

func insecureWarningFor(provider string, m locales.Messages) string {
	if provider == appsync.ProviderS3 {
		return m.SettingsSyncS3InsecureWarning()
	}
	return m.SettingsSyncWebDAVInsecureWarning()
}

// syncWarningTextColor keeps the warning amber readable at the 11-point body
// size. The theme's warning token is tuned for icons and borders; as small
// text on a light surface it falls below the 4.5:1 contrast target, so light
// mode uses a darker amber while dark mode keeps the brighter token.
func syncWarningTextColor(t *ui.Theme) ui.Color {
	if t.Dark {
		return t.Warning
	}
	return ui.Hex("#9a5c00")
}

// syncDangerDarkTexts are the dark-mode danger text candidates, brightest
// last. A single fixed red cannot clear 4.5:1 on every dark preset's fill:
// the lightest preset background (blue-dolphin, relative luminance 0.118)
// needs a very pale red, while the default Midnight fill already accepts the
// first candidate. The helper picks the first candidate that clears the
// target against the prompt's fill.
var syncDangerDarkTexts = []string{"#ff6b6b", "#ff9999", "#ffb3b3", "#ffd1d1"}

// syncDangerTextColor keeps danger text readable at the 12-point body size of
// the confirmations. The theme's destructive token is tuned for borders and
// filled buttons; as body text on the light sync panel it measures about
// 4.47:1, below the 4.5:1 target, so light mode uses a darker red that clears
// the target on the default Snow fill (6.16:1) and on white (6.79:1), and on
// every light preset's fill (at least 4.79:1). Dark mode starts from a red
// that clears 4.5:1 on the default Midnight fill and brightens it only as far
// as the selected preset's fill demands.
func syncDangerTextColor(t *ui.Theme) ui.Color {
	if !t.Dark {
		return ui.Hex("#b51616")
	}
	for _, hex := range syncDangerDarkTexts {
		candidate := ui.Hex(hex)
		if syncColorContrast(candidate, t.Background) >= 4.5 {
			return candidate
		}
	}
	return ui.Hex(syncDangerDarkTexts[len(syncDangerDarkTexts)-1])
}

// syncColorHex renders an opaque ui.Color as the #rrggbb form the domain WCAG
// helpers parse.
func syncColorHex(c ui.Color) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// syncColorContrast returns the WCAG contrast ratio of two opaque colors,
// using the domain's single luminance/contrast implementation.
func syncColorContrast(a, b ui.Color) float64 {
	la, okA := domaintheme.HexLuminance(syncColorHex(a))
	lb, okB := domaintheme.HexLuminance(syncColorHex(b))
	if !okA || !okB {
		return 0
	}
	return domaintheme.ContrastRatio(la, lb)
}

// syncS3Form renders the endpoint, region, bucket, prefix, and signing fields.
// The secret access key and session token are masked; the access key id is an
// identifier rather than a secret, but it is cleared after a successful
// connect and is never prefilled. The plain-HTTP warning appears before the
// Connect button.
func syncS3Form(c *ui.Context, state *SyncState, m locales.Messages, t *ui.Theme) {
	state.EnsureS3Form()
	ui.Field(c, m.SettingsSyncS3Endpoint(), func() {
		endpoint := state.S3Endpoint()
		if ui.TextInput(c, &endpoint).Width(360).Changed() {
			state.SetS3Endpoint(endpoint)
		}
	})
	ui.Row(c).FillWidth().Gap(12).AlignItems(ui.End).Children(func() {
		ui.Field(c, m.SettingsSyncS3Region(), func() {
			region := state.S3Region()
			if ui.TextInput(c, &region).Width(150).Changed() {
				state.SetS3Region(region)
			}
		})
		ui.Field(c, m.SettingsSyncS3Bucket(), func() {
			bucket := state.S3Bucket()
			if ui.TextInput(c, &bucket).Width(210).Changed() {
				state.SetS3Bucket(bucket)
			}
		})
	})
	ui.Field(c, m.SettingsSyncS3Prefix(), func() {
		prefix := state.S3Prefix()
		if ui.TextInput(c, &prefix).Width(220).Changed() {
			state.SetS3Prefix(prefix)
		}
	})
	ui.Row(c).FillWidth().Gap(12).AlignItems(ui.End).Children(func() {
		ui.Field(c, m.SettingsSyncS3AccessKeyID(), func() {
			accessKeyID := state.S3AccessKeyID()
			if ui.TextInput(c, &accessKeyID).Width(200).Changed() {
				state.SetS3AccessKeyID(accessKeyID)
			}
		})
		ui.Field(c, m.SettingsSyncS3SecretAccessKey(), func() {
			secret := state.S3SecretAccessKey()
			if ui.TextInput(c, &secret).Password().Width(220).Changed() {
				state.SetS3SecretAccessKey(secret)
			}
		})
	})
	ui.Field(c, m.SettingsSyncS3SessionToken(), func() {
		token := state.S3SessionToken()
		if ui.TextInput(c, &token).Password().Width(280).Changed() {
			state.SetS3SessionToken(token)
		}
	})

	if state.InsecureEndpointEntered() {
		ui.Column(c).Padding(10).Gap(4).Background(t.Background).Border(1, t.Warning).Children(func() {
			ui.Text(c, m.SettingsSyncS3InsecureWarning()).FontSize(11).TextColor(syncWarningTextColor(t))
		})
	}
}

// syncInsecurePrompt is the explicit decision the plain-HTTP warning demands.
// For a connect it reuses the settings captured before the warning; for a sync
// it records the confirmation in the service and retries.
func syncInsecurePrompt(c *ui.Context, endpointText, warning string, t *ui.Theme) {
	ui.Column(c).Padding(10).Gap(6).Background(t.Background).Border(1, t.Danger).Children(func() {
		if endpointText != "" {
			ui.Text(c, endpointText).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Text(c, warning).FontSize(12).TextColor(syncDangerTextColor(t))
		// Confirm and Cancel are pinned in syncActionsPinnedBar, outside this
		// scrolling card, so the decision is fully visible at every window size.
	})
}

// syncProviderSelector appears only when the service can switch providers.
// Choosing one clears the previous provider's token and merge anchor, so the
// next Connect authorizes the new provider from a clean baseline.
func syncProviderSelector(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages) {
	if !state.SupportsProviderSelection() || snapshot.Connected {
		return
	}
	values := []string{appsync.ProviderGitHub, appsync.ProviderGoogleDrive, appsync.ProviderOneDrive, appsync.ProviderWebDAV, appsync.ProviderS3}
	names := []string{
		m.SettingsSyncProviderGitHub(),
		m.SettingsSyncProviderGoogleDrive(),
		m.SettingsSyncProviderOneDrive(),
		m.SettingsSyncProviderWebDAV(),
		m.SettingsSyncProviderS3(),
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
	case appsync.ProviderS3:
		return m.SettingsSyncProviderS3()
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
	provider := appsync.NormalizeProvider(snapshot.Provider)
	ui.Row(c).Gap(8).Children(func() {
		ui.Text(c, m.SettingsSyncProvider()).FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, syncProviderName(snapshot.Provider, m)).FontSize(11)
	})
	ui.Text(c, m.SettingsSyncConnectedAs(snapshot.Login)).Bold()
	if provider == appsync.ProviderWebDAV {
		if snapshot.WebDAVEndpoint != "" {
			ui.Text(c, m.SettingsSyncWebDAVEndpointValue(snapshot.WebDAVEndpoint)).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, m.SettingsSyncWebDAVAuth()).FontSize(11).TextColor(t.TextMuted)
			ui.Text(c, webDAVAuthName(snapshot.WebDAVAuthType, m)).FontSize(11)
		})
	}
	if provider == appsync.ProviderS3 {
		if snapshot.S3Endpoint != "" {
			ui.Text(c, m.SettingsSyncS3EndpointValue(snapshot.S3Endpoint)).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, m.SettingsSyncS3Region()).FontSize(11).TextColor(t.TextMuted)
			ui.Text(c, snapshot.S3Region).FontSize(11)
			ui.Text(c, m.SettingsSyncS3Bucket()).FontSize(11).TextColor(t.TextMuted)
			ui.Text(c, snapshot.S3Bucket).FontSize(11)
		})
		if snapshot.S3Prefix != "" {
			ui.Text(c, m.SettingsSyncS3PrefixValue(snapshot.S3Prefix)).FontSize(11).TextColor(t.TextMuted)
		}
	}
	if state.InsecurePrompt() {
		endpoint := snapshot.WebDAVEndpoint
		if provider == appsync.ProviderS3 {
			endpoint = snapshot.S3Endpoint
		}
		syncInsecurePrompt(c, insecureEndpointText(provider, endpoint, m),
			insecureWarningFor(provider, m), t)
		return
	}
	if snapshot.HasPassword {
		ui.Text(c, m.SettingsSyncReady()).FontSize(11).TextColor(t.TextMuted)
	} else {
		ui.Text(c, m.SettingsSyncNeedsPassword()).FontSize(11).TextColor(t.Warning)
		syncPasswordField(c, state, m)
	}
	syncDeviceCode(c, snapshot, m, t)

	insecureProfile := snapshot.WebDAVInsecure
	if provider == appsync.ProviderS3 {
		insecureProfile = snapshot.S3Insecure
	}
	if insecureProfile {
		// A plain-HTTP profile never auto-syncs; the service also refuses the
		// automatic trigger. Explain why instead of offering a checkbox that
		// would do nothing.
		warning := m.SettingsSyncWebDAVInsecureAutoSync()
		if provider == appsync.ProviderS3 {
			warning = m.SettingsSyncS3InsecureAutoSync()
		}
		ui.Text(c, warning).FontSize(11).TextColor(syncWarningTextColor(t))
	} else {
		ui.Field(c, m.SettingsSyncAutoSync(), func() {
			auto := snapshot.AutoSync
			if ui.Checkbox(c, &auto, "").Changed() {
				state.SetAutoSync(auto)
				c.Invalidate()
			}
		})
	}
	strategies := []string{
		m.SettingsSyncStrategySmartMerge(),
		m.SettingsSyncStrategySmartMergePreserve(),
		m.SettingsSyncStrategyPreferCloud(),
		m.SettingsSyncStrategyPreferLocal(),
	}
	values := []domainsync.Strategy{
		domainsync.StrategySmartMerge,
		domainsync.StrategySmartMergePreserve,
		domainsync.StrategyPreferCloud,
		domainsync.StrategyPreferLocal,
	}
	strategy := strategies[syncStrategyIndex(values, snapshot.Strategy)]
	ui.Field(c, m.SettingsSyncConflictStrategy(), func() {
		if ui.Select(c, &strategy, strategies).Width(300).Changed() {
			state.SetStrategy(values[indexOf(strategies, strategy)])
			c.Invalidate()
		}
	})
	ui.Text(c, syncLastSynced(m, snapshot.LastSyncedAt)).FontSize(11).TextColor(t.TextMuted)

	syncConvergentPanel(c, state, snapshot, m, t)
	syncConflictPanel(c, state, snapshot, m, t)
	syncGuardPanel(c, state, snapshot, m, t)
	syncButtons(c, state, snapshot, m)
	syncStatus(c, state, snapshot, m, t)
	if _, ok := state.destructiveService(); ok && !state.BackupsAsked() {
		state.LoadBackups()
	}
	syncBackupsPanel(c, state, snapshot, m, t)

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

// syncEntityLabel localizes the payload family keys used by the sync engine.
// Those keys are protocol identifiers ("hosts", "portForwardingRules"), not
// display strings; ADR-0009 requires every user-visible label to come from the
// catalog, so guards and conflict rows must never print them raw.
func syncEntityLabel(entityType string, m locales.Messages) string {
	switch entityType {
	case "hosts":
		return m.SearchSectionHost()
	case "keys":
		return m.SearchSectionKey()
	case "identities":
		return m.SearchSectionIdentity()
	case "proxyProfiles":
		return m.SearchSectionProxyProfile()
	case "snippets":
		return m.SearchSectionSnippet()
	case "groups":
		return m.SearchSectionHostGroup()
	case "snippetPackages":
		return m.SearchSectionSnippetPackage()
	case "notes":
		return m.SearchSectionNote()
	case "noteGroups":
		return m.SearchSectionNoteGroup()
	case "portForwardingRules":
		return m.TrayForwarding()
	case "settings":
		return m.NavSettings()
	default:
		return m.SettingsSyncEntityUnknown()
	}
}

// syncConvergentBarHeight is the pinned action bar's height. The Settings
// viewport reserves the same height at the end of its scroll content.
const syncConvergentBarHeight float32 = 52

// syncConvergentPinnedBarShown reports whether the v2 action bar is available.
// The bar stays at the bottom edge of the client so the controls are never
// clipped by the scrolling report above them.
func syncConvergentPinnedBarShown(state *SyncState, snapshot appsync.Snapshot) bool {
	if state == nil || !snapshot.Connected {
		return false
	}
	_, ok := state.service.(ConvergentSyncService)
	return ok
}

// syncConvergentPinnedBar draws the convergence actions in an overlay pinned
// to the bottom of the Settings client. Only safe actions appear, and their
// availability follows the same phase the report renders.
func syncConvergentPinnedBar(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	if !syncConvergentPinnedBarShown(state, snapshot) {
		return
	}
	info := snapshot.Convergent
	phase := convergentPhase(info)
	showEnable := info.SchemaVersion == 0 && phase != "paused"
	showResume := phase == "paused"
	showPause := info.Enabled && phase != "paused" && phase != "diverged" && phase != "blocked"
	showDivergence := phase == "diverged"
	if !showEnable && !showResume && !showPause && !showDivergence {
		return
	}
	ui.Row(c).FillWidth().Height(syncConvergentBarHeight).
		Padding(10).Gap(12).AlignItems(ui.Center).Justify(ui.End).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if showEnable {
				if ui.Button(c, m.SettingsSyncConvergentEnable()).Disabled(state.Busy()).Clicked() {
					state.SetConvergentEnabled(true)
					c.Invalidate()
				}
			}
			if showResume {
				if ui.Button(c, m.SettingsSyncConvergentResume()).Disabled(state.Busy()).Clicked() {
					state.SetConvergentEnabled(true)
					c.Invalidate()
				}
			}
			if showPause {
				if ui.Button(c, m.SettingsSyncConvergentPause()).Disabled(state.Busy()).Clicked() {
					state.SetConvergentEnabled(false)
					c.Invalidate()
				}
			}
			if showDivergence {
				if ui.Button(c, m.SettingsSyncConvergentInspect()).Disabled(state.Busy()).Clicked() {
					state.ToggleConvergentDetails()
					c.Invalidate()
				}
				if ui.Button(c, m.SettingsSyncConvergentRestore()).Disabled(state.Busy()).Clicked() {
					state.RestoreLastVerified()
					c.Invalidate()
				}
				if ui.Button(c, m.SettingsSyncConvergentResync()).Disabled(state.Busy()).Clicked() {
					state.SyncNow()
					c.Invalidate()
				}
			}
		})
	})
}

// syncSetupShown reports whether the Sync page is on the setup/reconfigure
// surface rather than the connected surface.
func syncSetupShown(state *SyncState, snapshot appsync.Snapshot) bool {
	return !snapshot.Connected || state.Reconfiguring()
}

// syncActionsPinnedBar draws ticket 54's primary actions below the scrolling
// Settings content: the conflict resolutions, the shrink/tombstone guard
// Restore and Force push rows, the setup Connect action, and Refresh backups.
// The bar is a sibling of the bounded content viewport, so it subtracts its
// height from the content and is never cut at the scroll edge. It is drawn
// above ticket 55's convergent bar, which keeps the very bottom edge of the
// client.
func syncActionsPinnedBar(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme, layout *viewLayout) {
	if state == nil {
		return
	}
	setup := syncSetupShown(state, snapshot)
	prompt := state.InsecurePrompt()
	_, destructive := state.destructiveService()
	conflict := snapshot.Conflict
	finding := snapshot.Guard
	showSetup := setup && !prompt
	showConflict := !setup && !prompt && conflict != nil
	showGuard := !setup && !prompt && finding.Suspicious()
	showBackups := !setup && !prompt && destructive
	if !prompt && !showSetup && !showConflict && !showGuard && !showBackups {
		return
	}

	bar := ui.Column(c).FillWidth().Padding(10).Gap(6).
		Background(t.Surface).Border(1, t.Border)
	if layout != nil {
		bar = bar.Bind(&layout.SyncActions)
	}
	bar.Children(func() {
		if prompt {
			syncActionRow(c, func() {
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
			return
		}
		if showSetup {
			syncActionRow(c, func() {
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
			})
			return
		}
		if showConflict {
			syncActionRow(c, func() {
				if ui.Button(c, m.SettingsSyncConflictUseLocal()).Disabled(state.Busy()).Clicked() {
					state.ResolveConflict(appsync.ResolutionLocal)
					c.Invalidate()
				}
				if ui.Button(c, m.SettingsSyncConflictUseRemote()).Disabled(state.Busy()).Clicked() {
					state.ResolveConflict(appsync.ResolutionRemote)
					c.Invalidate()
				}
				if conflict.CanMerge {
					if ui.Button(c, m.SettingsSyncConflictMerge()).Disabled(state.Busy()).Clicked() {
						state.ResolveConflict(appsync.ResolutionMerge)
						c.Invalidate()
					}
				}
			})
		}
		if showGuard {
			syncActionRow(c, func() {
				if ui.Button(c, m.SettingsSyncGuardRestore()).Disabled(state.Busy()).Clicked() {
					state.RestoreGuard()
					c.Invalidate()
				}
				if ui.Button(c, m.SettingsSyncGuardForce()).Disabled(state.Busy()).Clicked() {
					state.AskForcePush()
					c.Invalidate()
				}
			})
			if state.ForcePushing() {
				syncActionRow(c, func() {
					if ui.Button(c, m.SettingsSyncDoForcePush()).Disabled(state.Busy()).Clicked() {
						state.ForcePush()
						c.Invalidate()
					}
					if ui.Button(c, m.SettingsCancel()).Disabled(state.Busy()).Clicked() {
						state.CancelForcePush()
						c.Invalidate()
					}
				})
			}
		}
		if showBackups {
			syncActionRow(c, func() {
				if ui.Button(c, m.SettingsSyncBackupsRefresh()).Disabled(state.Busy()).Clicked() {
					state.LoadBackups()
					c.Invalidate()
				}
			})
		}
	})
}

// syncActionRow lays out one right-aligned row of primary actions.
func syncActionRow(c *ui.Context, children func()) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Justify(ui.End).Children(children)
}

// convergentPhase fills in the device-local phase when a snapshot predates the
// explicit field, so the panel and the pinned bar never disagree.
func convergentPhase(info appsync.ConvergentInfo) string {
	if info.Phase != "" {
		return info.Phase
	}
	switch {
	case info.SchemaVersion == 0:
		return "disabled"
	case !info.Enabled:
		return "paused"
	default:
		return "idle"
	}
}

// syncConvergentPanel is the v2 enable, migration-refusal, verification, and
// divergence surface. It is shown only when the service provides the optional
// capability. Every body paragraph uses the theme text color, and warnings and
// refusals use the shared accessible warning/danger text helpers, so the panel
// stays at 4.5:1 at the settings body sizes in both themes. The actionable
// controls live in syncConvergentPinnedBar, outside this scrolling report.
func syncConvergentPanel(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	if _, ok := state.service.(ConvergentSyncService); !ok {
		return
	}
	info := snapshot.Convergent
	phase := convergentPhase(info)
	border := t.Border
	if phase == "blocked" || phase == "diverged" || phase == "failed" {
		border = t.Danger
	}
	ui.Column(c).Padding(10).Gap(8).Background(t.Background).Border(1, border).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SettingsSyncConvergentTitle()).FontSize(12).Bold().TextColor(t.Text)
			ui.Text(c, m.SettingsSyncConvergentState(convergentPhaseName(phase, m))).FontSize(11).TextColor(t.Text)
		})
		ui.Text(c, m.SettingsSyncConvergentDescription()).FontSize(11).TextColor(t.Text)

		switch {
		case phase == "blocked":
			syncConvergentMigrationBlocked(c, state, info, m, t)
			return
		case phase == "diverged":
			syncConvergentDivergence(c, state, info, m, t)
			return
		case info.SchemaVersion == 0:
			ui.Text(c, m.SettingsSyncConvergentEnableHint()).FontSize(11).TextColor(t.Text)
			return
		}

		if !info.Enabled {
			ui.Text(c, m.SettingsSyncConvergentPausedHint()).FontSize(11).TextColor(t.Text)
			return
		}
		syncConvergentVerification(c, state, info, m, t)
	})
}

// syncConvergentMigrationBlocked explains a fail-closed migration without
// rendering raw Go error text. The cloud bytes and local Vault are unchanged
// until the user resolves the named condition and retries.
func syncConvergentMigrationBlocked(c *ui.Context, state *SyncState, info appsync.ConvergentInfo, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SettingsSyncConvergentMigrationBlocked()).FontSize(12).Bold().TextColor(syncDangerTextColor(t))
	ui.Text(c, m.SettingsSyncConvergentEnableHint()).FontSize(11).TextColor(t.Text)
	if preview := info.Migration; preview != nil {
		ui.Text(c, m.SettingsSyncConvergentMigrationSummary(
			strconv.Itoa(preview.ProviderSchemaVersion),
			strconv.Itoa(preview.LocalSchemaVersion),
			strconv.Itoa(preview.EntityCount),
		)).FontSize(11).TextColor(t.Text)
		for _, reason := range preview.BlockedReasons {
			ui.Text(c, convergentReasonName(reason.Code, m)).FontSize(11).TextColor(syncDangerTextColor(t))
		}
	}
}

// syncConvergentDivergence offers only the safe actions: inspect the
// value-free report, restore the last provider-verified local copy, or re-run
// sync. There is deliberately no overwrite-provider action.
func syncConvergentDivergence(c *ui.Context, state *SyncState, info appsync.ConvergentInfo, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SettingsSyncConvergentDivergenceTitle()).FontSize(12).Bold().TextColor(syncDangerTextColor(t))
	ui.Text(c, m.SettingsSyncConvergentDivergenceDescription()).FontSize(11).TextColor(t.Text)
	divergence := info.Divergence
	if divergence != nil {
		ui.Text(c, m.SettingsSyncConvergentDivergenceKind(convergentDivergenceKindName(divergence.Kind, m))).
			FontSize(11).TextColor(t.Text)
		if divergence.Family != "" || divergence.EntityID != "" {
			family := m.SettingsSyncEntityUnknown()
			if divergence.Family != "" {
				family = syncEntityLabel(divergence.Family, m)
			}
			ui.Text(c, m.SettingsSyncConvergentDivergenceAddress(family, divergence.EntityID)).
				FontSize(11).TextColor(t.Text)
		}
		if divergence.Dot != "" {
			ui.Text(c, m.SettingsSyncConvergentDivergenceDot(divergence.Dot)).FontSize(11).TextColor(t.Text)
		}
	}
	ui.Text(c, m.SettingsSyncConvergentActionsHint()).FontSize(11).TextColor(t.Text)
	if state.ShowConvergentDetails() {
		detail := m.SettingsSyncConvergentInspectDetails(
			divergenceHash(divergence, true),
			divergenceHash(divergence, false),
		)
		ui.Text(c, detail).FontSize(11).TextColor(t.Text)
		if divergence != nil && (divergence.LocalVector != nil || divergence.RemoteVector != nil) {
			ui.Text(c, m.SettingsSyncConvergentVectorDetail(
				strconv.Itoa(len(divergence.LocalVector)),
				strconv.Itoa(len(divergence.RemoteVector)),
			)).FontSize(11).TextColor(t.Text)
		}
	}
	ui.Text(c, m.SettingsSyncConvergentRestoreHint()).FontSize(11).TextColor(t.Text)
}

func divergenceHash(divergence *appsync.ConvergentDivergenceInfo, local bool) string {
	if divergence == nil {
		return "—"
	}
	hash := divergence.RemoteHash
	if local {
		hash = divergence.LocalHash
	}
	if hash == "" {
		return "—"
	}
	if len(hash) > 16 {
		return hash[:16]
	}
	return hash
}

// syncConvergentVerification shows the three progressive comparisons, their
// live outcome, the write count (zero on a converged repeat), record/conflict
// counts, and the last verification time.
func syncConvergentVerification(c *ui.Context, state *SyncState, info appsync.ConvergentInfo, m locales.Messages, t *ui.Theme) {
	if info.TotalRounds > 0 || len(info.Rounds) > 0 {
		total := info.TotalRounds
		if total == 0 {
			total = len(info.Rounds)
		}
		if len(info.Rounds) == 0 {
			ui.Text(c, m.SettingsSyncConvergentRound(strconv.Itoa(info.Round), strconv.Itoa(total),
				convergentRoundKindName(info.RoundKind, m),
				convergentRoundOutcomeName(info.Outcome, m))).FontSize(11).TextColor(t.Text)
		} else {
			for _, round := range info.Rounds {
				ui.Text(c, m.SettingsSyncConvergentRound(strconv.Itoa(round.Round), strconv.Itoa(total),
					convergentRoundKindName(round.Kind, m),
					convergentRoundOutcomeName(round.Outcome, m))).FontSize(11).TextColor(t.Text)
			}
		}
	}
	ui.Text(c, m.SettingsSyncConvergentWrites(strconv.Itoa(info.Writes))).FontSize(11).TextColor(t.Text)
	ui.Text(c, m.SettingsSyncConvergentSummary(strconv.Itoa(info.EntityCount), strconv.Itoa(info.ConflictCount))).FontSize(11).TextColor(t.Text)
	if info.LastVerifiedAt > 0 {
		ui.Text(c, m.SettingsSyncConvergentLastVerified(formatSyncTime(info.LastVerifiedAt))).
			FontSize(11).TextColor(t.Text)
	}
}

func convergentPhaseName(phase string, m locales.Messages) string {
	switch phase {
	case "disabled":
		return m.SettingsSyncConvergentPhaseDisabled()
	case "paused":
		return m.SettingsSyncConvergentPhasePaused()
	case "migrating":
		return m.SettingsSyncConvergentPhaseMigrating()
	case "verifying":
		return m.SettingsSyncConvergentPhaseVerifying()
	case "converged":
		return m.SettingsSyncConvergentPhaseConverged()
	case "diverged":
		return m.SettingsSyncConvergentPhaseDiverged()
	case "blocked":
		return m.SettingsSyncConvergentPhaseBlocked()
	case "failed":
		return m.SettingsSyncConvergentPhaseFailed()
	default:
		return m.SettingsSyncConvergentPhaseIdle()
	}
}

func convergentRoundKindName(kind string, m locales.Messages) string {
	switch kind {
	case appsync.ConvergentRoundManifest:
		return m.SettingsSyncConvergentRoundManifest()
	case appsync.ConvergentRoundEntities:
		return m.SettingsSyncConvergentRoundEntities()
	case appsync.ConvergentRoundState:
		return m.SettingsSyncConvergentRoundState()
	default:
		return m.SettingsSyncConvergentRoundUnknown()
	}
}

func convergentRoundOutcomeName(outcome string, m locales.Messages) string {
	switch outcome {
	case "verified":
		return m.SettingsSyncConvergentOutcomeVerified()
	case "running":
		return m.SettingsSyncConvergentOutcomeRunning()
	case "retry":
		return m.SettingsSyncConvergentOutcomeRetry()
	case "failed":
		return m.SettingsSyncConvergentOutcomeFailed()
	case "converged":
		return m.SettingsSyncConvergentOutcomeVerified()
	default:
		return m.SettingsSyncConvergentOutcomePending()
	}
}

// convergentReasonName localizes the migration plan's stable reason codes.
func convergentReasonName(code string, m locales.Messages) string {
	switch code {
	case "unsupported-newer-schema", "future-schema":
		return m.SettingsSyncConvergentReasonNewer()
	case "invalid-schema":
		return m.SettingsSyncConvergentReasonInvalid()
	case "damaged-envelope", "missing-envelope", "corrupt-envelope",
		"envelope-metadata-mismatch", "envelope-materialized-mismatch",
		"damaged-materialized-payload":
		return m.SettingsSyncConvergentReasonDamaged()
	case "no-trusted-baseline":
		return m.SettingsSyncConvergentReasonNoBaseline()
	case "legacy-conflict", "conflicts-unresolved", "legacy-merge-failed", "pending-conflict":
		return m.SettingsSyncConvergentReasonConflict()
	case "shrunk-vault":
		return m.SettingsSyncConvergentReasonShrink()
	case "empty-vault":
		return m.SettingsSyncConvergentReasonEmpty()
	case "corrupt-local-baseline":
		return m.SettingsSyncConvergentReasonCorruptBaseline()
	default:
		return m.SettingsSyncConvergentReasonUnknown()
	}
}

func convergentDivergenceKindName(kind string, m locales.Messages) string {
	switch kind {
	case "origin-mismatch":
		return m.SettingsSyncConvergentDivergenceOrigin()
	case "dot-payload-mismatch":
		return m.SettingsSyncConvergentDivergenceDotPayload()
	case "entity-hash-mismatch":
		return m.SettingsSyncConvergentDivergenceEntityHash()
	case "equal-vector-different-state":
		return m.SettingsSyncConvergentDivergenceEqualVector()
	case "causal-overwrite":
		return m.SettingsSyncConvergentDivergenceCausal()
	case "unstable-replicas":
		return m.SettingsSyncConvergentDivergenceUnstable()
	default:
		return m.SettingsSyncConvergentDivergenceUnknown()
	}
}

// syncGuardPanel explains a blocked destructive upload. Restore is the safe
// default; the force push action opens a second, explicitly destructive
// confirmation and is never preselected.
func syncGuardPanel(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	finding := snapshot.Guard
	if !finding.Suspicious() {
		return
	}
	ui.Column(c).Padding(10).Gap(8).Background(t.Background).Border(1, t.Warning).Children(func() {
		ui.Text(c, m.SettingsSyncGuardTitle()).FontSize(12).Bold().TextColor(syncWarningTextColor(t))
		detail := m.SettingsSyncGuardShrink(
			strconv.Itoa(finding.Lost),
			strconv.Itoa(finding.BaseCount),
			syncEntityLabel(finding.EntityType, m),
			strconv.Itoa(finding.OutgoingCount),
		)
		if finding.Kind == appsync.GuardTombstone {
			detail = m.SettingsSyncGuardTombstone(strconv.Itoa(finding.BaseCount))
		}
		ui.Text(c, detail).FontSize(11).TextColor(t.Text)
		if finding.ViaRemote {
			ui.Text(c, m.SettingsSyncGuardViaRemote()).FontSize(11).TextColor(t.TextMuted)
		}
		// Restore and Force push are pinned in syncActionsPinnedBar, outside
		// this scrolling report, so a short window can never cut them.
		if state.ForcePushing() {
			ui.Column(c).Padding(8).Gap(6).Border(1, t.Danger).Children(func() {
				ui.Text(c, m.SettingsSyncGuardForceConfirm()).FontSize(12).Bold().TextColor(syncDangerTextColor(t))
				ui.Text(c, m.SettingsSyncGuardForceWarning()).FontSize(11).TextColor(t.Text)
			})
		}
	})
}

// syncConflictPanel shows the per-item conflict detail and the three
// resolutions. Merge is offered only when the selected strategy merges.
func syncConflictPanel(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	info := snapshot.Conflict
	if info == nil {
		return
	}
	device := info.RemoteDevice
	if device == "" {
		device = m.SettingsSyncConflictUnknownDevice()
	}
	ui.Column(c).Padding(10).Gap(6).Background(t.Background).Border(1, t.Danger).Children(func() {
		ui.Text(c, m.SettingsSyncConflictTitle()).FontSize(12).Bold()
		ui.Text(c, m.SettingsSyncConflictRemoteDevice(device)).FontSize(11).TextColor(t.TextMuted)
		if info.RemoteVersion > 0 {
			ui.Text(c, m.SettingsSyncConflictRemoteVersion(
				strconv.Itoa(info.RemoteVersion), formatSyncTime(info.RemoteUpdatedAt))).
				FontSize(11).TextColor(t.TextMuted)
		}
		ui.Text(c, m.SettingsSyncConflictSummary(
			strconv.Itoa(info.Summary.AddedLocal),
			strconv.Itoa(info.Summary.ModifiedLocal),
			strconv.Itoa(info.Summary.DeletedLocal),
			strconv.Itoa(info.Summary.AddedRemote),
			strconv.Itoa(info.Summary.ModifiedRemote),
			strconv.Itoa(info.Summary.DeletedRemote),
		)).FontSize(11).TextColor(t.Text)
		for _, item := range info.Items {
			id := item.EntityID
			if id == "" {
				id = "—"
			}
			ui.Text(c, m.SettingsSyncConflictItem(syncEntityLabel(item.EntityType, m), id,
				syncConflictKindName(item.Kind, m))).FontSize(11).TextColor(t.Text)
		}
		// The three resolutions are pinned in syncActionsPinnedBar so the
		// row is never cut at the bottom of a short window.
	})
}

func syncConflictKindName(kind appsync.ConflictKind, m locales.Messages) string {
	switch kind {
	case appsync.ConflictBothAdded:
		return m.SettingsSyncConflictKindBothAdded()
	case appsync.ConflictBothModified:
		return m.SettingsSyncConflictKindBothModified()
	case appsync.ConflictLocalDeletedRemoteModified:
		return m.SettingsSyncConflictKindLocalDeleted()
	case appsync.ConflictRemoteDeletedLocalModified:
		return m.SettingsSyncConflictKindRemoteDeleted()
	default:
		return string(kind)
	}
}

// syncBackupsPanel lists local restore points and retained cloud copies. Both
// are timestamped and restorable; no entry carries decrypted data.
func syncBackupsPanel(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, m.SettingsSyncBackupsTitle()).FontSize(12).Bold()
		ui.Text(c, m.SettingsSyncBackupsDescription()).FontSize(11).TextColor(t.TextMuted)
		// Refresh backups is pinned in syncActionsPinnedBar so it stays
		// visible even when the list is long.
		if snapshot.BackupsProblem != appsync.ProblemNone {
			ui.Text(c, m.SettingsSyncBackupsProblem()).FontSize(11).TextColor(syncWarningTextColor(t))
		}
		if len(snapshot.Backups) == 0 {
			ui.Text(c, m.SettingsSyncBackupsEmpty()).FontSize(11).TextColor(t.TextMuted)
			return
		}
		for _, backup := range snapshot.Backups {
			entry := backup
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, fmt.Sprintf("%s · %s · %s",
					syncBackupSourceName(entry.Source, m),
					syncBackupReasonName(entry.Reason, m),
					formatSyncTime(entry.CreatedAt))).FontSize(11).TextColor(t.Text)
				if ui.Button(c, m.SettingsSyncBackupRestore()).Disabled(state.Busy()).Clicked() {
					state.RestoreBackup(entry.Source, entry.ID)
					c.Invalidate()
				}
			})
		}
	})
}

func syncBackupSourceName(source appsync.BackupSource, m locales.Messages) string {
	if source == appsync.BackupSourceRemote {
		return m.SettingsSyncBackupRemote()
	}
	return m.SettingsSyncBackupLocal()
}

func syncBackupReasonName(reason string, m locales.Messages) string {
	switch reason {
	case "vault_change":
		return m.SettingsSyncBackupReasonVaultChange()
	case "before_restore":
		return m.SettingsSyncBackupReasonBeforeRestore()
	case "app_version_change":
		return m.SettingsSyncBackupReasonAppVersionChange()
	case "merge":
		return m.SettingsSyncBackupReasonMerge()
	case "conflict":
		return m.SettingsSyncBackupReasonConflict()
	case "guard-force":
		return m.SettingsSyncBackupReasonGuardForce()
	default:
		return m.SettingsSyncBackupReasonOther()
	}
}

func syncButtons(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages) {
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, m.SettingsSyncSyncNow()).Disabled(state.Busy()).Clicked() {
			state.SyncNow()
			c.Invalidate()
		}
		switch appsync.NormalizeProvider(snapshot.Provider) {
		case appsync.ProviderWebDAV:
			if ui.Button(c, m.SettingsSyncWebDAVReconfigure()).Disabled(state.Busy()).Clicked() {
				state.Reconfigure()
				c.Invalidate()
			}
		case appsync.ProviderS3:
			if ui.Button(c, m.SettingsSyncS3Reconfigure()).Disabled(state.Busy()).Clicked() {
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
	provider := appsync.NormalizeProvider(snapshot.Provider)
	if problem := syncProblemTextFor(m, provider, snapshot.Problem); problem != "" {
		ui.Text(c, problem).FontSize(12).TextColor(t.Danger)
		return
	}
	if err := state.Err(); err != nil {
		// Worker errors carry typed sentinels; never render Go error text.
		text := syncProblemTextFor(m, provider, syncProblemForError(err))
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

// syncProblemTextFor prefers the S3 wording for problems whose generic text
// names WebDAV, so an S3 failure never tells the user to fix a WebDAV URL.
func syncProblemTextFor(m locales.Messages, provider string, problem appsync.Problem) string {
	if provider == appsync.ProviderS3 {
		switch problem {
		case appsync.ProblemCredentials:
			return m.SettingsSyncS3ProblemCredentials()
		case appsync.ProblemForbidden:
			return m.SettingsSyncS3ProblemForbidden()
		case appsync.ProblemInvalidEndpoint:
			return m.SettingsSyncS3ProblemInvalidEndpoint()
		case appsync.ProblemInsecureEndpoint:
			return m.SettingsSyncS3ProblemInsecureEndpoint()
		case appsync.ProblemWrongRegion:
			return m.SettingsSyncS3ProblemWrongRegion()
		}
	}
	return syncProblemText(m, problem)
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
	case appsync.StatusConflict:
		return m.SettingsSyncStatusConflict()
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
	case appsync.ProblemWrongRegion:
		return m.SettingsSyncProblemWrongRegion()
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
	case appsync.ProblemShrinkGuard:
		return m.SettingsSyncProblemShrinkGuard()
	case appsync.ProblemTombstoneGuard:
		return m.SettingsSyncProblemTombstoneGuard()
	case appsync.ProblemBackup:
		return m.SettingsSyncProblemBackup()
	case appsync.ProblemConvergentMigration:
		return m.SettingsSyncProblemConvergentMigration()
	case appsync.ProblemConvergentDivergence:
		return m.SettingsSyncProblemConvergentDivergence()
	case appsync.ProblemConvergentCorrupt:
		return m.SettingsSyncProblemConvergentCorrupt()
	case appsync.ProblemConvergentPaused:
		return m.SettingsSyncProblemConvergentPaused()
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
	case errors.Is(err, appsync.ErrShrinkGuard):
		return appsync.ProblemShrinkGuard
	case errors.Is(err, appsync.ErrTombstoneGuard):
		return appsync.ProblemTombstoneGuard
	case errors.Is(err, appsync.ErrBackupUnavailable):
		return appsync.ProblemBackup
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
	case errors.Is(err, appsync.ErrWrongRegion):
		return appsync.ProblemWrongRegion
	case errors.Is(err, appsync.ErrUnauthorized):
		return appsync.ProblemUnauthorized
	case errors.Is(err, appsync.ErrRateLimited):
		return appsync.ProblemRateLimited
	case errors.Is(err, appsync.ErrNetwork):
		return appsync.ProblemNetwork
	case errors.Is(err, appsync.ErrConvergentMigration):
		return appsync.ProblemConvergentMigration
	case errors.Is(err, appsync.ErrConvergentDivergence):
		return appsync.ProblemConvergentDivergence
	case errors.Is(err, appsync.ErrConvergentCorrupt):
		return appsync.ProblemConvergentCorrupt
	case errors.Is(err, appsync.ErrConvergentPaused):
		return appsync.ProblemConvergentPaused
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
